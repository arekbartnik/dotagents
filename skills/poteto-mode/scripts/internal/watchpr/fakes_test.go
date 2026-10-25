package watchpr

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type fakeOptions struct {
	facts            func(*PullRequestFacts)
	fastPath         *ChecksFastPath
	rollupPages      []RollupPage
	threads          []ReviewThread
	threadPages      []ReviewThreadPage
	commitRollups    []CommitRollup
	openPullRequests []OpenPullRequest
	origin           *Repository
	noOrigin         bool
	current          *PrContext
}

// fakeReader serves canned answers and logs every call.
type fakeReader struct {
	options    fakeOptions
	calls      []string
	page       int
	threadPage int
}

func newFakeReader(options fakeOptions) *fakeReader { return &fakeReader{options: options} }

func (f *fakeReader) context() PrContext {
	if f.options.current != nil {
		return *f.options.current
	}
	return PrContext{Owner: "owner", Repo: "repo", Number: 1}
}

func (f *fakeReader) OriginRepo() (*Repository, error) {
	f.calls = append(f.calls, "originRepo")
	if f.options.noOrigin {
		return nil, nil
	}
	if f.options.origin != nil {
		return f.options.origin, nil
	}
	return &Repository{Owner: "owner", Repo: "repo"}, nil
}

func (f *fakeReader) CurrentPr(pr *PrNumber) (PrContext, error) {
	f.calls = append(f.calls, "currentPr")
	context := f.context()
	if pr != nil {
		context.Number = *pr
	}
	return context, nil
}

func (f *fakeReader) PullRequest(requested PrContext) (PullRequestFacts, error) {
	f.calls = append(f.calls, "pullRequest")
	head := "head"
	facts := PullRequestFacts{
		Context:          f.context(),
		Mergeable:        "MERGEABLE",
		MergeStateStatus: "CLEAN",
		ReviewDecision:   "APPROVED",
		HeadRefOid:       &head,
		HeadRefName:      "feature",
		BaseRefName:      "main",
		State:            "OPEN",
	}
	if f.options.facts != nil {
		f.options.facts(&facts)
	}
	facts.Context = requested
	return facts, nil
}

func (f *fakeReader) OpenPullRequests(Repository) ([]OpenPullRequest, error) {
	f.calls = append(f.calls, "openPullRequests")
	return f.options.openPullRequests, nil
}

func (f *fakeReader) ChecksFastPath(PrContext) (ChecksFastPath, error) {
	f.calls = append(f.calls, "checksFastPath")
	if f.options.fastPath != nil {
		return *f.options.fastPath, nil
	}
	return ChecksFastPath{Usable: true, Checks: []Check{passingCheck("ci")}}, nil
}

func cursorLabel(after string) string {
	if after == "" {
		return "null"
	}
	return after
}

func (f *fakeReader) CheckRollupPage(_ PrContext, after string) (RollupPage, error) {
	f.calls = append(f.calls, "checkRollupPage:"+cursorLabel(after))
	if f.page < len(f.options.rollupPages) {
		f.page++
		return f.options.rollupPages[f.page-1], nil
	}
	f.page++
	return RollupPage{}, nil
}

func (f *fakeReader) ReviewThreadPage(_ PrContext, after string) (ReviewThreadPage, error) {
	f.calls = append(f.calls, "reviewThreadPage:"+cursorLabel(after))
	if f.threadPage < len(f.options.threadPages) {
		f.threadPage++
		return f.options.threadPages[f.threadPage-1], nil
	}
	f.threadPage++
	var raw []RawReviewThread
	for _, thread := range f.options.threads {
		raw = append(raw, RawReviewThread{ID: thread.ID, FirstComment: thread.FirstComment})
	}
	return ReviewThreadPage{Threads: raw}, nil
}

func (f *fakeReader) CommitRollups(PrContext) ([]CommitRollup, error) {
	f.calls = append(f.calls, "commitRollups")
	if f.options.commitRollups != nil {
		return f.options.commitRollups, nil
	}
	return []CommitRollup{{Oid: "head", State: "SUCCESS"}}, nil
}

// hookReader replaces single reads on top of another reader.
type hookReader struct {
	Reader
	pullRequest    func(PrContext) (PullRequestFacts, error)
	checksFastPath func(PrContext) (ChecksFastPath, error)
}

func (h hookReader) PullRequest(context PrContext) (PullRequestFacts, error) {
	if h.pullRequest != nil {
		return h.pullRequest(context)
	}
	return h.Reader.PullRequest(context)
}

func (h hookReader) ChecksFastPath(context PrContext) (ChecksFastPath, error) {
	if h.checksFastPath != nil {
		return h.checksFastPath(context)
	}
	return h.Reader.ChecksFastPath(context)
}

func passingCheck(name string) Check {
	return Check{Kind: "passed", Name: name, ReportedState: "SUCCESS"}
}

func pendingCheck(name string) Check {
	return Check{Kind: "pending", Name: name, ReportedState: "PENDING"}
}

func failedCheck(name string) Check {
	return Check{Kind: "failed", Name: name, ReportedState: "FAILURE"}
}

func fastChecks(checks ...Check) *ChecksFastPath {
	return &ChecksFastPath{Usable: true, Checks: checks}
}

// fakeClock never really sleeps. A nil sleep fails the test's run.
type fakeClock struct {
	now   float64
	sleep func(c *fakeClock, seconds float64) error
}

func (c *fakeClock) Now() float64       { return c.now }
func (c *fakeClock) ObservedAt() string { return "2026-07-26T00:00:00.000Z" }

func (c *fakeClock) Sleep(seconds float64) error {
	if c.sleep == nil {
		return errors.New("test unexpectedly slept")
	}
	return c.sleep(c, seconds)
}

func advancingSleep(sleeps *[]float64) func(*fakeClock, float64) error {
	return func(c *fakeClock, seconds float64) error {
		*sleeps = append(*sleeps, seconds)
		c.now += seconds
		return nil
	}
}

// writes records each Write call separately.
type writes struct{ chunks []string }

func (w *writes) Write(p []byte) (int, error) {
	w.chunks = append(w.chunks, string(p))
	return len(p), nil
}

func (w *writes) String() string { return strings.Join(w.chunks, "") }

type testHarness struct {
	runtime runtime
	stdout  *writes
	stderr  *bytes.Buffer
}

func newHarness(reader Reader) testHarness {
	stdout, stderr := &writes{}, &bytes.Buffer{}
	return testHarness{
		runtime: runtime{reader: reader, clock: &fakeClock{}, stdout: stdout, stderr: stderr},
		stdout:  stdout,
		stderr:  stderr,
	}
}

func testContext(number PrNumber) PrContext {
	return PrContext{Owner: "owner", Repo: "repo", Number: number}
}

var testPolling = PollingOptions{Interval: 10, SweepInterval: 300, Timeout: 0, MaxQueryErrors: 5}

func ptr[T any](v T) *T { return &v }

func fromJSON[T any](t *testing.T, text string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// perPr edits the facts or replaces the checks of single PRs read through base.
func perPr(base Reader, facts map[PrNumber]func(*PullRequestFacts), checks map[PrNumber]*ChecksFastPath) Reader {
	return hookReader{
		Reader: base,
		pullRequest: func(pr PrContext) (PullRequestFacts, error) {
			f, err := base.PullRequest(pr)
			if edit := facts[pr.Number]; edit != nil {
				edit(&f)
			}
			return f, err
		},
		checksFastPath: func(pr PrContext) (ChecksFastPath, error) {
			if c := checks[pr.Number]; c != nil {
				return *c, nil
			}
			return base.ChecksFastPath(pr)
		},
	}
}
