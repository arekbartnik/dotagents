package watchpr

import (
	"fmt"
	"strconv"
	"strings"
)

func renderJSON(v Verdict) string {
	out, err := marshal(v)
	if err != nil {
		panic(err)
	}
	return string(out) + "\n"
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func ciCell(row PrSnapshot) string {
	if row.Kind != "open" {
		return "—"
	}
	was := ""
	if row.Ci.HadPreviousPassingCi {
		was = ", was ✅"
	}
	switch row.Ci.Kind {
	case "ci-clean":
		return "✅"
	case "ci-pending":
		return fmt.Sprintf("⏳ %d pending%s", len(row.Ci.Pending), was)
	case "ci-failing":
		pending := ""
		if len(row.Ci.Pending) > 0 {
			pending = fmt.Sprintf(", %d pending", len(row.Ci.Pending))
		}
		return fmt.Sprintf("❌ %d failed%s%s", len(row.Ci.Failed), pending, was)
	}
	return "❌ GitHub reports failing checks" + was
}

func reviewCell(row PrSnapshot) string {
	if row.Kind != "open" {
		return "—"
	}
	open := len(row.Threads)
	switch {
	case row.ReviewAutomationRunning && open > 0:
		return fmt.Sprintf("🤖 running, %d open", open)
	case row.ReviewAutomationRunning:
		return "🤖 running"
	case open > 0:
		return fmt.Sprintf("📝 %d open", open)
	}
	return "✅"
}

func mergeCell(row PrSnapshot) string {
	switch {
	case row.Kind == "merged":
		return "✅ merged"
	case row.Kind == "closed":
		return "❌ closed"
	case row.Facts.IsDraft:
		return "⏸ draft"
	case row.Facts.ReviewDecision == "CHANGES_REQUESTED":
		return "⚠️ changes requested"
	case row.Facts.Mergeable == "CONFLICTING" || row.Facts.MergeStateStatus == "DIRTY" || row.Facts.MergeStateStatus == "CONFLICTING":
		return "⚠️ conflict"
	}
	return "✅"
}

func renderStatusTable(rows []PrSnapshot) string {
	lines := []string{"| PR | CI | Review | Merge |", "| --- | --- | --- | --- |"}
	for _, row := range rows {
		c := row.Context
		url := fmt.Sprintf("https://github.com/%s/%s/pull/%s", c.Owner, c.Repo, c.Number)
		lines = append(lines, fmt.Sprintf("| [#%s](%s) | %s | %s | %s |", c.Number, url, ciCell(row), reviewCell(row), mergeCell(row)))
	}
	return strings.Join(lines, "\n") + "\n"
}

func threadLine(thread ReviewThread) string {
	comment := thread.FirstComment
	path, line, author, body := "None", "None", "None", ""
	if comment != nil {
		path, author, body = orString(comment.Path, "None"), orString(comment.AuthorLogin, "None"), comment.Body
		if comment.Line != nil {
			line = strconv.Itoa(*comment.Line)
		}
	}
	return strings.Join([]string{
		thread.ID, path, line, author,
		"isBugBot=" + strconv.FormatBool(thread.IsBugbot),
		"bugbotReviewPasses=" + strconv.Itoa(thread.BugbotReviewPasses),
		truncate(firstLine(body), 180),
	}, " ")
}

func renderBlocker(b *MergeBlocker) string {
	switch b.Kind {
	case "merge-conflicts":
		return strings.Join([]string{
			"BLOCKER: merge-conflicts",
			"pr=" + b.Pr.Number.String(),
			"mergeable=" + b.Facts.Mergeable,
			"mergeStateStatus=" + b.Facts.MergeStateStatus,
			"action=resolve merge conflicts before waiting for CI",
		}, "\n")
	case "review-threads":
		lines := []string{"BLOCKER: review-threads", "pr=" + b.Pr.Number.String(), fmt.Sprintf("unresolved=%d", len(b.Threads))}
		for _, thread := range b.Threads {
			lines = append(lines, threadLine(thread))
		}
		return strings.Join(lines, "\n")
	case "failing-checks":
		var failed []Check
		if b.Ci.Kind == "ci-failing" {
			failed = b.Ci.Failed
		}
		var details []string
		for _, check := range failed {
			details = append(details, fmt.Sprintf("%s %s %s %s", check.Name, check.ReportedState, check.Description, check.Link))
		}
		if b.Ci.Kind == "ci-github-rejected" {
			details = append(details, "mergeStateStatus="+b.Ci.Github.MergeStateStatus, "headRollupState="+string(b.Ci.Github.HeadRollupState))
		}
		lines := append([]string{"BLOCKER: failing-checks", "pr=" + b.Pr.Number.String(), fmt.Sprintf("failed=%d", len(failed))}, details...)
		return strings.Join(lines, "\n")
	case "merge-gate":
		action := "resolve the changes-requested review before waiting for the merge queue"
		switch b.Reason {
		case "closed-without-merge":
			action = "restore or remove the closed PR from the queued stack"
		case "draft-pr":
			action = "mark the PR ready for review before waiting for the merge queue"
		}
		return strings.Join([]string{"BLOCKER: " + b.Reason, "pr=" + b.Pr.Number.String(), "action=" + action}, "\n")
	}
	return strings.Join([]string{
		"BLOCKER: status-query",
		fmt.Sprintf("failures=%d", b.Failures),
		"detail=" + b.Failure.Detail,
		"action=verify current PR context, GitHub authentication, and API availability, then rearm",
	}, "\n")
}

func renderPretty(v Verdict) string {
	switch v.Kind {
	case "QUEUE":
		numbers := make([]string, len(v.Queue))
		for i, pr := range v.Queue {
			numbers[i] = "#" + pr.Number.String()
		}
		return fmt.Sprintf("QUEUE: captured %d PR%s bottom-to-top: %s\n", len(v.Queue), plural(len(v.Queue)), strings.Join(numbers, ","))
	case "STATUS":
		return renderStatusTable(v.Rows)
	case "WAITING":
		if v.Wait.Kind == "pending-checks" {
			return fmt.Sprintf("WAITING: frontier=#%s; %d check%s pending\n", v.Frontier.Number, len(v.Wait.Pending), plural(len(v.Wait.Pending)))
		}
		return fmt.Sprintf("WAITING: frontier=#%s is blocker-free; waiting for merge queue (%d PR%s unmerged)\n", v.Frontier.Number, v.Wait.UnmergedCount, plural(v.Wait.UnmergedCount))
	case "ADVANCE":
		return fmt.Sprintf("ADVANCE: merged #%s; next=#%s; remaining=%d\n", v.Merged.Number, v.Frontier.Number, v.Remaining)
	case "RETRY":
		return fmt.Sprintf("RETRY: GitHub status query failed; retrying in %ss\ndetail=%s\n", strconv.FormatFloat(v.RetryInSeconds, 'f', -1, 64), v.Failure.Detail)
	case "BLOCKER":
		return renderBlocker(v.Blocker) + "\n"
	case "READY":
		detail := ""
		if v.Scope.Kind == "single" && v.Scope.Pr.Kind == "ready-pr" {
			pr := v.Scope.Pr
			allowed := pr.Gate.Draft == "draft-allowed"
			detail = fmt.Sprintf("\nmergeStateStatus=%s\nreviewDecision=%s\nisDraft=%t", pr.Ci.Github.MergeStateStatus, nullText(pr.Gate.ReviewDecision), allowed)
			if allowed {
				detail += "\nnote=draft allowed (--allow-draft); leave draft — do not mark ready"
			}
		}
		return "READY: no merge conflicts, no unresolved review threads, no failing or pending checks" + detail + "\n"
	case "COMPLETE":
		return fmt.Sprintf("COMPLETE: queued stack merged (%d PR%s)\n", len(v.Queue), plural(len(v.Queue)))
	}
	switch v.Timeout.Kind {
	case "pending-checks":
		return "TIMEOUT: checks still pending\n"
	case "status-unavailable":
		return "TIMEOUT: GitHub status remained unavailable\n"
	}
	return fmt.Sprintf("TIMEOUT: queued stack still has %d PR%s unmerged; frontier=#%s\n", v.Timeout.UnmergedCount, plural(v.Timeout.UnmergedCount), v.Timeout.Frontier.Number)
}

func nullText(s enum) string {
	if s == "" {
		return "null"
	}
	return string(s)
}
