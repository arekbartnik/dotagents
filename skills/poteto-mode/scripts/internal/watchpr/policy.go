package watchpr

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

func assessGitHubMerge(mergeStateStatus, headRollupState string) GitHubMergeAssessment {
	if mergeStateStatus == "BLOCKED" {
		if headRollupState == "ERROR" || headRollupState == "FAILURE" {
			return GitHubMergeAssessment{Kind: "refused", MergeStateStatus: mergeStateStatus, HeadRollupState: enum(headRollupState)}
		}
		return GitHubMergeAssessment{Kind: "allowed", Basis: "rollup", MergeStateStatus: mergeStateStatus, HeadRollupState: enum(headRollupState)}
	}
	return GitHubMergeAssessment{Kind: "allowed", Basis: "merge-state", MergeStateStatus: mergeStateStatus, HeadRollupState: enum(headRollupState)}
}

func mergeAssessment(reader Reader, facts PullRequestFacts) (bool, GitHubMergeAssessment, error) {
	commits, err := reader.CommitRollups(facts.Context)
	if err != nil {
		return false, GitHubMergeAssessment{}, err
	}
	headRollupState := ""
	if facts.HeadRefOid != nil {
		for _, commit := range commits {
			if commit.Oid == *facts.HeadRefOid {
				headRollupState = commit.State
				break
			}
		}
	}
	hadPreviousPassingCi := false
	for _, commit := range commits {
		if (facts.HeadRefOid == nil || commit.Oid != *facts.HeadRefOid) && commit.State == "SUCCESS" {
			hadPreviousPassingCi = true
		}
	}
	return hadPreviousPassingCi, assessGitHubMerge(facts.MergeStateStatus, headRollupState), nil
}

var automationTokens = []string{"bugbot", "security review", "pr review automation", "review automation"}

func filterChecks(checks []Check, kind string) []Check {
	var out []Check
	for _, check := range checks {
		if check.Kind == kind {
			out = append(out, check)
		}
	}
	return out
}

// readSnapshot reads one PR. omitPendingHistory skips the commit-rollup query
// while visible checks are only pending, as queued mode does.
func readSnapshot(reader Reader, context PrContext, omitPendingHistory bool) (PrSnapshot, error) {
	facts, err := reader.PullRequest(context)
	if err != nil {
		return PrSnapshot{}, err
	}
	if facts.State == "MERGED" || facts.MergedAt != nil {
		return PrSnapshot{Kind: "merged", Context: context, Facts: facts}, nil
	}
	if facts.State == "CLOSED" {
		return PrSnapshot{Kind: "closed", Context: context, Facts: facts}, nil
	}
	threads, err := resolveReviewThreads(reader, context)
	if err != nil {
		return PrSnapshot{}, err
	}
	checks, err := resolveChecks(reader, context)
	if err != nil {
		return PrSnapshot{}, err
	}
	failed := filterChecks(checks.Checks, "failed")
	pending := filterChecks(checks.Checks, "pending")
	ci := &CiState{Source: checks.Source, All: checks.Checks, Failed: []Check{}, Pending: []Check{}}
	if len(failed) == 0 && len(pending) > 0 && omitPendingHistory {
		ci.Kind, ci.Pending, ci.historyOmitted = "ci-pending", pending, true
	} else {
		hadPrevious, github, err := mergeAssessment(reader, facts)
		if err != nil {
			return PrSnapshot{}, err
		}
		ci.HadPreviousPassingCi = hadPrevious
		if pending != nil {
			ci.Pending = pending
		}
		switch {
		case len(failed) > 0:
			ci.Kind, ci.Failed, ci.Github = "ci-failing", failed, &github
		case github.Kind == "refused":
			ci.Kind, ci.Github = "ci-github-rejected", &github
		case len(pending) > 0:
			ci.Kind = "ci-pending"
		default:
			ci.Kind, ci.Github = "ci-clean", &github
		}
	}
	running := false
	for _, check := range checks.Checks {
		if check.Kind != "pending" {
			continue
		}
		name := strings.ToLower(check.Name)
		for _, token := range automationTokens {
			if strings.Contains(name, token) {
				running = true
			}
		}
	}
	return PrSnapshot{Kind: "open", Context: context, Facts: facts, Threads: threads, Ci: ci, ReviewAutomationRunning: running}, nil
}

func conflictBlocker(row PrSnapshot) *MergeBlocker {
	if row.Kind == "open" && (row.Facts.Mergeable == "CONFLICTING" || row.Facts.MergeStateStatus == "DIRTY" || row.Facts.MergeStateStatus == "CONFLICTING") {
		facts := row.Facts
		return &MergeBlocker{Kind: "merge-conflicts", Pr: row.Context, Facts: &facts}
	}
	return nil
}

func threadBlocker(row PrSnapshot) *MergeBlocker {
	if row.Kind != "open" || len(row.Threads) == 0 {
		return nil
	}
	return &MergeBlocker{Kind: "review-threads", Pr: row.Context, Threads: row.Threads}
}

func ciBlocker(row PrSnapshot) *MergeBlocker {
	if row.Kind == "open" && (row.Ci.Kind == "ci-failing" || row.Ci.Kind == "ci-github-rejected") {
		return &MergeBlocker{Kind: "failing-checks", Pr: row.Context, Ci: row.Ci}
	}
	return nil
}

func gateReason(row PrSnapshot, allowDraft bool) string {
	switch {
	case row.Kind == "merged":
		return ""
	case row.Kind == "closed":
		return "closed-without-merge"
	case row.Facts.IsDraft && !allowDraft:
		return "draft-pr"
	case row.Facts.ReviewDecision == "CHANGES_REQUESTED":
		return "changes-requested"
	}
	return ""
}

func gateBlocker(row PrSnapshot, allowDraft bool) *MergeBlocker {
	reason := gateReason(row, allowDraft)
	if reason == "" || (reason == "draft-pr" && row.Kind == "open" && row.Ci.Kind == "ci-pending") {
		return nil
	}
	return &MergeBlocker{Kind: "merge-gate", Pr: row.Context, Reason: reason}
}

func readyContribution(row PrSnapshot, allowDraft bool) *Contribution {
	if row.Kind == "merged" {
		return &Contribution{Kind: "merged-pr", Context: row.Context, MergedAt: row.Facts.MergedAt}
	}
	if row.Kind != "open" || row.Ci.Kind != "ci-clean" || len(row.Threads) != 0 || conflictBlocker(row) != nil || gateReason(row, allowDraft) != "" {
		return nil
	}
	if row.Facts.ReviewDecision == "CHANGES_REQUESTED" {
		return nil
	}
	draft := "not-draft"
	if row.Facts.IsDraft {
		draft = "draft-allowed"
	}
	return &Contribution{Kind: "ready-pr", Context: row.Context, Ci: row.Ci, Gate: ReadyGate{State: "OPEN", ReviewDecision: row.Facts.ReviewDecision, Draft: draft}}
}

func classifyPr(row PrSnapshot, allowDraft bool) (Decision, error) {
	for _, blocker := range []*MergeBlocker{conflictBlocker(row), threadBlocker(row), ciBlocker(row), gateBlocker(row, allowDraft)} {
		if blocker != nil {
			return Decision{Kind: "blocker", Blocker: blocker}, nil
		}
	}
	if row.Kind == "open" && row.Ci.Kind == "ci-pending" {
		return Decision{Kind: "waiting", Frontier: row.Context, Pending: row.Ci.Pending}, nil
	}
	ready := readyContribution(row, allowDraft)
	if ready == nil {
		return Decision{}, errors.New("snapshot has no classified decision")
	}
	if ready.Kind == "merged-pr" {
		return Decision{Kind: "merged", Pr: ready}, nil
	}
	return Decision{Kind: "ready", Pr: ready}, nil
}

func selectTierMajorStackDecision(rows []PrSnapshot, allowDraft bool) (Decision, error) {
	for _, tier := range []func(PrSnapshot) *MergeBlocker{conflictBlocker, threadBlocker, ciBlocker} {
		for _, row := range rows {
			if blocker := tier(row); blocker != nil {
				return Decision{Kind: "blocker", Blocker: blocker}, nil
			}
		}
	}
	for _, row := range rows {
		if blocker := gateBlocker(row, allowDraft); blocker != nil {
			return Decision{Kind: "blocker", Blocker: blocker}, nil
		}
	}
	for _, row := range rows {
		if row.Kind == "open" && row.Ci.Kind == "ci-pending" {
			return Decision{Kind: "waiting", Frontier: row.Context, Pending: row.Ci.Pending}, nil
		}
	}
	var prs []Contribution
	for _, row := range rows {
		if ready := readyContribution(row, allowDraft); ready != nil {
			prs = append(prs, *ready)
		}
	}
	if len(prs) == 0 || len(prs) != len(rows) {
		return Decision{}, errors.New("stack has no classified decision")
	}
	return Decision{Kind: "clear", Prs: prs}, nil
}

func queryBackoffSeconds(interval float64, failures int) float64 {
	return math.Min(math.Max(interval, 60)*math.Pow(2, float64(failures-1)), 300)
}

// Clock is the watcher's time source, injectable so tests never sleep. Now is
// monotonic seconds; a Sleep error aborts the run.
type Clock interface {
	Now() float64
	ObservedAt() string
	Sleep(seconds float64) error
}

type verdictStamp struct {
	clock    Clock
	mode     string
	sequence int
}

func newStamp(clock Clock, mode string) *verdictStamp {
	return &verdictStamp{clock: clock, mode: mode}
}

func (s *verdictStamp) stamp(v Verdict) Verdict {
	s.sequence++
	v.Sequence = s.sequence
	v.ObservedAt = s.clock.ObservedAt()
	if v.Mode == "" {
		v.Mode = s.mode
	}
	return v
}

func blockerVerdict(s *verdictStamp, blocker *MergeBlocker) Verdict {
	codes := map[string]int{"merge-conflicts": 2, "review-threads": 3, "failing-checks": 4, "merge-gate": 6}
	return s.stamp(Verdict{Kind: "BLOCKER", Terminal: true, ExitCode: codes[blocker.Kind], Blocker: blocker})
}

func statusQueryVerdict(s *verdictStamp, failures int, failure QueryFailure) Verdict {
	return s.stamp(Verdict{Kind: "BLOCKER", Terminal: true, ExitCode: 7, Blocker: &MergeBlocker{Kind: "status-query", Failures: failures, Failure: &failure}})
}

type dependencies struct {
	reader Reader
	clock  Clock
	emit   func(Verdict)
}

func deadlinePassed(started float64, options PollingOptions, now float64) bool {
	return options.Timeout > 0 && now-started >= options.Timeout
}

// stepResult kinds: terminal (verdict), sleep (seconds, optional onDeadline),
// continue.
type stepResult struct {
	kind       string
	verdict    Verdict
	seconds    float64
	onDeadline func() Verdict
}

func pollUntilTerminal(deps dependencies, options PollingOptions, s *verdictStamp, step func() (stepResult, error)) (Verdict, error) {
	failures := 0
	started := deps.clock.Now()
	sleep := func(seconds float64) error {
		remaining := seconds
		if options.Timeout > 0 {
			remaining = math.Max(0, options.Timeout-(deps.clock.Now()-started))
		}
		return deps.clock.Sleep(math.Min(seconds, remaining))
	}
	for {
		result, err := step()
		if err != nil {
			var queryErr *QueryError
			if !errors.As(err, &queryErr) {
				return Verdict{}, err
			}
			failures++
			failure := queryErr.Failure
			if !failure.Retryable || failures >= options.MaxQueryErrors {
				return statusQueryVerdict(s, failures, failure), nil
			}
			retryInSeconds := queryBackoffSeconds(options.Interval, failures)
			deps.emit(s.stamp(Verdict{Kind: "RETRY", Failure: &failure, ConsecutiveFailures: failures, RetryInSeconds: retryInSeconds}))
			if deadlinePassed(started, options, deps.clock.Now()) {
				return s.stamp(Verdict{Kind: "TIMEOUT", Terminal: true, ExitCode: 5, Timeout: &TimeoutReason{Kind: "status-unavailable", Failure: &failure}}), nil
			}
			if err := sleep(retryInSeconds); err != nil {
				return Verdict{}, err
			}
			continue
		}
		failures = 0
		switch result.kind {
		case "terminal":
			return result.verdict, nil
		case "sleep":
			if result.onDeadline != nil && deadlinePassed(started, options, deps.clock.Now()) {
				return result.onDeadline(), nil
			}
			if err := sleep(result.seconds); err != nil {
				return Verdict{}, err
			}
		}
	}
}

func runSimple(deps dependencies, contexts []PrContext, mode string, statusOnly bool, options PollingOptions) (Verdict, error) {
	s := newStamp(deps.clock, mode)
	step := func() (stepResult, error) {
		var rows []PrSnapshot
		for _, context := range contexts {
			row, err := readSnapshot(deps.reader, context, false)
			if err != nil {
				return stepResult{}, err
			}
			rows = append(rows, row)
		}
		if len(rows) == 0 {
			return stepResult{}, errors.New("watch context cannot be empty")
		}
		if statusOnly {
			return stepResult{kind: "terminal", verdict: s.stamp(Verdict{Kind: "STATUS", Terminal: true, ExitCode: 0, StatusReason: "status-only", Rows: rows})}, nil
		}
		if mode == "queued-stack" {
			return stepResult{}, errors.New("queued-stack requires status-only in the simple runner")
		}
		if mode == "stack" {
			deps.emit(s.stamp(Verdict{Kind: "STATUS", StatusReason: "poll", Rows: rows}))
		}
		var decision Decision
		var err error
		if mode == "single" {
			decision, err = classifyPr(rows[0], options.AllowDraft)
		} else {
			decision, err = selectTierMajorStackDecision(rows, options.AllowDraft)
		}
		if err != nil {
			return stepResult{}, err
		}
		switch decision.Kind {
		case "blocker":
			return stepResult{kind: "terminal", verdict: blockerVerdict(s, decision.Blocker)}, nil
		case "ready", "merged":
			return stepResult{kind: "terminal", verdict: s.stamp(Verdict{Kind: "READY", Terminal: true, ExitCode: 0, Scope: &ReadyScope{Kind: "single", Pr: decision.Pr}})}, nil
		case "clear":
			return stepResult{kind: "terminal", verdict: s.stamp(Verdict{Kind: "READY", Terminal: true, ExitCode: 0, Scope: &ReadyScope{Kind: "stack", Prs: decision.Prs}})}, nil
		}
		deps.emit(s.stamp(Verdict{Kind: "WAITING", Frontier: decision.Frontier, Wait: &WaitReason{Kind: "pending-checks", Pending: decision.Pending}}))
		return stepResult{
			kind:    "sleep",
			seconds: options.Interval,
			onDeadline: func() Verdict {
				return s.stamp(Verdict{Kind: "TIMEOUT", Terminal: true, ExitCode: 5, Timeout: &TimeoutReason{Kind: "pending-checks", Pending: decision.Pending}})
			},
		}, nil
	}
	return pollUntilTerminal(deps, options, s, step)
}

// queueWork kinds: whole-stack-sweep (remaining), frontier-poll (frontier).
type queueWork struct {
	kind      string
	remaining []PrContext
	frontier  PrContext
}

type queueState struct {
	queue       []PrContext
	snapshots   map[PrNumber]PrSnapshot
	work        *queueWork
	nextSweepAt float64
	frontier    *PrContext
	lastWaitKey string
	startedAt   float64
}

func createQueueState(queue []PrContext, now float64) queueState {
	return queueState{
		queue:       queue,
		snapshots:   map[PrNumber]PrSnapshot{},
		work:        &queueWork{kind: "whole-stack-sweep", remaining: queue},
		nextSweepAt: now,
		startedAt:   now,
	}
}

func orderedRows(state queueState) []PrSnapshot {
	var rows []PrSnapshot
	for _, context := range state.queue {
		if row, ok := state.snapshots[context.Number]; ok {
			rows = append(rows, row)
		}
	}
	return rows
}

func activeRows(state queueState) []PrSnapshot {
	var rows []PrSnapshot
	for _, row := range orderedRows(state) {
		if row.Kind != "merged" {
			rows = append(rows, row)
		}
	}
	return rows
}

func planQueue(state queueState, now float64) queueState {
	if state.work != nil {
		return state
	}
	if len(state.snapshots) == 0 || now >= state.nextSweepAt {
		var remaining []PrContext
		for _, context := range state.queue {
			if row, ok := state.snapshots[context.Number]; !ok || row.Kind != "merged" {
				remaining = append(remaining, context)
			}
		}
		if len(remaining) > 0 {
			state.work = &queueWork{kind: "whole-stack-sweep", remaining: remaining}
			return state
		}
	}
	if active := activeRows(state); len(active) > 0 {
		state.work = &queueWork{kind: "frontier-poll", frontier: active[0].Context}
	}
	return state
}

// applyQueueSnapshot returns the sweep's rows once its final read lands.
func applyQueueSnapshot(state queueState, snapshot PrSnapshot, now float64, options PollingOptions) (queueState, []PrSnapshot, error) {
	if state.work == nil {
		return state, nil, errors.New("queue has no read in flight")
	}
	snapshots := make(map[PrNumber]PrSnapshot, len(state.snapshots)+1)
	for number, row := range state.snapshots {
		snapshots[number] = row
	}
	snapshots[snapshot.Context.Number] = snapshot
	work := state.work
	state.snapshots = snapshots
	if work.kind == "frontier-poll" {
		state.work = nil
		return state, nil, nil
	}
	if work.remaining[0].Number != snapshot.Context.Number {
		return state, nil, errors.New("snapshot does not match sweep head")
	}
	if tail := work.remaining[1:]; len(tail) > 0 {
		state.work = &queueWork{kind: "whole-stack-sweep", remaining: tail}
		return state, nil, nil
	}
	rows := orderedRows(state)
	if len(rows) == 0 || len(rows) != len(state.queue) {
		return state, nil, errors.New("sweep completed without every snapshot")
	}
	state.work = nil
	state.nextSweepAt = now + options.SweepInterval
	return state, rows, nil
}

// queueEvaluation kinds: complete (merged), blocker, advance (merged,
// frontier, remaining), timeout (frontier, unmergedCount), waiting (frontier,
// reason, emit).
type queueEvaluation struct {
	kind          string
	state         queueState
	mergedPrs     []Contribution
	blocker       *MergeBlocker
	merged        PrContext
	frontier      PrContext
	remaining     int
	unmergedCount int
	reason        *WaitReason
	emit          bool
}

func evaluateQueue(state queueState, now float64, options PollingOptions) (queueEvaluation, error) {
	active := activeRows(state)
	if len(active) == 0 {
		var merged []Contribution
		for _, row := range orderedRows(state) {
			if row.Kind == "merged" {
				merged = append(merged, Contribution{Kind: "merged-pr", Context: row.Context, MergedAt: row.Facts.MergedAt})
			}
		}
		if len(merged) == 0 {
			return queueEvaluation{}, errors.New("empty queue cannot complete")
		}
		return queueEvaluation{kind: "complete", state: state, mergedPrs: merged}, nil
	}
	decision, err := selectTierMajorStackDecision(active, options.AllowDraft)
	if err != nil {
		return queueEvaluation{}, err
	}
	if decision.Kind == "blocker" {
		return queueEvaluation{kind: "blocker", state: state, blocker: decision.Blocker}, nil
	}
	frontier := active[0].Context
	if state.frontier != nil && state.frontier.Number != frontier.Number {
		merged := *state.frontier
		state.frontier, state.lastWaitKey = &frontier, ""
		return queueEvaluation{kind: "advance", state: state, merged: merged, frontier: frontier, remaining: len(active)}, nil
	}
	if deadlinePassed(state.startedAt, options, now) {
		state.frontier = &frontier
		return queueEvaluation{kind: "timeout", state: state, frontier: frontier, unmergedCount: len(active)}, nil
	}
	row := active[0]
	reason := &WaitReason{Kind: "merge-queue", UnmergedCount: len(active)}
	key := fmt.Sprintf("queue:%s:%d", frontier.Number, len(active))
	if row.Kind == "open" && row.Ci.Kind == "ci-pending" {
		reason = &WaitReason{Kind: "pending-checks", Pending: row.Ci.Pending}
		key = fmt.Sprintf("pending:%s:%d", frontier.Number, len(row.Ci.Pending))
	}
	emit := state.lastWaitKey != key
	state.frontier, state.lastWaitKey = &frontier, key
	return queueEvaluation{kind: "waiting", state: state, frontier: frontier, reason: reason, emit: emit}, nil
}

func runQueued(deps dependencies, contexts []PrContext, options PollingOptions) (Verdict, error) {
	state := createQueueState(contexts, deps.clock.Now())
	s := newStamp(deps.clock, "queued-stack")
	deps.emit(s.stamp(Verdict{Kind: "QUEUE", Queue: contexts}))
	complete := func(merged []Contribution) stepResult {
		return stepResult{kind: "terminal", verdict: s.stamp(Verdict{Kind: "COMPLETE", Terminal: true, ExitCode: 0, Queue: state.queue, MergedPrs: merged})}
	}
	step := func() (stepResult, error) {
		state = planQueue(state, deps.clock.Now())
		if state.work == nil {
			evaluation, err := evaluateQueue(state, deps.clock.Now(), options)
			if err != nil {
				return stepResult{}, err
			}
			if evaluation.kind != "complete" {
				return stepResult{}, errors.New("queue has no work while active")
			}
			return complete(evaluation.mergedPrs), nil
		}
		context := state.work.frontier
		if state.work.kind == "whole-stack-sweep" {
			context = state.work.remaining[0]
		}
		snapshot, err := readSnapshot(deps.reader, context, true)
		if err != nil {
			return stepResult{}, err
		}
		next, sweepRows, err := applyQueueSnapshot(state, snapshot, deps.clock.Now(), options)
		if err != nil {
			return stepResult{}, err
		}
		state = next
		if sweepRows != nil {
			deps.emit(s.stamp(Verdict{Kind: "STATUS", StatusReason: "whole-stack-sweep", Rows: sweepRows}))
		}
		if state.work != nil {
			return stepResult{kind: "continue"}, nil
		}
		evaluation, err := evaluateQueue(state, deps.clock.Now(), options)
		if err != nil {
			return stepResult{}, err
		}
		state = evaluation.state
		switch evaluation.kind {
		case "complete":
			return complete(evaluation.mergedPrs), nil
		case "blocker":
			return stepResult{kind: "terminal", verdict: blockerVerdict(s, evaluation.blocker)}, nil
		case "advance":
			deps.emit(s.stamp(Verdict{Kind: "ADVANCE", Merged: evaluation.merged, Frontier: evaluation.frontier, Remaining: evaluation.remaining}))
			return stepResult{kind: "continue"}, nil
		case "timeout":
			return stepResult{kind: "terminal", verdict: s.stamp(Verdict{Kind: "TIMEOUT", Terminal: true, ExitCode: 5, Timeout: &TimeoutReason{Kind: "queued-stack", Frontier: evaluation.frontier, UnmergedCount: evaluation.unmergedCount}})}, nil
		}
		if evaluation.emit {
			deps.emit(s.stamp(Verdict{Kind: "WAITING", Frontier: evaluation.frontier, Wait: evaluation.reason}))
		}
		return stepResult{kind: "sleep", seconds: options.Interval}, nil
	}
	return pollUntilTerminal(deps, options, s, step)
}
