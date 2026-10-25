package watchpr

import (
	"errors"
	"reflect"
	"strconv"
	"testing"
)

func TestReadinessTruthTable(t *testing.T) {
	cases := []struct{ mergeStateStatus, headRollupState, want string }{
		{"BLOCKED", "FAILURE", "refused"},
		{"BLOCKED", "ERROR", "refused"},
		{"BLOCKED", "PENDING", "allowed"},
		{"UNSTABLE", "FAILURE", "allowed"},
		{"UNKNOWN", "ERROR", "allowed"},
		{"UNKNOWN", "EXPECTED", "allowed"},
		{"UNKNOWN", "FAILURE", "allowed"},
		{"UNKNOWN", "PENDING", "allowed"},
		{"UNKNOWN", "SUCCESS", "allowed"},
		{"UNKNOWN", "", "allowed"},
		{"CLEAN", "SUCCESS", "allowed"},
	}
	for _, c := range cases {
		if got := assessGitHubMerge(c.mergeStateStatus, c.headRollupState).Kind; got != c.want {
			t.Errorf("%s/%q = %s, want %s", c.mergeStateStatus, c.headRollupState, got, c.want)
		}
	}
}

func mustSnapshot(t *testing.T, reader Reader, context PrContext, omitPendingHistory bool) PrSnapshot {
	t.Helper()
	snapshot, err := readSnapshot(reader, context, omitPendingHistory)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func mustClassify(t *testing.T, row PrSnapshot) Decision {
	t.Helper()
	decision, err := classifyPr(row, false)
	if err != nil {
		t.Fatal(err)
	}
	return decision
}

func TestCleanListPlusGitHubRefusalIsACIBlocker(t *testing.T) {
	reader := newFakeReader(fakeOptions{
		facts:         func(f *PullRequestFacts) { f.MergeStateStatus = "BLOCKED" },
		fastPath:      fastChecks(passingCheck("ci")),
		commitRollups: []CommitRollup{{Oid: "head", State: "FAILURE"}},
	})
	snapshot := mustSnapshot(t, reader, testContext(1), false)
	if snapshot.Kind != "open" || snapshot.Ci.Kind != "ci-github-rejected" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if decision := mustClassify(t, snapshot); decision.Kind != "blocker" || decision.Blocker.Kind != "failing-checks" {
		t.Errorf("decision = %+v", decision)
	}
}

func TestPendingQueuedChecksSkipCommitRollups(t *testing.T) {
	reader := newFakeReader(fakeOptions{fastPath: fastChecks(pendingCheck("ci"))})
	snapshot := mustSnapshot(t, reader, testContext(2), true)
	if snapshot.Kind != "open" || snapshot.Ci.Kind != "ci-pending" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if want := []string{"pullRequest", "reviewThreadPage:null", "checksFastPath"}; !reflect.DeepEqual(reader.calls, want) {
		t.Errorf("calls = %v, want %v", reader.calls, want)
	}
}

func TestSettledAndFailedListsQueryRollups(t *testing.T) {
	settled := newFakeReader(fakeOptions{})
	mustSnapshot(t, settled, testContext(3), true)
	failed := newFakeReader(fakeOptions{fastPath: fastChecks(failedCheck("ci"))})
	mustSnapshot(t, failed, testContext(4), true)
	for name, reader := range map[string]*fakeReader{"settled": settled, "failed": failed} {
		found := false
		for _, call := range reader.calls {
			found = found || call == "commitRollups"
		}
		if !found {
			t.Errorf("%s calls = %v, want commitRollups", name, reader.calls)
		}
	}
}

func TestMergedRowsShortCircuitBeforeThreadsAndChecks(t *testing.T) {
	reader := newFakeReader(fakeOptions{facts: func(f *PullRequestFacts) {
		mergedAt := "2026-07-26T00:00:00Z"
		f.State, f.MergedAt = "MERGED", &mergedAt
	}})
	if snapshot := mustSnapshot(t, reader, testContext(5), false); snapshot.Kind != "merged" {
		t.Errorf("kind = %s", snapshot.Kind)
	}
	if !reflect.DeepEqual(reader.calls, []string{"pullRequest"}) {
		t.Errorf("calls = %v", reader.calls)
	}
}

func TestStacksScanTierMajorSoUpstackConflictOutranksFrontierCI(t *testing.T) {
	frontier := mustSnapshot(t, newFakeReader(fakeOptions{
		fastPath:      fastChecks(failedCheck("ci")),
		commitRollups: []CommitRollup{{Oid: "head", State: "FAILURE"}},
	}), testContext(10), true)
	upstack := mustSnapshot(t, newFakeReader(fakeOptions{facts: func(f *PullRequestFacts) { f.Mergeable = "CONFLICTING" }}), testContext(11), true)
	decision, err := selectTierMajorStackDecision([]PrSnapshot{frontier, upstack}, false)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != "blocker" || decision.Blocker.Kind != "merge-conflicts" || decision.Blocker.Pr.Number != 11 {
		t.Errorf("decision = %+v", decision)
	}
}

func TestStackWaitIsAttributedToThePendingPR(t *testing.T) {
	readyBottom := mustSnapshot(t, newFakeReader(fakeOptions{}), testContext(20), true)
	pendingUpstack := mustSnapshot(t, newFakeReader(fakeOptions{fastPath: fastChecks(pendingCheck("upstack-build"))}), testContext(21), true)
	decision, err := selectTierMajorStackDecision([]PrSnapshot{readyBottom, pendingUpstack}, false)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != "waiting" || decision.Frontier.Number != 21 || !reflect.DeepEqual(checkNames(decision.Pending), []string{"upstack-build"}) {
		t.Errorf("decision = %+v", decision)
	}
}

func TestDraftWaitsWhilePendingThenReportsTheGate(t *testing.T) {
	draft := func(f *PullRequestFacts) { f.IsDraft = true }
	pending := mustSnapshot(t, newFakeReader(fakeOptions{facts: draft, fastPath: fastChecks(pendingCheck("ci"))}), testContext(12), true)
	if decision := mustClassify(t, pending); decision.Kind != "waiting" {
		t.Errorf("pending decision = %+v", decision)
	}
	settled := mustSnapshot(t, newFakeReader(fakeOptions{facts: draft}), testContext(12), true)
	if decision := mustClassify(t, settled); decision.Kind != "blocker" || decision.Blocker.Kind != "merge-gate" || decision.Blocker.Reason != "draft-pr" {
		t.Errorf("settled decision = %+v", decision)
	}
}

func openSnapshot(t *testing.T, context PrContext) PrSnapshot {
	return mustSnapshot(t, newFakeReader(fakeOptions{}), context, true)
}

func mustApply(t *testing.T, state queueState, snapshot PrSnapshot, now float64) (queueState, []PrSnapshot) {
	t.Helper()
	next, rows, err := applyQueueSnapshot(state, snapshot, now, testPolling)
	if err != nil {
		t.Fatal(err)
	}
	return next, rows
}

func TestQueueDropsSweepHeadOnlyAfterItsSnapshotSucceeds(t *testing.T) {
	queue := []PrContext{testContext(20), testContext(21), testContext(22)}
	state := createQueueState(queue, 0)
	state, _ = mustApply(t, state, openSnapshot(t, queue[0]), 0)
	if state.work.kind != "whole-stack-sweep" || !reflect.DeepEqual(stackNumbers(state.work.remaining), []PrNumber{21, 22}) {
		t.Errorf("work = %+v", state.work)
	}
	state, _ = mustApply(t, state, openSnapshot(t, queue[1]), 60)
	if state.work.kind != "whole-stack-sweep" || !reflect.DeepEqual(stackNumbers(state.work.remaining), []PrNumber{22}) {
		t.Errorf("work = %+v", state.work)
	}
}

func timelineDeps(reader Reader, timeline *[]string, stopAfter int, stopMessage string) (dependencies, *fakeClock) {
	sleeps := 0
	clock := &fakeClock{sleep: func(c *fakeClock, seconds float64) error {
		*timeline = append(*timeline, "sleep")
		c.now += seconds
		sleeps++
		if sleeps == stopAfter {
			return errors.New(stopMessage)
		}
		return nil
	}}
	return dependencies{reader: reader, clock: clock, emit: func(v Verdict) { *timeline = append(*timeline, "emit:"+v.Kind) }}, clock
}

func TestQueueResumesTheSweepAtThePRWhoseReadFailed(t *testing.T) {
	middle := testContext(21)
	base := newFakeReader(fakeOptions{})
	failNext := true
	var timeline []string
	reader := hookReader{Reader: base, pullRequest: func(pr PrContext) (PullRequestFacts, error) {
		if pr.Number == middle.Number && failNext {
			failNext = false
			timeline = append(timeline, "fail:"+pr.Number.String())
			return PullRequestFacts{}, queryError(QueryFailure{Kind: "command-exit", Retryable: true, Detail: "rate limited", Code: 1})
		}
		timeline = append(timeline, "read:"+pr.Number.String())
		return base.PullRequest(pr)
	}}
	deps, _ := timelineDeps(reader, &timeline, 2, "stop after resume proof")
	_, err := runQueued(deps, []PrContext{testContext(20), middle, testContext(22)}, testPolling)
	if err == nil || err.Error() != "stop after resume proof" {
		t.Fatalf("err = %v", err)
	}
	want := []string{"emit:QUEUE", "read:20", "fail:21", "emit:RETRY", "sleep", "read:21", "read:22", "emit:STATUS", "emit:WAITING", "sleep"}
	if !reflect.DeepEqual(timeline, want) {
		t.Errorf("timeline = %v\nwant %v", timeline, want)
	}
}

func TestQueueEmitsCompletedSweepOnlyAfterItsFinalSnapshot(t *testing.T) {
	queue := []PrContext{testContext(30), testContext(31)}
	state := createQueueState(queue, 0)
	state, rows := mustApply(t, state, openSnapshot(t, queue[0]), 0)
	if rows != nil {
		t.Errorf("first rows = %v", rows)
	}
	state, rows = mustApply(t, state, openSnapshot(t, queue[1]), 5)
	var numbers []PrNumber
	for _, row := range rows {
		numbers = append(numbers, row.Context.Number)
	}
	if !reflect.DeepEqual(numbers, []PrNumber{30, 31}) {
		t.Errorf("rows = %v", numbers)
	}
	if state.nextSweepAt != 305 {
		t.Errorf("nextSweepAt = %v", state.nextSweepAt)
	}
}

func TestQueueAdvanceContinuesToTheNewFrontierWithoutSleeping(t *testing.T) {
	one, two := testContext(40), testContext(41)
	base := newFakeReader(fakeOptions{})
	reads := map[PrNumber]int{}
	var timeline []string
	reader := hookReader{Reader: base, pullRequest: func(pr PrContext) (PullRequestFacts, error) {
		timeline = append(timeline, "read:"+pr.Number.String())
		facts, _ := base.PullRequest(pr)
		reads[pr.Number]++
		if pr.Number == one.Number && reads[pr.Number] > 1 {
			mergedAt := "2026-07-26T00:00:00Z"
			facts.State, facts.MergedAt = "MERGED", &mergedAt
		}
		return facts, nil
	}}
	deps, _ := timelineDeps(reader, &timeline, 2, "stop after advance proof")
	_, err := runQueued(deps, []PrContext{one, two}, testPolling)
	if err == nil || err.Error() != "stop after advance proof" {
		t.Fatalf("err = %v", err)
	}
	first := -1
	advanced := false
	for i, event := range timeline {
		if event == "sleep" && first < 0 {
			first = i
		}
		advanced = advanced || event == "emit:ADVANCE"
	}
	if !advanced {
		t.Errorf("no ADVANCE in %v", timeline)
	}
	if want := []string{"sleep", "read:40", "emit:ADVANCE", "read:41", "emit:WAITING"}; first < 0 || !reflect.DeepEqual(timeline[first:first+5], want) {
		t.Errorf("timeline = %v", timeline)
	}
}

func TestQueueDeduplicatesIdenticalWaitsAndSchedulesTheNextSweep(t *testing.T) {
	queue := []PrContext{testContext(50)}
	state := createQueueState(queue, 0)
	state, _ = mustApply(t, state, openSnapshot(t, queue[0]), 0)
	first, err := evaluateQueue(state, 0, testPolling)
	if err != nil || first.kind != "waiting" || !first.emit {
		t.Fatalf("first = %+v, %v", first, err)
	}
	second, err := evaluateQueue(first.state, 10, testPolling)
	if err != nil || second.kind != "waiting" || second.emit {
		t.Fatalf("second = %+v, %v", second, err)
	}
	if planned := planQueue(second.state, 300); planned.work == nil || planned.work.kind != "whole-stack-sweep" {
		t.Errorf("planned work = %+v", planned.work)
	}
}

func TestRetryBackoffFloorAndCap(t *testing.T) {
	for _, c := range []struct {
		interval float64
		failures int
		want     float64
	}{{1, 1, 60}, {1, 2, 120}, {60, 4, 300}} {
		if got := queryBackoffSeconds(c.interval, c.failures); got != c.want {
			t.Errorf("queryBackoffSeconds(%v, %d) = %v, want %v", c.interval, c.failures, got, c.want)
		}
	}
}

func TestPollingSleepsAreCappedAtTheDeadline(t *testing.T) {
	for _, mode := range []string{"single", "queued-stack", "query-retry"} {
		t.Run(mode, func(t *testing.T) {
			var sleeps []float64
			clock := &fakeClock{sleep: advancingSleep(&sleeps)}
			var reader Reader = newFakeReader(fakeOptions{fastPath: fastChecks(pendingCheck("ci"))})
			if mode == "query-retry" {
				reader = hookReader{Reader: reader, pullRequest: func(PrContext) (PullRequestFacts, error) {
					return PullRequestFacts{}, queryError(QueryFailure{Kind: "command-exit", Retryable: true, Code: 1, Detail: "temporary failure"})
				}}
			}
			deps := dependencies{reader: reader, clock: clock, emit: func(Verdict) {}}
			polling := testPolling
			polling.Interval, polling.Timeout = 60, 5
			var result Verdict
			var err error
			if mode == "queued-stack" {
				result, err = runQueued(deps, []PrContext{testContext(1)}, polling)
			} else {
				result, err = runSimple(deps, []PrContext{testContext(1)}, "single", false, polling)
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.Kind != "TIMEOUT" {
				t.Errorf("kind = %s", result.Kind)
			}
			if !reflect.DeepEqual(sleeps, []float64{5}) || clock.now != 5 {
				t.Errorf("sleeps = %v, now = %v", sleeps, clock.now)
			}
		})
	}
}

func TestPollingIntervalIsKeptWhenTheDeadlineIsDisabled(t *testing.T) {
	var sleeps []float64
	reads := 0
	pending := newFakeReader(fakeOptions{fastPath: fastChecks(pendingCheck("ci"))})
	reader := hookReader{Reader: pending, checksFastPath: func(PrContext) (ChecksFastPath, error) {
		reads++
		if reads == 1 {
			return *fastChecks(pendingCheck("ci")), nil
		}
		return *fastChecks(passingCheck("ci")), nil
	}}
	clock := &fakeClock{sleep: advancingSleep(&sleeps)}
	polling := testPolling
	polling.Interval, polling.Timeout = 60, 0
	result, err := runSimple(dependencies{reader: reader, clock: clock, emit: func(Verdict) {}}, []PrContext{testContext(1)}, "single", false, polling)
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != "READY" || !reflect.DeepEqual(sleeps, []float64{60}) {
		t.Errorf("kind = %s, sleeps = %v", result.Kind, sleeps)
	}
}

func TestRetryEmitsBackoffThenGivesUpAtTheErrorBudget(t *testing.T) {
	var emitted []Verdict
	var sleeps []float64
	reader := hookReader{Reader: newFakeReader(fakeOptions{}), pullRequest: func(PrContext) (PullRequestFacts, error) {
		return PullRequestFacts{}, queryError(QueryFailure{Kind: "command-exit", Retryable: true, Code: 1, Detail: "down"})
	}}
	deps := dependencies{reader: reader, clock: &fakeClock{sleep: advancingSleep(&sleeps)}, emit: func(v Verdict) { emitted = append(emitted, v) }}
	polling := testPolling
	polling.MaxQueryErrors = 4
	result, err := runSimple(deps, []PrContext{testContext(1)}, "single", false, polling)
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != "BLOCKER" || result.ExitCode != 7 || result.Blocker.Failures != 4 || result.Sequence != 4 {
		t.Errorf("result = %+v", result)
	}
	var retries []string
	for _, v := range emitted {
		retries = append(retries, v.Kind+":"+strconv.Itoa(v.ConsecutiveFailures))
	}
	if !reflect.DeepEqual(retries, []string{"RETRY:1", "RETRY:2", "RETRY:3"}) || !reflect.DeepEqual(sleeps, []float64{60, 120, 240}) {
		t.Errorf("retries = %v, sleeps = %v", retries, sleeps)
	}
}

func TestNonRetryableFailureStopsAtOnce(t *testing.T) {
	value := "https://example.com/x"
	reader := hookReader{Reader: newFakeReader(fakeOptions{}), pullRequest: func(PrContext) (PullRequestFacts, error) {
		return PullRequestFacts{}, queryError(QueryFailure{Kind: "invalid-context-url", Retryable: false, RawValue: &value, Detail: "bad"})
	}}
	result, err := runSimple(dependencies{reader: reader, clock: &fakeClock{}, emit: func(Verdict) { t.Error("emitted") }}, []PrContext{testContext(1)}, "single", false, testPolling)
	if err != nil || result.ExitCode != 7 || result.Blocker.Failures != 1 {
		t.Errorf("result = %+v, %v", result, err)
	}
}
