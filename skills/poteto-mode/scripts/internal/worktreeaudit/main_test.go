package worktreeaudit

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type fixture struct {
	root, repo, transcripts string
}

func sh(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// newFixture builds a repo with one worktree per bucket and column value, a
// fake gh that reports PR state, and a transcript store.
func newFixture(t *testing.T) fixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{root: root, repo: filepath.Join(root, "repo"), transcripts: filepath.Join(root, "transcripts")}
	origin := filepath.Join(root, "origin.git")
	sh(t, root, "git", "init", "-q", "--bare", "-b", "main", origin)
	sh(t, root, "git", "clone", "-q", origin, f.repo)
	write(t, filepath.Join(f.repo, "a.txt"), "a\n")
	sh(t, f.repo, "git", "add", ".")
	sh(t, f.repo, "git", "commit", "-qm", "base")
	sh(t, f.repo, "git", "push", "-q", "origin", "main")
	sh(t, f.repo, "git", "remote", "set-head", "origin", "main")

	wt := func(name string, args ...string) string {
		p := filepath.Join(root, name)
		sh(t, f.repo, append([]string{"git", "worktree", "add", "-q"}, append(args, p)...)...)
		return p
	}
	commit := func(dir, msg string) {
		write(t, filepath.Join(dir, msg+".txt"), msg+"\n")
		sh(t, dir, "git", "add", ".")
		sh(t, dir, "git", "commit", "-qm", msg)
	}

	merged := wt("wt-merged", "-b", "merged")
	sh(t, merged, "git", "push", "-q", "origin", "merged")

	wip := wt("wt-wip", "-b", "wip")
	write(t, filepath.Join(wip, "a.txt"), "changed\n")

	scratch := wt("wt-scratch", "--detach")
	commit(scratch, "scratch")
	write(t, filepath.Join(scratch, "junk.txt"), "junk\n")

	open := wt("wt-open", "-b", "open-pr")
	commit(open, "open1")
	sh(t, open, "git", "push", "-q", "origin", "open-pr")
	commit(open, "open2")

	recent := wt("wt-recent", "-b", "noremote")
	commit(recent, "recent")

	closed := wt("wt-closed", "-b", "closed-pr")
	commit(closed, "closed")

	wt("glint-482", "-b", "glint")
	r37 := wt("glint-482-r37", "-b", "glint-r37")
	commit(r37, "r37")

	write(t, filepath.Join(f.transcripts, "s1.jsonl"), `{"cwd":"`+recent+`/src"}`+"\n"+`{"cwd":"`+r37+`"}`+"\n")
	write(t, filepath.Join(f.transcripts, ".hidden.jsonl"), `{"cwd":"`+closed+`/"}`+"\n")

	bin := filepath.Join(root, "bin")
	write(t, filepath.Join(bin, "gh"), "#!/bin/sh\necho '[{\"number\":7,\"state\":\"CLOSED\",\"headRefName\":\"closed-pr\"},{\"number\":9,\"state\":\"OPEN\",\"headRefName\":\"open-pr\"}]'\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return f
}

func rows(t *testing.T, stdout string) map[string][]string {
	t.Helper()
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if lines[0] != "# base: origin/main" || lines[1] != "SIZE\tAGE\tMERGED\tDIRTY\tREMOTE\tPR\tLAST_CHAT\tBUCKET\tWORKTREE" {
		t.Fatalf("bad header:\n%s", stdout)
	}
	out := map[string][]string{}
	for _, l := range lines[2:] {
		cols := strings.Split(l, "\t")
		out[filepath.Base(cols[8])] = cols
	}
	return out
}

func TestClassifiesEveryWorktree(t *testing.T) {
	f := newFixture(t)
	var stdout, stderr bytes.Buffer
	if code := Main([]string{"-t", f.transcripts, f.repo}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "warn: no -b given; MERGED checks against origin/main") {
		t.Errorf("stderr lacks the base warning:\n%s", stderr.String())
	}
	got := rows(t, stdout.String())
	want := map[string][]string{
		"wt-merged":     {"YES", "clean", "pushed", "-", "safe"},
		"wt-wip":        {"YES", "wip:1", "no-remote", "-", "hold-wip"},
		"wt-scratch":    {"no", "scratch:1", "detached", "-", "review"},
		"wt-open":       {"no", "clean", "ahead1", "#9/open", "hold-open-pr"},
		"wt-recent":     {"no", "clean", "no-remote", "-", "verify-recent-chat"},
		"wt-closed":     {"no", "clean", "no-remote", "#7/closed", "safe"},
		"glint-482":     {"YES", "clean", "no-remote", "-", "safe"},
		"glint-482-r37": {"no", "clean", "no-remote", "-", "verify-recent-chat"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d:\n%s", len(got), len(want), stdout.String())
	}
	for name, w := range want {
		cols := got[name]
		if cols == nil {
			t.Errorf("%s: missing row", name)
			continue
		}
		have := []string{cols[2], cols[3], cols[4], cols[5], cols[7]}
		if strings.Join(have, " ") != strings.Join(w, " ") {
			t.Errorf("%s: got %v, want %v", name, have, w)
		}
		if cols[1] != "0d" || cols[0] == "" {
			t.Errorf("%s: size %q age %q", name, cols[0], cols[1])
		}
		if chat := cols[6]; (w[4] == "verify-recent-chat") == (chat == "-") {
			t.Errorf("%s: LAST_CHAT %q", name, chat)
		}
	}
}

func TestOptionErrors(t *testing.T) {
	for _, c := range []struct {
		args []string
		code int
		err  string
	}{
		{[]string{"-b"}, 2, "error: -b needs a ref"},
		{[]string{"--transcripts"}, 2, "error: --transcripts needs a dir"},
		{[]string{"--nope"}, 2, "error: unknown option: --nope"},
		{[]string{t.TempDir()}, 1, "error: not a git repo"},
	} {
		var stdout, stderr bytes.Buffer
		if code := Main(c.args, &stdout, &stderr); code != c.code || !strings.Contains(stderr.String(), c.err) {
			t.Errorf("%v: exit %d, stderr %q", c.args, code, stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if code := Main([]string{"-h"}, &stdout, &stderr); code != 0 || !strings.HasPrefix(stdout.String(), "Usage: poteto worktree-audit") {
		t.Errorf("-h: exit %d, stdout %q", code, stdout.String())
	}
}

func TestHumanBytesOrdersLikeSortH(t *testing.T) {
	sizes := []string{"0B", "512B", "4.0K", "452K", "1.2M", "999M", "1.0G", "2T"}
	for i := 1; i < len(sizes); i++ {
		if humanBytes(sizes[i-1]) >= humanBytes(sizes[i]) {
			t.Errorf("%s should sort below %s", sizes[i-1], sizes[i])
		}
	}
}
