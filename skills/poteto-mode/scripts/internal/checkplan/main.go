package checkplan

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// jsSpace is the JavaScript \s class, which is wider than RE2's.
const jsSpace = `\t\n\v\f\r \x{00a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

const rule = "Tests alone are not sufficient verification. A PR is verified only when its unit, live, and perf boxes are all checked."

var (
	subBlocks       = []string{"Depends on.", "Files.", "Build.", "You see.", "Verify, unit.", "Verify, live.", "Verify, perf.", "Review gate.", "Merge."}
	programH3       = []string{"Arm the program", "Spawn owners", "PR mechanics", "Verdict and merge", "Boot recipe"}
	programMarkers  = []string{"git show origin/main:", "/loop 1h", "status message"}
	howToReadMarker = []string{"One box is one unit of work", "names the evidence", "Check a box only when its evidence exists", "playbooks/", rule}
	perfItems       = []string{"Metric.", "Probe.", "Baseline.", "Rule."}

	lanesRe        = regexp.MustCompile("Ten lanes on `[^`<>]+` at the PR head")
	boxRe          = regexp.MustCompile(`^[` + jsSpace + `]*- \[[ x]\] (.*)$`)
	openingFenceRe = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})(.*)$")
	closingFenceRe = regexp.MustCompile("^ {0,3}(`+|~+)[ \t]*$")
	imageRe        = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	linkTargetRe   = regexp.MustCompile(`\]\([^)]*\)`)
	longDashRe     = regexp.MustCompile("[–—]")
	curlyQuoteRe   = regexp.MustCompile("[‘’“”]")
	colonRe        = regexp.MustCompile(`: [^` + jsSpace + `]`)
	subBlockRe     = regexp.MustCompile(`^\*\*([^*]+)\*\*(.*)$`)
	laneRe         = regexp.MustCompile(`^Lane (\d+)\. `)
	screenshotRe   = regexp.MustCompile("Save `[^`]+`")
	slugRe         = regexp.MustCompile(`[ ,.]+`)
)

type line struct {
	n    int
	text string
	code bool
}

type fence struct {
	char   byte
	length int
}

type section struct {
	title string
	n     int
	body  []line
}

type box struct {
	n    int
	text string
}

type subBlock struct {
	name  string
	n     int
	rest  string
	lines []line
}

func openingFence(text string) *fence {
	m := openingFenceRe.FindStringSubmatch(text)
	if m == nil || (m[1][0] == '`' && strings.Contains(m[2], "`")) {
		return nil
	}
	return &fence{char: m[1][0], length: len(m[1])}
}

func closingFence(text string, opening *fence) bool {
	m := closingFenceRe.FindStringSubmatch(text)
	return m != nil && m[1][0] == opening.char && len(m[1]) >= opening.length
}

// stripCodeSpans replaces each code span with a single backtick. A span opens
// at a maximal backtick run and closes at the next maximal run of the same
// length; an unmatched run stays literal.
func stripCodeSpans(text string) string {
	runEnd := func(i int) int {
		for i < len(text) && text[i] == '`' {
			i++
		}
		return i
	}
	var out strings.Builder
	i := 0
	for i < len(text) {
		if text[i] != '`' {
			out.WriteByte(text[i])
			i++
			continue
		}
		end := runEnd(i)
		width := end - i
		closed := -1
		for j := end; j < len(text); {
			if text[j] != '`' {
				j++
				continue
			}
			k := runEnd(j)
			if k-j == width {
				closed = k
				break
			}
			j = k
		}
		if closed == -1 {
			out.WriteString(text[i:end])
			i = end
			continue
		}
		out.WriteByte('`')
		i = closed
	}
	return out.String()
}

func boxes(ls []line) []box {
	var out []box
	for _, l := range ls {
		if l.code {
			continue
		}
		if m := boxRe.FindStringSubmatch(l.text); m != nil {
			out = append(out, box{l.n, m[1]})
		}
	}
	return out
}

func bodyText(ls []line) string {
	texts := make([]string, len(ls))
	for i, l := range ls {
		texts[i] = l.text
	}
	return strings.Join(texts, "\n")
}

func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "Usage: poteto check-plan <plan.md>")
		return 2
	}
	file := args[0]
	data, err := os.ReadFile(file)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	report, problems, prCount := check(file, string(data))
	for _, r := range report {
		fmt.Fprintln(stdout, r)
	}
	fmt.Fprintf(stdout, "%d PR sections, %d problems\n", prCount, len(problems))
	for _, p := range problems {
		fmt.Fprintln(stderr, p)
	}
	if len(problems) > 0 {
		return 1
	}
	return 0
}

func check(file, content string) (report, problems []string, prCount int) {
	fail := func(n int, message string) {
		problems = append(problems, fmt.Sprintf("%s:%d: %s", file, n, message))
	}
	raw := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")

	start := 0
	if raw[0] == "---" {
		start = slices.Index(raw[1:], "---") + 2
		if start == 1 {
			start = 0
		}
	}

	var lines []line
	var open *fence
	for i := start; i < len(raw); i++ {
		text := raw[i]
		n := i + 1
		code := open != nil
		if open != nil && closingFence(text, open) {
			code = false
			open = nil
		} else if open == nil {
			open = openingFence(text)
			code = open != nil
		}
		lines = append(lines, line{n, text, code})
		if code {
			continue
		}
		prose := linkTargetRe.ReplaceAllString(imageRe.ReplaceAllString(stripCodeSpans(text), ""), "]")
		if longDashRe.MatchString(prose) {
			fail(n, "long dash")
		}
		if curlyQuoteRe.MatchString(prose) {
			fail(n, "curly quote")
		}
		if colonRe.MatchString(prose) {
			fail(n, "mid-sentence colon")
		}
	}

	var sections []*section
	for _, l := range lines {
		if !l.code && strings.HasPrefix(l.text, "## ") {
			sections = append(sections, &section{title: strings.TrimSpace(l.text[3:]), n: l.n})
		} else if len(sections) > 0 {
			last := sections[len(sections)-1]
			last.body = append(last.body, l)
		}
	}
	find := func(title string) int {
		return slices.IndexFunc(sections, func(s *section) bool { return s.title == title })
	}

	h1 := slices.IndexFunc(lines, func(l line) bool { return !l.code && strings.HasPrefix(l.text, "# ") })
	if h1 == -1 {
		fail(1, "no H1 title")
	}
	howToRead := find("How to read this")
	if howToRead == -1 {
		fail(1, `no "## How to read this" section`)
	}
	if h1 != -1 && howToRead != -1 {
		intro := 0
		for _, l := range lines[h1+1:] {
			if l.n < sections[howToRead].n && strings.TrimSpace(l.text) != "" {
				intro++
			}
		}
		if intro >= 10 {
			fail(lines[h1].n, fmt.Sprintf("intro is %d lines, under ten required", intro))
		}
		for _, marker := range howToReadMarker {
			if !strings.Contains(bodyText(sections[howToRead].body), marker) {
				fail(sections[howToRead].n, fmt.Sprintf("How to read this lacks \"%s\"", marker))
			}
		}
	}

	programIndex := find("Program checklist")
	if programIndex == -1 {
		fail(1, `no "## Program checklist" section`)
	} else {
		program := sections[programIndex]
		var h3s []string
		for _, l := range program.body {
			if !l.code && strings.HasPrefix(l.text, "### ") {
				h3s = append(h3s, strings.TrimSpace(l.text[4:]))
			}
		}
		cursor := 0
		for _, name := range programH3 {
			at := -1
			for i := cursor; i < len(h3s); i++ {
				if strings.HasPrefix(h3s[i], name) {
					at = i
					break
				}
			}
			if at == -1 {
				fail(program.n, fmt.Sprintf(`Program checklist lacks "### %s" in order`, name))
			} else {
				cursor = at + 1
			}
		}
		for _, marker := range programMarkers {
			if !strings.Contains(bodyText(program.body), marker) {
				fail(program.n, fmt.Sprintf("Program checklist lacks \"%s\"", marker))
			}
		}
	}

	closeIndex := find("Close the program")
	if closeIndex == -1 {
		fail(1, `no "## Close the program" section`)
	}
	var prSections []*section
	if programIndex != -1 && closeIndex != -1 && programIndex+1 < closeIndex {
		prSections = sections[programIndex+1 : closeIndex]
	}
	if len(prSections) == 0 {
		fail(1, "no PR sections between Program checklist and Close the program")
	}

	for _, pr := range prSections {
		var heads []*subBlock
		for _, l := range pr.body {
			if l.code {
				continue
			}
			if m := subBlockRe.FindStringSubmatch(l.text); m != nil && slices.Contains(subBlocks, m[1]) {
				heads = append(heads, &subBlock{name: m[1], n: l.n, rest: strings.TrimSpace(m[2])})
			} else if len(heads) > 0 {
				last := heads[len(heads)-1]
				last.lines = append(last.lines, l)
			}
		}
		names := make([]string, len(heads))
		for i, h := range heads {
			names[i] = h.name
		}
		if strings.Join(names, "|") != strings.Join(subBlocks, "|") {
			fail(pr.n, fmt.Sprintf("%s: sub-blocks are [%s], expected [%s]", pr.title, strings.Join(names, ", "), strings.Join(subBlocks, ", ")))
		}
		block := func(name string) *subBlock {
			for _, h := range heads {
				if h.name == name {
					return h
				}
			}
			return nil
		}
		counts := map[string]int{}
		for _, h := range heads {
			counts[h.name] = len(boxes(h.lines))
		}

		if depends := block("Depends on."); depends != nil && depends.rest == "" {
			fail(depends.n, fmt.Sprintf("%s: Depends on names nothing", pr.title))
		}
		for _, name := range []string{"Files.", "Build.", "You see.", "Verify, unit.", "Merge."} {
			if b := block(name); b != nil && len(boxes(b.lines)) == 0 {
				fail(b.n, fmt.Sprintf("%s: %s has no box", pr.title, name))
			}
		}
		for _, name := range []string{"Verify, unit.", "Verify, live.", "Verify, perf."} {
			if b := block(name); b != nil && !strings.HasPrefix(b.rest, rule) {
				fail(b.n, fmt.Sprintf("%s: %s does not open with the rule", pr.title, name))
			}
		}

		if live := block("Verify, live."); live != nil {
			if !lanesRe.MatchString(live.rest) {
				fail(live.n, fmt.Sprintf("%s: Verify, live lacks \"Ten lanes on `<swarm workers model>` at the PR head\" with the model filled in", pr.title))
			}
			liveBoxes := boxes(live.lines)
			var numbers []int
			for _, b := range liveBoxes {
				if m := laneRe.FindStringSubmatch(b.text); m != nil {
					v, _ := strconv.Atoi(m[1])
					numbers = append(numbers, v)
				}
			}
			slices.Sort(numbers)
			if joinInts(numbers) != "1,2,3,4,5,6,7,8,9,10" {
				fail(live.n, fmt.Sprintf("%s: lanes are [%s], expected 1 to 10", pr.title, joinInts(numbers)))
			}
			for _, b := range liveBoxes {
				m := laneRe.FindStringSubmatch(b.text)
				switch {
				case m == nil:
					fail(b.n, fmt.Sprintf("%s: live box is not a lane", pr.title))
				case !screenshotRe.MatchString(b.text):
					fail(b.n, fmt.Sprintf("%s: lane %s names no screenshot", pr.title, m[1]))
				case !strings.Contains(b.text, "Pass when"):
					fail(b.n, fmt.Sprintf("%s: lane %s has no pass predicate", pr.title, m[1]))
				}
			}
		}

		if perf := block("Verify, perf."); perf != nil {
			var items []string
			for _, b := range boxes(perf.lines) {
				items = append(items, strings.Split(b.text, " ")[0])
			}
			if strings.Join(items, "|") != strings.Join(perfItems, "|") {
				fail(perf.n, fmt.Sprintf("%s: perf boxes are [%s], expected [%s]", pr.title, strings.Join(items, ", "), strings.Join(perfItems, ", ")))
			}
		}

		if gate := block("Review gate."); gate != nil {
			gateBoxes := boxes(gate.lines)
			if strings.HasPrefix(gate.rest, "None.") {
				if len(gateBoxes) > 0 {
					fail(gate.n, fmt.Sprintf("%s: Review gate says None but has boxes", pr.title))
				}
			} else {
				text := bodyText(gate.lines)
				if len(gateBoxes) == 0 {
					fail(gate.n, fmt.Sprintf("%s: Review gate has no box", pr.title))
				}
				for _, word := range []string{"screenshot", "video", "operator"} {
					if !strings.Contains(text, word) {
						fail(gate.n, fmt.Sprintf("%s: Review gate lacks \"%s\"", pr.title, word))
					}
				}
			}
		}

		var cells []string
		for _, s := range subBlocks[1:] {
			slug := strings.ToLower(strings.TrimSuffix(slugRe.ReplaceAllString(s, "-"), "-"))
			cells = append(cells, fmt.Sprintf("%s=%d", slug, counts[s]))
		}
		report = append(report, fmt.Sprintf("%s  boxes=%d  %s", pr.title, len(boxes(pr.body)), strings.Join(cells, " ")))
	}

	if closeIndex != -1 {
		tail := sections[closeIndex+1:]
		for _, s := range tail {
			if !strings.HasPrefix(s.title, "Appendix") {
				fail(s.n, fmt.Sprintf(`"## %s" after Close the program is not an appendix`, s.title))
			}
		}
		if !slices.ContainsFunc(tail, func(s *section) bool { return strings.Contains(s.title, "Prototype evidence") }) {
			fail(sections[closeIndex].n, `no "## Appendix ... Prototype evidence" section`)
		}
	}
	return report, problems, len(prSections)
}

func joinInts(ns []int) string {
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ",")
}
