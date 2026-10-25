package watchpr

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os/exec"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const reviewThreadsQuery = "\nquery ReviewThreads($owner: String!, $repo: String!, $pr: Int!, $after: String) {\n  repository(owner: $owner, name: $repo) {\n    pullRequest(number: $pr) {\n      reviewThreads(first: 100, after: $after) {\n        pageInfo {\n          hasNextPage\n          endCursor\n        }\n        nodes {\n          id\n          isResolved\n          comments(first: 10) {\n            nodes {\n              body\n              createdAt\n              path\n              line\n              author { login }\n            }\n          }\n        }\n      }\n    }\n  }\n}\n"

const prCommitStatusQuery = "\nquery PrCommitStatuses($owner: String!, $repo: String!, $pr: Int!) {\n  repository(owner: $owner, name: $repo) {\n    pullRequest(number: $pr) {\n      commits(last: 50) {\n        nodes {\n          commit {\n            oid\n            statusCheckRollup {\n              state\n            }\n          }\n        }\n      }\n    }\n  }\n}\n"

const prCheckRollupQuery = "\nquery PrCheckRollup($owner: String!, $repo: String!, $pr: Int!, $after: String) {\n  repository(owner: $owner, name: $repo) {\n    pullRequest(number: $pr) {\n      commits(last: 1) {\n        nodes {\n          commit {\n            statusCheckRollup {\n              contexts(first: 100, after: $after) {\n                pageInfo {\n                  hasNextPage\n                  endCursor\n                }\n                nodes {\n                  __typename\n                  ... on CheckRun {\n                    name\n                    status\n                    conclusion\n                    detailsUrl\n                  }\n                  ... on StatusContext {\n                    context\n                    state\n                    targetUrl\n                  }\n                }\n              }\n            }\n          }\n        }\n      }\n    }\n  }\n}\n"

// ChecksFastPath is the `gh pr checks` read: Checks when Usable, otherwise the
// exit code and stderr that made it unusable.
type ChecksFastPath struct {
	Usable   bool
	Checks   []Check
	ExitCode int
	Stderr   string
}

type RollupPage struct {
	Checks    []Check
	EndCursor string
}

type ReviewThreadPage struct {
	Threads   []RawReviewThread
	EndCursor string
}

// Reader is every GitHub read the watcher makes. An empty cursor means null.
type Reader interface {
	OriginRepo() (*Repository, error)
	CurrentPr(pr *PrNumber) (PrContext, error)
	PullRequest(context PrContext) (PullRequestFacts, error)
	OpenPullRequests(repository Repository) ([]OpenPullRequest, error)
	ChecksFastPath(context PrContext) (ChecksFastPath, error)
	CheckRollupPage(context PrContext, after string) (RollupPage, error)
	ReviewThreadPage(context PrContext, after string) (ReviewThreadPage, error)
	CommitRollups(context PrContext) ([]CommitRollup, error)
}

type CommandResult struct {
	Code   int
	Stdout string
	Stderr string
}

// Runner runs argv to completion. An error means the process could not run at
// all, which the watcher does not classify as a query failure.
type Runner func(argv []string) (CommandResult, error)

func execRunner(argv []string) (CommandResult, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return CommandResult{}, fmt.Errorf("spawn %s: %w", argv[0], err)
	}
	return CommandResult{
		Code:   cmd.ProcessState.ExitCode(),
		Stdout: strings.ToValidUTF8(stdout.String(), "\uFFFD"),
		Stderr: strings.ToValidUTF8(stderr.String(), "\uFFFD"),
	}, nil
}

func queryError(failure QueryFailure) error { return &QueryError{Failure: failure} }

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return strings.TrimSuffix(line, "\r")
}

func detailLine(s string) string { return truncate(firstLine(strings.TrimSpace(s)), 240) }

// decode unmarshals gh output. A value of the wrong JSON type is a missing-key
// failure like any other malformed field.
func decode(text, label string, v any) error {
	err := json.Unmarshal([]byte(text), v)
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		return queryError(QueryFailure{Kind: "missing-key", Retryable: true, Detail: "invalid " + cmp.Or(typeErr.Field, "response") + ": " + typeErr.Value})
	}
	if err != nil {
		return queryError(QueryFailure{Kind: "json-parse", Retryable: true, Detail: label + ": " + err.Error()})
	}
	return nil
}

func runJSON(run Runner, argv []string, v any) error {
	result, err := run(argv)
	if err != nil {
		return err
	}
	if result.Code != 0 {
		detail := detailLine(result.Stderr)
		if detail == "" {
			detail = fmt.Sprintf("%s exited %d", strings.Join(argv, " "), result.Code)
		}
		return queryError(QueryFailure{Kind: "command-exit", Retryable: true, Code: result.Code, Detail: detail})
	}
	return decode(result.Stdout, strings.Join(argv, " "), v)
}

func missing(path string) error {
	return queryError(QueryFailure{Kind: "missing-key", Retryable: true, Detail: "missing " + path})
}

func invalid(path string, value any) error {
	raw, _ := marshal(value)
	text := string(raw)
	return queryError(QueryFailure{Kind: "missing-key", Retryable: true, Detail: "invalid " + path + ": " + text, RawValue: &text})
}

// fields validates decoded gh fields and keeps the first failure. A nil
// pointer is a missing (or null) field.
type fields struct{ err error }

func (f *fields) fail(err error) {
	if f.err == nil {
		f.err = err
	}
}

func (f *fields) present(ok bool, path string) bool {
	if !ok {
		f.fail(missing(path))
	}
	return ok
}

func (f *fields) str(value *string, path string) string {
	if !f.present(value != nil, path) {
		return ""
	}
	return *value
}

func (f *fields) boolean(value *bool, path string) bool {
	if !f.present(value != nil, path) {
		return false
	}
	return *value
}

func (f *fields) oneOf(value *string, path string, allowed []string) string {
	s := f.str(value, path)
	if value != nil && !slices.Contains(allowed, s) {
		f.fail(invalid(path, s))
	}
	return s
}

func (f *fields) nullableOneOf(value *string, path string, allowed []string) enum {
	if value == nil {
		return ""
	}
	return enum(f.oneOf(value, path, allowed))
}

func (f *fields) cursor(page *ghPageInfo, path string) string {
	if !f.present(page != nil, path) {
		return ""
	}
	if f.boolean(page.HasNextPage, path+".hasNextPage") && page.EndCursor != nil {
		return *page.EndCursor
	}
	return ""
}

var (
	mergeableStates = []string{"MERGEABLE", "CONFLICTING", "UNKNOWN"}
	mergeStates     = []string{"BEHIND", "BLOCKED", "CLEAN", "CONFLICTING", "DIRTY", "DRAFT", "HAS_HOOKS", "UNKNOWN", "UNSTABLE"}
	prStates        = []string{"OPEN", "CLOSED", "MERGED"}
	rollupStates    = []string{"ERROR", "EXPECTED", "FAILURE", "PENDING", "SUCCESS"}
	reviewDecisions = []string{"APPROVED", "CHANGES_REQUESTED", "REVIEW_REQUIRED"}
)

type ghPageInfo struct {
	HasNextPage *bool   `json:"hasNextPage"`
	EndCursor   *string `json:"endCursor"`
}

// ghResponse is a GraphQL response rooted at data.repository.pullRequest.
type ghResponse[T any] struct {
	Data *struct {
		Repository *struct {
			PullRequest *T `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
}

func (r ghResponse[T]) pullRequest() *T {
	if r.Data == nil || r.Data.Repository == nil {
		return nil
	}
	return r.Data.Repository.PullRequest
}

type ghPullRequest struct {
	Mergeable        *string `json:"mergeable"`
	MergeStateStatus *string `json:"mergeStateStatus"`
	ReviewDecision   *string `json:"reviewDecision"`
	HeadRefOid       *string `json:"headRefOid"`
	HeadRefName      *string `json:"headRefName"`
	BaseRefName      *string `json:"baseRefName"`
	State            *string `json:"state"`
	MergedAt         *string `json:"mergedAt"`
	IsDraft          *bool   `json:"isDraft"`
}

type ghOpenPullRequest struct {
	Number            any     `json:"number"`
	HeadRefName       *string `json:"headRefName"`
	BaseRefName       *string `json:"baseRefName"`
	IsCrossRepository *bool   `json:"isCrossRepository"`
}

type ghFastCheck struct {
	Name        *string `json:"name"`
	State       *string `json:"state"`
	Bucket      *string `json:"bucket"`
	Description string  `json:"description"`
	Link        string  `json:"link"`
	Workflow    string  `json:"workflow"`
}

type rollupNode struct {
	Typename   string  `json:"__typename"`
	Name       *string `json:"name"`
	Context    *string `json:"context"`
	Status     string  `json:"status"`
	Conclusion string  `json:"conclusion"`
	State      string  `json:"state"`
	DetailsURL string  `json:"detailsUrl"`
	TargetURL  string  `json:"targetUrl"`
}

// ghCommits serves both commit queries: the rollup query reads contexts from
// the last commit, the status query reads every commit's oid and state.
type ghCommits struct {
	Commits *struct {
		Nodes []struct {
			Commit *struct {
				Oid               *string `json:"oid"`
				StatusCheckRollup *struct {
					State    *string `json:"state"`
					Contexts *struct {
						PageInfo *ghPageInfo  `json:"pageInfo"`
						Nodes    []rollupNode `json:"nodes"`
					} `json:"contexts"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
}

type ghComment struct {
	Author *struct {
		Login *string `json:"login"`
	} `json:"author"`
	Body      *string  `json:"body"`
	Path      *string  `json:"path"`
	Line      *float64 `json:"line"`
	CreatedAt *string  `json:"createdAt"`
}

type ghReviewThreads struct {
	ReviewThreads *struct {
		PageInfo *ghPageInfo `json:"pageInfo"`
		Nodes    []struct {
			ID         *string `json:"id"`
			IsResolved *bool   `json:"isResolved"`
			Comments   *struct {
				Nodes []ghComment `json:"nodes"`
			} `json:"comments"`
		} `json:"nodes"`
	} `json:"reviewThreads"`
}

func canonicalGitHub(u *url.URL) bool {
	return u.Scheme == "https" && strings.EqualFold(u.Hostname(), "github.com") && u.Port() == "" &&
		u.User == nil && u.RawQuery == "" && u.Fragment == ""
}

func parseRemote(value string) *Repository {
	normalized := strings.TrimSpace(value)
	if rest, ok := strings.CutPrefix(normalized, "git@github.com:"); ok {
		normalized = "https://github.com/" + rest
	}
	if rest, ok := strings.CutPrefix(normalized, "ssh://git@github.com/"); ok {
		normalized = "https://github.com/" + rest
	}
	u, err := url.Parse(normalized)
	if err != nil || !canonicalGitHub(u) {
		return nil
	}
	parts := pathParts(strings.TrimSuffix(u.Path, ".git"))
	if len(parts) != 2 {
		return nil
	}
	return &Repository{Owner: parts[0], Repo: parts[1]}
}

func parsePrUrl(value string) (PrContext, error) {
	context, reason := func() (PrContext, string) {
		u, err := url.Parse(value)
		if err != nil || !u.IsAbs() {
			return PrContext{}, "Invalid URL"
		}
		parts := pathParts(u.Path)
		if !canonicalGitHub(u) || len(parts) != 4 || parts[2] != "pull" {
			return PrContext{}, "not a canonical GitHub pull URL"
		}
		number, err := parsePrNumber(parts[3], "PR number")
		if err != nil {
			return PrContext{}, err.Error()
		}
		return PrContext{Owner: parts[0], Repo: parts[1], Number: number}, ""
	}()
	if reason == "" {
		return context, nil
	}
	return PrContext{}, queryError(QueryFailure{
		Kind:      "invalid-context-url",
		Retryable: false,
		RawValue:  &value,
		Detail:    fmt.Sprintf("could not infer owner/repo from PR URL: %s (%s)", value, reason),
	})
}

func pathParts(pathname string) []string {
	var parts []string
	for _, part := range strings.Split(pathname, "/") {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return parts
}

func parsePrNumber(value, label string) (PrNumber, error) {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", label)
	}
	return PrNumber(n), nil
}

// parsePrNumberValue validates a PR number from gh JSON. Its error is a plain
// error, not a QueryError, so a malformed number crashes the run.
func parsePrNumberValue(value any, label string) (PrNumber, error) {
	f, ok := value.(float64)
	if !ok || f <= 0 || f != math.Trunc(f) || f > 1<<53 {
		return 0, fmt.Errorf("%s must be a positive integer", label)
	}
	return PrNumber(f), nil
}

func withKind(details Check, kind, reportedState string) Check {
	details.Kind = kind
	details.ReportedState = reportedState
	return details
}

func parseFastCheck(c ghFastCheck) (Check, error) {
	var f fields
	details := Check{Name: f.str(c.Name, "name"), Description: c.Description, Link: c.Link, Workflow: c.Workflow}
	state := strings.ToUpper(f.str(c.State, "check.state"))
	bucket := f.str(c.Bucket, "check.bucket")
	if f.err != nil {
		return Check{}, f.err
	}
	switch {
	case state == "ACTION_REQUIRED":
		return gateOr("failed", details, state), nil
	case bucket == "fail" || state == "FAILURE" || state == "ERROR":
		return withKind(details, "failed", state), nil
	case bucket == "pending":
		return gateOr("pending", details, state), nil
	case bucket == "pass":
		return withKind(details, "passed", state), nil
	case bucket == "skipping":
		return withKind(details, "skipped", state), nil
	}
	return withKind(details, "failed", state), nil
}

// Every read path classifies the owner-approval gate here, so the rule has one
// home. A gate that is pending or waits on an owner approval must not count as
// pending or failed, or the watcher waits on a human.
func gateOr(kind string, details Check, reportedState string) Check {
	if details.Name == "Code Review Gate" {
		return withKind(details, "code-review-gate", reportedState)
	}
	return withKind(details, kind, reportedState)
}

// mapRollupNode returns ok=false for a node type it does not know.
func mapRollupNode(node rollupNode) (Check, bool, error) {
	if node.Typename != "CheckRun" && node.Typename != "StatusContext" {
		return Check{}, false, nil
	}
	var f fields
	details := Check{Link: cmp.Or(node.TargetURL, node.DetailsURL)}
	if node.Typename == "CheckRun" {
		details.Name = f.str(node.Name, "name")
	} else {
		details.Name = f.str(node.Context, "context")
	}
	if f.err != nil {
		return Check{}, false, f.err
	}
	if node.Typename == "CheckRun" {
		conclusion := strings.ToUpper(node.Conclusion)
		switch {
		case strings.ToUpper(node.Status) != "COMPLETED":
			return gateOr("pending", details, "PENDING"), true, nil
		case conclusion == "SUCCESS":
			return withKind(details, "passed", "SUCCESS"), true, nil
		case conclusion == "NEUTRAL" || conclusion == "SKIPPED":
			return withKind(details, "skipped", conclusion), true, nil
		case conclusion == "ACTION_REQUIRED":
			return gateOr("failed", details, conclusion), true, nil
		}
		return withKind(details, "failed", "FAILURE"), true, nil
	}
	state := strings.ToUpper(node.State)
	switch {
	case state == "PENDING" || state == "EXPECTED":
		return gateOr("pending", details, "PENDING"), true, nil
	case state == "SUCCESS":
		return withKind(details, "passed", state), true, nil
	case state == "":
		return withKind(details, "failed", "FAILURE"), true, nil
	}
	return withKind(details, "failed", state), true, nil
}

func parseComment(c ghComment) (*ReviewComment, error) {
	var f fields
	comment := &ReviewComment{Path: c.Path}
	if c.Author != nil {
		comment.AuthorLogin = c.Author.Login
	}
	comment.Body = f.str(c.Body, "review comment.body")
	if c.Line != nil {
		if line := *c.Line; line != math.Trunc(line) {
			f.fail(invalid("review comment.line", line))
		} else {
			n := int(line)
			comment.Line = &n
		}
	}
	comment.CreatedAt = f.str(c.CreatedAt, "review comment.createdAt")
	if f.err != nil {
		return nil, f.err
	}
	return comment, nil
}

var bugbotBodyTokens = []string{"bugbot", "cursor_automation_id", "agentic security review", "description start", "severity"}

func isBugbot(comment *ReviewComment) bool {
	if comment == nil {
		return false
	}
	author := ""
	if comment.AuthorLogin != nil {
		author = strings.ToLower(*comment.AuthorLogin)
	}
	body := strings.ToLower(comment.Body)
	if strings.Contains(author, "bugbot") {
		return true
	}
	if author != "cursor" {
		return false
	}
	for _, token := range bugbotBodyTokens {
		if strings.Contains(body, token) {
			return true
		}
	}
	return false
}

// The separator class adds Unicode spaces to \s: Bugbot can write a
// non-breaking space after the key.
var passKeyPatterns = []*regexp.Regexp{
	regexp.MustCompile(`RUN_ID:[\s\v\p{Z}\x{FEFF}]*([a-zA-Z0-9_.:-]+)`),
	regexp.MustCompile(`CURSOR_AUTOMATION_ID:[\s\v\p{Z}\x{FEFF}]*([a-zA-Z0-9_.:-]+)`),
}

func passKey(comment *ReviewComment) (string, bool) {
	if comment == nil {
		return "", false
	}
	for _, pattern := range passKeyPatterns {
		if match := pattern.FindStringSubmatch(comment.Body); match != nil && match[1] != "" {
			return match[1], true
		}
	}
	return "", false
}

func parseReviewThreadPage(response ghResponse[ghReviewThreads]) (ReviewThreadPage, error) {
	pr := response.pullRequest()
	if pr == nil || pr.ReviewThreads == nil {
		return ReviewThreadPage{}, missing("data.repository.pullRequest.reviewThreads")
	}
	reviewThreads := pr.ReviewThreads
	if reviewThreads.Nodes == nil {
		return ReviewThreadPage{}, missing("reviewThreads.nodes")
	}
	threads := []RawReviewThread{}
	for _, node := range reviewThreads.Nodes {
		var f fields
		resolved := f.boolean(node.IsResolved, "review thread.isResolved")
		f.present(node.Comments != nil && node.Comments.Nodes != nil, "comments.nodes")
		id := f.str(node.ID, "review thread.id")
		if f.err != nil {
			return ReviewThreadPage{}, f.err
		}
		var first *ReviewComment
		if comments := node.Comments.Nodes; len(comments) > 0 {
			var err error
			if first, err = parseComment(comments[0]); err != nil {
				return ReviewThreadPage{}, err
			}
		}
		threads = append(threads, RawReviewThread{ID: id, FirstComment: first, Resolved: resolved})
	}
	var f fields
	cursor := f.cursor(reviewThreads.PageInfo, "reviewThreads.pageInfo")
	if f.err != nil {
		return ReviewThreadPage{}, f.err
	}
	return ReviewThreadPage{Threads: threads, EndCursor: cursor}, nil
}

func buildReviewThreads(threads []RawReviewThread) []ReviewThread {
	keys := map[string]bool{}
	keyless := false
	for _, thread := range threads {
		if !isBugbot(thread.FirstComment) {
			continue
		}
		if key, ok := passKey(thread.FirstComment); ok {
			keys[key] = true
		} else {
			keyless = true
		}
	}
	passes := len(keys)
	if passes == 0 && keyless {
		passes = 1
	}
	out := []ReviewThread{}
	for _, thread := range threads {
		if thread.Resolved {
			continue
		}
		out = append(out, ReviewThread{ID: thread.ID, FirstComment: thread.FirstComment, IsBugbot: isBugbot(thread.FirstComment), BugbotReviewPasses: passes})
	}
	return out
}

func parsePullRequest(raw ghPullRequest, context PrContext) (PullRequestFacts, error) {
	var f fields
	isDraft := f.boolean(raw.IsDraft, "pull request.isDraft")
	mergeable := f.oneOf(raw.Mergeable, "pull request.mergeable", mergeableStates)
	mergeStateStatus := f.oneOf(raw.MergeStateStatus, "pull request.mergeStateStatus", mergeStates)
	// `gh pr view` reports no review decision as "", not null. Only this field
	// does it, so the normalization stays here rather than in nullableOneOf,
	// where it would stop a genuinely unexpected rollup state from failing closed.
	reviewDecision := raw.ReviewDecision
	if reviewDecision != nil && *reviewDecision == "" {
		reviewDecision = nil
	}
	facts := PullRequestFacts{
		Context:          context,
		Mergeable:        mergeable,
		MergeStateStatus: mergeStateStatus,
		ReviewDecision:   f.nullableOneOf(reviewDecision, "pull request.reviewDecision", reviewDecisions),
		HeadRefOid:       raw.HeadRefOid,
		HeadRefName:      f.str(raw.HeadRefName, "pull request.headRefName"),
		BaseRefName:      f.str(raw.BaseRefName, "pull request.baseRefName"),
		State:            f.oneOf(raw.State, "pull request.state", prStates),
		MergedAt:         raw.MergedAt,
		IsDraft:          isDraft,
	}
	if f.err != nil {
		return PullRequestFacts{}, f.err
	}
	return facts, nil
}

func graphqlArgs(query string, context PrContext, after string) []string {
	argv := []string{"gh", "api", "graphql", "-f", "query=" + query, "-f", "owner=" + context.Owner, "-f", "repo=" + context.Repo, "-F", "pr=" + context.Number.String()}
	if after != "" {
		argv = append(argv, "-f", "after="+after)
	}
	return argv
}

// GhReader reads GitHub through the gh CLI (and git for the origin remote).
type GhReader struct{ Run Runner }

func (g GhReader) OriginRepo() (*Repository, error) {
	result, err := g.Run([]string{"git", "remote", "get-url", "origin"})
	if err != nil {
		return nil, err
	}
	if result.Code != 0 {
		return nil, nil
	}
	return parseRemote(result.Stdout), nil
}

func (g GhReader) CurrentPr(pr *PrNumber) (PrContext, error) {
	argv := []string{"gh", "pr", "view"}
	if pr != nil {
		argv = append(argv, pr.String())
	}
	argv = append(argv, "--json", "number,url")
	var current struct {
		Number any     `json:"number"`
		URL    *string `json:"url"`
	}
	if err := runJSON(g.Run, argv, &current); err != nil {
		return PrContext{}, err
	}
	var f fields
	prURL := f.str(current.URL, "current PR.url")
	if f.err != nil {
		return PrContext{}, f.err
	}
	parsed, err := parsePrUrl(prURL)
	if err != nil {
		return PrContext{}, err
	}
	if pr != nil {
		parsed.Number = *pr
		return parsed, nil
	}
	if parsed.Number, err = parsePrNumberValue(current.Number, "current PR.number"); err != nil {
		return PrContext{}, err
	}
	return parsed, nil
}

func (g GhReader) PullRequest(context PrContext) (PullRequestFacts, error) {
	var raw ghPullRequest
	err := runJSON(g.Run, []string{
		"gh", "pr", "view", context.Number.String(),
		"--repo", context.Owner + "/" + context.Repo,
		"--json", "mergeable,mergeStateStatus,reviewDecision,headRefOid,headRefName,baseRefName,state,mergedAt,isDraft",
	}, &raw)
	if err != nil {
		return PullRequestFacts{}, err
	}
	return parsePullRequest(raw, context)
}

func (g GhReader) OpenPullRequests(repository Repository) ([]OpenPullRequest, error) {
	var items []ghOpenPullRequest
	err := runJSON(g.Run, []string{
		"gh", "pr", "list",
		"--repo", repository.Owner + "/" + repository.Repo,
		"--state", "open", "--limit", "300",
		"--json", "number,headRefName,baseRefName,isCrossRepository",
	}, &items)
	if err != nil {
		return nil, err
	}
	if items == nil {
		return nil, missing("open PRs")
	}
	out := []OpenPullRequest{}
	for index, item := range items {
		label := fmt.Sprintf("open PRs[%d]", index)
		var f fields
		pr := OpenPullRequest{
			IsCrossRepository: f.boolean(item.IsCrossRepository, label+".isCrossRepository"),
			HeadRefName:       f.str(item.HeadRefName, label+".headRefName"),
			BaseRefName:       f.str(item.BaseRefName, label+".baseRefName"),
		}
		if f.err != nil {
			return nil, f.err
		}
		var err error
		if pr.Number, err = parsePrNumberValue(item.Number, label+".number"); err != nil {
			return nil, err
		}
		out = append(out, pr)
	}
	return out, nil
}

func (g GhReader) ChecksFastPath(context PrContext) (ChecksFastPath, error) {
	result, err := g.Run([]string{
		"gh", "pr", "checks", context.Number.String(),
		"--repo", context.Owner + "/" + context.Repo,
		"--json", "name,state,description,link,workflow,bucket",
	})
	if err != nil {
		return ChecksFastPath{}, err
	}
	if (result.Code == 0 || result.Code == 1 || result.Code == 8) && strings.TrimSpace(result.Stdout) != "" {
		var items []ghFastCheck
		if json.Unmarshal([]byte(result.Stdout), &items) == nil && items != nil {
			if checks, err := parseFastChecks(items); err == nil {
				return ChecksFastPath{Usable: true, Checks: checks}, nil
			}
		}
	}
	return ChecksFastPath{ExitCode: result.Code, Stderr: result.Stderr}, nil
}

func parseFastChecks(items []ghFastCheck) ([]Check, error) {
	checks := []Check{}
	for _, item := range items {
		check, err := parseFastCheck(item)
		if err != nil {
			return nil, err
		}
		checks = append(checks, check)
	}
	return checks, nil
}

func (g GhReader) CheckRollupPage(context PrContext, after string) (RollupPage, error) {
	var response ghResponse[ghCommits]
	if err := runJSON(g.Run, graphqlArgs(prCheckRollupQuery, context, after), &response); err != nil {
		return RollupPage{}, err
	}
	pr := response.pullRequest()
	if pr == nil || pr.Commits == nil || pr.Commits.Nodes == nil {
		return RollupPage{}, missing("data.repository.pullRequest.commits.nodes")
	}
	commits := pr.Commits.Nodes
	if len(commits) == 0 {
		return RollupPage{Checks: []Check{}}, nil
	}
	commit := commits[len(commits)-1].Commit
	if commit == nil {
		return RollupPage{}, missing("commit")
	}
	if commit.StatusCheckRollup == nil {
		return RollupPage{Checks: []Check{}}, nil
	}
	contexts := commit.StatusCheckRollup.Contexts
	if contexts == nil {
		return RollupPage{}, missing("statusCheckRollup.contexts")
	}
	if contexts.Nodes == nil {
		return RollupPage{}, missing("contexts.nodes")
	}
	checks := []Check{}
	for _, node := range contexts.Nodes {
		check, ok, err := mapRollupNode(node)
		if err != nil {
			return RollupPage{}, err
		}
		if ok {
			checks = append(checks, check)
		}
	}
	var f fields
	cursor := f.cursor(contexts.PageInfo, "contexts.pageInfo")
	if f.err != nil {
		return RollupPage{}, f.err
	}
	return RollupPage{Checks: checks, EndCursor: cursor}, nil
}

func (g GhReader) ReviewThreadPage(context PrContext, after string) (ReviewThreadPage, error) {
	var response ghResponse[ghReviewThreads]
	if err := runJSON(g.Run, graphqlArgs(reviewThreadsQuery, context, after), &response); err != nil {
		return ReviewThreadPage{}, err
	}
	return parseReviewThreadPage(response)
}

func (g GhReader) CommitRollups(context PrContext) ([]CommitRollup, error) {
	var response ghResponse[ghCommits]
	if err := runJSON(g.Run, graphqlArgs(prCommitStatusQuery, context, ""), &response); err != nil {
		return nil, err
	}
	pr := response.pullRequest()
	if pr == nil || pr.Commits == nil || pr.Commits.Nodes == nil {
		return nil, missing("data.repository.pullRequest.commits.nodes")
	}
	out := []CommitRollup{}
	for index, node := range pr.Commits.Nodes {
		if node.Commit == nil {
			return nil, missing("commit")
		}
		var f fields
		rollup := CommitRollup{Oid: f.str(node.Commit.Oid, fmt.Sprintf("commits[%d].oid", index))}
		if status := node.Commit.StatusCheckRollup; status != nil {
			rollup.State = string(f.nullableOneOf(status.State, fmt.Sprintf("commits[%d].statusCheckRollup.state", index), rollupStates))
		}
		if f.err != nil {
			return nil, f.err
		}
		out = append(out, rollup)
	}
	return out, nil
}

func resolveChecks(reader Reader, context PrContext) (CheckRead, error) {
	fast, err := reader.ChecksFastPath(context)
	if err != nil {
		return CheckRead{}, err
	}
	if fast.Usable && len(fast.Checks) > 0 {
		return CheckRead{Source: "gh-pr-checks", Checks: fast.Checks}, nil
	}
	checks := []Check{}
	after := ""
	for {
		page, err := reader.CheckRollupPage(context, after)
		if err != nil {
			return CheckRead{}, err
		}
		checks = append(checks, page.Checks...)
		if after = page.EndCursor; after == "" {
			break
		}
	}
	if len(checks) > 0 {
		return CheckRead{Source: "graphql-rollup", Checks: checks}, nil
	}
	suffix := "fast path and GraphQL rollup were empty"
	if !fast.Usable {
		suffix = fmt.Sprintf("fast path exit=%d; GraphQL rollup was empty", fast.ExitCode)
		if line := detailLine(fast.Stderr); line != "" {
			suffix += "; " + line
		}
	}
	return CheckRead{}, queryError(QueryFailure{Kind: "checks-unavailable", Retryable: true, Detail: "could not read PR checks: " + suffix})
}

func resolveReviewThreads(reader Reader, context PrContext) ([]ReviewThread, error) {
	threads := []RawReviewThread{}
	after := ""
	for {
		page, err := reader.ReviewThreadPage(context, after)
		if err != nil {
			return nil, err
		}
		threads = append(threads, page.Threads...)
		if after = page.EndCursor; after == "" {
			break
		}
	}
	return buildReviewThreads(threads), nil
}

func resolveContext(reader Reader, owner, repo *string, pr *PrNumber) (PrContext, error) {
	if pr != nil && owner != nil && repo != nil {
		return PrContext{Owner: *owner, Repo: *repo, Number: *pr}, nil
	}
	if pr != nil {
		origin, err := reader.OriginRepo()
		if err != nil {
			return PrContext{}, err
		}
		if origin != nil {
			return PrContext{Owner: orString(owner, origin.Owner), Repo: orString(repo, origin.Repo), Number: *pr}, nil
		}
	}
	inferred, err := reader.CurrentPr(pr)
	if err != nil {
		return PrContext{}, err
	}
	number := inferred.Number
	if pr != nil {
		number = *pr
	}
	return PrContext{Owner: orString(owner, inferred.Owner), Repo: orString(repo, inferred.Repo), Number: number}, nil
}

func orString(value *string, fallback string) string {
	if value != nil {
		return *value
	}
	return fallback
}

func orderStack(context PrContext, open []OpenPullRequest) []PrContext {
	byNumber := map[PrNumber]OpenPullRequest{}
	for _, pr := range open {
		byNumber[pr.Number] = pr
	}
	// A base ref belongs to this repository. A fork head with the same name
	// cannot be its parent, even when that name is the default branch.
	byHead := map[string]OpenPullRequest{}
	for _, pr := range open {
		if !pr.IsCrossRepository {
			byHead[pr.HeadRefName] = pr
		}
	}
	children := map[string][]OpenPullRequest{}
	for _, pr := range open {
		children[pr.BaseRefName] = append(children[pr.BaseRefName], pr)
	}
	for _, values := range children {
		sort.SliceStable(values, func(i, j int) bool { return values[i].Number < values[j].Number })
	}
	start, ok := byNumber[context.Number]
	if !ok {
		return []PrContext{context}
	}
	var down []OpenPullRequest
	current := start
	for {
		parent, ok := byHead[current.BaseRefName]
		if !ok {
			break
		}
		down = append(down, parent)
		current = parent
	}
	seen := map[PrNumber]bool{start.Number: true}
	for _, pr := range down {
		seen[pr.Number] = true
	}
	var up []OpenPullRequest
	var visit func(parent OpenPullRequest)
	visit = func(parent OpenPullRequest) {
		if parent.IsCrossRepository {
			return
		}
		for _, child := range children[parent.HeadRefName] {
			if seen[child.Number] {
				continue
			}
			seen[child.Number] = true
			up = append(up, child)
			visit(child)
		}
	}
	visit(start)
	var stack []OpenPullRequest
	for i := len(down) - 1; i >= 0; i-- {
		stack = append(stack, down[i])
	}
	stack = append(append(stack, start), up...)
	ordered := make([]PrContext, len(stack))
	for i, pr := range stack {
		ordered[i] = PrContext{Owner: context.Owner, Repo: context.Repo, Number: pr.Number}
	}
	return ordered
}

func discoverStack(reader Reader, context PrContext) ([]PrContext, error) {
	open, err := reader.OpenPullRequests(Repository{Owner: context.Owner, Repo: context.Repo})
	if err != nil {
		return nil, err
	}
	return orderStack(context, open), nil
}
