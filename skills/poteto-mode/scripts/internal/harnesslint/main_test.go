package harnesslint

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func lint(t *testing.T, files map[string]string) (int, string, string) {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var stdout, stderr bytes.Buffer
	code := Main([]string{root}, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestFlagsHarnessTokens(t *testing.T) {
	code, stdout, stderr := lint(t, map[string]string{"a.md": "Use `Task` with model `claude-opus-5-5-xhigh`.\nRead ~/.cursor/skills/x.\nRun on gpt-5.5-high.\nWe renamed gpt-4 to gpt-4o.\n"})
	if code != 1 || stdout != "1 files, 4 problems\n" {
		t.Fatalf("exit %d, stdout %q", code, stdout)
	}
	for _, want := range []string{"a.md:1: harness spawn tool name ``Task``", "a.md:1: model slug `claude-opus-5-5-xhigh`", "a.md:2: names only Cursor", "a.md:3: model slug `gpt-5.5-high`"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
}

func TestAllowsLinesThatNameEveryHarness(t *testing.T) {
	code, stdout, _ := lint(t, map[string]string{"a.md": "Skills live in `.cursor/skills/` or `.claude/skills/`.\nCursor and Claude Code both work.\n"})
	if code != 0 || stdout != "1 files, 0 problems\n" {
		t.Fatalf("exit %d, stdout %q", code, stdout)
	}
}

func TestSkipsHarnessReferenceAndNodeModules(t *testing.T) {
	code, stdout, _ := lint(t, map[string]string{
		"poteto-mode/references/harness.md": "`Task` in Cursor.\n",
		"node_modules/x/README.md":          "`Task` in Cursor.\n",
		"ok.md":                             "plain\n",
	})
	if code != 0 || stdout != "1 files, 0 problems\n" {
		t.Fatalf("exit %d, stdout %q", code, stdout)
	}
}

func TestDefaultRootsNeedTheScriptsDir(t *testing.T) {
	t.Setenv("POTETO_SCRIPTS_DIR", "")
	var stdout, stderr bytes.Buffer
	if code := Main(nil, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "scripts/poteto") {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
}
