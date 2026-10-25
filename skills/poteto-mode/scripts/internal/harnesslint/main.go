package harnesslint

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	harnessReference = filepath.Join("poteto-mode", "references", "harness.md")
	newline          = regexp.MustCompile(`\r?\n`)
)

var tokens = []struct {
	pattern *regexp.Regexp
	what    string
}{
	{regexp.MustCompile("`Task`|\\bTask (tool|call|calls|schema)\\b"), "harness spawn tool name"},
	{regexp.MustCompile(`\bAsk(User)?Question\b`), "harness question tool name"},
	{regexp.MustCompile(`\b(subagent_type|run_in_background|cloud_base_branch|file_attachments)\b|\breadonly: (true|false)\b|\benvironment: "`), "harness spawn field"},
	{regexp.MustCompile(`\bgrok-\d|\b[a-z]+(-[a-z]+)?-\d[\d.-]*-(low|medium|high|xhigh|max)(-fast)?\b`), "model slug"},
	{regexp.MustCompile(`pstack-models|setup-pstack|cursor-team-kit`), "Cursor-only config or plugin"},
}

var harnesses = []struct {
	name    string
	pattern *regexp.Regexp
}{
	{"Cursor", regexp.MustCompile(`\bCursor\b|\.cursor/`)},
	{"Claude Code", regexp.MustCompile(`\bClaude Code\b|\.claude/`)},
}

// defaultRoots scans the skills folder that holds this binary's sources, plus
// a sibling agents folder. The wrapper exports the sources' location because
// the binary itself runs from a build cache.
func defaultRoots() ([]string, error) {
	scripts := os.Getenv("POTETO_SCRIPTS_DIR")
	if scripts == "" {
		return nil, fmt.Errorf("pass the folders to scan, or run through scripts/poteto")
	}
	skills := filepath.Join(scripts, "..", "..")
	roots := []string{skills}
	if agents := filepath.Join(skills, "..", "agents"); isDir(agents) {
		roots = append(roots, agents)
	}
	return roots, nil
}

func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

func markdown(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == "node_modules" {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(p, ".md") && !strings.HasSuffix(p, harnessReference) {
			files = append(files, p)
		}
		return nil
	})
	return files, err
}

func Main(args []string, stdout, stderr io.Writer) int {
	roots := args
	if len(roots) == 0 {
		var err error
		if roots, err = defaultRoots(); err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
	}
	cwd, _ := os.Getwd()
	var files, problems []string
	for _, root := range roots {
		abs, _ := filepath.Abs(root)
		found, err := markdown(abs)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		files = append(files, found...)
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		rel, err := filepath.Rel(cwd, file)
		if err != nil {
			rel = file
		}
		for i, text := range newline.Split(string(data), -1) {
			at := fmt.Sprintf("%s:%d", rel, i+1)
			for _, t := range tokens {
				if m := t.pattern.FindString(text); m != "" {
					problems = append(problems, fmt.Sprintf("%s: %s `%s`. Use the neutral term and map it in %s", at, t.what, m, harnessReference))
				}
			}
			var named []string
			for _, h := range harnesses {
				if h.pattern.MatchString(text) {
					named = append(named, h.name)
				}
			}
			if len(named) == 1 {
				problems = append(problems, fmt.Sprintf("%s: names only %s. Name every harness on the line, or move the mapping to %s", at, named[0], harnessReference))
			}
		}
	}
	fmt.Fprintf(stdout, "%d files, %d problems\n", len(files), len(problems))
	for _, p := range problems {
		fmt.Fprintln(stderr, p)
	}
	if len(problems) > 0 {
		return 1
	}
	return 0
}
