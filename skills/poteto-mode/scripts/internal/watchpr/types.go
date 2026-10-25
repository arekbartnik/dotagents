package watchpr

import (
	"bytes"
	"encoding/json"
	"strconv"
)

type PrNumber int

func (n PrNumber) String() string { return strconv.Itoa(int(n)) }

// enum is a nullable GitHub enum value. None of the valid values is empty, so
// "" stands for null.
type enum string

func (e enum) MarshalJSON() ([]byte, error) {
	if e == "" {
		return []byte("null"), nil
	}
	return marshal(string(e))
}

// marshal is json.Marshal without HTML escaping, so check names and comment
// bodies keep their < > & as written.
func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	encoder := json.NewEncoder(&b)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

type Repository struct {
	Owner string
	Repo  string
}

type PrContext struct {
	Owner  string   `json:"owner"`
	Repo   string   `json:"repo"`
	Number PrNumber `json:"number"`
}

type PullRequestFacts struct {
	Context          PrContext `json:"context"`
	Mergeable        string    `json:"mergeable"`
	MergeStateStatus string    `json:"mergeStateStatus"`
	ReviewDecision   enum      `json:"reviewDecision"`
	HeadRefOid       *string   `json:"headRefOid"`
	HeadRefName      string    `json:"headRefName"`
	BaseRefName      string    `json:"baseRefName"`
	State            string    `json:"state"`
	MergedAt         *string   `json:"mergedAt"`
	IsDraft          bool      `json:"isDraft"`
}

type OpenPullRequest struct {
	Number            PrNumber
	IsCrossRepository bool
	HeadRefName       string
	BaseRefName       string
}

type ReviewComment struct {
	AuthorLogin *string `json:"authorLogin"`
	Body        string  `json:"body"`
	Path        *string `json:"path"`
	Line        *int    `json:"line"`
	CreatedAt   string  `json:"createdAt"`
}

type ReviewThread struct {
	ID                 string         `json:"id"`
	FirstComment       *ReviewComment `json:"firstComment"`
	IsBugbot           bool           `json:"isBugbot"`
	BugbotReviewPasses int            `json:"bugbotReviewPasses"`
}

type RawReviewThread struct {
	ID           string
	FirstComment *ReviewComment
	Resolved     bool
}

// Check kinds: passed, skipped, failed, pending, code-review-gate.
type Check struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	Link          string `json:"link"`
	Workflow      string `json:"workflow"`
	Kind          string `json:"kind"`
	ReportedState string `json:"reportedState"`
}

type CheckRead struct {
	Source string
	Checks []Check
}

type CommitRollup struct {
	Oid   string
	State string
}

// GitHubMergeAssessment kinds: allowed (with Basis) and refused.
type GitHubMergeAssessment struct {
	Kind             string `json:"kind"`
	Basis            string `json:"basis,omitempty"`
	MergeStateStatus string `json:"mergeStateStatus"`
	HeadRollupState  enum   `json:"headRollupState"`
}

// CiState kinds: ci-failing, ci-github-rejected, ci-pending, ci-clean.
// Github is nil exactly for ci-pending. historyOmitted records which literal
// built a pending state, since the two spell their keys in different orders.
type CiState struct {
	Kind                 string
	Source               string
	All                  []Check
	Failed               []Check
	Pending              []Check
	HadPreviousPassingCi bool
	Github               *GitHubMergeAssessment
	historyOmitted       bool
}

func (c *CiState) MarshalJSON() ([]byte, error) {
	if c.historyOmitted {
		return marshal(struct {
			Kind                 string  `json:"kind"`
			Source               string  `json:"source"`
			All                  []Check `json:"all"`
			Failed               []Check `json:"failed"`
			Pending              []Check `json:"pending"`
			HadPreviousPassingCi bool    `json:"hadPreviousPassingCi"`
		}{c.Kind, c.Source, c.All, c.Failed, c.Pending, c.HadPreviousPassingCi})
	}
	return marshal(struct {
		Source               string                 `json:"source"`
		All                  []Check                `json:"all"`
		HadPreviousPassingCi bool                   `json:"hadPreviousPassingCi"`
		Kind                 string                 `json:"kind"`
		Failed               []Check                `json:"failed"`
		Pending              []Check                `json:"pending"`
		Github               *GitHubMergeAssessment `json:"github,omitempty"`
	}{c.Source, c.All, c.HadPreviousPassingCi, c.Kind, c.Failed, c.Pending, c.Github})
}

// PrSnapshot kinds: merged, closed, open. Threads and Ci are set only when open.
type PrSnapshot struct {
	Kind                    string
	Context                 PrContext
	Facts                   PullRequestFacts
	Threads                 []ReviewThread
	Ci                      *CiState
	ReviewAutomationRunning bool
}

type snapshotHead struct {
	Kind    string           `json:"kind"`
	Context PrContext        `json:"context"`
	Facts   PullRequestFacts `json:"facts"`
}

func (s PrSnapshot) MarshalJSON() ([]byte, error) {
	head := snapshotHead{s.Kind, s.Context, s.Facts}
	if s.Kind != "open" {
		return marshal(head)
	}
	return marshal(struct {
		snapshotHead
		Threads                 []ReviewThread `json:"threads"`
		Ci                      *CiState       `json:"ci"`
		ReviewAutomationRunning bool           `json:"reviewAutomationRunning"`
	}{head, s.Threads, s.Ci, s.ReviewAutomationRunning})
}

// Contribution is a ReadyPr (kind ready-pr, with Ci and Gate as its proof) or
// a MergedPr (kind merged-pr, with MergedAt).
type Contribution struct {
	Kind     string
	Context  PrContext
	Ci       *CiState
	Gate     ReadyGate
	MergedAt *string
}

type ReadyGate struct {
	State          string `json:"state"`
	ReviewDecision enum   `json:"reviewDecision"`
	Draft          string `json:"draft"`
}

func (c Contribution) MarshalJSON() ([]byte, error) {
	if c.Kind == "merged-pr" {
		return marshal(struct {
			Kind     string    `json:"kind"`
			Context  PrContext `json:"context"`
			MergedAt *string   `json:"mergedAt"`
		}{c.Kind, c.Context, c.MergedAt})
	}
	type proof struct {
		Mergeability string         `json:"mergeability"`
		Threads      []ReviewThread `json:"threads"`
		Ci           *CiState       `json:"ci"`
		Gate         ReadyGate      `json:"gate"`
	}
	return marshal(struct {
		Kind    string    `json:"kind"`
		Context PrContext `json:"context"`
		Proof   proof     `json:"proof"`
	}{c.Kind, c.Context, proof{"clear", []ReviewThread{}, c.Ci, c.Gate}})
}

// MergeBlocker kinds: merge-conflicts (Facts), review-threads (Threads),
// failing-checks (Ci), merge-gate (Reason), and status-query (Failures,
// Failure), which carries no PR.
type MergeBlocker struct {
	Kind     string
	Pr       PrContext
	Facts    *PullRequestFacts
	Threads  []ReviewThread
	Ci       *CiState
	Reason   string
	Failures int
	Failure  *QueryFailure
}

func (b MergeBlocker) MarshalJSON() ([]byte, error) {
	if b.Kind == "status-query" {
		return marshal(struct {
			Kind     string        `json:"kind"`
			Failures int           `json:"failures"`
			Failure  *QueryFailure `json:"failure"`
		}{b.Kind, b.Failures, b.Failure})
	}
	return marshal(struct {
		Kind    string            `json:"kind"`
		Pr      PrContext         `json:"pr"`
		Facts   *PullRequestFacts `json:"facts,omitempty"`
		Threads []ReviewThread    `json:"threads,omitempty"`
		Ci      *CiState          `json:"ci,omitempty"`
		Reason  string            `json:"reason,omitempty"`
	}{b.Kind, b.Pr, b.Facts, b.Threads, b.Ci, b.Reason})
}

// QueryFailure kinds: json-parse, missing-key (RawValue optional),
// command-exit (Code), checks-unavailable, invalid-context-url (RawValue).
type QueryFailure struct {
	Kind      string
	Retryable bool
	Detail    string
	RawValue  *string
	Code      int
}

func (f QueryFailure) MarshalJSON() ([]byte, error) {
	switch f.Kind {
	case "command-exit":
		return marshal(struct {
			Kind      string `json:"kind"`
			Retryable bool   `json:"retryable"`
			Code      int    `json:"code"`
			Detail    string `json:"detail"`
		}{f.Kind, f.Retryable, f.Code, f.Detail})
	case "invalid-context-url":
		return marshal(struct {
			Kind      string  `json:"kind"`
			Retryable bool    `json:"retryable"`
			RawValue  *string `json:"rawValue"`
			Detail    string  `json:"detail"`
		}{f.Kind, f.Retryable, f.RawValue, f.Detail})
	}
	return marshal(struct {
		Kind      string  `json:"kind"`
		Retryable bool    `json:"retryable"`
		Detail    string  `json:"detail"`
		RawValue  *string `json:"rawValue,omitempty"`
	}{f.Kind, f.Retryable, f.Detail, f.RawValue})
}

type QueryError struct{ Failure QueryFailure }

func (e *QueryError) Error() string { return e.Failure.Detail }

// Decision kinds: blocker, waiting (Frontier, Pending), ready and merged (Pr),
// and clear (Prs) for stacks.
type Decision struct {
	Kind     string
	Blocker  *MergeBlocker
	Frontier PrContext
	Pending  []Check
	Pr       *Contribution
	Prs      []Contribution
}

// WaitReason kinds: pending-checks (Pending), merge-queue (UnmergedCount).
type WaitReason struct {
	Kind          string
	Pending       []Check
	UnmergedCount int
}

func (r WaitReason) MarshalJSON() ([]byte, error) {
	if r.Kind == "pending-checks" {
		return marshal(struct {
			Kind    string  `json:"kind"`
			Pending []Check `json:"pending"`
		}{r.Kind, r.Pending})
	}
	return marshal(struct {
		Kind          string `json:"kind"`
		UnmergedCount int    `json:"unmergedCount"`
	}{r.Kind, r.UnmergedCount})
}

// TimeoutReason kinds: pending-checks (Pending), status-unavailable (Failure),
// queued-stack (Frontier, UnmergedCount).
type TimeoutReason struct {
	Kind          string
	Pending       []Check
	Failure       *QueryFailure
	Frontier      PrContext
	UnmergedCount int
}

func (r TimeoutReason) MarshalJSON() ([]byte, error) {
	switch r.Kind {
	case "pending-checks":
		return marshal(struct {
			Kind    string  `json:"kind"`
			Pending []Check `json:"pending"`
		}{r.Kind, r.Pending})
	case "status-unavailable":
		return marshal(struct {
			Kind    string        `json:"kind"`
			Failure *QueryFailure `json:"failure"`
		}{r.Kind, r.Failure})
	}
	return marshal(struct {
		Kind          string    `json:"kind"`
		Frontier      PrContext `json:"frontier"`
		UnmergedCount int       `json:"unmergedCount"`
	}{r.Kind, r.Frontier, r.UnmergedCount})
}

// ReadyScope kinds: single (Pr), stack (Prs).
type ReadyScope struct {
	Kind string         `json:"kind"`
	Pr   *Contribution  `json:"pr,omitempty"`
	Prs  []Contribution `json:"prs,omitempty"`
}

// Verdict is every watcher event. Kind selects which payload fields apply:
// QUEUE (Queue), STATUS (StatusReason, Rows), WAITING (Frontier, Wait),
// ADVANCE (Merged, Frontier, Remaining), RETRY (Failure, ConsecutiveFailures,
// RetryInSeconds), BLOCKER (Blocker), READY (Scope), COMPLETE (Queue,
// MergedPrs), TIMEOUT (Timeout). Terminal verdicts carry ExitCode.
type Verdict struct {
	Sequence            int
	ObservedAt          string
	Mode                string
	Kind                string
	Terminal            bool
	ExitCode            int
	Queue               []PrContext
	StatusReason        string
	Rows                []PrSnapshot
	Frontier            PrContext
	Wait                *WaitReason
	Merged              PrContext
	Remaining           int
	Failure             *QueryFailure
	ConsecutiveFailures int
	RetryInSeconds      float64
	Blocker             *MergeBlocker
	Scope               *ReadyScope
	MergedPrs           []Contribution
	Timeout             *TimeoutReason
}

type verdictHead struct {
	SchemaVersion int    `json:"schemaVersion"`
	Sequence      int    `json:"sequence"`
	ObservedAt    string `json:"observedAt"`
	Mode          string `json:"mode"`
	Kind          string `json:"kind"`
	Terminal      bool   `json:"terminal"`
	ExitCode      *int   `json:"exitCode,omitempty"`
}

func (v Verdict) MarshalJSON() ([]byte, error) {
	head := verdictHead{SchemaVersion: 1, Sequence: v.Sequence, ObservedAt: v.ObservedAt, Mode: v.Mode, Kind: v.Kind, Terminal: v.Terminal}
	if v.Terminal {
		head.ExitCode = &v.ExitCode
	}
	switch v.Kind {
	case "QUEUE":
		return marshal(struct {
			verdictHead
			Queue []PrContext `json:"queue"`
		}{head, v.Queue})
	case "STATUS":
		return marshal(struct {
			verdictHead
			Reason string       `json:"reason"`
			Rows   []PrSnapshot `json:"rows"`
		}{head, v.StatusReason, v.Rows})
	case "WAITING":
		return marshal(struct {
			verdictHead
			Frontier PrContext   `json:"frontier"`
			Reason   *WaitReason `json:"reason"`
		}{head, v.Frontier, v.Wait})
	case "ADVANCE":
		return marshal(struct {
			verdictHead
			Merged    PrContext `json:"merged"`
			Frontier  PrContext `json:"frontier"`
			Remaining int       `json:"remaining"`
		}{head, v.Merged, v.Frontier, v.Remaining})
	case "RETRY":
		return marshal(struct {
			verdictHead
			Failure             *QueryFailure `json:"failure"`
			ConsecutiveFailures int           `json:"consecutiveFailures"`
			RetryInSeconds      float64       `json:"retryInSeconds"`
		}{head, v.Failure, v.ConsecutiveFailures, v.RetryInSeconds})
	case "BLOCKER":
		return marshal(struct {
			verdictHead
			Blocker *MergeBlocker `json:"blocker"`
		}{head, v.Blocker})
	case "READY":
		return marshal(struct {
			verdictHead
			Scope *ReadyScope `json:"scope"`
		}{head, v.Scope})
	case "COMPLETE":
		return marshal(struct {
			verdictHead
			Queue  []PrContext    `json:"queue"`
			Merged []Contribution `json:"merged"`
		}{head, v.Queue, v.MergedPrs})
	}
	return marshal(struct {
		verdictHead
		Reason *TimeoutReason `json:"reason"`
	}{head, v.Timeout})
}

type PollingOptions struct {
	Interval       float64
	SweepInterval  float64
	Timeout        float64
	MaxQueryErrors int
	AllowDraft     bool
}
