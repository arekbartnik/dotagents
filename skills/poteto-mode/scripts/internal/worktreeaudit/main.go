// Package worktreeaudit is a read-only worktree prune audit. It classifies
// every git worktree by size, merge state, uncommitted work, remote and PR
// state, and the most recent chat that operated in it, then suggests a bucket.
// It never deletes anything; deletion stays a human-gated step in the playbook.
package worktreeaudit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const usage = `Usage: poteto worktree-audit [-b|--base <ref>] [-t|--transcripts <dir>]... [repo-path]
  repo-path defaults to the current repo.
  -t names a transcript dir to scan for LAST_CHAT. Repeat it for several.
  Without it the scan covers Cursor's and Claude Code's stores for this repo.
  -b sets the ref the MERGED column checks against (e.g. origin/integration).
  Without it the base is origin/HEAD, then origin/main, then origin/master.
  origin/HEAD is the remote's default branch, not necessarily the branch PRs
  merge into; pass -b when they differ.
`

type pr struct {
	number int64
	state  string
	head   string
}

type row struct {
	size, age, merged, dirty, remote, pr, last, bucket, worktree string
}

func (r row) String() string {
	return strings.Join([]string{r.size, r.age, r.merged, r.dirty, r.remote, r.pr, r.last, r.bucket, r.worktree}, "\t")
}

type repo struct{ dir string }

func (r repo) git(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	out, err := cmd.Output()
	return strings.TrimRight(string(out), "\n"), err
}

func (r repo) ok(args ...string) bool {
	_, err := r.git(args...)
	return err == nil
}

func Main(args []string, stdout, stderr io.Writer) int {
	var base string
	var transcriptDirs []string
flags:
	for len(args) > 0 {
		switch a := args[0]; {
		case a == "-b" || a == "--base" || a == "-t" || a == "--transcripts":
			if len(args) < 2 {
				what := "a ref"
				if a == "-t" || a == "--transcripts" {
					what = "a dir"
				}
				fmt.Fprintf(stderr, "error: %s needs %s\n", a, what)
				return 2
			}
			if a == "-b" || a == "--base" {
				base = args[1]
			} else {
				transcriptDirs = append(transcriptDirs, args[1])
			}
			args = args[2:]
			continue
		case a == "-h" || a == "--help":
			fmt.Fprint(stdout, usage)
			return 0
		case strings.HasPrefix(a, "-"):
			fmt.Fprintf(stderr, "error: unknown option: %s\n", a)
			return 2
		}
		break flags
	}

	dir := ""
	if len(args) > 0 {
		dir = args[0]
	} else if top, err := (repo{}).git("rev-parse", "--show-toplevel"); err == nil {
		dir = top
	}
	if dir == "" {
		fmt.Fprintln(stderr, "not in a git repo; pass a repo path")
		return 1
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		fmt.Fprintf(stderr, "error: not a directory: %s\n", dir)
		return 1
	}
	r := repo{dir}
	if !r.ok("rev-parse", "--git-dir") {
		fmt.Fprintf(stderr, "error: not a git repo: %s\n", dir)
		return 1
	}

	worktrees := listWorktrees(r)
	if len(worktrees) == 0 {
		fmt.Fprintln(stderr, "error: git worktree list returned nothing")
		return 1
	}
	mainWorktree := worktrees[0]

	if base == "" {
		originHead, _ := r.git("symbolic-ref", "--short", "refs/remotes/origin/HEAD")
		for _, cand := range []string{originHead, "origin/main", "origin/master"} {
			if cand != "" && r.ok("rev-parse", "--verify", "--quiet", cand+"^{commit}") {
				base = cand
				break
			}
		}
		if base == "" {
			fmt.Fprintln(stderr, "error: no base ref (origin/HEAD, origin/main, origin/master all missing); pass -b <ref>")
			return 1
		}
		fmt.Fprintf(stderr, "warn: no -b given; MERGED checks against %s\n", base)
	}

	// The base drives the merge check. The fetch is best-effort (stale is fine
	// for a first pass) and runs before the check, since -b may name a branch
	// not fetched yet. The explicit refspec creates it even in a single-branch
	// clone.
	if branch, ok := strings.CutPrefix(base, "origin/"); ok {
		if !r.ok("fetch", "origin", "+refs/heads/"+branch+":refs/remotes/"+base, "--quiet") {
			fmt.Fprintf(stderr, "warn: could not fetch %s; merged column may be stale\n", base)
		}
	}
	if !r.ok("rev-parse", "--verify", "--quiet", base+"^{commit}") {
		fmt.Fprintf(stderr, "error: base ref not found: %s\n", base)
		return 1
	}

	prs := listPRs(dir)
	if len(transcriptDirs) == 0 {
		transcriptDirs = defaultTranscriptDirs(mainWorktree)
	}
	candidates := worktrees[1:]
	lastChat := lastChats(transcriptDirs, candidates)
	now := time.Now()

	fmt.Fprintf(stdout, "# base: %s\n", base)
	fmt.Fprintln(stdout, "SIZE\tAGE\tMERGED\tDIRTY\tREMOTE\tPR\tLAST_CHAT\tBUCKET\tWORKTREE")

	var rows []row
	for _, wt := range candidates {
		if wt == mainWorktree {
			continue
		}
		rows = append(rows, audit(repo{wt}, wt, base, prs, lastChat[wt], now))
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := humanBytes(rows[i].size), humanBytes(rows[j].size)
		if a != b {
			return a > b
		}
		return rows[i].String() > rows[j].String()
	})
	for _, row := range rows {
		fmt.Fprintln(stdout, row)
	}
	return 0
}

func listWorktrees(r repo) []string {
	out, err := r.git("worktree", "list", "--porcelain")
	if err != nil {
		return nil
	}
	var paths []string
	for _, l := range strings.Split(out, "\n") {
		if p, ok := strings.CutPrefix(l, "worktree "); ok {
			paths = append(paths, p)
		}
	}
	return paths
}

// listPRs reads PR state by branch once, through Origin when its CLI can read
// this repo's PRs, otherwise gh. It is empty when neither can.
func listPRs(dir string) []pr {
	run := func(name string, args ...string) ([]byte, bool) {
		if _, err := exec.LookPath(name); err != nil {
			return nil, false
		}
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		out, err := cmd.Output()
		return out, err == nil
	}
	if raw, ok := run("origin", "pr", "list", "--author", "@me", "--state", "all", "--limit", "1000", "--json", "number,status,headRef"); ok {
		var rows []struct {
			Number  int64  `json:"number"`
			Status  string `json:"status"`
			HeadRef string `json:"headRef"`
		}
		if json.Unmarshal(raw, &rows) != nil {
			return nil
		}
		prs := make([]pr, len(rows))
		for i, p := range rows {
			prs[i] = pr{p.Number, p.Status, p.HeadRef}
		}
		return prs
	}
	if raw, ok := run("gh", "pr", "list", "--author", "@me", "--state", "all", "--limit", "1000", "--json", "number,state,headRefName"); ok {
		var rows []struct {
			Number      int64  `json:"number"`
			State       string `json:"state"`
			HeadRefName string `json:"headRefName"`
		}
		if json.Unmarshal(raw, &rows) != nil {
			return nil
		}
		prs := make([]pr, len(rows))
		for i, p := range rows {
			prs[i] = pr{p.Number, strings.ToLower(p.State), p.HeadRefName}
		}
		return prs
	}
	return nil
}

// defaultTranscriptDirs covers Cursor, which slugs the repo path by dropping
// the leading / and turning each / into -, and Claude Code, which turns each /
// and . into -, keeping the leading one.
func defaultTranscriptDirs(mainWorktree string) []string {
	home, _ := os.UserHomeDir()
	cursor := strings.ReplaceAll(strings.TrimPrefix(mainWorktree, "/"), "/", "-")
	claude := strings.NewReplacer("/", "-", ".", "-").Replace(mainWorktree)
	return []string{
		filepath.Join(home, ".cursor", "projects", cursor, "agent-transcripts"),
		filepath.Join(home, ".claude", "projects", claude),
	}
}

// lastChats finds, for each worktree, the newest transcript that operated in
// it. A path counts when it is followed by "/" or a quote, so glint-482 does
// not match glint-482-r37. Hidden files are skipped, as ripgrep did.
func lastChats(dirs, worktrees []string) map[string]time.Time {
	last := map[string]time.Time{}
	for _, dir := range dirs {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			continue
		}
		filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if p != dir && strings.HasPrefix(d.Name(), ".") {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			for _, wt := range worktrees {
				if bytes.Contains(data, []byte(wt+"/")) || bytes.Contains(data, []byte(wt+`"`)) {
					if info.ModTime().After(last[wt]) {
						last[wt] = info.ModTime()
					}
				}
			}
			return nil
		})
	}
	return last
}

func audit(w repo, wt, base string, prs []pr, lastChat, now time.Time) row {
	r := row{worktree: wt, size: diskUsage(wt)}

	head, _ := w.git("rev-parse", "HEAD")
	headTS := int64(0)
	if out, err := w.git("log", "-1", "--format=%ct", "HEAD"); err == nil {
		headTS, _ = strconv.ParseInt(out, 10, 64)
	}
	r.age = "?"
	if headTS > 0 {
		r.age = fmt.Sprintf("%dd", (now.Unix()-headTS)/86400)
	}

	// Squash-merged branches are not ancestors of the base, so PR state is the
	// real signal; merge-base only catches fast-forward or rebase merges.
	r.merged = "no"
	if w.ok("merge-base", "--is-ancestor", head, base) {
		r.merged = "YES"
	}

	// Distinguish real WIP (tracked edits) from disposable untracked scratch.
	porcelain, _ := w.git("status", "--porcelain")
	tracked, untracked := 0, 0
	if porcelain != "" {
		for _, l := range strings.Split(porcelain, "\n") {
			if strings.HasPrefix(l, "??") {
				untracked++
			} else {
				tracked++
			}
		}
	}
	switch {
	case porcelain == "":
		r.dirty = "clean"
	case tracked > 0:
		r.dirty = fmt.Sprintf("wip:%d", tracked)
	default:
		r.dirty = fmt.Sprintf("scratch:%d", untracked)
	}

	branch, _ := w.git("symbolic-ref", "--quiet", "--short", "HEAD")
	switch {
	case branch == "":
		r.remote = "detached"
	case w.ok("show-ref", "--verify", "--quiet", "refs/remotes/origin/"+branch):
		if upstream, _ := w.git("rev-parse", "origin/"+branch); upstream == head {
			r.remote = "pushed"
		} else {
			count, _ := w.git("rev-list", "--count", "origin/"+branch+"..HEAD")
			r.remote = "ahead" + count
		}
	default:
		r.remote = "no-remote"
	}

	r.pr = "-"
	if branch != "" {
		for _, p := range prs {
			if p.head == branch {
				r.pr = fmt.Sprintf("#%d/%s", p.number, p.state)
				break
			}
		}
	}

	r.last = "-"
	recent := false
	if !lastChat.IsZero() {
		r.last = lastChat.Local().Format("2006-01-02")
		recent = (now.Unix()-lastChat.Unix())/86400 <= 4
	}

	switch {
	case strings.HasPrefix(r.dirty, "wip:"):
		r.bucket = "hold-wip"
	case strings.HasSuffix(r.pr, "/open") || strings.HasSuffix(r.pr, "/draft"):
		r.bucket = "hold-open-pr"
	case recent:
		r.bucket = "verify-recent-chat"
	case r.merged == "YES" || r.pr != "-":
		r.bucket = "safe"
	default:
		r.bucket = "review"
	}
	return r
}

// diskUsage keeps du's own human-readable size, so the column reads the same
// as `du -sh`.
func diskUsage(path string) string {
	out, err := exec.Command("du", "-sh", path).Output()
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func humanBytes(size string) float64 {
	units := "BKMGTPE"
	s := strings.TrimSpace(size)
	if s == "" {
		return 0
	}
	exp := 0
	if i := strings.IndexByte(units, s[len(s)-1]); i >= 0 {
		exp = i
		s = s[:len(s)-1]
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	for ; exp > 0; exp-- {
		v *= 1024
	}
	return v
}
