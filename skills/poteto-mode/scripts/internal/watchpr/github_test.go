package watchpr

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

var githubContext = testContext(42)

func checkNames(checks []Check) []string {
	var names []string
	for _, check := range checks {
		names = append(names, check.Name)
	}
	return names
}

func TestResolveChecksUsesNonEmptyFastPath(t *testing.T) {
	reader := newFakeReader(fakeOptions{fastPath: fastChecks(passingCheck("fast"))})
	read, err := resolveChecks(reader, githubContext)
	if err != nil {
		t.Fatal(err)
	}
	if read.Source != "gh-pr-checks" || !reflect.DeepEqual(checkNames(read.Checks), []string{"fast"}) {
		t.Errorf("read = %+v", read)
	}
	if !reflect.DeepEqual(reader.calls, []string{"checksFastPath"}) {
		t.Errorf("calls = %v", reader.calls)
	}
}

func TestResolveChecksPaginatesGraphQLWhenFastPathUnusable(t *testing.T) {
	reader := newFakeReader(fakeOptions{
		fastPath: &ChecksFastPath{ExitCode: 8},
		rollupPages: []RollupPage{
			{Checks: []Check{passingCheck("first")}, EndCursor: "next"},
			{Checks: []Check{failedCheck("second")}},
		},
	})
	read, err := resolveChecks(reader, githubContext)
	if err != nil {
		t.Fatal(err)
	}
	if read.Source != "graphql-rollup" || !reflect.DeepEqual(checkNames(read.Checks), []string{"first", "second"}) {
		t.Errorf("read = %+v", read)
	}
	if want := []string{"checksFastPath", "checkRollupPage:null", "checkRollupPage:next"}; !reflect.DeepEqual(reader.calls, want) {
		t.Errorf("calls = %v, want %v", reader.calls, want)
	}
}

func TestResolveChecksFallsBackOnEmptyFastPathList(t *testing.T) {
	reader := newFakeReader(fakeOptions{
		fastPath:    fastChecks(),
		rollupPages: []RollupPage{{Checks: []Check{pendingCheck("fallback")}}},
	})
	read, err := resolveChecks(reader, githubContext)
	if err != nil {
		t.Fatal(err)
	}
	if read.Checks[0].Name != "fallback" {
		t.Errorf("checks = %+v", read.Checks)
	}
	if want := []string{"checksFastPath", "checkRollupPage:null"}; !reflect.DeepEqual(reader.calls, want) {
		t.Errorf("calls = %v, want %v", reader.calls, want)
	}
}

func TestResolveChecksFailsClosedWhenBothPathsEmpty(t *testing.T) {
	reader := newFakeReader(fakeOptions{fastPath: &ChecksFastPath{ExitCode: 8, Stderr: "credential cannot read checks"}})
	_, err := resolveChecks(reader, githubContext)
	var queryErr *QueryError
	if !errors.As(err, &queryErr) || queryErr.Failure.Kind != "checks-unavailable" {
		t.Fatalf("err = %v, want checks-unavailable", err)
	}
	if want := "could not read PR checks: fast path exit=8; GraphQL rollup was empty; credential cannot read checks"; queryErr.Failure.Detail != want {
		t.Errorf("detail = %q, want %q", queryErr.Failure.Detail, want)
	}
	if want := []string{"checksFastPath", "checkRollupPage:null"}; !reflect.DeepEqual(reader.calls, want) {
		t.Errorf("calls = %v, want %v", reader.calls, want)
	}
}

func mustMapRollupNode(t *testing.T, node rollupNode) Check {
	t.Helper()
	check, ok, err := mapRollupNode(node)
	if err != nil || !ok {
		t.Fatalf("mapRollupNode(%+v) = %v, %v", node, ok, err)
	}
	return check
}

func TestMapRollupNodeCheckRunStatesFailClosed(t *testing.T) {
	cases := []struct {
		status, conclusion  string
		kind, reportedState string
	}{
		{"IN_PROGRESS", "", "pending", "PENDING"},
		{"COMPLETED", "SUCCESS", "passed", "SUCCESS"},
		{"COMPLETED", "NEUTRAL", "skipped", "NEUTRAL"},
		{"COMPLETED", "SKIPPED", "skipped", "SKIPPED"},
		{"COMPLETED", "ACTION_REQUIRED", "failed", "ACTION_REQUIRED"},
		{"COMPLETED", "TIMED_OUT", "failed", "FAILURE"},
		{"COMPLETED", "FUTURE_VALUE", "failed", "FAILURE"},
	}
	for _, c := range cases {
		check := mustMapRollupNode(t, rollupNode{Typename: "CheckRun", Name: ptr("ci"), Status: c.status, Conclusion: c.conclusion})
		if check.Kind != c.kind || check.ReportedState != c.reportedState {
			t.Errorf("%v/%v: got %s/%s, want %s/%s", c.status, c.conclusion, check.Kind, check.ReportedState, c.kind, c.reportedState)
		}
	}
}

func TestMapRollupNodeInProgressCodeReviewGateIsTheGate(t *testing.T) {
	for _, node := range []rollupNode{
		{Typename: "CheckRun", Name: ptr("Code Review Gate"), Status: "IN_PROGRESS"},
		{Typename: "StatusContext", Context: ptr("Code Review Gate"), State: "PENDING"},
	} {
		if check := mustMapRollupNode(t, node); check.Kind != "code-review-gate" {
			t.Errorf("%+v: kind = %s", node, check.Kind)
		}
	}
}

func TestMapRollupNodeCodeReviewGateAwaitingOwnerIsTheGate(t *testing.T) {
	check := mustMapRollupNode(t, fromJSON[rollupNode](t, `{"__typename":"CheckRun","name":"Code Review Gate","status":"COMPLETED","conclusion":"ACTION_REQUIRED","description":"1 of 2 policies"}`))
	if check.Kind != "code-review-gate" || check.ReportedState != "ACTION_REQUIRED" {
		t.Errorf("check = %+v", check)
	}
}

func TestParseFastCheckCodeReviewGateAwaitingOwnerIsTheGate(t *testing.T) {
	gate, err := parseFastCheck(ghFastCheck{Name: ptr("Code Review Gate"), State: ptr("ACTION_REQUIRED"), Bucket: ptr("fail")})
	if err != nil || gate.Kind != "code-review-gate" || gate.ReportedState != "ACTION_REQUIRED" {
		t.Errorf("gate = %+v, %v", gate, err)
	}
	ci, err := parseFastCheck(ghFastCheck{Name: ptr("ci"), State: ptr("ACTION_REQUIRED"), Bucket: ptr("fail")})
	if err != nil || ci.Kind != "failed" || ci.ReportedState != "ACTION_REQUIRED" {
		t.Errorf("ci = %+v, %v", ci, err)
	}
}

func TestMapRollupNodeStatusContextsAndUnknownTypenames(t *testing.T) {
	if check := mustMapRollupNode(t, rollupNode{Typename: "StatusContext", Context: ptr("ci"), State: "EXPECTED"}); check.Kind != "pending" || check.ReportedState != "PENDING" {
		t.Errorf("EXPECTED = %+v", check)
	}
	if check := mustMapRollupNode(t, rollupNode{Typename: "StatusContext", Context: ptr("ci"), State: "FUTURE_VALUE"}); check.Kind != "failed" || check.ReportedState != "FUTURE_VALUE" {
		t.Errorf("FUTURE_VALUE = %+v", check)
	}
	if _, ok, err := mapRollupNode(rollupNode{Typename: "FutureNode"}); ok || err != nil {
		t.Errorf("FutureNode mapped: %v, %v", ok, err)
	}
}

func rawPullRequest(edit func(*ghPullRequest)) ghPullRequest {
	raw := ghPullRequest{
		Mergeable: ptr("MERGEABLE"), MergeStateStatus: ptr("CLEAN"), ReviewDecision: ptr("APPROVED"), HeadRefOid: ptr("head"),
		HeadRefName: ptr("feature"), BaseRefName: ptr("main"), State: ptr("OPEN"), IsDraft: ptr(false),
	}
	edit(&raw)
	return raw
}

func TestParsePullRequestAcceptsConflictingMergeState(t *testing.T) {
	facts, err := parsePullRequest(rawPullRequest(func(r *ghPullRequest) { r.MergeStateStatus = ptr("CONFLICTING") }), githubContext)
	if err != nil || facts.MergeStateStatus != "CONFLICTING" {
		t.Errorf("facts = %+v, %v", facts, err)
	}
}

func TestParsePullRequestReadsEmptyReviewDecisionAsNone(t *testing.T) {
	facts, err := parsePullRequest(rawPullRequest(func(r *ghPullRequest) { r.ReviewDecision = ptr("") }), githubContext)
	if err != nil || facts.ReviewDecision != "" {
		t.Errorf("facts = %+v, %v", facts, err)
	}
}

func TestParsePullRequestRejectsUnknownReviewDecision(t *testing.T) {
	_, err := parsePullRequest(rawPullRequest(func(r *ghPullRequest) { r.ReviewDecision = ptr("MAYBE") }), githubContext)
	var queryErr *QueryError
	if !errors.As(err, &queryErr) {
		t.Errorf("err = %v, want a QueryError", err)
	}
}

func TestParsePullRequestUnknownEnumIsRetryableWithRawValue(t *testing.T) {
	_, err := parsePullRequest(rawPullRequest(func(r *ghPullRequest) { r.MergeStateStatus = ptr("FUTURE_STATE") }), githubContext)
	var queryErr *QueryError
	if !errors.As(err, &queryErr) {
		t.Fatalf("err = %v, want a QueryError", err)
	}
	f := queryErr.Failure
	if f.Kind != "missing-key" || !f.Retryable || f.RawValue == nil || *f.RawValue != `"FUTURE_STATE"` {
		t.Errorf("failure = %+v", f)
	}
}

func TestBugbotThreadsCountDistinctReviewPasses(t *testing.T) {
	response := fromJSON[ghResponse[ghReviewThreads]](t, `{"data":{"repository":{"pullRequest":{"reviewThreads":{
		"pageInfo":{"hasNextPage":false,"endCursor":null},
		"nodes":[
			{"id":"one","isResolved":false,"comments":{"nodes":[{"body":"RUN_ID: run-1","createdAt":"now","path":"a.ts","line":1,"author":{"login":"bugbot"}}]}},
			{"id":"two","isResolved":false,"comments":{"nodes":[{"body":"CURSOR_AUTOMATION_ID: run-2 severity high","createdAt":"now","path":null,"line":null,"author":{"login":"cursor"}}]}},
			{"id":"resolved","isResolved":true,"comments":{"nodes":[{"body":"RUN_ID: run-3","createdAt":"now","path":null,"line":null,"author":{"login":"bugbot"}}]}}
		]}}}}}`)
	page, err := parseReviewThreadPage(response)
	if err != nil {
		t.Fatal(err)
	}
	threads := buildReviewThreads(page.Threads)
	if len(threads) != 2 {
		t.Fatalf("threads = %+v", threads)
	}
	for _, thread := range threads {
		if !thread.IsBugbot || thread.BugbotReviewPasses != 3 {
			t.Errorf("thread = %+v", thread)
		}
	}
}

func rawThread(id string, resolved bool) RawReviewThread {
	login, path, line := "someone", "a.ts", 1
	return RawReviewThread{ID: id, Resolved: resolved, FirstComment: &ReviewComment{AuthorLogin: &login, Body: "please fix", Path: &path, Line: &line, CreatedAt: "now"}}
}

func threadIDs(threads []ReviewThread) []string {
	var ids []string
	for _, thread := range threads {
		ids = append(ids, thread.ID)
	}
	return ids
}

func TestReviewThreadPagination(t *testing.T) {
	t.Run("keeps walking pages until the cursor is exhausted", func(t *testing.T) {
		reader := newFakeReader(fakeOptions{threadPages: []ReviewThreadPage{
			{Threads: []RawReviewThread{rawThread("one", false)}, EndCursor: "next"},
			{Threads: []RawReviewThread{rawThread("two", false)}, EndCursor: "last"},
			{Threads: []RawReviewThread{rawThread("three", false)}},
		}})
		threads, err := resolveReviewThreads(reader, githubContext)
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"one", "two", "three"}; !reflect.DeepEqual(threadIDs(threads), want) {
			t.Errorf("ids = %v", threadIDs(threads))
		}
		if want := []string{"reviewThreadPage:null", "reviewThreadPage:next", "reviewThreadPage:last"}; !reflect.DeepEqual(reader.calls, want) {
			t.Errorf("calls = %v", reader.calls)
		}
	})
	t.Run("sees an unresolved thread past the first page", func(t *testing.T) {
		reader := newFakeReader(fakeOptions{threadPages: []ReviewThreadPage{
			{Threads: []RawReviewThread{rawThread("early", true)}, EndCursor: "next"},
			{Threads: []RawReviewThread{rawThread("late", false)}},
		}})
		threads, _ := resolveReviewThreads(reader, githubContext)
		if want := []string{"late"}; !reflect.DeepEqual(threadIDs(threads), want) {
			t.Errorf("ids = %v", threadIDs(threads))
		}
	})
	t.Run("stops after a single page when the cursor is exhausted", func(t *testing.T) {
		reader := newFakeReader(fakeOptions{threadPages: []ReviewThreadPage{{Threads: []RawReviewThread{rawThread("only", false)}}}})
		threads, _ := resolveReviewThreads(reader, githubContext)
		if len(threads) != 1 || !reflect.DeepEqual(reader.calls, []string{"reviewThreadPage:null"}) {
			t.Errorf("threads = %v, calls = %v", threads, reader.calls)
		}
	})
	t.Run("counts Bugbot review passes across every page", func(t *testing.T) {
		bugbot := func(id, runID string) RawReviewThread {
			thread := rawThread(id, false)
			login := "bugbot"
			thread.FirstComment.AuthorLogin, thread.FirstComment.Body = &login, "RUN_ID: "+runID
			return thread
		}
		reader := newFakeReader(fakeOptions{threadPages: []ReviewThreadPage{
			{Threads: []RawReviewThread{bugbot("one", "run-1")}, EndCursor: "next"},
			{Threads: []RawReviewThread{bugbot("two", "run-2")}},
		}})
		threads, _ := resolveReviewThreads(reader, githubContext)
		for _, thread := range threads {
			if thread.BugbotReviewPasses != 2 {
				t.Errorf("thread = %+v", thread)
			}
		}
	})
	t.Run("drops resolved threads from later pages too", func(t *testing.T) {
		reader := newFakeReader(fakeOptions{threadPages: []ReviewThreadPage{
			{Threads: []RawReviewThread{rawThread("one", false)}, EndCursor: "next"},
			{Threads: []RawReviewThread{rawThread("two", true)}},
		}})
		threads, _ := resolveReviewThreads(reader, githubContext)
		if want := []string{"one"}; !reflect.DeepEqual(threadIDs(threads), want) {
			t.Errorf("ids = %v", threadIDs(threads))
		}
	})
	t.Run("rejects a page that omits pageInfo instead of assuming one page", func(t *testing.T) {
		_, err := parseReviewThreadPage(fromJSON[ghResponse[ghReviewThreads]](t, `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[]}}}}}`))
		var queryErr *QueryError
		if !errors.As(err, &queryErr) {
			t.Errorf("err = %v, want a QueryError", err)
		}
	})
}

func TestResolveContextExplicitNeedsNoReads(t *testing.T) {
	reader := newFakeReader(fakeOptions{})
	owner, repo, pr := "explicit", "repo", githubContext.Number
	got, err := resolveContext(reader, &owner, &repo, &pr)
	if err != nil || got != (PrContext{Owner: "explicit", Repo: "repo", Number: 42}) {
		t.Errorf("context = %+v, %v", got, err)
	}
	if len(reader.calls) != 0 {
		t.Errorf("calls = %v", reader.calls)
	}
}

func TestResolveContextUsesOriginBeforeCurrentPr(t *testing.T) {
	reader := newFakeReader(fakeOptions{origin: &Repository{Owner: "local", Repo: "checkout"}})
	pr := githubContext.Number
	got, err := resolveContext(reader, nil, nil, &pr)
	if err != nil || got != (PrContext{Owner: "local", Repo: "checkout", Number: 42}) {
		t.Errorf("context = %+v, %v", got, err)
	}
	if !reflect.DeepEqual(reader.calls, []string{"originRepo"}) {
		t.Errorf("calls = %v", reader.calls)
	}
}

func stackNumbers(contexts []PrContext) []PrNumber {
	var numbers []PrNumber
	for _, context := range contexts {
		numbers = append(numbers, context.Number)
	}
	return numbers
}

func TestOrderStack(t *testing.T) {
	cases := []struct {
		name string
		open []OpenPullRequest
		want []PrNumber
	}{
		{"orders the connected stack bottom-to-top", []OpenPullRequest{
			{Number: 41, HeadRefName: "base-feature", BaseRefName: "main"},
			{Number: 42, HeadRefName: "feature", BaseRefName: "base-feature"},
			{Number: 43, HeadRefName: "upstack", BaseRefName: "feature"},
		}, []PrNumber{41, 42, 43}},
		{"does not use a fork's same-named head as a stack parent", []OpenPullRequest{
			{Number: 41, HeadRefName: "base-feature", BaseRefName: "main", IsCrossRepository: true},
			{Number: 42, HeadRefName: "feature", BaseRefName: "base-feature"},
		}, []PrNumber{42}},
		{"does not attach upstream branch children to a fork head", []OpenPullRequest{
			{Number: 42, HeadRefName: "feature", BaseRefName: "main", IsCrossRepository: true},
			{Number: 43, HeadRefName: "upstack", BaseRefName: "feature"},
		}, []PrNumber{42}},
	}
	for _, c := range cases {
		if got := stackNumbers(orderStack(githubContext, c.open)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestOrderStackTerminatesWhenForkOpensMainAgainstMain(t *testing.T) {
	// Bound the call so this regression reports a failure instead of hanging the suite.
	done := make(chan []PrContext, 1)
	go func() {
		done <- orderStack(githubContext, []OpenPullRequest{
			{Number: 41, HeadRefName: "main", BaseRefName: "main", IsCrossRepository: true},
			{Number: 42, HeadRefName: "feature", BaseRefName: "main"},
		})
	}()
	select {
	case got := <-done:
		if !reflect.DeepEqual(stackNumbers(got), []PrNumber{42}) {
			t.Errorf("got %v", stackNumbers(got))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("orderStack did not terminate")
	}
}

func TestOpenPullRequestsRequestsAndParsesForkIdentity(t *testing.T) {
	var argv []string
	run := func(args []string) (CommandResult, error) {
		argv = args
		return CommandResult{Stdout: `[{"number":42,"headRefName":"feature","baseRefName":"main","isCrossRepository":true}]` + "\n"}, nil
	}
	got, err := GhReader{Run: run}.OpenPullRequests(Repository{Owner: "owner", Repo: "repo"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "gh pr list --repo owner/repo --state open --limit 300 --json number,headRefName,baseRefName,isCrossRepository"; strings.Join(argv, " ") != want {
		t.Errorf("argv = %q, want %q", argv, want)
	}
	if want := []OpenPullRequest{{Number: 42, HeadRefName: "feature", BaseRefName: "main", IsCrossRepository: true}}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

type ghAnswer struct {
	match  string
	result CommandResult
}

// ghFake is an in-process gh and git: the first answer whose match is in the
// argv wins, and every argv is recorded.
type ghFake struct {
	answers []ghAnswer
	calls   [][]string
}

func (g *ghFake) run(argv []string) (CommandResult, error) {
	g.calls = append(g.calls, argv)
	joined := strings.Join(argv, " ")
	for _, answer := range g.answers {
		if strings.Contains(joined, answer.match) {
			return answer.result, nil
		}
	}
	return CommandResult{Code: 1, Stderr: "fake gh: no answer for " + joined}, nil
}

func ok(stdout string) CommandResult { return CommandResult{Stdout: stdout + "\n"} }

func runGh(t *testing.T, fake *ghFake, args string) (int, string, string) {
	t.Helper()
	stdout, stderr := &writes{}, &bytes.Buffer{}
	code := run(strings.Fields(args), runtime{reader: GhReader{Run: fake.run}, clock: &fakeClock{}, stdout: stdout, stderr: stderr})
	return code, stdout.String(), stderr.String()
}

var cleanAnswers = []ghAnswer{
	{"gh pr view", ok(`{"baseRefName":"main","headRefName":"feature","headRefOid":"head1","isDraft":false,"mergeStateStatus":"CLEAN","mergeable":"MERGEABLE","mergedAt":null,"reviewDecision":"APPROVED","state":"OPEN"}`)},
	{"query ReviewThreads", ok(`{"data":{"repository":{"pullRequest":{"reviewThreads":{"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":[]}}}}}`)},
	{"gh pr checks", ok(`[{"bucket":"pass","description":"","link":"","name":"build","state":"SUCCESS","workflow":"CI"}]`)},
	{"query PrCommitStatuses", ok(`{"data":{"repository":{"pullRequest":{"commits":{"nodes":[{"commit":{"oid":"old1","statusCheckRollup":{"state":"FAILURE"}}},{"commit":{"oid":"head1","statusCheckRollup":{"state":"SUCCESS"}}}]}}}}}`)},
}

func TestGhReaderFallsBackToThePaginatedRollup(t *testing.T) {
	fake := &ghFake{answers: append([]ghAnswer{
		{"gh pr checks", CommandResult{Code: 8, Stderr: "no checks reported on the 'feature' branch\n"}},
		{"after=r1", ok(`{"data": {"repository": {"pullRequest": {"commits": {"nodes": [{"commit": {"statusCheckRollup": {"contexts": {"pageInfo": {"hasNextPage": false, "endCursor": null}, "nodes": [{"__typename": "CheckRun", "name": "Code Review Gate", "status": "COMPLETED", "conclusion": "ACTION_REQUIRED", "detailsUrl": null}, {"__typename": "CheckRun", "name": "deploy", "status": "COMPLETED", "conclusion": "neutral", "detailsUrl": "https://ci.example/d"}]}}}}]}}}}}`)},
		{"query PrCheckRollup", ok(`{"data": {"repository": {"pullRequest": {"commits": {"nodes": [{"commit": {"statusCheckRollup": {"contexts": {"pageInfo": {"hasNextPage": true, "endCursor": "r1"}, "nodes": [{"__typename": "CheckRun", "name": "build", "status": "COMPLETED", "conclusion": "SUCCESS", "detailsUrl": "https://ci.example/r/1"}, {"__typename": "StatusContext", "context": "ci/legacy", "state": "SUCCESS", "targetUrl": "https://legacy.example/1"}, {"__typename": "FutureNode"}]}}}}]}}}}}`)},
	}, cleanAnswers...)}
	code, stdout, stderr := runGh(t, fake, "--owner octo --repo hello --pr 70")
	if code != 0 || stderr != "" {
		t.Errorf("exit = %d, stderr = %q", code, stderr)
	}
	if want := `{"schemaVersion":1,"sequence":1,"observedAt":"2026-07-26T00:00:00.000Z","mode":"single","kind":"READY","terminal":true,"exitCode":0,"scope":{"kind":"single","pr":{"kind":"ready-pr","context":{"owner":"octo","repo":"hello","number":70},"proof":{"mergeability":"clear","threads":[],"ci":{"source":"graphql-rollup","all":[{"name":"build","description":"","link":"https://ci.example/r/1","workflow":"","kind":"passed","reportedState":"SUCCESS"},{"name":"ci/legacy","description":"","link":"https://legacy.example/1","workflow":"","kind":"passed","reportedState":"SUCCESS"},{"name":"Code Review Gate","description":"","link":"","workflow":"","kind":"code-review-gate","reportedState":"ACTION_REQUIRED"},{"name":"deploy","description":"","link":"https://ci.example/d","workflow":"","kind":"skipped","reportedState":"NEUTRAL"}],"hadPreviousPassingCi":false,"kind":"ci-clean","failed":[],"pending":[],"github":{"kind":"allowed","basis":"merge-state","mergeStateStatus":"CLEAN","headRollupState":"SUCCESS"}},"gate":{"state":"OPEN","reviewDecision":"APPROVED","draft":"not-draft"}}}}}` + "\n"; stdout != want {
		t.Errorf("stdout =\n%s\nwant\n%s", stdout, want)
	}
	graphql := func(query string, extra ...string) []string {
		return append([]string{"gh", "api", "graphql", "-f", "query=" + query, "-f", "owner=octo", "-f", "repo=hello", "-F", "pr=70"}, extra...)
	}
	want := [][]string{
		{"gh", "pr", "view", "70", "--repo", "octo/hello", "--json", "mergeable,mergeStateStatus,reviewDecision,headRefOid,headRefName,baseRefName,state,mergedAt,isDraft"},
		graphql(reviewThreadsQuery),
		{"gh", "pr", "checks", "70", "--repo", "octo/hello", "--json", "name,state,description,link,workflow,bucket"},
		graphql(prCheckRollupQuery),
		graphql(prCheckRollupQuery, "-f", "after=r1"),
		graphql(prCommitStatusQuery),
	}
	if !reflect.DeepEqual(fake.calls, want) {
		t.Errorf("calls =\n%q\nwant\n%q", fake.calls, want)
	}
}

func TestGhReaderFallsBackToCurrentPrWithoutAnOrigin(t *testing.T) {
	fake := &ghFake{answers: append([]ghAnswer{
		{"git remote", CommandResult{Code: 2, Stderr: "error: No such remote 'origin'\n"}},
		{"--json number,url", ok(`{"number": 83, "url": "https://github.com/cur/rent/pull/83"}`)},
	}, cleanAnswers...)}
	code, stdout, _ := runGh(t, fake, "--pr 83 --status-only")
	if code != 0 || !strings.Contains(stdout, `"rows":[{"kind":"open","context":{"owner":"cur","repo":"rent","number":83}`) {
		t.Errorf("exit = %d, stdout = %s", code, stdout)
	}
	if want := [][]string{{"git", "remote", "get-url", "origin"}, {"gh", "pr", "view", "83", "--json", "number,url"}}; !reflect.DeepEqual(fake.calls[:2], want) {
		t.Errorf("calls = %q, want %q", fake.calls[:2], want)
	}
}

func TestGhReaderFailures(t *testing.T) {
	const view = "gh pr view 73 --repo octo/hello --json mergeable,mergeStateStatus,reviewDecision,headRefOid,headRefName,baseRefName,state,mergedAt,isDraft"
	cases := []struct {
		name   string
		answer ghAnswer
		want   string
	}{
		{"silent exit", ghAnswer{"gh pr view", CommandResult{Code: 4}},
			`"failure":{"kind":"command-exit","retryable":true,"code":4,"detail":"` + view + ` exited 4"}`},
		{"stderr first line", ghAnswer{"gh pr view", CommandResult{Code: 1, Stderr: "\n  gh: rate limited\r\nretry later\n"}},
			`"failure":{"kind":"command-exit","retryable":true,"code":1,"detail":"gh: rate limited"}`},
		{"unparsable output", ghAnswer{"gh pr view", ok("not json")},
			`"failure":{"kind":"json-parse","retryable":true,"detail":"` + view + `: `},
		{"missing field", ghAnswer{"gh pr view", ok(`{"baseRefName":"main","headRefName":"feature","headRefOid":"head1","mergeStateStatus":"CLEAN","mergeable":"MERGEABLE","mergedAt":null,"reviewDecision":"APPROVED","state":"OPEN"}`)},
			`"failure":{"kind":"missing-key","retryable":true,"detail":"missing pull request.isDraft"}`},
		{"fractional comment line", ghAnswer{"query ReviewThreads", ok(`{"data":{"repository":{"pullRequest":{"reviewThreads":{"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":[{"id":"t","isResolved":false,"comments":{"nodes":[{"body":"b","createdAt":"now","path":null,"line":1.5,"author":null}]}}]}}}}}`)},
			`"failure":{"kind":"missing-key","retryable":true,"detail":"invalid review comment.line: 1.5","rawValue":"1.5"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := &ghFake{answers: append([]ghAnswer{c.answer}, cleanAnswers...)}
			code, stdout, _ := runGh(t, fake, "--owner octo --repo hello --pr 73 --max-query-errors 1")
			if code != 7 || !strings.Contains(stdout, c.want) {
				t.Errorf("exit = %d, stdout = %s\nwant it to contain %s", code, stdout, c.want)
			}
		})
	}
}

func TestGhReaderCrashesOnAMalformedPrNumber(t *testing.T) {
	fake := &ghFake{answers: []ghAnswer{{"gh pr list", ok(`[{"number": "x", "headRefName": "a", "baseRefName": "main", "isCrossRepository": false}]`)}}}
	code, stdout, stderr := runGh(t, fake, "--owner octo --repo hello --stack --pr 89")
	if code != 1 || stdout != "" || stderr != "error: open PRs[0].number must be a positive integer\n" {
		t.Errorf("exit = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
}

func TestParseRemote(t *testing.T) {
	cases := map[string]*Repository{
		"git@github.com:origin-owner/origin-repo.git\n":  {Owner: "origin-owner", Repo: "origin-repo"},
		"ssh://git@github.com/origin-owner/origin-repo":  {Owner: "origin-owner", Repo: "origin-repo"},
		"  https://GitHub.com/Origin-Owner/origin-repo ": {Owner: "Origin-Owner", Repo: "origin-repo"},
		"https://gitlab.com/owner/repo":                  nil,
		"http://github.com/owner/repo":                   nil,
		"https://github.com:8443/owner/repo":             nil,
		"https://token@github.com/owner/repo":            nil,
		"https://github.com/owner/repo?tab=1":            nil,
		"https://github.com/owner":                       nil,
	}
	for remote, want := range cases {
		if got := parseRemote(remote); !reflect.DeepEqual(got, want) {
			t.Errorf("parseRemote(%q) = %+v, want %+v", remote, got, want)
		}
	}
}

func TestParsePrUrlFailsWithoutRetry(t *testing.T) {
	if got, err := parsePrUrl("https://github.com/cur/rent/pull/86"); err != nil || got != (PrContext{Owner: "cur", Repo: "rent", Number: 86}) {
		t.Errorf("valid URL = %+v, %v", got, err)
	}
	for url, reason := range map[string]string{
		"https://example.com/cur/rent/pull/85": "not a canonical GitHub pull URL",
		"https://github.com/cur/rent/pull/0x":  "PR number must be a positive integer",
		"https://github.com/cur/rent/issues/1": "not a canonical GitHub pull URL",
		"not a url":                            "Invalid URL",
	} {
		_, err := parsePrUrl(url)
		var queryErr *QueryError
		if !errors.As(err, &queryErr) {
			t.Errorf("%q: err = %v, want a QueryError", url, err)
			continue
		}
		f := queryErr.Failure
		want := "could not infer owner/repo from PR URL: " + url + " (" + reason + ")"
		if f.Kind != "invalid-context-url" || f.Retryable || f.RawValue == nil || *f.RawValue != url || f.Detail != want {
			t.Errorf("%q: failure = %+v, want detail %q", url, f, want)
		}
	}
}
