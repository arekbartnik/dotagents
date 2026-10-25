package orch

import (
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

type forgeQuery struct {
	stateField, headField, baseField string
	states                           map[string]string
}

var forgeQueries = map[string]forgeQuery{
	"gh": {
		stateField: "state", headField: "headRefName", baseField: "baseRefName",
		states: map[string]string{"OPEN": "OPEN", "MERGED": "MERGED", "CLOSED": "CLOSED"},
	},
	"origin": {
		stateField: "status", headField: "headRef", baseField: "baseRef",
		states: map[string]string{"open": "OPEN", "draft": "OPEN", "merged": "MERGED", "closed": "CLOSED"},
	},
}

type forgePullRequest struct {
	pr     int64
	state  string
	branch string
	base   string
}

type forgeResolver struct {
	repo  string
	forge string
}

func (f forgeResolver) runTool(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = f.repo
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			err = fmt.Errorf("%w: %s", err, strings.TrimSpace(validUTF8(string(exitErr.Stderr))))
		}
		return "", userErrorf("%s %s failed: %v", name, strings.Join(args, " "), err)
	}
	return validUTF8(string(out)), nil
}

func (f forgeResolver) listPullRequests(filter, branch string) ([]forgePullRequest, error) {
	query := forgeQueries[f.forge]
	command := f.forge + " pr list " + filter + " " + branch
	// Every state is fetched because a merged or closed PR still anchors the
	// chain, and a draft counts as OPEN.
	raw, err := f.runTool(f.forge, "pr", "list", filter, branch, "--state", "all", "--json",
		strings.Join([]string{"number", query.stateField, query.headField, query.baseField}, ","))
	if err != nil {
		return nil, err
	}
	value, err := decodeJSON(raw)
	if err != nil {
		return nil, userErrorf("%s output is not valid JSON", command)
	}
	rows, ok := value.([]any)
	if !ok {
		return nil, userErrorf("%s output is not a JSON array", command)
	}
	result := make([]forgePullRequest, 0, len(rows))
	for _, item := range rows {
		row, isRecord := item.(map[string]any)
		status, _ := row[query.stateField].(string)
		state, known := query.states[status]
		head, _ := row[query.headField].(string)
		base, _ := row[query.baseField].(string)
		number, numberOK := jsonSafeInteger(row["number"])
		if !isRecord || !numberOK || number < 1 || !known || head == "" || base == "" {
			return nil, userErrorf("%s output has an invalid PR row", command)
		}
		result = append(result, forgePullRequest{pr: number, state: state, branch: head, base: base})
	}
	return result, nil
}

func (f forgeResolver) branchPullRequest(branch string) (forgePullRequest, error) {
	rows, err := f.listPullRequests("--head", branch)
	if err != nil {
		return forgePullRequest{}, err
	}
	// A branch may have closed or merged historical PRs after stack surgery.
	// Prefer its sole active PR, but retain historical anchors when none is open.
	var active []forgePullRequest
	for _, row := range rows {
		if row.state == "OPEN" {
			active = append(active, row)
		}
	}
	candidates := rows
	if len(active) > 0 {
		candidates = active
	}
	if len(candidates) == 0 {
		return forgePullRequest{}, userErrorf("branch %s has no pull request; open one with %s pr create, or resolve the frontier from a branch whose chain is fully submitted", branch, f.forge)
	}
	if len(candidates) > 1 {
		numbers := make([]string, 0, len(candidates))
		for _, item := range candidates {
			numbers = append(numbers, "#"+strconv.FormatInt(item.pr, 10))
		}
		return forgePullRequest{}, userErrorf("branch %s has multiple pull requests (%s); close the stale ones first", branch, strings.Join(numbers, ", "))
	}
	return candidates[0], nil
}

func (f forgeResolver) openChildren(branch string) ([]forgePullRequest, error) {
	rows, err := f.listPullRequests("--base", branch)
	if err != nil {
		return nil, err
	}
	var open []forgePullRequest
	for _, row := range rows {
		if row.state == "OPEN" {
			open = append(open, row)
		}
	}
	return open, nil
}

func (f forgeResolver) runGit(args ...string) (string, error) {
	out, err := f.runTool("git", args...)
	return strings.TrimSpace(out), err
}

var shaPattern = regexp.MustCompile(`^[0-9a-fA-F]{40,64}$`)

func (f forgeResolver) branchSHA(branch string) (string, error) {
	sha, err := f.runGit("rev-parse", branch)
	if err != nil {
		return "", err
	}
	if !shaPattern.MatchString(sha) {
		return "", userErrorf("git rev-parse %s returned an invalid SHA", branch)
	}
	return sha, nil
}

func (f forgeResolver) trunkBranch() string {
	trunk, err := f.runGit("symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	if err != nil {
		// Init-plus-fetch checkouts and local fixtures lack origin/HEAD.
		return "main"
	}
	return strings.TrimPrefix(trunk, "origin/")
}

func (f forgeResolver) resolve() ([]frontierPR, error) {
	trunk := f.trunkBranch()
	current, err := f.runGit("symbolic-ref", "--short", "HEAD")
	if err != nil {
		return nil, err
	}
	if current == trunk {
		return []frontierPR{}, nil
	}
	seen := map[string]bool{current: true}
	bottom, err := f.branchPullRequest(current)
	if err != nil {
		return nil, err
	}
	chain := []forgePullRequest{bottom}
	for bottom.base != trunk {
		if seen[bottom.base] {
			return nil, userErrorf("pull request base chain loops at branch %s", bottom.base)
		}
		seen[bottom.base] = true
		if bottom, err = f.branchPullRequest(bottom.base); err != nil {
			return nil, err
		}
		chain = append([]forgePullRequest{bottom}, chain...)
	}
	tip := current
	for {
		children, err := f.openChildren(tip)
		if err != nil {
			return nil, err
		}
		if len(children) == 0 {
			break
		}
		if len(children) > 1 {
			branches := make([]string, 0, len(children))
			for _, row := range children {
				branches = append(branches, row.branch)
			}
			return nil, userErrorf("stack is not linear: branches %s all target %s", strings.Join(branches, ", "), tip)
		}
		child := children[0]
		if seen[child.branch] {
			return nil, userErrorf("pull request base chain loops at branch %s", child.branch)
		}
		seen[child.branch] = true
		chain = append(chain, child)
		tip = child.branch
	}
	numbers := map[int64]bool{}
	for _, row := range chain {
		numbers[row.pr] = true
	}
	if len(numbers) != len(chain) {
		return nil, userErrorf("pull request base chain contains duplicate pull requests")
	}
	result := make([]frontierPR, 0, len(chain))
	for _, row := range chain {
		sha, err := f.branchSHA(row.branch)
		if err != nil {
			return nil, err
		}
		result = append(result, frontierPR{PR: row.pr, Branches: row.branch, SHA: sha, State: row.state})
	}
	return result, nil
}

func validateFrontierPin(actual, expected []int64) error {
	if slices.Equal(actual, expected) {
		return nil
	}
	var missing, extra []int64
	for _, pr := range expected {
		if !slices.Contains(actual, pr) {
			missing = append(missing, pr)
		}
	}
	for _, pr := range actual {
		if !slices.Contains(expected, pr) {
			extra = append(extra, pr)
		}
	}
	var drift []string
	if len(missing) > 0 {
		drift = append(drift, "missing from resolved chain: "+joinInts(missing))
	}
	if len(extra) > 0 {
		drift = append(drift, "extra in resolved chain: "+joinInts(extra))
	}
	if len(drift) == 0 {
		drift = append(drift, "order differs: expected "+joinInts(expected)+"; resolved "+joinInts(actual))
	}
	return userErrorf("frontier pin mismatch: %s", strings.Join(drift, "; "))
}

func joinInts(values []int64) string {
	parts := make([]string, 0, len(values))
	for _, v := range values {
		parts = append(parts, strconv.FormatInt(v, 10))
	}
	return strings.Join(parts, ",")
}
