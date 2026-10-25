package orch

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	unitHeader   = "id\ttrack\tstate\tbranch\tpr\tsha\tbrief"
	ledgerHeader = "pr\tsha\tverdict\tevidence\tverifier\tts"
)

var verdicts = []string{"live-ui-verified", "unit-test-verified", "type-check-only", "verifier-blocked", "verifier-failed"}

type unit struct {
	ID     string `json:"id"`
	Track  string `json:"track"`
	State  string `json:"state"`
	Branch string `json:"branch"`
	PR     string `json:"pr"`
	SHA    string `json:"sha"`
	Brief  string `json:"brief"`
}

func (u unit) cells() []string {
	return []string{u.ID, u.Track, u.State, u.Branch, u.PR, u.SHA, u.Brief}
}

type ledgerEntry struct {
	PR       string `json:"pr"`
	SHA      string `json:"sha"`
	Verdict  string `json:"verdict"`
	Evidence string `json:"evidence"`
	Verifier string `json:"verifier"`
	TS       string `json:"ts"`
}

func (e ledgerEntry) cells() []string {
	return []string{e.PR, e.SHA, e.Verdict, e.Evidence, e.Verifier, e.TS}
}

type inboxPointer struct {
	TS     string `json:"ts"`
	Agent  string `json:"agent"`
	Unit   string `json:"unit"`
	Status string `json:"status"`
	Report string `json:"report"`
}

func (p inboxPointer) cells() []string {
	return []string{p.TS, p.Agent, p.Unit, p.Status, p.Report}
}

// inboxPushResult marshals as its pointer; the filename is compact output only.
type inboxPushResult struct {
	inboxPointer
	filename string
}

type gate struct {
	Kind          string  `json:"kind"`
	ID            string  `json:"id"`
	Question      string  `json:"question"`
	Options       string  `json:"options"`
	DefaultAnswer string  `json:"defaultAnswer"`
	Answer        *string `json:"answer,omitempty"`
}

func (g gate) resolved() bool { return g.Kind == "resolved" }

type frontierPR struct {
	PR       int64  `json:"pr"`
	Branches string `json:"branches"`
	SHA      string `json:"sha"`
	State    string `json:"state"`
}

type frontier struct {
	Generation     int64        `json:"generation"`
	PRs            []frontierPR `json:"prs"`
	LowestUnmerged *int64       `json:"lowestUnmerged"`
}

type standingLine struct {
	Number int    `json:"number"`
	Line   string `json:"line"`
}

type statusSummary struct {
	UnitStates         counts   `json:"unitStates"`
	LedgerVerdicts     counts   `json:"ledgerVerdicts"`
	FrontierGeneration int64    `json:"frontierGeneration"`
	OpenGateIDs        []string `json:"openGateIds"`
}

// previousSummary is the summary read back from status.md, where the
// generation may be any JSON number.
type previousSummary struct {
	unitStates, ledgerVerdicts counts
	frontierGeneration         float64
	openGateIDs                []string
}

type statusReport struct {
	Units    []unit        `json:"units"`
	Ledger   []ledgerEntry `json:"ledger"`
	Frontier frontier      `json:"frontier"`
	Gates    []gate        `json:"gates"`
	Summary  statusSummary `json:"summary"`
	Changed  string        `json:"changed"`
}

type storeOptions struct {
	force        bool
	onLockStolen func(holder string)
	onStaleLock  func(holder string)
}

type store struct {
	dir     string
	options storeOptions
	closed  bool
	release func() error
}

func parseVerdict(value string) (string, error) {
	if slices.Contains(verdicts, value) {
		return value, nil
	}
	return "", userErrorf("verdict must be live-ui-verified, unit-test-verified, type-check-only, verifier-blocked, or verifier-failed")
}

// cleanCell keeps a value on one TSV cell and stops spreadsheets from
// reading it as a formula.
func cleanCell(value string) string {
	cleaned := strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace(value)
	if cleaned != "" && strings.ContainsRune("=+-@", rune(cleaned[0])) {
		return "'" + cleaned
	}
	return cleaned
}

func requiredCell(value, label string) (string, error) {
	cleaned := cleanCell(value)
	if strings.TrimSpace(cleaned) == "" {
		return "", userErrorf("%s must not be empty", label)
	}
	return cleaned, nil
}

func optionalCell(value *string, label, fallback string) (string, error) {
	if value == nil {
		return fallback, nil
	}
	return requiredCell(*value, label)
}

func requiredLine(value, label string) (string, error) {
	cleaned := strings.TrimSpace(strings.NewReplacer("\n", " ", "\r", " ").Replace(value))
	if cleaned == "" {
		return "", userErrorf("%s must not be empty", label)
	}
	return cleaned, nil
}

func positiveInteger(value int64, label string) (int64, error) {
	if value < 1 || value > maxSafeInteger {
		return 0, userErrorf("%s must be a positive integer", label)
	}
	return value, nil
}

func isoTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

func notInitialized(dir string) error {
	return userErrorf("store is not initialized at %s; run orch init", dir)
}

func requiredFile(path string) (string, error) {
	raw, err := readFileText(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", notInitialized(filepath.Dir(path))
	}
	return raw, err
}

func readTSV(path, header string, width int) ([][]string, error) {
	raw, err := requiredFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.ReplaceAll(raw, "\r", ""), "\n")
	if lines[0] != header {
		return nil, userErrorf("%s has an invalid header", filepath.Base(path))
	}
	var rows [][]string
	for _, line := range lines[1:] {
		if line == "" {
			continue
		}
		cells := strings.Split(line, "\t")
		if len(cells) != width {
			return nil, userErrorf("%s has a malformed row", filepath.Base(path))
		}
		rows = append(rows, cells)
	}
	return rows, nil
}

func writeTSV[T interface{ cells() []string }](path, header string, rows []T) error {
	var b strings.Builder
	b.WriteString(header + "\n")
	for _, row := range rows {
		cells := row.cells()
		for i, cell := range cells {
			cells[i] = cleanCell(cell)
		}
		b.WriteString(strings.Join(cells, "\t") + "\n")
	}
	return atomicWrite(path, b.String())
}

func readUnits(dir string) ([]unit, error) {
	rows, err := readTSV(filepath.Join(dir, "units.tsv"), unitHeader, 7)
	if err != nil {
		return nil, err
	}
	units := make([]unit, 0, len(rows))
	for _, row := range rows {
		units = append(units, unit{row[0], row[1], row[2], row[3], row[4], row[5], row[6]})
	}
	return units, nil
}

func readLedger(dir string) ([]ledgerEntry, error) {
	rows, err := readTSV(filepath.Join(dir, "ledger.tsv"), ledgerHeader, 6)
	if err != nil {
		return nil, err
	}
	entries := make([]ledgerEntry, 0, len(rows))
	for _, row := range rows {
		if !slices.Contains(verdicts, row[2]) {
			return nil, userErrorf("ledger.tsv has invalid verdict %s", row[2])
		}
		entries = append(entries, ledgerEntry{row[0], row[1], row[2], row[3], row[4], row[5]})
	}
	return entries, nil
}

func readPointers(dir string) ([]inboxPointer, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, notInitialized(filepath.Dir(dir))
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".tsv") {
			names = append(names, entry.Name())
		}
	}
	slices.SortStableFunc(names, localeCompare)
	result := []inboxPointer{}
	for _, name := range names {
		raw, err := readFileText(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		if strings.HasSuffix(raw, "\r\n") {
			raw = raw[:len(raw)-2]
		} else {
			raw = strings.TrimSuffix(raw, "\n")
		}
		row := strings.Split(raw, "\t")
		if strings.ContainsAny(raw, "\r\n") || len(row) != 5 {
			return nil, userErrorf("inbox pointer %s is malformed", name)
		}
		result = append(result, inboxPointer{row[0], row[1], row[2], row[3], row[4]})
	}
	return result, nil
}

func renderGates(rows []gate) string {
	if len(rows) == 0 {
		return ""
	}
	blocks := make([]string, 0, len(rows))
	for _, g := range rows {
		block := "## " + g.ID + "\n\n- Status: " + g.Kind + "\n- Question: " + g.Question +
			"\n- Options: " + g.Options + "\n- Default: " + g.DefaultAnswer
		if g.resolved() {
			block += "\n- Answer: " + *g.Answer
		}
		blocks = append(blocks, block)
	}
	return "# Gates\n\n" + strings.Join(blocks, "\n\n") + "\n"
}

// The field and standing patterns reject U+2028 and U+2029 because the
// TypeScript store's "." did.
var (
	gateFieldPattern = regexp.MustCompile(`^- ([^:]+): ([^\n\r\x{2028}\x{2029}]*)$`)
	standingPattern  = regexp.MustCompile(`^([1-9][0-9]*)\. ([^\n\r\x{2028}\x{2029}]+)$`)
	summaryPattern   = regexp.MustCompile(`<!-- orch-summary ([^\n\r\x{2028}\x{2029}]+) -->`)
)

func readGates(dir string) ([]gate, error) {
	content, err := requiredFile(filepath.Join(dir, "gates.md"))
	if err != nil {
		return nil, err
	}
	result := []gate{}
	raw := strings.TrimSpace(strings.ReplaceAll(content, "\r", ""))
	if raw == "" {
		return result, nil
	}
	const prefix = "# Gates\n\n## "
	if !strings.HasPrefix(raw, prefix) {
		return nil, userErrorf("gates.md has an invalid heading")
	}
	seen := map[string]bool{}
	for _, block := range strings.Split(raw[len(prefix):], "\n\n## ") {
		var lines []string
		for _, line := range strings.Split(block, "\n") {
			if line != "" {
				lines = append(lines, line)
			}
		}
		id := ""
		if len(lines) > 0 {
			id, lines = lines[0], lines[1:]
		}
		fields := map[string]string{}
		for _, line := range lines {
			match := gateFieldPattern.FindStringSubmatch(line)
			if match == nil {
				return nil, userErrorf("gates.md has a malformed gate %s", id)
			}
			fields[match[1]] = match[2]
		}
		status, hasStatus := fields["Status"]
		question, hasQuestion := fields["Question"]
		options, hasOptions := fields["Options"]
		defaultAnswer, hasDefault := fields["Default"]
		if id == "" || !hasQuestion || !hasOptions || !hasDefault {
			return nil, userErrorf("gates.md has a malformed gate %s", id)
		}
		g := gate{Kind: status, ID: id, Question: question, Options: options, DefaultAnswer: defaultAnswer}
		answer, hasAnswer := fields["Answer"]
		switch {
		case hasStatus && status == "open":
		case hasStatus && status == "resolved" && hasAnswer:
			g.Answer = &answer
		default:
			return nil, userErrorf("gates.md has invalid status %s", status)
		}
		result = append(result, g)
		seen[id] = true
	}
	if len(seen) != len(result) {
		return nil, userErrorf("gates.md has duplicate gate ids")
	}
	return result, nil
}

func parseFrontier(raw string) (frontier, error) {
	value, err := decodeJSON(raw)
	if err != nil {
		return frontier{}, userErrorf("frontier.json is not valid JSON")
	}
	record, ok := value.(map[string]any)
	if !ok {
		return frontier{}, userErrorf("frontier.json must contain an object")
	}
	result := frontier{PRs: []frontierPR{}}
	if len(record) == 0 {
		return result, nil
	}
	generation, generationOK := jsonSafeInteger(record["generation"])
	prs, prsOK := record["prs"].([]any)
	lowestRaw, hasLowest := record["lowestUnmerged"]
	lowest, lowestOK := jsonSafeInteger(lowestRaw)
	if !generationOK || generation < 0 || !prsOK || !hasLowest || !(lowestRaw == nil || lowestOK) {
		return frontier{}, userErrorf("frontier.json has an invalid shape")
	}
	result.Generation = generation
	if lowestRaw != nil {
		result.LowestUnmerged = &lowest
	}
	for _, item := range prs {
		row, isRecord := item.(map[string]any)
		pr, prOK := jsonSafeInteger(row["pr"])
		branches, _ := row["branches"].(string)
		sha, shaOK := row["sha"].(string)
		state, _ := row["state"].(string)
		if !isRecord || !prOK || pr < 1 || branches == "" || !shaOK ||
			!slices.Contains([]string{"OPEN", "MERGED", "CLOSED"}, state) {
			return frontier{}, userErrorf("frontier.json has an invalid PR row")
		}
		result.PRs = append(result.PRs, frontierPR{PR: pr, Branches: branches, SHA: sha, State: state})
	}
	return result, nil
}

func readFrontier(dir string) (frontier, error) {
	raw, err := requiredFile(filepath.Join(dir, "frontier.json"))
	if err != nil {
		return frontier{}, err
	}
	return parseFrontier(raw)
}

func readStanding(dir string) ([]standingLine, error) {
	content, err := requiredFile(filepath.Join(dir, "preferences.md"))
	if err != nil {
		return nil, err
	}
	result := []standingLine{}
	raw := strings.ReplaceAll(content, "\r", "")
	if strings.TrimSpace(raw) == "" {
		return result, nil
	}
	for _, line := range strings.Split(raw, "\n") {
		if line == "" {
			continue
		}
		match := standingPattern.FindStringSubmatch(line)
		if match == nil || match[1] != strconv.Itoa(len(result)+1) {
			return nil, userErrorf("preferences.md has malformed numbering")
		}
		result = append(result, standingLine{Number: len(result) + 1, Line: match[2]})
	}
	return result, nil
}

func summarize(units []unit, ledger []ledgerEntry, current frontier, gates []gate) statusSummary {
	states := make([]string, 0, len(units))
	for _, u := range units {
		states = append(states, u.State)
	}
	verdictValues := make([]string, 0, len(ledger))
	for _, e := range ledger {
		verdictValues = append(verdictValues, e.Verdict)
	}
	open := []string{}
	for _, g := range gates {
		if !g.resolved() {
			open = append(open, g.ID)
		}
	}
	slices.Sort(open)
	return statusSummary{
		UnitStates:         countValues(states),
		LedgerVerdicts:     countValues(verdictValues),
		FrontierGeneration: current.Generation,
		OpenGateIDs:        open,
	}
}

func countRecord(value any) (counts, bool) {
	record, ok := value.(map[string]any)
	if !ok {
		return counts{}, false
	}
	result := counts{values: map[string]countValue{}}
	for name, raw := range record {
		count, ok := jsonSafeInteger(raw)
		if !ok || count < 0 {
			return counts{}, false
		}
		if name != "__proto__" {
			result.keys = append(result.keys, name)
			result.values[name] = countValue{n: count}
		}
	}
	return result, true
}

func parsePreviousSummary(raw string) *previousSummary {
	match := summaryPattern.FindStringSubmatch(raw)
	if match == nil {
		return nil
	}
	value, err := decodeJSON(match[1])
	if err != nil {
		return nil
	}
	record, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	generation, generationOK := jsonNumber(record["frontierGeneration"])
	ids, idsOK := record["openGateIds"].([]any)
	unitStates, unitsOK := countRecord(record["unitStates"])
	ledgerVerdicts, ledgerOK := countRecord(record["ledgerVerdicts"])
	if !generationOK || !idsOK || !unitsOK || !ledgerOK {
		return nil
	}
	openGateIDs := []string{}
	for _, id := range ids {
		s, isString := id.(string)
		if !isString {
			return nil
		}
		openGateIDs = append(openGateIDs, s)
	}
	return &previousSummary{
		unitStates:         unitStates,
		ledgerVerdicts:     ledgerVerdicts,
		frontierGeneration: generation,
		openGateIDs:        openGateIDs,
	}
}

func describeChanges(before *previousSummary, after statusSummary) string {
	if before == nil {
		return "first render"
	}
	var result []string
	groups := []struct {
		label                string
		oldCounts, newCounts counts
	}{
		{"units", before.unitStates, after.UnitStates},
		{"ledger", before.ledgerVerdicts, after.LedgerVerdicts},
	}
	for _, group := range groups {
		names := slices.Concat(group.oldCounts.keys, group.newCounts.keys)
		slices.Sort(names)
		for _, name := range slices.Compact(names) {
			oldCount, newCount := group.oldCounts.get(name), group.newCounts.get(name)
			if oldCount != newCount {
				result = append(result, group.label+" "+name+" "+oldCount.String()+"->"+newCount.String())
			}
		}
	}
	if before.frontierGeneration != float64(after.FrontierGeneration) {
		result = append(result, "frontier generation "+formatNumber(before.frontierGeneration)+"->"+strconv.FormatInt(after.FrontierGeneration, 10))
	}
	if !slices.Equal(before.openGateIDs, after.OpenGateIDs) {
		result = append(result, "open gates "+strconv.Itoa(len(before.openGateIDs))+"->"+strconv.Itoa(len(after.OpenGateIDs)))
	}
	if len(result) == 0 {
		return "no derived changes"
	}
	return strings.Join(result, "; ")
}

func markdownTable(headers []string, rows [][]string) string {
	if len(rows) == 0 {
		return "(none)"
	}
	escape := strings.NewReplacer(`\`, `\\`, "|", `\|`)
	lines := []string{
		"| " + strings.Join(headers, " | ") + " |",
		"|" + strings.Repeat(" --- |", len(headers)),
	}
	for _, row := range rows {
		for i, cell := range row {
			row[i] = escape.Replace(cell)
		}
		lines = append(lines, "| "+strings.Join(row, " | ")+" |")
	}
	return strings.Join(lines, "\n")
}

func cellRows[T interface{ cells() []string }](rows []T) [][]string {
	out := make([][]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.cells())
	}
	return out
}

func statusMarkdown(generated time.Time, report statusReport) string {
	lowest := "none"
	if report.Frontier.LowestUnmerged != nil {
		lowest = strconv.FormatInt(*report.Frontier.LowestUnmerged, 10)
	}
	var frontierRows, gateRows [][]string
	for _, row := range report.Frontier.PRs {
		frontierRows = append(frontierRows, []string{row.Branches, strconv.FormatInt(row.PR, 10), row.SHA, row.State})
	}
	for _, g := range report.Gates {
		answer := ""
		if g.resolved() {
			answer = *g.Answer
		}
		gateRows = append(gateRows, []string{g.ID, g.Kind, g.Question, g.Options, g.DefaultAnswer, answer})
	}
	summary := report.Summary
	return "# Orchestrate status\n\nGenerated: " + isoTime(generated) +
		"\n\n## Units\n\nStates: " + countLine(summary.UnitStates) + "\n\n" +
		markdownTable([]string{"ID", "Track", "State", "Branch", "PR", "SHA", "Brief"}, cellRows(report.Units)) +
		"\n\n## Verification ledger\n\nVerdicts: " + countLine(summary.LedgerVerdicts) + "\n\n" +
		markdownTable([]string{"PR", "SHA", "Verdict", "Evidence", "Verifier", "Timestamp"}, cellRows(report.Ledger)) +
		"\n\n## Frontier\n\nGeneration: " + strconv.FormatInt(report.Frontier.Generation, 10) + "\nLowest unmerged: " + lowest + "\n\n" +
		markdownTable([]string{"Branch", "PR", "SHA", "State"}, frontierRows) +
		"\n\n## Gates\n\n" +
		markdownTable([]string{"ID", "Status", "Question", "Options", "Default", "Answer"}, gateRows) +
		"\n\n<!-- orch-summary " + string(marshalJSON(summary, false)) + " -->\n"
}

func openStore(directory string, options storeOptions) (*store, error) {
	dir, err := resolvePath(directory)
	if err != nil {
		return nil, err
	}
	return &store{dir: dir, options: options}, nil
}

func (s *store) ensureOpen() error {
	if s.closed {
		return userErrorf("store is closed")
	}
	return nil
}

func (s *store) ensureLock() error {
	if err := s.ensureOpen(); err != nil || s.release != nil {
		return err
	}
	release, err := acquireLock(s.dir, s.options)
	if err != nil {
		return err
	}
	s.release = release
	return nil
}

func (s *store) beginWrite() error {
	if err := s.ensureOpen(); err != nil {
		return err
	}
	found, err := exists(s.dir)
	if err != nil {
		return err
	}
	if !found {
		return notInitialized(s.dir)
	}
	return s.ensureLock()
}

func (s *store) close() error {
	if s.closed {
		return nil
	}
	release := s.release
	s.release, s.closed = nil, true
	if release != nil {
		return release()
	}
	return nil
}

func (s *store) init() (string, error) {
	if err := s.ensureOpen(); err != nil {
		return "", err
	}
	if err := os.MkdirAll(s.dir, 0o777); err != nil {
		return "", err
	}
	if err := s.ensureLock(); err != nil {
		return "", err
	}
	steps := []func() error{
		func() error { return writeIfMissing(filepath.Join(s.dir, "units.tsv"), unitHeader+"\n") },
		func() error { return writeIfMissing(filepath.Join(s.dir, "ledger.tsv"), ledgerHeader+"\n") },
		func() error { return os.MkdirAll(filepath.Join(s.dir, "inbox"), 0o777) },
		func() error { return writeIfMissing(filepath.Join(s.dir, "gates.md"), "") },
		func() error { return writeIfMissing(filepath.Join(s.dir, "preferences.md"), "") },
		func() error { return writeIfMissing(filepath.Join(s.dir, "frontier.json"), "{}\n") },
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return "", err
		}
	}
	return s.dir, nil
}

func (s *store) addUnit(id, track string, brief *string) (unit, error) {
	if err := s.beginWrite(); err != nil {
		return unit{}, err
	}
	row := unit{State: "pending"}
	var err error
	if row.ID, err = requiredCell(id, "unit id"); err != nil {
		return unit{}, err
	}
	if row.Track, err = requiredCell(track, "track"); err != nil {
		return unit{}, err
	}
	if row.Brief, err = optionalCell(brief, "brief", ""); err != nil {
		return unit{}, err
	}
	rows, err := readUnits(s.dir)
	if err != nil {
		return unit{}, err
	}
	if slices.ContainsFunc(rows, func(u unit) bool { return u.ID == row.ID }) {
		return unit{}, userErrorf("unit %s already exists", row.ID)
	}
	return row, writeTSV(filepath.Join(s.dir, "units.tsv"), unitHeader, append(rows, row))
}

func (s *store) setUnit(id, state string, branch *string, pr *int64, sha *string) (unit, error) {
	if err := s.beginWrite(); err != nil {
		return unit{}, err
	}
	cleanID, err := requiredCell(id, "unit id")
	if err != nil {
		return unit{}, err
	}
	cleanState, err := requiredCell(state, "state")
	if err != nil {
		return unit{}, err
	}
	rows, err := readUnits(s.dir)
	if err != nil {
		return unit{}, err
	}
	index := slices.IndexFunc(rows, func(u unit) bool { return u.ID == cleanID })
	if index < 0 {
		return unit{}, &notFoundError{msg: "unit " + cleanID + " not found"}
	}
	row := rows[index]
	row.State = cleanState
	if row.Branch, err = optionalCell(branch, "branch", row.Branch); err != nil {
		return unit{}, err
	}
	if pr != nil {
		value, err := positiveInteger(*pr, "PR")
		if err != nil {
			return unit{}, err
		}
		row.PR = strconv.FormatInt(value, 10)
	}
	if row.SHA, err = optionalCell(sha, "SHA", row.SHA); err != nil {
		return unit{}, err
	}
	rows[index] = row
	return row, writeTSV(filepath.Join(s.dir, "units.tsv"), unitHeader, rows)
}

func (s *store) getUnit(id string) (unit, error) {
	if err := s.ensureOpen(); err != nil {
		return unit{}, err
	}
	cleanID, err := requiredCell(id, "unit id")
	if err != nil {
		return unit{}, err
	}
	rows, err := readUnits(s.dir)
	if err != nil {
		return unit{}, err
	}
	for _, row := range rows {
		if row.ID == cleanID {
			return row, nil
		}
	}
	return unit{}, &notFoundError{msg: "unit " + cleanID + " not found"}
}

func (s *store) listUnits(state, track *string) ([]unit, error) {
	if err := s.ensureOpen(); err != nil {
		return nil, err
	}
	var err error
	var cleanState, cleanTrack string
	if state != nil {
		if cleanState, err = requiredCell(*state, "state"); err != nil {
			return nil, err
		}
	}
	if track != nil {
		if cleanTrack, err = requiredCell(*track, "track"); err != nil {
			return nil, err
		}
	}
	rows, err := readUnits(s.dir)
	if err != nil {
		return nil, err
	}
	result := []unit{}
	for _, row := range rows {
		if (state == nil || row.State == cleanState) && (track == nil || row.Track == cleanTrack) {
			result = append(result, row)
		}
	}
	return result, nil
}

func (s *store) unitCounts() (counts, error) {
	if err := s.ensureOpen(); err != nil {
		return counts{}, err
	}
	rows, err := readUnits(s.dir)
	if err != nil {
		return counts{}, err
	}
	states := make([]string, 0, len(rows))
	for _, row := range rows {
		states = append(states, row.State)
	}
	return countValues(states), nil
}

func (s *store) recordLedger(pr int64, sha, verdict, evidence string, verifier *string) (ledgerEntry, error) {
	if err := s.beginWrite(); err != nil {
		return ledgerEntry{}, err
	}
	var row ledgerEntry
	var err error
	if row.Verdict, err = parseVerdict(verdict); err != nil {
		return ledgerEntry{}, err
	}
	value, err := positiveInteger(pr, "PR")
	if err != nil {
		return ledgerEntry{}, err
	}
	row.PR = strconv.FormatInt(value, 10)
	if row.SHA, err = requiredCell(sha, "SHA"); err != nil {
		return ledgerEntry{}, err
	}
	if row.Evidence, err = requiredCell(evidence, "evidence"); err != nil {
		return ledgerEntry{}, err
	}
	if row.Verifier, err = optionalCell(verifier, "verifier", ""); err != nil {
		return ledgerEntry{}, err
	}
	row.TS = isoTime(time.Now())
	rows, err := readLedger(s.dir)
	if err != nil {
		return ledgerEntry{}, err
	}
	index := slices.IndexFunc(rows, func(old ledgerEntry) bool { return old.PR == row.PR && old.SHA == row.SHA })
	if index < 0 {
		rows = append(rows, row)
	} else {
		rows[index] = row
	}
	return row, writeTSV(filepath.Join(s.dir, "ledger.tsv"), ledgerHeader, rows)
}

func (s *store) checkLedger(pr int64, sha string) (ledgerEntry, error) {
	if err := s.ensureOpen(); err != nil {
		return ledgerEntry{}, err
	}
	value, err := positiveInteger(pr, "PR")
	if err != nil {
		return ledgerEntry{}, err
	}
	prText := strconv.FormatInt(value, 10)
	cleanSHA, err := requiredCell(sha, "SHA")
	if err != nil {
		return ledgerEntry{}, err
	}
	rows, err := readLedger(s.dir)
	if err != nil {
		return ledgerEntry{}, err
	}
	for _, row := range rows {
		if row.PR == prText && row.SHA == cleanSHA {
			return row, nil
		}
	}
	return ledgerEntry{}, &notFoundError{msg: "NOT-VERIFIED", output: &notFoundOutput{
		compact: "NOT-VERIFIED",
		json: struct {
			PR      string `json:"pr"`
			SHA     string `json:"sha"`
			Verdict string `json:"verdict"`
		}{prText, cleanSHA, "NOT-VERIFIED"},
	}}
}

func (s *store) ledgerSummary() (counts, error) {
	if err := s.ensureOpen(); err != nil {
		return counts{}, err
	}
	rows, err := readLedger(s.dir)
	if err != nil {
		return counts{}, err
	}
	values := make([]string, 0, len(rows))
	for _, row := range rows {
		values = append(values, row.Verdict)
	}
	return countValues(values), nil
}

func (s *store) pushInbox(agent, unitID, status string, report *string) (inboxPushResult, error) {
	if err := s.beginWrite(); err != nil {
		return inboxPushResult{}, err
	}
	pointer := inboxPointer{TS: isoTime(time.Now())}
	var err error
	if pointer.Agent, err = requiredCell(agent, "agent"); err != nil {
		return inboxPushResult{}, err
	}
	if pointer.Unit, err = requiredCell(unitID, "unit"); err != nil {
		return inboxPushResult{}, err
	}
	if pointer.Status, err = requiredCell(status, "status"); err != nil {
		return inboxPushResult{}, err
	}
	if pointer.Report, err = optionalCell(report, "report", ""); err != nil {
		return inboxPushResult{}, err
	}
	inbox := filepath.Join(s.dir, "inbox")
	found, err := exists(inbox)
	if err != nil {
		return inboxPushResult{}, err
	}
	if !found {
		return inboxPushResult{}, notInitialized(s.dir)
	}
	timestamp := strings.NewReplacer(":", "-", ".", "-").Replace(pointer.TS)
	filename := timestamp + "-" + strconv.Itoa(os.Getpid()) + "-" + randomUUID() + ".tsv"
	cells := pointer.cells()
	for i, cell := range cells {
		cells[i] = cleanCell(cell)
	}
	if err := atomicWrite(filepath.Join(inbox, filename), strings.Join(cells, "\t")+"\n"); err != nil {
		return inboxPushResult{}, err
	}
	return inboxPushResult{pointer, filename}, nil
}

// drainInbox swaps the inbox for an empty one so pointers pushed during the
// drain are not lost.
func (s *store) drainInbox() ([]inboxPointer, error) {
	if err := s.beginWrite(); err != nil {
		return nil, err
	}
	inbox := filepath.Join(s.dir, "inbox")
	rows, err := readPointers(inbox)
	if err != nil {
		return nil, err
	}
	drained := filepath.Join(s.dir, ".inbox-drain-"+strconv.Itoa(os.Getpid())+"-"+randomUUID())
	if err := os.Rename(inbox, drained); err != nil {
		return nil, err
	}
	if err := os.Mkdir(inbox, 0o777); err != nil {
		if renameErr := os.Rename(drained, inbox); renameErr != nil {
			return nil, renameErr
		}
		return nil, err
	}
	return rows, os.RemoveAll(drained)
}

func (s *store) peekInbox() ([]inboxPointer, error) {
	if err := s.ensureOpen(); err != nil {
		return nil, err
	}
	return readPointers(filepath.Join(s.dir, "inbox"))
}

func (s *store) inboxCount() (int, error) {
	rows, err := s.peekInbox()
	return len(rows), err
}

func (s *store) parkGate(id, question, options, defaultAnswer string) (gate, error) {
	if err := s.beginWrite(); err != nil {
		return gate{}, err
	}
	g := gate{Kind: "open"}
	var err error
	if g.ID, err = requiredLine(id, "gate id"); err != nil {
		return gate{}, err
	}
	if g.Question, err = requiredLine(question, "question"); err != nil {
		return gate{}, err
	}
	if g.Options, err = requiredLine(options, "options"); err != nil {
		return gate{}, err
	}
	if g.DefaultAnswer, err = requiredLine(defaultAnswer, "default"); err != nil {
		return gate{}, err
	}
	rows, err := readGates(s.dir)
	if err != nil {
		return gate{}, err
	}
	if index := slices.IndexFunc(rows, func(old gate) bool { return old.ID == g.ID }); index < 0 {
		rows = append(rows, g)
	} else {
		rows[index] = g
	}
	return g, atomicWrite(filepath.Join(s.dir, "gates.md"), renderGates(rows))
}

func (s *store) openGates() ([]gate, error) {
	if err := s.ensureOpen(); err != nil {
		return nil, err
	}
	rows, err := readGates(s.dir)
	if err != nil {
		return nil, err
	}
	open := []gate{}
	for _, g := range rows {
		if !g.resolved() {
			open = append(open, g)
		}
	}
	return open, nil
}

func (s *store) resolveGate(id, answer string) (gate, error) {
	if err := s.beginWrite(); err != nil {
		return gate{}, err
	}
	cleanID, err := requiredLine(id, "gate id")
	if err != nil {
		return gate{}, err
	}
	rows, err := readGates(s.dir)
	if err != nil {
		return gate{}, err
	}
	index := slices.IndexFunc(rows, func(g gate) bool { return g.ID == cleanID })
	if index < 0 {
		return gate{}, &notFoundError{msg: "gate " + cleanID + " not found"}
	}
	cleanAnswer, err := requiredLine(answer, "answer")
	if err != nil {
		return gate{}, err
	}
	resolved := rows[index]
	resolved.Kind, resolved.Answer = "resolved", &cleanAnswer
	rows[index] = resolved
	return resolved, atomicWrite(filepath.Join(s.dir, "gates.md"), renderGates(rows))
}

// setFrontier takes a nil forge for the default and a nil prs for no pin.
func (s *store) setFrontier(repo string, forge *string, prs []int64) (frontier, error) {
	if err := s.beginWrite(); err != nil {
		return frontier{}, err
	}
	line, err := requiredLine(repo, "repo directory")
	if err != nil {
		return frontier{}, err
	}
	repoDir, err := resolvePath(line)
	if err != nil {
		return frontier{}, err
	}
	forgeName := "gh"
	if forge != nil {
		forgeName = *forge
	}
	if forgeName != "gh" && forgeName != "origin" {
		return frontier{}, userErrorf("forge must be gh or origin")
	}
	for _, pr := range prs {
		if _, err := positiveInteger(pr, "PR"); err != nil {
			return frontier{}, err
		}
	}
	seen := map[int64]bool{}
	for _, pr := range prs {
		if seen[pr] {
			return frontier{}, userErrorf("--prs must not contain duplicates")
		}
		seen[pr] = true
	}
	old, err := readFrontier(s.dir)
	if err != nil {
		return frontier{}, err
	}
	rows, err := forgeResolver{repo: repoDir, forge: forgeName}.resolve()
	if err != nil {
		return frontier{}, err
	}
	if prs != nil {
		actual := make([]int64, 0, len(rows))
		for _, row := range rows {
			actual = append(actual, row.PR)
		}
		if err := validateFrontierPin(actual, prs); err != nil {
			return frontier{}, err
		}
	}
	value := frontier{Generation: old.Generation + 1, PRs: rows}
	for _, row := range rows {
		if row.State == "OPEN" {
			value.LowestUnmerged = &row.PR
			break
		}
	}
	return value, atomicWrite(filepath.Join(s.dir, "frontier.json"), string(marshalJSON(value, true))+"\n")
}

func (s *store) showFrontier() (frontier, error) {
	if err := s.ensureOpen(); err != nil {
		return frontier{}, err
	}
	return readFrontier(s.dir)
}

func (s *store) showStanding() ([]standingLine, error) {
	if err := s.ensureOpen(); err != nil {
		return nil, err
	}
	return readStanding(s.dir)
}

func (s *store) addStanding(line string) (standingLine, error) {
	if err := s.beginWrite(); err != nil {
		return standingLine{}, err
	}
	rows, err := readStanding(s.dir)
	if err != nil {
		return standingLine{}, err
	}
	cleaned, err := requiredLine(line, "standing order")
	if err != nil {
		return standingLine{}, err
	}
	item := standingLine{Number: len(rows) + 1, Line: cleaned}
	var b strings.Builder
	for _, row := range append(rows, item) {
		b.WriteString(strconv.Itoa(row.Number) + ". " + row.Line + "\n")
	}
	return item, atomicWrite(filepath.Join(s.dir, "preferences.md"), b.String())
}

func (s *store) renderStatus() (statusReport, error) {
	if err := s.beginWrite(); err != nil {
		return statusReport{}, err
	}
	var report statusReport
	var err error
	if report.Units, err = readUnits(s.dir); err != nil {
		return statusReport{}, err
	}
	if report.Ledger, err = readLedger(s.dir); err != nil {
		return statusReport{}, err
	}
	if report.Frontier, err = readFrontier(s.dir); err != nil {
		return statusReport{}, err
	}
	if report.Gates, err = readGates(s.dir); err != nil {
		return statusReport{}, err
	}
	report.Summary = summarize(report.Units, report.Ledger, report.Frontier, report.Gates)
	path := filepath.Join(s.dir, "status.md")
	var before *previousSummary
	found, err := exists(path)
	if err != nil {
		return statusReport{}, err
	}
	if found {
		raw, err := readFileText(path)
		if err != nil {
			return statusReport{}, err
		}
		before = parsePreviousSummary(raw)
	}
	report.Changed = describeChanges(before, report.Summary)
	return report, atomicWrite(path, statusMarkdown(time.Now(), report))
}
