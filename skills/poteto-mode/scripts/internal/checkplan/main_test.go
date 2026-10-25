package checkplan

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validPlan() string {
	var lanes []string
	for i := 1; i <= 10; i++ {
		lanes = append(lanes, fmt.Sprintf("- [ ] Lane %d. Save `lane-%d.png`. Pass when evidence exists.", i, i))
	}
	return `# Demo

## How to read this
One box is one unit of work and names the evidence. Check a box only when its evidence exists. See playbooks/.
` + rule + `

## Program checklist
### Arm the program
### Spawn owners
### PR mechanics
### Verdict and merge
### Boot recipe
` + "`git show origin/main:`" + ` /loop 1h status message

## PR 1
**Depends on.** None.
**Files.**
- [ ] Do the thing.
**Build.**
- [ ] Do the thing.
**You see.**
- [ ] Do the thing.
**Verify, unit.** ` + rule + `
- [ ] Check the thing.
**Verify, live.** ` + rule + " Ten lanes on `example-model` at the PR head\n" + strings.Join(lanes, "\n") + `
**Verify, perf.** ` + rule + `
- [ ] Metric. Recorded.
- [ ] Probe. Recorded.
- [ ] Baseline. Recorded.
- [ ] Rule. Recorded.
**Review gate.** None.
**Merge.**
- [ ] Merge when approved.

## Close the program
Close.

## Appendix Prototype evidence
Recorded.
`
}

func runPlan(t *testing.T, example string, wantExit int) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(file, []byte(validPlan()+example), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if got := Main([]string{file}, &stdout, &stderr); got != wantExit {
		t.Fatalf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", got, wantExit, stdout.String(), stderr.String())
	}
	return stderr.String()
}

func TestAcceptsOrdinaryIndentedAndLongerClosingFences(t *testing.T) {
	for _, indent := range []string{"", " ", "  ", "   "} {
		runPlan(t, indent+"```markdown\n## Example only\n"+indent+"```\n", 0)
	}
	runPlan(t, "```markdown\n## Example only\n````\n", 0)
}

func TestAcceptsNestedBackticksAndTildeInfoStrings(t *testing.T) {
	runPlan(t, "````markdown\n```markdown\n## Example only\n```\n````\n", 0)
	runPlan(t, "~~~markdown `info`\n## Example only\n ~~~\n", 0)
}

func TestKeepsMismatchedShorterAndSuffixedClosersInsideTheFence(t *testing.T) {
	runPlan(t, "````markdown\n~~~\n## Example only\n````\n", 0)
	runPlan(t, "````markdown\n```\n## Example only\n````\n", 0)
	runPlan(t, "````markdown\n``` info\n## Example only\n````\n", 0)
}

func TestRejectsHeadingsAndProseOutsideAProperClose(t *testing.T) {
	for _, suffix := range []string{"  ", "\t"} {
		stderr := runPlan(t, "```markdown\n## Example only\n```"+suffix+"\n## Example only\n", 1)
		if !strings.Contains(stderr, `"## Example only" after Close the program is not an appendix`) {
			t.Errorf("suffix %q: stderr lacks the appendix error:\n%s", suffix, stderr)
		}
	}
	stderr := runPlan(t, "```markdown\nThis prose has a mid-sentence colon: inside code\n```\nThis prose has a mid-sentence colon: bad.\n", 1)
	if !strings.Contains(stderr, "mid-sentence colon") {
		t.Errorf("stderr lacks the colon error:\n%s", stderr)
	}
}

func TestRejectsBacktickInfoStringsContainingABacktick(t *testing.T) {
	stderr := runPlan(t, "```markdown `invalid`\n## Example only\n```\n", 1)
	if !strings.Contains(stderr, `"## Example only" after Close the program is not an appendix`) {
		t.Errorf("stderr lacks the appendix error:\n%s", stderr)
	}
}

func TestIgnoresProsePunctuationInsideMatchingMultiBacktickCodeSpans(t *testing.T) {
	for _, count := range []int{2, 3, 4} {
		delimiter := strings.Repeat("`", count)
		runPlan(t, "Use "+delimiter+"echo `x`: “quoted” — example"+delimiter+".\n", 0)
	}
}

func TestChecksProseFollowingAMultiBacktickSpanAndUnmatchedDelimiters(t *testing.T) {
	if stderr := runPlan(t, "Use ``echo `x`: y``. Bad: prose.\n", 1); !strings.Contains(stderr, "mid-sentence colon") {
		t.Errorf("outside span: stderr lacks the colon error:\n%s", stderr)
	}
	if stderr := runPlan(t, "Use ``unclosed: bad` here.\n", 1); !strings.Contains(stderr, "mid-sentence colon") {
		t.Errorf("unmatched: stderr lacks the colon error:\n%s", stderr)
	}
}

func TestReportsBoxCountsPerPR(t *testing.T) {
	file := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(file, []byte(validPlan()), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if got := Main([]string{file}, &stdout, &stderr); got != 0 {
		t.Fatalf("exit %d\n%s", got, stderr.String())
	}
	want := "PR 1  boxes=19  files=1 build=1 you-see=1 verify-unit=1 verify-live=10 verify-perf=4 review-gate=0 merge=1\n1 PR sections, 0 problems\n"
	if stdout.String() != want {
		t.Errorf("stdout:\n%s\nwant:\n%s", stdout.String(), want)
	}
}
