package orch

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func makeDirectory(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("", "orch-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(directory) })
	return directory
}

func useStore(t *testing.T, directory string, options storeOptions) *store {
	t.Helper()
	s, err := openStore(directory, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.close() })
	return s
}

func initializedStore(t *testing.T) (string, *store) {
	t.Helper()
	directory := makeDirectory(t)
	s := useStore(t, directory, storeOptions{})
	if _, err := s.init(); err != nil {
		t.Fatal(err)
	}
	return directory, s
}

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func wantError(t *testing.T, err error, substring string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), substring) {
		t.Fatalf("got error %v, want one containing %q", err, substring)
	}
}

func readText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func writeText(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func listDir(t *testing.T, directory string) []string {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func ptr[T any](value T) *T { return &value }

func git(t *testing.T, repo string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %s", strings.Join(args, " "), out)
	}
	return strings.TrimSpace(string(out))
}

type gitStackFixture struct {
	repo                                    string
	mergedSHA, closedSHA, openSHA, draftSHA string
}

func makeGitStack(t *testing.T, directory string) gitStackFixture {
	t.Helper()
	repo := filepath.Join(directory, "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init", "--initial-branch=main")
	git(t, repo, "config", "user.name", "Orch Test")
	git(t, repo, "config", "user.email", "orch@example.com")
	git(t, repo, "config", "commit.gpgsign", "false")
	writeText(t, filepath.Join(repo, "main.txt"), "main\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "main")
	for index, branch := range []string{"stack/merged", "stack/closed", "stack/open", "stack/draft"} {
		git(t, repo, "checkout", "-b", branch)
		writeText(t, filepath.Join(repo, fmt.Sprintf("stack-%d.txt", index)), branch+"\n")
		git(t, repo, "add", ".")
		git(t, repo, "commit", "-m", branch)
	}
	git(t, repo, "checkout", "stack/open")
	return gitStackFixture{
		repo:      repo,
		mergedSHA: git(t, repo, "rev-parse", "stack/merged"),
		closedSHA: git(t, repo, "rev-parse", "stack/closed"),
		openSHA:   git(t, repo, "rev-parse", "stack/open"),
		draftSHA:  git(t, repo, "rev-parse", "stack/draft"),
	}
}

type fixturePR struct {
	number            int
	state, head, base string
}

var forgeFields = map[string]string{
	"gh":     "number,state,headRefName,baseRefName",
	"origin": "number,status,headRef,baseRef",
}

func forgeRows(forge string, prs ...fixturePR) string {
	items := make([]map[string]any, 0, len(prs))
	for _, pr := range prs {
		if forge == "gh" {
			state := strings.ToUpper(pr.state)
			if pr.state == "draft" {
				state = "OPEN"
			}
			items = append(items, map[string]any{"number": pr.number, "state": state, "headRefName": pr.head, "baseRefName": pr.base})
		} else {
			items = append(items, map[string]any{"number": pr.number, "status": pr.state, "headRef": pr.head, "baseRef": pr.base})
		}
	}
	data, err := json.Marshal(items)
	if err != nil {
		panic(err)
	}
	return string(data)
}

// withFakeForge puts a fake forge CLI first on PATH that answers the stack
// fixture's queries.
func withFakeForge(t *testing.T, directory, forge, currentRows string) {
	t.Helper()
	bin := filepath.Join(directory, "bin")
	outputPath := filepath.Join(directory, "forge-output.txt")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if currentRows == "" {
		currentRows = forgeRows(forge, fixturePR{11, "open", "stack/open", "stack/closed"})
	}
	writeText(t, outputPath, currentRows+"\n")
	repo, err := filepath.EvalSymlinks(filepath.Join(directory, "repo"))
	if err != nil {
		t.Fatal(err)
	}
	list := func(filter, branch string) string {
		return "pr list " + filter + " " + branch + " --state all --json " + forgeFields[forge]
	}
	script := `#!/usr/bin/env bash
set -euo pipefail
if [ "$(pwd -P)" != "` + repo + `" ]; then
  printf '` + forge + ` ran outside the fixture repo: %s\n' "$(pwd -P)" >&2
  exit 2
fi
case "$*" in
  "` + list("--head", "stack/open") + `")
    cat "` + outputPath + `"
    ;;
  "` + list("--head", "stack/closed") + `")
    printf '%s\n' '` + forgeRows(forge, fixturePR{13, "closed", "stack/closed", "stack/merged"}) + `'
    ;;
  "` + list("--head", "stack/merged") + `")
    printf '%s\n' '` + forgeRows(forge, fixturePR{10, "merged", "stack/merged", "main"}) + `'
    ;;
  "` + list("--base", "stack/open") + `")
    printf '%s\n' '` + forgeRows(forge, fixturePR{14, "draft", "stack/draft", "stack/open"}, fixturePR{15, "closed", "stack/abandoned", "stack/open"}) + `'
    ;;
  "` + list("--base", "stack/draft") + `")
    printf '[]\n'
    ;;
  *)
    printf 'unexpected ` + forge + ` arguments: %s\n' "$*" >&2
    exit 2
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, forge), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
}

func TestStoreInitializesIdempotentlyAndReleasesLock(t *testing.T) {
	directory := makeDirectory(t)
	s := useStore(t, directory, storeOptions{})

	if got := must(s.init()); got != directory {
		t.Fatalf("init returned %q, want %q", got, directory)
	}
	firstUnits := readText(t, filepath.Join(directory, "units.tsv"))
	firstLedger := readText(t, filepath.Join(directory, "ledger.tsv"))

	if got := must(s.init()); got != directory {
		t.Fatalf("init returned %q, want %q", got, directory)
	}
	if readText(t, filepath.Join(directory, "units.tsv")) != firstUnits || readText(t, filepath.Join(directory, "ledger.tsv")) != firstLedger {
		t.Fatal("second init rewrote the store")
	}
	want := []string{".orch.lock", "frontier.json", "gates.md", "inbox", "ledger.tsv", "preferences.md", "units.tsv"}
	if got := listDir(t, directory); !reflect.DeepEqual(got, want) {
		t.Fatalf("store holds %v, want %v", got, want)
	}

	if err := s.close(); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(listDir(t, directory), ".orch.lock") {
		t.Fatal("close left the lock behind")
	}
}

func TestStoreComposesUnitAddSetGetListAndCounts(t *testing.T) {
	_, s := initializedStore(t)

	added := must(s.addUnit("u1", "build", ptr("briefs/u1.md")))
	if added.ID != "u1" || added.State != "pending" {
		t.Fatalf("added %+v", added)
	}
	escaped := must(s.addUnit("=SUM(A1)", "+build", nil))
	if escaped.ID != "'=SUM(A1)" || escaped.Track != "'+build" {
		t.Fatalf("formula cells were not escaped: %+v", escaped)
	}

	updated := must(s.setUnit("u1", "done", ptr("poteto/u1"), ptr(int64(184530)), ptr("abc123")))
	want := unit{ID: "u1", Track: "build", State: "done", Branch: "poteto/u1", PR: "184530", SHA: "abc123", Brief: "briefs/u1.md"}
	if updated != want {
		t.Fatalf("set returned %+v, want %+v", updated, want)
	}
	if got := must(s.getUnit("u1")); got != updated {
		t.Fatalf("get returned %+v", got)
	}
	if got := must(s.listUnits(ptr("done"), ptr("build"))); !reflect.DeepEqual(got, []unit{updated}) {
		t.Fatalf("list returned %+v", got)
	}
	if got := countLine(must(s.unitCounts())); got != "done=1, pending=1" {
		t.Fatalf("counts are %q", got)
	}
	_, err := s.addUnit("u1", "build", nil)
	wantError(t, err, "unit u1 already exists")
	_, err = s.setUnit("missing", "done", nil, nil, nil)
	var notFound *notFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("set of a missing unit returned %v, want a not-found error", err)
	}
}

func TestStoreRecordsReplacesChecksAndSummarizesLedger(t *testing.T) {
	_, s := initializedStore(t)

	_, err := s.checkLedger(184530, "abc123")
	var notFound *notFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("check returned %v, want a not-found error", err)
	}
	if notFound.output == nil || notFound.output.compact != "NOT-VERIFIED" ||
		string(marshalJSON(notFound.output.json, false)) != `{"pr":"184530","sha":"abc123","verdict":"NOT-VERIFIED"}` {
		t.Fatalf("not-found output is %+v", notFound.output)
	}
	_, err = parseVerdict("looks-good")
	wantError(t, err, "verdict must be")

	recorded := must(s.recordLedger(184530, "abc123", "unit-test-verified", "reports/verify.md", ptr("opus")))
	if got := must(s.checkLedger(184530, "abc123")); got != recorded {
		t.Fatalf("check returned %+v, want %+v", got, recorded)
	}
	if got := string(marshalJSON(must(s.ledgerSummary()), false)); got != `{"unit-test-verified":1}` {
		t.Fatalf("summary is %s", got)
	}

	must(s.recordLedger(184530, "abc123", "live-ui-verified", "reports/live.md", nil))
	if got := string(marshalJSON(must(s.ledgerSummary()), false)); got != `{"live-ui-verified":1}` {
		t.Fatalf("summary is %s", got)
	}
}

func TestStorePushesPeeksAndAtomicallyDrainsInbox(t *testing.T) {
	directory, s := initializedStore(t)

	first := must(s.pushInbox("worker-1", "u1", "done", ptr("reports/u1.md")))
	if first.Unit != "u1" || first.Status != "done" || !strings.HasSuffix(first.filename, ".tsv") {
		t.Fatalf("push returned %+v", first)
	}
	must(s.pushInbox("worker-2", "u2", "failed", nil))

	if got := must(s.inboxCount()); got != 2 {
		t.Fatalf("count is %d", got)
	}
	if got := must(s.peekInbox()); len(got) != 2 {
		t.Fatalf("peek returned %d pointers", len(got))
	}
	if got := must(s.inboxCount()); got != 2 {
		t.Fatalf("count after peek is %d", got)
	}
	if got := must(s.drainInbox()); len(got) != 2 {
		t.Fatalf("drain returned %d pointers", len(got))
	}
	if got := must(s.inboxCount()); got != 0 {
		t.Fatalf("count after drain is %d", got)
	}
	if got := listDir(t, filepath.Join(directory, "inbox")); len(got) != 0 {
		t.Fatalf("inbox still holds %v", got)
	}
	for _, name := range listDir(t, directory) {
		if strings.HasPrefix(name, ".inbox-drain-") {
			t.Fatalf("drain left %s behind", name)
		}
	}
}

func TestStoreReplacesStaleLockWhoseHolderIsDead(t *testing.T) {
	directory, _ := initializedStore(t)
	exited := exec.Command("true")
	if err := exited.Run(); err != nil {
		t.Fatal(err)
	}
	pid := strconv.Itoa(exited.Process.Pid)
	writeText(t, filepath.Join(directory, ".orch.lock"), pid+"\n")

	var stale []string
	recovered := useStore(t, directory, storeOptions{onStaleLock: func(holder string) { stale = append(stale, holder) }})
	if got := must(recovered.addUnit("u1", "build", nil)); got.ID != "u1" {
		t.Fatalf("added %+v", got)
	}
	if !reflect.DeepEqual(stale, []string{pid}) {
		t.Fatalf("stale holders %v, want [%s]", stale, pid)
	}
	if err := recovered.close(); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(listDir(t, directory), ".orch.lock") {
		t.Fatal("close left the lock behind")
	}
}

func TestStoreBlocksWriterAndStealsLockOnlyWithForce(t *testing.T) {
	directory, s := initializedStore(t)
	if err := s.close(); err != nil {
		t.Fatal(err)
	}
	pid := strconv.Itoa(os.Getpid())
	writeText(t, filepath.Join(directory, ".orch.lock"), pid+"\n")

	blocked := useStore(t, directory, storeOptions{})
	_, err := blocked.addUnit("u1", "build", nil)
	wantError(t, err, "store lock held by pid "+pid)

	var stolen []string
	forced := useStore(t, directory, storeOptions{force: true, onLockStolen: func(holder string) { stolen = append(stolen, holder) }})
	if got := must(forced.addUnit("u1", "build", nil)); got.ID != "u1" {
		t.Fatalf("added %+v", got)
	}
	if !reflect.DeepEqual(stolen, []string{pid}) {
		t.Fatalf("stolen holders %v, want [%s]", stolen, pid)
	}
	if err := forced.close(); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(listDir(t, directory), ".orch.lock") {
		t.Fatal("close left the lock behind")
	}
}

func TestStoreParksGatesStoresStandingOrdersAndRendersStatus(t *testing.T) {
	directory, s := initializedStore(t)
	must(s.addUnit("u1", "build", nil))
	parked := must(s.parkGate("release", "Ship now?", "ship,wait", "wait"))
	if parked.resolved() || parked.ID != "release" {
		t.Fatalf("parked %+v", parked)
	}
	if got := must(s.addStanding("Never force push.")); got != (standingLine{1, "Never force push."}) {
		t.Fatalf("standing add returned %+v", got)
	}

	first := must(s.renderStatus())
	if first.Changed != "first render" || !reflect.DeepEqual(first.Summary.OpenGateIDs, []string{"release"}) {
		t.Fatalf("first render reported %q with open gates %v", first.Changed, first.Summary.OpenGateIDs)
	}
	if !strings.Contains(readText(t, filepath.Join(directory, "status.md")), "| release | open | Ship now? |") {
		t.Fatal("status.md lacks the gate row")
	}
	if got := must(s.renderStatus()).Changed; got != "no derived changes" {
		t.Fatalf("second render reported %q", got)
	}

	resolved := must(s.resolveGate("release", "ship"))
	if !resolved.resolved() || *resolved.Answer != "ship" {
		t.Fatalf("resolved %+v", resolved)
	}
	if got := must(s.renderStatus()).Changed; got != "open gates 1->0" {
		t.Fatalf("render after resolve reported %q", got)
	}
	if got := must(s.openGates()); len(got) != 0 {
		t.Fatalf("open gates %+v", got)
	}
	if got := must(s.showStanding()); !reflect.DeepEqual(got, []standingLine{{1, "Never force push."}}) {
		t.Fatalf("standing orders %+v", got)
	}
}

// forgeParam is how the TS tests passed the forge: gh by default, origin
// explicitly.
func forgeParam(forge string) *string {
	if forge == "gh" {
		return nil
	}
	return &forge
}

func TestStoreResolvesOrderedFrontierAndValidatesPin(t *testing.T) {
	for _, forge := range []string{"gh", "origin"} {
		t.Run(forge, func(t *testing.T) {
			directory := makeDirectory(t)
			stack := makeGitStack(t, directory)
			withFakeForge(t, directory, forge, "")
			s := useStore(t, directory, storeOptions{})
			must(s.init())

			got := must(s.setFrontier(stack.repo, forgeParam(forge), nil))
			lowest := int64(11)
			want := frontier{Generation: 1, PRs: []frontierPR{
				{10, "stack/merged", stack.mergedSHA, "MERGED"},
				{13, "stack/closed", stack.closedSHA, "CLOSED"},
				{11, "stack/open", stack.openSHA, "OPEN"},
				{14, "stack/draft", stack.draftSHA, "OPEN"},
			}, LowestUnmerged: &lowest}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("frontier is %+v, want %+v", got, want)
			}
			if got := must(s.setFrontier(stack.repo, forgeParam(forge), []int64{10, 13, 11, 14})); got.Generation != 2 {
				t.Fatalf("pinned set reached generation %d", got.Generation)
			}
			if got := must(s.showFrontier()); got.Generation != 2 {
				t.Fatalf("show reports generation %d", got.Generation)
			}
			_, err := s.setFrontier(stack.repo, forgeParam(forge), []int64{10, 11, 12})
			wantError(t, err, "frontier pin mismatch: missing from resolved chain: 12; extra in resolved chain: 13,14")
			_, err = s.setFrontier(stack.repo, forgeParam(forge), []int64{13, 10, 11, 14})
			wantError(t, err, "frontier pin mismatch: order differs: expected 13,10,11,14; resolved 10,13,11,14")
			_, err = s.setFrontier(stack.repo, forgeParam(forge), []int64{10, 10})
			wantError(t, err, "--prs must not contain duplicates")
		})
	}
}

func TestStoreResolvesReplacementPRAlongsideClosedHistory(t *testing.T) {
	for _, forge := range []string{"gh", "origin"} {
		t.Run(forge, func(t *testing.T) {
			for _, historicalState := range []string{"closed", "merged"} {
				directory := makeDirectory(t)
				stack := makeGitStack(t, directory)
				withFakeForge(t, directory, forge, forgeRows(forge,
					fixturePR{9, historicalState, "stack/open", "main"},
					fixturePR{11, "open", "stack/open", "stack/closed"},
				))
				s := useStore(t, directory, storeOptions{})
				must(s.init())
				got := must(s.setFrontier(stack.repo, &forge, nil))
				var numbers []int64
				for _, row := range got.PRs {
					numbers = append(numbers, row.PR)
				}
				if !reflect.DeepEqual(numbers, []int64{10, 13, 11, 14}) {
					t.Fatalf("%s history resolved %v", historicalState, numbers)
				}
				if got.LowestUnmerged == nil || *got.LowestUnmerged != 11 {
					t.Fatalf("%s history has lowest unmerged %v", historicalState, got.LowestUnmerged)
				}
			}
		})
	}
}

func TestStoreRejectsMultipleActivePRsOnOneBranch(t *testing.T) {
	for _, forge := range []string{"gh", "origin"} {
		t.Run(forge, func(t *testing.T) {
			directory := makeDirectory(t)
			stack := makeGitStack(t, directory)
			withFakeForge(t, directory, forge, forgeRows(forge,
				fixturePR{11, "open", "stack/open", "stack/closed"},
				fixturePR{12, "open", "stack/open", "main"},
			))
			s := useStore(t, directory, storeOptions{})
			must(s.init())
			_, err := s.setFrontier(stack.repo, &forge, nil)
			wantError(t, err, "multiple pull requests")
		})
	}
}

func TestStoreRejectsUnparseableForgeOutput(t *testing.T) {
	for _, forge := range []string{"gh", "origin"} {
		t.Run(forge, func(t *testing.T) {
			directory := makeDirectory(t)
			stack := makeGitStack(t, directory)
			withFakeForge(t, directory, forge, "this line is not forge output")
			s := useStore(t, directory, storeOptions{})
			must(s.init())
			_, err := s.setFrontier(stack.repo, &forge, nil)
			wantError(t, err, forge+" pr list --head stack/open output is not valid JSON")
		})
	}
}

func TestStoreNamesMissingPullRequest(t *testing.T) {
	directory := makeDirectory(t)
	stack := makeGitStack(t, directory)
	withFakeForge(t, directory, "gh", "[]")
	s := useStore(t, directory, storeOptions{})
	must(s.init())
	_, err := s.setFrontier(stack.repo, nil, nil)
	wantError(t, err, "branch stack/open has no pull request; open one with gh pr create")
}

func TestStoreRejectsMalformedData(t *testing.T) {
	directory, s := initializedStore(t)

	writeText(t, filepath.Join(directory, "units.tsv"), "wrong\n")
	_, err := s.listUnits(nil, nil)
	wantError(t, err, "units.tsv has an invalid header")
	writeText(t, filepath.Join(directory, "units.tsv"), "id\ttrack\tstate\tbranch\tpr\tsha\tbrief\nshort\trow\n")
	_, err = s.listUnits(nil, nil)
	wantError(t, err, "units.tsv has a malformed row")

	writeText(t, filepath.Join(directory, "ledger.tsv"), "pr\tsha\tverdict\tevidence\tverifier\tts\n1\tsha\tinvalid\treport\tme\tnow\n")
	_, err = s.ledgerSummary()
	wantError(t, err, "ledger.tsv has invalid verdict invalid")

	writeText(t, filepath.Join(directory, "frontier.json"), `{"generation":"1"}`+"\n")
	_, err = s.showFrontier()
	wantError(t, err, "frontier.json has an invalid shape")

	writeText(t, filepath.Join(directory, "inbox", "bad.tsv"), "too\tshort\n")
	_, err = s.peekInbox()
	wantError(t, err, "inbox pointer bad.tsv is malformed")
}

func TestStoreRejectsOperationsAfterClose(t *testing.T) {
	_, s := initializedStore(t)
	if err := s.close(); err != nil {
		t.Fatal(err)
	}
	_, err := s.listUnits(nil, nil)
	wantError(t, err, "store is closed")
	_, err = s.renderStatus()
	var user *userError
	if !errors.As(err, &user) {
		t.Fatalf("render after close returned %v, want a user error", err)
	}
}

func deadPID(t *testing.T) string {
	t.Helper()
	exited := exec.Command("true")
	if err := exited.Run(); err != nil {
		t.Fatal(err)
	}
	return strconv.Itoa(exited.Process.Pid)
}

func TestStoreWritesPlainFileFormats(t *testing.T) {
	directory, s := initializedStore(t)
	for name, want := range map[string]string{
		"units.tsv": unitHeader + "\n", "ledger.tsv": ledgerHeader + "\n", "gates.md": "", "preferences.md": "", "frontier.json": "{}\n",
	} {
		if got := readText(t, filepath.Join(directory, name)); got != want {
			t.Errorf("init wrote %s as %q, want %q", name, got, want)
		}
	}

	must(s.addUnit("=SUM(A1)", "tab\there", ptr("line\nbreak\r")))
	must(s.addUnit("u2", "@at", nil))
	must(s.setUnit("u2", "done", ptr("-b"), ptr(int64(12)), ptr("abc")))
	if got, want := readText(t, filepath.Join(directory, "units.tsv")),
		unitHeader+"\n'=SUM(A1)\ttab here\tpending\t\t\t\tline break \nu2\t'@at\tdone\t'-b\t12\tabc\t\n"; got != want {
		t.Errorf("units.tsv is %q, want %q", got, want)
	}
	if got := readText(t, filepath.Join(directory, lockFile)); got != strconv.Itoa(os.Getpid())+"\n" {
		t.Errorf("lock file holds %q", got)
	}

	entry := must(s.recordLedger(3, "sha", "verifier-blocked", "+e", ptr("me")))
	if got, want := readText(t, filepath.Join(directory, "ledger.tsv")), ledgerHeader+"\n3\tsha\tverifier-blocked\t'+e\tme\t"+entry.TS+"\n"; got != want {
		t.Errorf("ledger.tsv is %q, want %q", got, want)
	}
	if !regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z$`).MatchString(entry.TS) {
		t.Errorf("ledger timestamp is %q", entry.TS)
	}

	pushed := must(s.pushInbox("w", "u", "=bad", nil))
	if got := readText(t, filepath.Join(directory, "inbox", pushed.filename)); got != pushed.TS+"\tw\tu\t'=bad\t\n" {
		t.Errorf("inbox pointer is %q", got)
	}

	must(s.parkGate("g1", "Ship\nnow?", "a|b", "a"))
	must(s.parkGate("g2", "q", "o", "d"))
	must(s.resolveGate("g1", "b: yes"))
	if got, want := readText(t, filepath.Join(directory, "gates.md")),
		"# Gates\n\n## g1\n\n- Status: resolved\n- Question: Ship now?\n- Options: a|b\n- Default: a\n- Answer: b: yes\n\n## g2\n\n- Status: open\n- Question: q\n- Options: o\n- Default: d\n"; got != want {
		t.Errorf("gates.md is %q, want %q", got, want)
	}

	must(s.addStanding("Never force push."))
	must(s.addStanding("  Rebase\nfirst  "))
	if got := readText(t, filepath.Join(directory, "preferences.md")); got != "1. Never force push.\n2. Rebase first\n" {
		t.Errorf("preferences.md is %q", got)
	}

	for _, name := range listDir(t, directory) {
		if strings.HasSuffix(name, ".tmp") {
			t.Errorf("an atomic write left %s behind", name)
		}
	}
}

func TestStoreRendersStatusMarkdownAndDerivedChanges(t *testing.T) {
	directory, s := initializedStore(t)
	must(s.addUnit("u1", "a|b", nil))
	must(s.recordLedger(1, "abc", "unit-test-verified", `C:\e`, nil))
	must(s.parkGate("g1", "q?", "x|y", "x"))
	writeText(t, filepath.Join(directory, "frontier.json"), `{"generation":5,"prs":[{"pr":4,"branches":"c","sha":"s","state":"OPEN"}],"lowestUnmerged":4}`)

	report := must(s.renderStatus())
	want := "# Orchestrate status\n\nGenerated: <ts>\n\n## Units\n\nStates: pending=1\n\n" +
		"| ID | Track | State | Branch | PR | SHA | Brief |\n| --- | --- | --- | --- | --- | --- | --- |\n| u1 | a\\|b | pending |  |  |  |  |\n\n" +
		"## Verification ledger\n\nVerdicts: unit-test-verified=1\n\n" +
		"| PR | SHA | Verdict | Evidence | Verifier | Timestamp |\n| --- | --- | --- | --- | --- | --- |\n| 1 | abc | unit-test-verified | C:\\\\e |  | <ts> |\n\n" +
		"## Frontier\n\nGeneration: 5\nLowest unmerged: 4\n\n| Branch | PR | SHA | State |\n| --- | --- | --- | --- |\n| c | 4 | s | OPEN |\n\n" +
		"## Gates\n\n| ID | Status | Question | Options | Default | Answer |\n| --- | --- | --- | --- | --- | --- |\n| g1 | open | q? | x\\|y | x |  |\n\n" +
		`<!-- orch-summary {"unitStates":{"pending":1},"ledgerVerdicts":{"unit-test-verified":1},"frontierGeneration":5,"openGateIds":["g1"]} -->` + "\n"
	got := regexp.MustCompile(`\d{4}-\d\d-\d\dT[\d:.]+Z`).ReplaceAllString(readText(t, filepath.Join(directory, "status.md")), "<ts>")
	if got != want || report.Changed != "first render" {
		t.Fatalf("status.md is\n%q\nwant\n%q\n(changed %q)", got, want, report.Changed)
	}

	for previous, change := range map[string]string{
		`{"unitStates":{"pending":1},"ledgerVerdicts":{"unit-test-verified":1},"frontierGeneration":1.5,"openGateIds":["g1"]}`:       "frontier generation 1.5->5",
		`{"unitStates":{"pending":1},"ledgerVerdicts":{"unit-test-verified":1},"frontierGeneration":1e21,"openGateIds":["g1"]}`:      "frontier generation 1e+21->5",
		`{"unitStates":{"pending":1},"ledgerVerdicts":{"unit-test-verified":1},"frontierGeneration":0.000001,"openGateIds":[]}`:      "frontier generation 0.000001->5; open gates 0->1",
		`{"unitStates":{"pending":1,"constructor":2,"__proto__":4},"ledgerVerdicts":{},"frontierGeneration":5,"openGateIds":["g1"]}`: "units constructor 2->function Object() { [native code] }; ledger unit-test-verified 0->1",
		`{"unitStates":{"done":-1},"ledgerVerdicts":{},"frontierGeneration":5,"openGateIds":[]}`:                                     "first render",
		`{"unitStates":{},"ledgerVerdicts":{},"frontierGeneration":5,"openGateIds":[1]}`:                                             "first render",
		`not json`: "first render",
	} {
		writeText(t, filepath.Join(directory, "status.md"), "x\n<!-- orch-summary "+previous+" -->\n")
		if got := must(s.renderStatus()).Changed; got != change {
			t.Errorf("after %s render reported %q, want %q", previous, got, change)
		}
	}
}

func TestStoreKeepsLineSeparatorsRaw(t *testing.T) {
	directory, s := initializedStore(t)
	must(s.addUnit("u1", "t", nil))
	must(s.setUnit("u1", "line\u2028sep", nil, nil, nil))
	// The raw separator keeps the summary pattern from matching, so every
	// render reports a first render, as it always has.
	for range 2 {
		if got := must(s.renderStatus()).Changed; got != "first render" {
			t.Fatalf("render reported %q", got)
		}
	}
	if !strings.Contains(readText(t, filepath.Join(directory, "status.md")), `{"unitStates":{"line`+"\u2028"+`sep":1}`) {
		t.Fatal("status.md escaped the line separator")
	}
	if got := string(marshalJSON([]string{"a\u2028b", `c\u2029d`, "<&>"}, false)); got != `["a`+"\u2028"+`b","c\\u2029d","<&>"]` {
		t.Fatalf("marshalJSON gave %q", got)
	}
}

func TestStoreReadsLockHolders(t *testing.T) {
	live := strconv.Itoa(os.Getpid())
	for contents, holder := range map[string]string{
		"":                   "unknown",
		"abc\n":              "abc",
		"0\n":                "0",
		"+5\n":               "+5",
		"2147483648\n":       "2147483648",
		"99999999999\n":      "99999999999",
		"  " + live + "  \n": live,
	} {
		directory, s := initializedStore(t)
		if err := s.close(); err != nil {
			t.Fatal(err)
		}
		writeText(t, filepath.Join(directory, lockFile), contents)
		_, err := useStore(t, directory, storeOptions{}).addUnit("u1", "t", nil)
		wantError(t, err, "store lock held by pid "+holder)

		var stolen []string
		forced := useStore(t, directory, storeOptions{force: true, onLockStolen: func(h string) { stolen = append(stolen, h) }})
		must(forced.addUnit("u1", "t", nil))
		if !reflect.DeepEqual(stolen, []string{holder}) {
			t.Errorf("lock %q was stolen from %v, want [%s]", contents, stolen, holder)
		}
	}
}

func TestStorePeeksInboxInCollationOrder(t *testing.T) {
	directory, s := initializedStore(t)
	inbox := filepath.Join(directory, "inbox")
	for name, contents := range map[string]string{
		"crlf.tsv": "t\ta\tu\ts\tr\r\n", "B.tsv": "1\tB\tu\ts\t\n", "a.tsv": "2\ta\tu\ts\t", "_x.tsv": "3\t_x\tu\ts\t\n",
		"-x.tsv": "4\t-x\tu\ts\t\n", "10.tsv": "5\t10\tu\ts\t\n", "9.tsv": "6\t9\tu\ts\t\n", "notes.txt": "ignored\n",
	} {
		writeText(t, filepath.Join(inbox, name), contents)
	}
	if err := os.Mkdir(filepath.Join(inbox, "dir.tsv"), 0o755); err != nil {
		t.Fatal(err)
	}
	var agents []string
	for _, pointer := range must(s.peekInbox()) {
		agents = append(agents, pointer.Agent)
	}
	if want := []string{"_x", "-x", "10", "9", "a", "B", "a"}; !reflect.DeepEqual(agents, want) {
		t.Fatalf("peek order %v, want %v", agents, want)
	}
}

func TestCountValuesKeepsObjectPrototypeQuirk(t *testing.T) {
	got := countValues([]string{"b", "a", "10", "2", "01", "4294967295", "__proto__", "constructor", "constructor", "toString", "B", "in-progress", "in_progress"})
	want := `{"2":1,"10":1,"01":1,"4294967295":1,"a":1,"b":1,"B":1,"constructor":"function Object() { [native code] }11","in_progress":1,"in-progress":1,"toString":"function toString() { [native code] }1"}`
	if s := string(marshalJSON(got, false)); s != want {
		t.Fatalf("countValues = %s, want %s", s, want)
	}
	if line := countLine(got); !strings.HasPrefix(line, "2=1, 10=1, 01=1, 4294967295=1, a=1, b=1, B=1, constructor=function Object() { [native code] }11") {
		t.Fatalf("countLine = %s", line)
	}
}
