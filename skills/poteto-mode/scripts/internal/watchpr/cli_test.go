package watchpr

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestParseArgsDefaults(t *testing.T) {
	parsed, err := parseArgs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.owner != nil || parsed.repo != nil || parsed.pr != nil || parsed.mode != "single" ||
		len(parsed.stackPrs) != 0 || parsed.statusOnly || parsed.pretty {
		t.Errorf("parsed = %+v", parsed)
	}
	want := PollingOptions{Interval: 60, SweepInterval: 300, Timeout: 0, MaxQueryErrors: 5, AllowDraft: false}
	if parsed.polling != want {
		t.Errorf("polling = %+v, want %+v", parsed.polling, want)
	}
}

func TestParseArgsFrozenQueuedStack(t *testing.T) {
	parsed, err := parseArgs([]string{
		"--queued-stack", "--stack-prs", "#10, 11,#12", "--interval", "2.5", "--sweep-interval", "30",
		"--timeout", "0", "--max-query-errors", "3", "--allow-draft", "--pretty",
	})
	if err != nil {
		t.Fatal(err)
	}
	if parsed.mode != "queued-stack" {
		t.Errorf("mode = %q", parsed.mode)
	}
	if !reflect.DeepEqual(parsed.stackPrs, []PrNumber{10, 11, 12}) {
		t.Errorf("stackPrs = %v", parsed.stackPrs)
	}
	want := PollingOptions{Interval: 2.5, SweepInterval: 30, Timeout: 0, MaxQueryErrors: 3, AllowDraft: true}
	if parsed.polling != want {
		t.Errorf("polling = %+v, want %+v", parsed.polling, want)
	}
	if !parsed.pretty {
		t.Error("pretty = false")
	}
}

func TestRejectsInvalidModesAndNumbersAsUsage(t *testing.T) {
	for _, argv := range [][]string{
		{"--unknown"},
		{"--interval", "0"},
		{"--sweep-interval", "-1"},
		{"--timeout", "-1"},
		{"--max-query-errors", "1.5"},
		{"--stack", "--queued-stack"},
		{"--stack-prs", "1,2"},
		{"--queued-stack", "--stack-prs", "1,1"},
	} {
		h := newHarness(newFakeReader(fakeOptions{}))
		if code := run(argv, h.runtime); code != 64 {
			t.Errorf("%v: exit = %d, want 64", argv, code)
		}
		if len(h.stdout.chunks) != 0 {
			t.Errorf("%v: stdout = %q", argv, h.stdout.chunks)
		}
		if !strings.Contains(h.stderr.String(), "error:") {
			t.Errorf("%v: stderr = %q", argv, h.stderr.String())
		}
	}
}

var renderContext = PrContext{Owner: "owner", Repo: "repo", Number: 1}

func mergedStatusVerdict() Verdict {
	head, mergedAt := "head", "now"
	return Verdict{
		Sequence: 1, ObservedAt: "2026-07-26T00:00:00.000Z", Mode: "single", Kind: "STATUS", Terminal: true, ExitCode: 0,
		StatusReason: "status-only",
		Rows: []PrSnapshot{{Kind: "merged", Context: renderContext, Facts: PullRequestFacts{
			Context: renderContext, Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN", ReviewDecision: "APPROVED",
			HeadRefOid: &head, HeadRefName: "feature", BaseRefName: "main", State: "MERGED", MergedAt: &mergedAt,
		}}},
	}
}

func TestRenderJSONIsCompactWithTSKeyOrder(t *testing.T) {
	want := `{"schemaVersion":1,"sequence":1,"observedAt":"2026-07-26T00:00:00.000Z","mode":"single","kind":"STATUS","terminal":true,"exitCode":0,"reason":"status-only","rows":[{"kind":"merged","context":{"owner":"owner","repo":"repo","number":1},"facts":{"context":{"owner":"owner","repo":"repo","number":1},"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","reviewDecision":"APPROVED","headRefOid":"head","headRefName":"feature","baseRefName":"main","state":"MERGED","mergedAt":"now","isDraft":false}}]}` + "\n"
	if got := renderJSON(mergedStatusVerdict()); got != want {
		t.Errorf("renderJSON =\n%s\nwant\n%s", got, want)
	}
}

func TestRenderPrettyTableFromTheSameVerdict(t *testing.T) {
	rendered := renderPretty(mergedStatusVerdict())
	for _, want := range []string{"| PR | CI | Review | Merge |", "| [#1](https://github.com/owner/repo/pull/1) | — | — | ✅ merged |"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered %q lacks %q", rendered, want)
		}
	}
}

func TestMainUsageErrorsGoOnlyToStderr(t *testing.T) {
	h := newHarness(newFakeReader(fakeOptions{}))
	if code := run([]string{"--interval", "0"}, h.runtime); code != 64 {
		t.Errorf("exit = %d, want 64", code)
	}
	if len(h.stdout.chunks) != 0 {
		t.Errorf("stdout = %q", h.stdout.chunks)
	}
	if want := `invalid --interval "0"`; !strings.Contains(h.stderr.String(), want) {
		t.Errorf("stderr = %q, want it to contain %q", h.stderr.String(), want)
	}
}

func TestMainQueuedStackStatusOnlyBypassesTheQueueMachine(t *testing.T) {
	h := newHarness(newFakeReader(fakeOptions{}))
	code := run([]string{"--owner", "owner", "--repo", "repo", "--queued-stack", "--stack-prs", "1", "--status-only"}, h.runtime)
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if len(h.stdout.chunks) != 1 {
		t.Fatalf("stdout writes = %d, want 1", len(h.stdout.chunks))
	}
	out := h.stdout.chunks[0]
	for _, want := range []string{`"mode":"queued-stack","kind":"STATUS","terminal":true,"exitCode":0`} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout %s lacks %s", out, want)
		}
	}
	if strings.Contains(out, `"kind":"QUEUE"`) {
		t.Errorf("stdout %s contains a QUEUE event", out)
	}
}

func TestMainHiddenGitHubRefusalExitsFour(t *testing.T) {
	reader := newFakeReader(fakeOptions{
		facts:         func(f *PullRequestFacts) { f.MergeStateStatus = "BLOCKED" },
		fastPath:      fastChecks(passingCheck("ci")),
		commitRollups: []CommitRollup{{Oid: "head", State: "FAILURE"}},
	})
	h := newHarness(reader)
	if code := run([]string{"--owner", "owner", "--repo", "repo", "--pr", "1"}, h.runtime); code != 4 {
		t.Errorf("exit = %d, want 4", code)
	}
	if len(h.stdout.chunks) != 1 {
		t.Fatalf("stdout writes = %d, want 1", len(h.stdout.chunks))
	}
	out := h.stdout.chunks[0]
	for _, want := range []string{`"kind":"BLOCKER"`, `"exitCode":4`, `"blocker":{"kind":"failing-checks"`, `"kind":"ci-github-rejected"`} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout %s lacks %s", out, want)
		}
	}
}

func TestMainShowsHelpWithoutTouchingTheReader(t *testing.T) {
	reader := newFakeReader(fakeOptions{})
	h := newHarness(reader)
	if code := run([]string{"--help"}, h.runtime); code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if !strings.Contains(h.stdout.String(), "JSON (NDJSON while polling)") {
		t.Errorf("stdout = %q", h.stdout.String())
	}
	if len(reader.calls) != 0 {
		t.Errorf("calls = %v", reader.calls)
	}
}

func TestHelpOutranksEveryMistakeButABadValue(t *testing.T) {
	cases := []struct {
		args []string
		want int
	}{
		{[]string{"--bogus", "--help"}, 0},
		{[]string{"--stack=1", "-h"}, 0},
		{[]string{"--", "--help"}, 0},
		{[]string{"--help", "--timeout", "-1"}, 64},
		{[]string{"--help", "--owner"}, 64},
	}
	for _, c := range cases {
		var stdout, stderr strings.Builder
		if code := Main(c.args, &stdout, &stderr); code != c.want {
			t.Errorf("%q: exit = %d, want %d (stderr %q)", c.args, code, c.want, stderr.String())
		}
		if c.want == 0 && stdout.String() != helpText {
			t.Errorf("%q: stdout = %q, want the help", c.args, stdout.String())
		}
	}
}

func mergeQueueReader() Reader {
	base := newFakeReader(fakeOptions{
		fastPath: fastChecks(
			Check{Kind: "passed", Name: "build", ReportedState: "SUCCESS", Link: "https://ci.example/build/1", Workflow: "CI"},
			Check{Kind: "skipped", Name: "lint", ReportedState: "SKIPPED", Workflow: "CI"},
		),
		commitRollups: []CommitRollup{{Oid: "head1", State: "SUCCESS"}},
	})
	reads := map[PrNumber]int{}
	openReads := map[PrNumber]int{10: 1, 11: 2}
	mergedAt := map[PrNumber]string{10: "2026-07-26T01:00:00Z", 11: "2026-07-26T02:00:00Z"}
	return hookReader{Reader: base, pullRequest: func(pr PrContext) (PullRequestFacts, error) {
		facts, err := base.PullRequest(pr)
		reads[pr.Number]++
		facts.HeadRefOid, facts.HeadRefName = ptr("head1"), "f"+pr.Number.String()
		if pr.Number == 11 {
			facts.BaseRefName = "f10"
		}
		if reads[pr.Number] > openReads[pr.Number] {
			facts.State, facts.MergedAt = "MERGED", ptr(mergedAt[pr.Number])
		}
		return facts, err
	}}
}

func TestQueuedStackEmitsOneNDJSONLinePerEvent(t *testing.T) {
	stdout := &writes{}
	var sleeps []float64
	rt := runtime{reader: mergeQueueReader(), clock: &fakeClock{sleep: advancingSleep(&sleeps)}, stdout: stdout, stderr: &bytes.Buffer{}}
	if code := run(strings.Fields("--owner octo --repo hello --queued-stack --stack-prs 10,#11 --interval 0.001"), rt); code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	want := []string{
		`{"schemaVersion":1,"sequence":1,"observedAt":"2026-07-26T00:00:00.000Z","mode":"queued-stack","kind":"QUEUE","terminal":false,"queue":[{"owner":"octo","repo":"hello","number":10},{"owner":"octo","repo":"hello","number":11}]}`,
		`{"schemaVersion":1,"sequence":2,"observedAt":"2026-07-26T00:00:00.000Z","mode":"queued-stack","kind":"STATUS","terminal":false,"reason":"whole-stack-sweep","rows":[{"kind":"open","context":{"owner":"octo","repo":"hello","number":10},"facts":{"context":{"owner":"octo","repo":"hello","number":10},"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","reviewDecision":"APPROVED","headRefOid":"head1","headRefName":"f10","baseRefName":"main","state":"OPEN","mergedAt":null,"isDraft":false},"threads":[],"ci":{"source":"gh-pr-checks","all":[{"name":"build","description":"","link":"https://ci.example/build/1","workflow":"CI","kind":"passed","reportedState":"SUCCESS"},{"name":"lint","description":"","link":"","workflow":"CI","kind":"skipped","reportedState":"SKIPPED"}],"hadPreviousPassingCi":false,"kind":"ci-clean","failed":[],"pending":[],"github":{"kind":"allowed","basis":"merge-state","mergeStateStatus":"CLEAN","headRollupState":"SUCCESS"}},"reviewAutomationRunning":false},{"kind":"open","context":{"owner":"octo","repo":"hello","number":11},"facts":{"context":{"owner":"octo","repo":"hello","number":11},"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","reviewDecision":"APPROVED","headRefOid":"head1","headRefName":"f11","baseRefName":"f10","state":"OPEN","mergedAt":null,"isDraft":false},"threads":[],"ci":{"source":"gh-pr-checks","all":[{"name":"build","description":"","link":"https://ci.example/build/1","workflow":"CI","kind":"passed","reportedState":"SUCCESS"},{"name":"lint","description":"","link":"","workflow":"CI","kind":"skipped","reportedState":"SKIPPED"}],"hadPreviousPassingCi":false,"kind":"ci-clean","failed":[],"pending":[],"github":{"kind":"allowed","basis":"merge-state","mergeStateStatus":"CLEAN","headRollupState":"SUCCESS"}},"reviewAutomationRunning":false}]}`,
		`{"schemaVersion":1,"sequence":3,"observedAt":"2026-07-26T00:00:00.000Z","mode":"queued-stack","kind":"WAITING","terminal":false,"frontier":{"owner":"octo","repo":"hello","number":10},"reason":{"kind":"merge-queue","unmergedCount":2}}`,
		`{"schemaVersion":1,"sequence":4,"observedAt":"2026-07-26T00:00:00.000Z","mode":"queued-stack","kind":"ADVANCE","terminal":false,"merged":{"owner":"octo","repo":"hello","number":10},"frontier":{"owner":"octo","repo":"hello","number":11},"remaining":1}`,
		`{"schemaVersion":1,"sequence":5,"observedAt":"2026-07-26T00:00:00.000Z","mode":"queued-stack","kind":"WAITING","terminal":false,"frontier":{"owner":"octo","repo":"hello","number":11},"reason":{"kind":"merge-queue","unmergedCount":1}}`,
		`{"schemaVersion":1,"sequence":6,"observedAt":"2026-07-26T00:00:00.000Z","mode":"queued-stack","kind":"COMPLETE","terminal":true,"exitCode":0,"queue":[{"owner":"octo","repo":"hello","number":10},{"owner":"octo","repo":"hello","number":11}],"merged":[{"kind":"merged-pr","context":{"owner":"octo","repo":"hello","number":10},"mergedAt":"2026-07-26T01:00:00Z"},{"kind":"merged-pr","context":{"owner":"octo","repo":"hello","number":11},"mergedAt":"2026-07-26T02:00:00Z"}]}`,
	}
	if len(stdout.chunks) != len(want) {
		t.Fatalf("writes = %d, want %d:\n%s", len(stdout.chunks), len(want), stdout)
	}
	for i, line := range want {
		if got := stdout.chunks[i]; got != line+"\n" {
			t.Errorf("line %d =\n%s\nwant\n%s", i+1, got, line)
		}
	}
	if !reflect.DeepEqual(sleeps, []float64{0.001, 0.001}) {
		t.Errorf("sleeps = %v", sleeps)
	}
}

type scenario struct {
	name   string
	args   string
	reader func() Reader
	exit   int
	pretty string
	json   []string
}

func failedBuild() Check {
	return Check{Kind: "failed", Name: "build", ReportedState: "FAILURE", Description: "2 tests failed", Link: "https://ci.example/build/2"}
}

func failingReader() Reader {
	return newFakeReader(fakeOptions{
		fastPath:      fastChecks(failedBuild(), pendingCheck("deploy")),
		commitRollups: []CommitRollup{{Oid: "old", State: "SUCCESS"}, {Oid: "head", State: "FAILURE"}},
	})
}

func stackReader(open []OpenPullRequest, facts map[PrNumber]func(*PullRequestFacts), checks map[PrNumber]*ChecksFastPath) func() Reader {
	return func() Reader { return perPr(newFakeReader(fakeOptions{openPullRequests: open}), facts, checks) }
}

func failingRead(failure QueryFailure) func() Reader {
	return func() Reader {
		return hookReader{Reader: newFakeReader(fakeOptions{}), pullRequest: func(PrContext) (PullRequestFacts, error) {
			return PullRequestFacts{}, queryError(failure)
		}}
	}
}

const readyLine = "READY: no merge conflicts, no unresolved review threads, no failing or pending checks\n"

var stack20To22 = []OpenPullRequest{
	{Number: 20, HeadRefName: "f20", BaseRefName: "main"},
	{Number: 21, HeadRefName: "f21", BaseRefName: "f20"},
	{Number: 22, HeadRefName: "f22", BaseRefName: "f21"},
}

var scenarios = []scenario{
	{
		name:   "single ready",
		args:   "--owner octo --repo hello --pr 1",
		reader: func() Reader { return newFakeReader(fakeOptions{}) },
		pretty: readyLine + "mergeStateStatus=CLEAN\nreviewDecision=APPROVED\nisDraft=false\n",
		json: []string{
			`"github":{"kind":"allowed","basis":"merge-state","mergeStateStatus":"CLEAN","headRollupState":"SUCCESS"}},"gate":{"state":"OPEN","reviewDecision":"APPROVED","draft":"not-draft"}`,
		},
	},
	{
		name: "draft allowed",
		args: "--owner octo --repo hello --pr 6 --allow-draft",
		reader: func() Reader {
			return newFakeReader(fakeOptions{facts: func(f *PullRequestFacts) { f.IsDraft, f.MergeStateStatus, f.ReviewDecision = true, "DRAFT", "" }})
		},
		pretty: readyLine + "mergeStateStatus=DRAFT\nreviewDecision=null\nisDraft=true\nnote=draft allowed (--allow-draft); leave draft — do not mark ready\n",
		json:   []string{`"gate":{"state":"OPEN","reviewDecision":null,"draft":"draft-allowed"}`},
	},
	{
		name:   "failing check",
		args:   "--owner octo --repo hello --pr 2",
		reader: failingReader,
		exit:   4,
		pretty: "BLOCKER: failing-checks\npr=2\nfailed=1\nbuild FAILURE 2 tests failed https://ci.example/build/2\n",
		json:   []string{`"blocker":{"kind":"failing-checks","pr":{"owner":"octo","repo":"hello","number":2},"ci":{"source":"gh-pr-checks"`},
	},
	{
		name:   "failing check status only",
		args:   "--owner octo --repo hello --pr 2 --status-only",
		reader: failingReader,
		pretty: "| PR | CI | Review | Merge |\n| --- | --- | --- | --- |\n| [#2](https://github.com/octo/hello/pull/2) | ❌ 1 failed, 1 pending, was ✅ | ✅ | ✅ |\n",
	},
	{
		name: "github rejected",
		args: "--owner octo --repo hello --pr 60",
		reader: func() Reader {
			return newFakeReader(fakeOptions{
				facts:         func(f *PullRequestFacts) { f.MergeStateStatus = "BLOCKED" },
				commitRollups: []CommitRollup{{Oid: "head", State: "ERROR"}},
			})
		},
		exit:   4,
		pretty: "BLOCKER: failing-checks\npr=60\nfailed=0\nmergeStateStatus=BLOCKED\nheadRollupState=ERROR\n",
		json:   []string{`"kind":"ci-github-rejected","failed":[],"pending":[],"github":{"kind":"refused","mergeStateStatus":"BLOCKED","headRollupState":"ERROR"}}`},
	},
	{
		name: "pending status only",
		args: "--owner octo --repo hello --pr 3 --status-only",
		reader: func() Reader {
			return newFakeReader(fakeOptions{
				fastPath:      fastChecks(pendingCheck("build"), pendingCheck("Bugbot review")),
				commitRollups: []CommitRollup{{Oid: "old", State: "SUCCESS"}, {Oid: "head", State: "PENDING"}},
			})
		},
		pretty: "| PR | CI | Review | Merge |\n| --- | --- | --- | --- |\n| [#3](https://github.com/octo/hello/pull/3) | ⏳ 2 pending, was ✅ | 🤖 running | ✅ |\n",
		json:   []string{`"hadPreviousPassingCi":true,"kind":"ci-pending","failed":[],"pending":[{"name":"build",`, `"reviewAutomationRunning":true}`},
	},
	{
		name: "pending timeout",
		args: "--owner octo --repo hello --pr 3 --timeout 30",
		reader: func() Reader {
			return newFakeReader(fakeOptions{fastPath: fastChecks(pendingCheck("build"), pendingCheck("test"))})
		},
		exit:   5,
		pretty: "WAITING: frontier=#3; 2 checks pending\nWAITING: frontier=#3; 2 checks pending\nTIMEOUT: checks still pending\n",
		json: []string{
			`"kind":"WAITING","terminal":false,"frontier":{"owner":"octo","repo":"hello","number":3},"reason":{"kind":"pending-checks","pending":[{"name":"build"`,
			`"kind":"TIMEOUT","terminal":true,"exitCode":5,"reason":{"kind":"pending-checks","pending":[{"name":"build"`,
		},
	},
	{
		name: "review threads",
		args: "--owner octo --repo hello --pr 4",
		reader: func() Reader {
			return newFakeReader(fakeOptions{threads: []ReviewThread{
				{ID: "PRRT_human", FirstComment: &ReviewComment{AuthorLogin: ptr("octocat"), Body: "Please rename this.\r\nSecond line", Path: ptr("src/a.ts"), Line: ptr(12), CreatedAt: "now"}},
				{ID: "PRRT_long", FirstComment: &ReviewComment{Body: strings.Repeat("x", 200), CreatedAt: "now"}},
				{ID: "PRRT_empty"},
			}})
		},
		exit: 3,
		pretty: "BLOCKER: review-threads\npr=4\nunresolved=3\n" +
			"PRRT_human src/a.ts 12 octocat isBugBot=false bugbotReviewPasses=0 Please rename this.\n" +
			"PRRT_long None None None isBugBot=false bugbotReviewPasses=0 " + strings.Repeat("x", 180) + "\n" +
			"PRRT_empty None None None isBugBot=false bugbotReviewPasses=0 \n",
		json: []string{
			`{"id":"PRRT_human","firstComment":{"authorLogin":"octocat","body":"Please rename this.\r\nSecond line","path":"src/a.ts","line":12,"createdAt":"now"},"isBugbot":false,"bugbotReviewPasses":0}`,
			`{"id":"PRRT_empty","firstComment":null,"isBugbot":false,"bugbotReviewPasses":0}`,
		},
	},
	{
		name: "stack upstack conflict",
		args: "--owner octo --repo hello --stack --pr 21",
		reader: stackReader(stack20To22,
			map[PrNumber]func(*PullRequestFacts){22: func(f *PullRequestFacts) { f.Mergeable, f.MergeStateStatus = "CONFLICTING", "DIRTY" }},
			map[PrNumber]*ChecksFastPath{20: fastChecks(failedBuild())}),
		exit: 2,
		pretty: "| PR | CI | Review | Merge |\n| --- | --- | --- | --- |\n" +
			"| [#20](https://github.com/octo/hello/pull/20) | ❌ 1 failed | ✅ | ✅ |\n" +
			"| [#21](https://github.com/octo/hello/pull/21) | ✅ | ✅ | ✅ |\n" +
			"| [#22](https://github.com/octo/hello/pull/22) | ✅ | ✅ | ⚠️ conflict |\n" +
			"BLOCKER: merge-conflicts\npr=22\nmergeable=CONFLICTING\nmergeStateStatus=DIRTY\naction=resolve merge conflicts before waiting for CI\n",
		json: []string{
			`"mode":"stack","kind":"STATUS","terminal":false,"reason":"poll","rows":[`,
			`"blocker":{"kind":"merge-conflicts","pr":{"owner":"octo","repo":"hello","number":22},"facts":{"context":{"owner":"octo","repo":"hello","number":22},"mergeable":"CONFLICTING"`,
		},
	},
	{
		name: "stack ready with a merged bottom",
		args: "--owner octo --repo hello --stack --pr 50",
		reader: stackReader(
			[]OpenPullRequest{{Number: 50, HeadRefName: "f50", BaseRefName: "main"}, {Number: 51, HeadRefName: "f51", BaseRefName: "f50"}},
			map[PrNumber]func(*PullRequestFacts){50: func(f *PullRequestFacts) { f.State, f.MergedAt = "MERGED", ptr("2026-07-26T00:00:00Z") }},
			nil),
		pretty: "| PR | CI | Review | Merge |\n| --- | --- | --- | --- |\n" +
			"| [#50](https://github.com/octo/hello/pull/50) | — | — | ✅ merged |\n" +
			"| [#51](https://github.com/octo/hello/pull/51) | ✅ | ✅ | ✅ |\n" + readyLine,
		json: []string{`"scope":{"kind":"stack","prs":[{"kind":"merged-pr","context":{"owner":"octo","repo":"hello","number":50},"mergedAt":"2026-07-26T00:00:00Z"},{"kind":"ready-pr","context":{"owner":"octo","repo":"hello","number":51},"proof":{"mergeability":"clear","threads":[],"ci":{`},
	},
	{
		name: "changes requested",
		args: "--owner octo --repo hello --pr 61",
		reader: func() Reader {
			return newFakeReader(fakeOptions{facts: func(f *PullRequestFacts) { f.ReviewDecision = "CHANGES_REQUESTED" }})
		},
		exit:   6,
		pretty: "BLOCKER: changes-requested\npr=61\naction=resolve the changes-requested review before waiting for the merge queue\n",
		json:   []string{`"blocker":{"kind":"merge-gate","pr":{"owner":"octo","repo":"hello","number":61},"reason":"changes-requested"}`},
	},
	{
		name:   "queued closed blocker",
		args:   "--owner octo --repo hello --queued-stack --stack-prs 14,15",
		reader: stackReader(nil, map[PrNumber]func(*PullRequestFacts){15: func(f *PullRequestFacts) { f.State = "CLOSED" }}, nil),
		exit:   6,
		pretty: "QUEUE: captured 2 PRs bottom-to-top: #14,#15\n| PR | CI | Review | Merge |\n| --- | --- | --- | --- |\n" +
			"| [#14](https://github.com/octo/hello/pull/14) | ✅ | ✅ | ✅ |\n" +
			"| [#15](https://github.com/octo/hello/pull/15) | — | — | ❌ closed |\n" +
			"BLOCKER: closed-without-merge\npr=15\naction=restore or remove the closed PR from the queued stack\n",
		json: []string{`"blocker":{"kind":"merge-gate","pr":{"owner":"octo","repo":"hello","number":15},"reason":"closed-without-merge"}`},
	},
	{
		name:   "queued pending timeout",
		args:   "--owner octo --repo hello --queued-stack --stack-prs 12,13 --timeout 30",
		reader: stackReader(nil, nil, map[PrNumber]*ChecksFastPath{12: fastChecks(pendingCheck("ci"))}),
		exit:   5,
		pretty: "QUEUE: captured 2 PRs bottom-to-top: #12,#13\n| PR | CI | Review | Merge |\n| --- | --- | --- | --- |\n" +
			"| [#12](https://github.com/octo/hello/pull/12) | ⏳ 1 pending | ✅ | ✅ |\n" +
			"| [#13](https://github.com/octo/hello/pull/13) | ✅ | ✅ | ✅ |\n" +
			"WAITING: frontier=#12; 1 check pending\nTIMEOUT: queued stack still has 2 PRs unmerged; frontier=#12\n",
		json: []string{
			`"ci":{"kind":"ci-pending","source":"gh-pr-checks","all":[{"name":"ci","description":"","link":"","workflow":"","kind":"pending","reportedState":"PENDING"}],"failed":[],"pending":[{"name":"ci","description":"","link":"","workflow":"","kind":"pending","reportedState":"PENDING"}],"hadPreviousPassingCi":false}`,
			`"reason":{"kind":"queued-stack","frontier":{"owner":"octo","repo":"hello","number":12},"unmergedCount":2}`,
		},
	},
	{
		name:   "merge queue",
		args:   "--owner octo --repo hello --queued-stack --stack-prs 10,#11 --interval 0.001",
		reader: mergeQueueReader,
		pretty: "QUEUE: captured 2 PRs bottom-to-top: #10,#11\n| PR | CI | Review | Merge |\n| --- | --- | --- | --- |\n" +
			"| [#10](https://github.com/octo/hello/pull/10) | ✅ | ✅ | ✅ |\n" +
			"| [#11](https://github.com/octo/hello/pull/11) | ✅ | ✅ | ✅ |\n" +
			"WAITING: frontier=#10 is blocker-free; waiting for merge queue (2 PRs unmerged)\n" +
			"ADVANCE: merged #10; next=#11; remaining=1\n" +
			"WAITING: frontier=#11 is blocker-free; waiting for merge queue (1 PR unmerged)\n" +
			"COMPLETE: queued stack merged (2 PRs)\n",
	},
	{
		name:   "retry until the deadline",
		args:   "--owner octo --repo hello --pr 72 --timeout 30",
		reader: failingRead(QueryFailure{Kind: "command-exit", Retryable: true, Code: 1, Detail: "HTTP 502: Bad Gateway (https://api.github.com/graphql)"}),
		exit:   5,
		pretty: "RETRY: GitHub status query failed; retrying in 60s\ndetail=HTTP 502: Bad Gateway (https://api.github.com/graphql)\n" +
			"RETRY: GitHub status query failed; retrying in 120s\ndetail=HTTP 502: Bad Gateway (https://api.github.com/graphql)\n" +
			"TIMEOUT: GitHub status remained unavailable\n",
		json: []string{
			`"kind":"RETRY","terminal":false,"failure":{"kind":"command-exit","retryable":true,"code":1,"detail":"HTTP 502: Bad Gateway (https://api.github.com/graphql)"},"consecutiveFailures":1,"retryInSeconds":60}`,
			`"reason":{"kind":"status-unavailable","failure":{"kind":"command-exit","retryable":true,"code":1,`,
		},
	},
	{
		name:   "status query",
		args:   "--owner octo --repo hello --pr 74 --max-query-errors 1",
		reader: failingRead(QueryFailure{Kind: "missing-key", Retryable: true, Detail: `invalid pull request.mergeStateStatus: "FUTURE_STATE"`, RawValue: ptr(`"FUTURE_STATE"`)}),
		exit:   7,
		pretty: "BLOCKER: status-query\nfailures=1\ndetail=invalid pull request.mergeStateStatus: \"FUTURE_STATE\"\naction=verify current PR context, GitHub authentication, and API availability, then rearm\n",
		json:   []string{`"exitCode":7,"blocker":{"kind":"status-query","failures":1,"failure":{"kind":"missing-key","retryable":true,"detail":"invalid pull request.mergeStateStatus: \"FUTURE_STATE\"","rawValue":"\"FUTURE_STATE\""}}}`},
	},
}

func runScenario(t *testing.T, s scenario, extra ...string) string {
	t.Helper()
	stdout, stderr := &writes{}, &bytes.Buffer{}
	var sleeps []float64
	rt := runtime{reader: s.reader(), clock: &fakeClock{sleep: advancingSleep(&sleeps)}, stdout: stdout, stderr: stderr}
	if code := run(slices.Concat(strings.Fields(s.args), extra), rt); code != s.exit {
		t.Errorf("exit = %d, want %d", code, s.exit)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q", stderr)
	}
	return stdout.String()
}

func TestScenarioOutput(t *testing.T) {
	for _, s := range scenarios {
		t.Run(s.name, func(t *testing.T) {
			if got := runScenario(t, s, "--pretty"); got != s.pretty {
				t.Errorf("pretty =\n%s\nwant\n%s", got, s.pretty)
			}
			out := runScenario(t, s)
			for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
				if !json.Valid([]byte(line)) {
					t.Errorf("not one JSON value per line: %s", line)
				}
			}
			for _, want := range s.json {
				if !strings.Contains(out, want) {
					t.Errorf("JSON lacks\n%s\nin\n%s", want, out)
				}
			}
		})
	}
}
