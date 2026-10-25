package orch

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

type runResult struct {
	code           int
	stdout, stderr string
}

func runCLI(t *testing.T, args ...string) runResult {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Main(args, &stdout, &stderr)
	return runResult{code, stdout.String(), stderr.String()}
}

// isolateEnv clears the store and repo fallbacks, which an empty value
// leaves unset as far as orch is concerned.
func isolateEnv(t *testing.T) {
	t.Setenv("ORCH_STORE", "")
	t.Setenv("ORCH_REPO", "")
}

func TestCLIPrintsHelpAndRejectsInvalidParsingWithExit1(t *testing.T) {
	isolateEnv(t)
	help := runCLI(t, "--help")
	if help.code != 0 || !strings.Contains(help.stdout, "Commands:") || !strings.Contains(help.stdout, "unit") || !strings.Contains(help.stdout, "ledger") {
		t.Fatalf("--help gave %+v", help)
	}

	frontierHelp := runCLI(t, "frontier", "set", "--help")
	for _, flag := range []string{"--repo <dir>", "--forge <forge>", "--prs <n,...>"} {
		if frontierHelp.code != 0 || !strings.Contains(frontierHelp.stdout, flag) {
			t.Fatalf("frontier set --help lacks %s: %+v", flag, frontierHelp)
		}
	}

	directory := makeDirectory(t)
	invalid := runCLI(t, "--store", directory, "unit", "add", "u1")
	if invalid.code != 1 || !strings.Contains(invalid.stderr, "required option '--track <track>'") {
		t.Fatalf("unit add without --track gave %+v", invalid)
	}

	badForge := runCLI(t, "--store", directory, "frontier", "set", "--repo", directory, "--forge", "gt")
	if badForge.code != 1 || !strings.Contains(badForge.stderr, "allowed choices are gh, origin") {
		t.Fatalf("--forge gt gave %+v", badForge)
	}
}

func TestCLIAcceptsOrchStoreAndEmitsCompleteJSON(t *testing.T) {
	isolateEnv(t)
	directory := makeDirectory(t)
	t.Setenv("ORCH_STORE", directory)
	if got := runCLI(t, "init"); got.code != 0 {
		t.Fatalf("init gave %+v", got)
	}

	added := runCLI(t, "unit", "add", "u1", "--track", "build", "--json")
	var row map[string]any
	if added.code != 0 || json.Unmarshal([]byte(added.stdout), &row) != nil {
		t.Fatalf("unit add --json gave %+v", added)
	}
	want := map[string]any{"id": "u1", "track": "build", "state": "pending", "branch": "", "pr": "", "sha": "", "brief": ""}
	if len(row) != len(want) {
		t.Fatalf("unit add --json returned %v", row)
	}
	for key, value := range want {
		if row[key] != value {
			t.Fatalf("unit add --json returned %v", row)
		}
	}
}

func TestCLIMapsUserAndNotFoundOutcomesToExitCodes(t *testing.T) {
	isolateEnv(t)
	directory := makeDirectory(t)
	if got := runCLI(t, "--store", directory, "init"); got.code != 0 {
		t.Fatalf("init gave %+v", got)
	}

	missingRepo := runCLI(t, "--store", directory, "frontier", "set")
	if missingRepo.code != 1 || !strings.Contains(missingRepo.stderr, "set --repo <dir> or ORCH_REPO") {
		t.Fatalf("frontier set without --repo gave %+v", missingRepo)
	}

	userError := runCLI(t, "--store", directory, "unit", "add", "", "--track", "build")
	if userError.code != 1 || !strings.Contains(userError.stderr, "unit id must not be empty") {
		t.Fatalf("empty unit id gave %+v", userError)
	}

	missingUnit := runCLI(t, "--store", directory, "unit", "get", "missing")
	if missingUnit.code != 2 || !strings.Contains(missingUnit.stderr, "unit missing not found") {
		t.Fatalf("missing unit gave %+v", missingUnit)
	}

	missingLedger := runCLI(t, "--store", directory, "--json", "ledger", "check", "184530", "abc123")
	var row map[string]any
	if missingLedger.code != 2 || json.Unmarshal([]byte(missingLedger.stdout), &row) != nil || missingLedger.stderr != "" {
		t.Fatalf("missing ledger row gave %+v", missingLedger)
	}
	if row["pr"] != "184530" || row["sha"] != "abc123" || row["verdict"] != "NOT-VERIFIED" || len(row) != 3 {
		t.Fatalf("missing ledger row printed %v", row)
	}
}

func TestCLIParsesOptionsAnywhere(t *testing.T) {
	isolateEnv(t)
	store := makeDirectory(t)
	cases := []struct {
		args   []string
		code   int
		stdout string
	}{
		{[]string{"--store", store, "init"}, 0, "initialized " + store + "\n"},
		{[]string{"--store", store, "unit", "add", "--track", "b", "u1"}, 0, "u1\tb\tpending\t\t\t\t\n"},
		{[]string{"unit", "add", "u2", "--track=b", "--brief=x", "--store=" + store}, 0, "u2\tb\tpending\t\t\t\tx\n"},
		{[]string{"--store", store, "unit", "add", "-5", "--track", "neg"}, 0, "'-5\tneg\tpending\t\t\t\t\n"},
		{[]string{"--store", store, "--", "unit", "get", "u1"}, 0, "u1\tb\tpending\t\t\t\t\n"},
		{[]string{"--store", store, "unit", "--json", "counts"}, 0, "{\n  \"pending\": 3\n}\n"},
		{[]string{"--store", store, "unit", "list", "--state", "x", "--state", "pending", "--track", "neg"}, 0, "'-5\tneg\tpending\t\t\t\t\n"},
		{[]string{"--store", store, "inbox", "drain", "--peek", "--peek"}, 0, "(empty)\n"},
		{[]string{"--store", store, "unit", "add", "u3", "--track", "b", "--", "--json"}, 1, ""},
		{[]string{"--store", store, "unit", "add", "--", "u3", "--track", "b"}, 1, ""},
		{[]string{"--store", store, "unit", "add", "u3", "--track", "--json"}, 1, ""},
		{[]string{"--store", store, "unit", "--track", "x", "add", "u3"}, 1, ""},
		{[]string{"--store", store, "ledger", "check", "-5", "abc"}, 1, ""},
		{[]string{"--store", store, "inbox", "drain", "--peek=yes"}, 1, ""},
		{[]string{"--store", store, "bogus"}, 1, ""},
		{[]string{"--store", store, "unit"}, 1, ""},
		{[]string{"--store=", "init"}, 1, ""},
		{[]string{"--store"}, 1, ""},
		{[]string{"ledger", "record", "1", "sha", "looks-good", "--evidence", "e"}, 1, ""},
	}
	for _, c := range cases {
		got := runCLI(t, c.args...)
		if got.code != c.code || got.stdout != c.stdout {
			t.Errorf("orch %q gave %+v, want exit %d and stdout %q", c.args, got, c.code, c.stdout)
		}
		if c.code != 0 && !strings.HasPrefix(got.stderr, "error: ") {
			t.Errorf("orch %q wrote stderr %q", c.args, got.stderr)
		}
	}

	if got := runCLI(t, "ledger", "record", "1", "sha", "looks-good", "--evidence", "e"); !strings.Contains(got.stderr, "verdict must be live-ui-verified") {
		t.Errorf("bad verdict wrote %q", got.stderr)
	}
	for _, args := range [][]string{{"-h"}, {"--help", "unit"}, {"unit", "--help"}, {"unit", "add", "u1", "--track", "x", "-h"}} {
		if got := runCLI(t, args...); got.code != 0 || !strings.HasPrefix(got.stdout, "Usage: orch") || got.stderr != "" {
			t.Errorf("orch %q gave %+v", args, got)
		}
	}
}

func TestCLIPrintsCompactAndJSONOutput(t *testing.T) {
	isolateEnv(t)
	store := makeDirectory(t)
	t.Setenv("ORCH_STORE", store)
	expect := func(want string, args ...string) {
		t.Helper()
		if got := runCLI(t, args...); got.code != 0 || got.stdout != want || got.stderr != "" {
			t.Errorf("orch %q gave %+v, want stdout %q", args, got, want)
		}
	}
	expect("{\n  \"store\": \""+store+"\"\n}\n", "--json", "init")
	expect("(no units)\n", "unit", "list")
	expect("[]\n", "unit", "list", "--json")
	expect("none\n", "unit", "counts")
	for i := 1; i <= 5; i++ {
		runCLI(t, "unit", "add", "u"+strconv.Itoa(i), "--track", "t")
	}
	expect("u1\tt\tpending\t\t\t\t\nu2\tt\tpending\t\t\t\t\nu3\tt\tpending\t\t\t\t\nu4\tt\tpending\t\t\t\t\n... 1 more; use --json\n", "unit", "list")
	expect("{\n  \"id\": \"u1\",\n  \"track\": \"t\",\n  \"state\": \"done\",\n  \"branch\": \"\",\n  \"pr\": \"7\",\n  \"sha\": \"\",\n  \"brief\": \"\"\n}\n",
		"unit", "set", "u1", "--state", "done", "--pr", "7", "--json")
	expect("done=1, pending=4\n", "unit", "counts")

	if got := runCLI(t, "ledger", "check", "7", "abc"); got != (runResult{2, "NOT-VERIFIED\n", ""}) {
		t.Errorf("ledger check of a missing row gave %+v", got)
	}
	expect("7\tabc\tunit-test-verified\n", "ledger", "record", "7", "abc", "unit-test-verified", "--evidence", "e")
	expect("unit-test-verified\n", "ledger", "check", "7", "abc")

	expect("0\n", "inbox", "count")
	pushed := runCLI(t, "inbox", "push", "w1", "u1", "done")
	if !regexp.MustCompile(`^u1\tdone\t\d{4}-\d\d-\d\dT\d\d-\d\d-\d\d-\d{3}Z-\d+-[0-9a-f-]{36}\.tsv\n$`).MatchString(pushed.stdout) {
		t.Errorf("inbox push printed %q", pushed.stdout)
	}
	expect("{\n  \"count\": 1\n}\n", "inbox", "count", "--json")

	expect("g1\topen\n", "gate", "park", "g1", "--question", "Ship?", "--options", "a|b", "--default", "a")
	expect("g1\tShip?\ta|b\ta\n", "gate", "list")
	expect("{\n  \"kind\": \"resolved\",\n  \"id\": \"g1\",\n  \"question\": \"Ship?\",\n  \"options\": \"a|b\",\n  \"defaultAnswer\": \"a\",\n  \"answer\": \"b\"\n}\n",
		"--json", "gate", "resolve", "g1", "--answer", "b")
	expect("(no open gates)\n", "gate", "list")

	expect("generation=0 prs=none lowest-unmerged=none\n", "frontier", "show")
	expect("{\n  \"generation\": 0,\n  \"prs\": [],\n  \"lowestUnmerged\": null\n}\n", "frontier", "show", "--json")
	writeText(t, filepath.Join(store, "frontier.json"), `{"generation":5,"prs":[{"pr":3,"branches":"b","sha":"","state":"MERGED"},{"pr":4,"branches":"c","sha":"s","state":"OPEN"}],"lowestUnmerged":4}`)
	expect("generation=5 prs=b#3@:MERGED,c#4@s:OPEN lowest-unmerged=4\n", "frontier", "show")

	expect("(no standing orders)\n", "standing", "show")
	expect("1. Rebase first\n", "standing", "add", "  Rebase\nfirst  ")
	expect("1. Rebase first\n", "standing", "show")

	for _, id := range []string{"g5", "g4", "g3", "g2", "Zed"} {
		runCLI(t, "gate", "park", id, "--question", "q", "--options", "o", "--default", "d")
	}
	expect("counts: units=5; states=done=1, pending=4; ledger=unit-test-verified=1\nchanged: first render\ngates open: 5; ids=Zed,g2,g3,g4,+1 more\n", "status")
	expect("counts: units=5; states=done=1, pending=4; ledger=unit-test-verified=1\nchanged: no derived changes\ngates open: 5; ids=Zed,g2,g3,g4,+1 more\n", "status")
}

func TestCLIReportsLockTakeover(t *testing.T) {
	isolateEnv(t)
	store := makeDirectory(t)
	t.Setenv("ORCH_STORE", store)
	runCLI(t, "init")
	lock := filepath.Join(store, lockFile)
	pid := strconv.Itoa(os.Getpid())

	writeText(t, lock, pid+"\n")
	if got := runCLI(t, "unit", "add", "u1", "--track", "t"); got.code != 1 || got.stderr != "error: store lock held by pid "+pid+"\n" {
		t.Fatalf("locked add gave %+v", got)
	}
	if got := runCLI(t, "unit", "list"); got.code != 0 {
		t.Fatalf("reads must not need the lock: %+v", got)
	}
	if got := runCLI(t, "--force", "unit", "add", "u1", "--track", "t"); got.code != 0 || got.stderr != "stealing store lock held by pid "+pid+"\n" {
		t.Fatalf("forced add gave %+v", got)
	}

	dead := deadPID(t)
	writeText(t, lock, dead+"\n")
	if got := runCLI(t, "unit", "add", "u2", "--track", "t"); got.code != 0 || got.stderr != "replacing stale store lock (pid "+dead+" is dead)\n" {
		t.Fatalf("stale add gave %+v", got)
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatalf("the CLI left its lock behind: %v", err)
	}
}
