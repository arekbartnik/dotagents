package orch

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
)

const displayLimit = 4

type option struct {
	name     string
	value    string // placeholder such as "<track>"; empty for a boolean flag
	help     string
	required bool
}

func (o option) usage() string { return strings.TrimSpace("--" + o.name + " " + o.value) }

type values map[string]string

func (v values) ptr(name string) *string {
	if value, ok := v[name]; ok {
		return &value
	}
	return nil
}

type command struct {
	name    string
	summary string
	args    []string
	options []option
	subs    []*command
	parent  *command
	run     func(c *cli, args []string, opts values) error
}

type cli struct {
	stdout, stderr io.Writer
	cmd            *command
	store          *string
	json, force    bool
}

// usageError is a command-line mistake; the command's help follows it.
type usageError struct {
	msg string
	cmd *command
}

func (e *usageError) Error() string { return e.msg }

func (c *cli) usagef(format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...), cmd: c.cmd}
}

func Main(args []string, stdout, stderr io.Writer) int {
	args = slices.Clone(args)
	for i, arg := range args {
		args[i] = validUTF8(arg)
	}
	c := &cli{stdout: stdout, stderr: stderr, cmd: program}
	err := c.run(args)
	var notFound *notFoundError
	var usage *usageError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &notFound):
		if notFound.output != nil {
			c.emit(notFound.output.json, notFound.output.compact)
		} else {
			fmt.Fprintf(stderr, "error: %s\n", notFound.msg)
		}
		return 2
	case errors.As(err, &usage):
		fmt.Fprintf(stderr, "error: %s\n\n%s", usage.msg, usage.cmd.help())
	default:
		fmt.Fprintf(stderr, "error: %v\n", err)
	}
	return 1
}

func isOption(arg string) bool { return len(arg) > 1 && arg[0] == '-' }

func isHelp(arg string) bool { return arg == "-h" || arg == "--help" }

var negativeNumber = regexp.MustCompile(`^-(\d+|\d*\.\d+)(e[+-]?\d+)?$`)

// run takes the global options from anywhere before "--", then the command
// path from the leading words, then the command's own arguments.
func (c *cli) run(args []string) error {
	var rest []string
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "--":
			rest = append(rest, args[i:]...)
			i = len(args)
		case arg == "--json":
			c.json = true
		case arg == "--force":
			c.force = true
		case arg == "--store":
			if i+1 == len(args) {
				return c.usagef("option '--store <dir>' argument missing")
			}
			i++
			c.store = &args[i]
		case strings.HasPrefix(arg, "--store="):
			value := strings.TrimPrefix(arg, "--store=")
			c.store = &value
		default:
			rest = append(rest, arg)
		}
	}
	positional := false
	for len(c.cmd.subs) > 0 && len(rest) > 0 {
		if rest[0] == "--" && !positional {
			positional, rest = true, rest[1:]
			continue
		}
		i := slices.IndexFunc(c.cmd.subs, func(sub *command) bool { return sub.name == rest[0] })
		if i < 0 {
			break
		}
		c.cmd, rest = c.cmd.subs[i], rest[1:]
	}
	if len(c.cmd.subs) > 0 {
		return c.runGroup(rest, positional)
	}
	return c.runLeaf(rest, positional)
}

func (c *cli) runGroup(rest []string, positional bool) error {
	switch {
	case !positional && slices.ContainsFunc(rest, isHelp):
		io.WriteString(c.stdout, c.cmd.help())
		return nil
	case len(rest) > 0 && !positional && isOption(rest[0]):
		return c.usagef("unknown option '%s'", rest[0])
	case len(rest) > 0:
		return c.usagef("unknown command '%s'", rest[0])
	}
	if _, err := c.storeDir(); err != nil {
		return err
	}
	return c.usagef("a valid command is required")
}

func (c *cli) runLeaf(tokens []string, positional bool) error {
	cmd := c.cmd
	var args, unknown []string
	opts := values{}
	help := false
	for i := 0; i < len(tokens); i++ {
		token := tokens[i]
		switch {
		case positional || !isOption(token) || negativeNumber.MatchString(token):
			args = append(args, token)
			continue
		case token == "--":
			positional = true
			continue
		case isHelp(token):
			help = true
			continue
		}
		name, value, inline := strings.Cut(token, "=")
		j := slices.IndexFunc(cmd.options, func(o option) bool { return "--"+o.name == name })
		switch {
		case j < 0 || inline && cmd.options[j].value == "":
			unknown = append(unknown, token)
		case cmd.options[j].value == "":
			opts[cmd.options[j].name] = "true"
		case inline:
			opts[cmd.options[j].name] = value
		case i+1 < len(tokens):
			i++
			opts[cmd.options[j].name] = tokens[i]
		default:
			return c.usagef("option '%s' argument missing", cmd.options[j].usage())
		}
	}
	if help {
		io.WriteString(c.stdout, cmd.help())
		return nil
	}
	for _, o := range cmd.options {
		if _, ok := opts[o.name]; o.required && !ok {
			return c.usagef("required option '%s' not specified", o.usage())
		}
	}
	switch {
	case len(unknown) > 0:
		return c.usagef("unknown option '%s'", unknown[0])
	case len(args) < len(cmd.args):
		return c.usagef("missing required argument '%s'", cmd.args[len(args)])
	case len(args) > len(cmd.args):
		return c.usagef("too many arguments for '%s': expected %d, got %d", cmd.name, len(cmd.args), len(args))
	}
	return cmd.run(c, args, opts)
}

func (c *cli) storeDir() (string, error) {
	dir := validUTF8(os.Getenv("ORCH_STORE"))
	if c.store != nil {
		dir = *c.store
	}
	if strings.TrimSpace(dir) == "" {
		return "", c.usagef("set --store <dir> or ORCH_STORE")
	}
	return dir, nil
}

func (c *cli) emit(value any, compact string) {
	text := compact
	if c.json {
		text = string(marshalJSON(value, true))
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	io.WriteString(c.stdout, text)
}

// runStore opens the store, runs op, prints its result, and releases the
// lock even when op fails.
func runStore[T any](c *cli, op func(*store) (T, error), compact func(T) string) error {
	dir, err := c.storeDir()
	if err != nil {
		return err
	}
	s, err := openStore(dir, storeOptions{
		force: c.force,
		onLockStolen: func(holder string) {
			fmt.Fprintf(c.stderr, "stealing store lock held by pid %s\n", holder)
		},
		onStaleLock: func(holder string) {
			fmt.Fprintf(c.stderr, "replacing stale store lock (pid %s is dead)\n", holder)
		},
	})
	if err != nil {
		return err
	}
	result, err := op(s)
	if err == nil {
		c.emit(result, compact(result))
	}
	if closeErr := s.close(); closeErr != nil {
		return closeErr
	}
	return err
}

var positiveIntegerPattern = regexp.MustCompile(`^[1-9][0-9]*$`)

func (c *cli) positiveInteger(what, value string) (int64, error) {
	n, err := strconv.ParseInt(value, 10, 64)
	if !positiveIntegerPattern.MatchString(value) || err != nil || n > maxSafeInteger {
		return 0, c.usagef("invalid %s '%s': must be a positive integer", what, value)
	}
	return n, nil
}

func (c *cli) prList(value string) ([]int64, error) {
	parts := strings.Split(value, ",")
	if slices.Contains(parts, "") {
		return nil, c.usagef("invalid --prs '%s': requires a comma-separated PR list", value)
	}
	prs := make([]int64, 0, len(parts))
	for _, part := range parts {
		pr, err := c.positiveInteger("--prs entry", part)
		if err != nil {
			return nil, err
		}
		prs = append(prs, pr)
	}
	return prs, nil
}

type storeInfo struct {
	Store string `json:"store"`
}

type inboxTally struct {
	Count int `json:"count"`
}

func unitLine(u unit) string { return strings.Join(u.cells(), "\t") }

func pointerLine(p inboxPointer) string { return strings.Join(p.cells(), "\t") }

func gateLine(g gate) string {
	return strings.Join([]string{g.ID, g.Question, g.Options, g.DefaultAnswer}, "\t")
}

func standingText(item standingLine) string { return strconv.Itoa(item.Number) + ". " + item.Line }

// compactRows prints one line per row, at most limit rows when limit > 0.
func compactRows[T any](rows []T, format func(T) string, empty string, limit int) string {
	if len(rows) == 0 {
		return empty
	}
	var lines []string
	for i, row := range rows {
		if limit > 0 && i == limit {
			lines = append(lines, "... "+strconv.Itoa(len(rows)-limit)+" more; use --json")
			break
		}
		lines = append(lines, format(row))
	}
	return strings.Join(lines, "\n")
}

func frontierLine(f frontier) string {
	prs := "none"
	if len(f.PRs) > 0 {
		parts := make([]string, 0, len(f.PRs))
		for _, row := range f.PRs {
			parts = append(parts, row.Branches+"#"+strconv.FormatInt(row.PR, 10)+"@"+row.SHA+":"+row.State)
		}
		prs = strings.Join(parts, ",")
	}
	lowest := "none"
	if f.LowestUnmerged != nil {
		lowest = strconv.FormatInt(*f.LowestUnmerged, 10)
	}
	return "generation=" + strconv.FormatInt(f.Generation, 10) + " prs=" + prs + " lowest-unmerged=" + lowest
}

func statusLines(report statusReport) string {
	ids := report.Summary.OpenGateIDs
	gates := "gates open: " + strconv.Itoa(len(ids))
	if len(ids) > 0 {
		gates += "; ids=" + strings.Join(ids[:min(len(ids), displayLimit)], ",")
		if len(ids) > displayLimit {
			gates += ",+" + strconv.Itoa(len(ids)-displayLimit) + " more"
		}
	}
	return "counts: units=" + strconv.Itoa(len(report.Units)) + "; states=" + countLine(report.Summary.UnitStates) +
		"; ledger=" + countLine(report.Summary.LedgerVerdicts) + "\nchanged: " + report.Changed + "\n" + gates
}

// fullName is the command's path after "orch".
func (cmd *command) fullName() string {
	if cmd.parent == nil || cmd.parent.parent == nil {
		return cmd.name
	}
	return cmd.parent.fullName() + " " + cmd.name
}

func (cmd *command) synopsis() string {
	words := []string{cmd.fullName()}
	for _, arg := range cmd.args {
		words = append(words, "<"+arg+">")
	}
	return strings.Join(words, " ")
}

func (cmd *command) help() string {
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	switch {
	case cmd.parent == nil:
		fmt.Fprintf(w, "Usage: orch [--store <dir>] [--json] [--force] <command>\n\n%s\n\nOptions:\n", cmd.summary)
		fmt.Fprint(w, "  --store <dir>\tstore directory (or ORCH_STORE)\n  --json\tprint complete rows as JSON\n",
			"  --force\tsteal an existing store lock\n  -h, --help\tshow help\n\nCommands:\n")
		for _, sub := range cmd.subs {
			for _, leaf := range append([]*command{sub}, sub.subs...) {
				if leaf.run != nil {
					fmt.Fprintf(w, "  %s\t%s\n", leaf.synopsis(), leaf.summary)
				}
			}
		}
		fmt.Fprint(w, "\nRun 'orch <command> --help' for a command's options.\n")
	case len(cmd.subs) > 0:
		fmt.Fprintf(w, "Usage: orch %s <command>\n\n%s\n\nCommands:\n", cmd.fullName(), cmd.summary)
		for _, sub := range cmd.subs {
			fmt.Fprintf(w, "  %s\t%s\n", sub.synopsis(), sub.summary)
		}
	default:
		fmt.Fprintf(w, "Usage: orch %s", cmd.synopsis())
		if len(cmd.options) > 0 {
			fmt.Fprint(w, " [options]")
		}
		fmt.Fprintf(w, "\n\n%s\n", cmd.summary)
		if len(cmd.options) > 0 {
			fmt.Fprint(w, "\nOptions:\n")
		}
		for _, o := range cmd.options {
			required := ""
			if o.required {
				required = " (required)"
			}
			fmt.Fprintf(w, "  %s\t%s%s\n", o.usage(), o.help, required)
		}
	}
	w.Flush()
	return b.String()
}

func link(cmd *command) *command {
	for _, sub := range cmd.subs {
		sub.parent = cmd
		link(sub)
	}
	return cmd
}

var program = link(&command{
	name:    "orch",
	summary: "Plain-file orchestrate bookkeeping",
	subs: []*command{
		{name: "init", summary: "initialize the store", run: func(c *cli, _ []string, _ values) error {
			return runStore(c, func(s *store) (storeInfo, error) {
				dir, err := s.init()
				return storeInfo{dir}, err
			}, func(r storeInfo) string { return "initialized " + r.Store })
		}},
		{name: "unit", summary: "manage work units", subs: []*command{
			{
				name: "add", args: []string{"id"}, summary: "add a unit",
				options: []option{
					{name: "track", value: "<track>", help: "unit track", required: true},
					{name: "brief", value: "<path>", help: "brief path"},
				},
				run: func(c *cli, args []string, o values) error {
					return runStore(c, func(s *store) (unit, error) {
						return s.addUnit(args[0], o["track"], o.ptr("brief"))
					}, unitLine)
				},
			},
			{
				name: "set", args: []string{"id"}, summary: "update a unit",
				options: []option{
					{name: "state", value: "<state>", help: "unit state", required: true},
					{name: "branch", value: "<branch>", help: "branch name"},
					{name: "pr", value: "<number>", help: "pull request number"},
					{name: "sha", value: "<sha>", help: "commit SHA"},
				},
				run: func(c *cli, args []string, o values) error {
					var pr *int64
					if value, ok := o["pr"]; ok {
						n, err := c.positiveInteger("--pr", value)
						if err != nil {
							return err
						}
						pr = &n
					}
					return runStore(c, func(s *store) (unit, error) {
						return s.setUnit(args[0], o["state"], o.ptr("branch"), pr, o.ptr("sha"))
					}, unitLine)
				},
			},
			{
				name: "get", args: []string{"id"}, summary: "get a unit",
				run: func(c *cli, args []string, _ values) error {
					return runStore(c, func(s *store) (unit, error) { return s.getUnit(args[0]) }, unitLine)
				},
			},
			{
				name: "list", summary: "list units",
				options: []option{
					{name: "state", value: "<state>", help: "filter by state"},
					{name: "track", value: "<track>", help: "filter by track"},
				},
				run: func(c *cli, _ []string, o values) error {
					return runStore(c, func(s *store) ([]unit, error) {
						return s.listUnits(o.ptr("state"), o.ptr("track"))
					}, func(rows []unit) string { return compactRows(rows, unitLine, "(no units)", displayLimit) })
				},
			},
			{
				name: "counts", summary: "count units by state",
				run: func(c *cli, _ []string, _ values) error {
					return runStore(c, (*store).unitCounts, countLine)
				},
			},
		}},
		{name: "ledger", summary: "manage verification records", subs: []*command{
			{
				name: "record", args: []string{"pr", "sha", "verdict"}, summary: "record a verification verdict",
				options: []option{
					{name: "evidence", value: "<path>", help: "evidence path", required: true},
					{name: "verifier", value: "<name>", help: "verifier name"},
				},
				run: func(c *cli, args []string, o values) error {
					pr, err := c.positiveInteger("<pr>", args[0])
					if err != nil {
						return err
					}
					verdict, err := parseVerdict(args[2])
					if err != nil {
						return err
					}
					return runStore(c, func(s *store) (ledgerEntry, error) {
						return s.recordLedger(pr, args[1], verdict, o["evidence"], o.ptr("verifier"))
					}, func(row ledgerEntry) string { return row.PR + "\t" + row.SHA + "\t" + row.Verdict })
				},
			},
			{
				name: "check", args: []string{"pr", "sha"}, summary: "check a verification verdict",
				run: func(c *cli, args []string, _ values) error {
					pr, err := c.positiveInteger("<pr>", args[0])
					if err != nil {
						return err
					}
					return runStore(c, func(s *store) (ledgerEntry, error) {
						return s.checkLedger(pr, args[1])
					}, func(row ledgerEntry) string { return row.Verdict })
				},
			},
			{
				name: "summary", summary: "count verification verdicts",
				run: func(c *cli, _ []string, _ values) error {
					return runStore(c, (*store).ledgerSummary, countLine)
				},
			},
		}},
		{name: "inbox", summary: "manage agent pointers", subs: []*command{
			{
				name: "push", args: []string{"agent", "unit", "status"}, summary: "push an inbox pointer",
				options: []option{{name: "report", value: "<path>", help: "report path"}},
				run: func(c *cli, args []string, o values) error {
					return runStore(c, func(s *store) (inboxPushResult, error) {
						return s.pushInbox(args[0], args[1], args[2], o.ptr("report"))
					}, func(r inboxPushResult) string { return r.Unit + "\t" + r.Status + "\t" + r.filename })
				},
			},
			{
				name: "drain", summary: "drain inbox pointers",
				options: []option{{name: "peek", help: "read without draining"}},
				run: func(c *cli, _ []string, o values) error {
					drain := (*store).drainInbox
					if _, peek := o["peek"]; peek {
						drain = (*store).peekInbox
					}
					return runStore(c, drain, func(rows []inboxPointer) string {
						return compactRows(rows, pointerLine, "(empty)", 0)
					})
				},
			},
			{
				name: "count", summary: "count inbox pointers",
				run: func(c *cli, _ []string, _ values) error {
					return runStore(c, func(s *store) (inboxTally, error) {
						n, err := s.inboxCount()
						return inboxTally{n}, err
					}, func(t inboxTally) string { return strconv.Itoa(t.Count) })
				},
			},
		}},
		{name: "gate", summary: "manage decision gates", subs: []*command{
			{
				name: "park", args: []string{"id"}, summary: "park a decision gate",
				options: []option{
					{name: "question", value: "<question>", help: "gate question", required: true},
					{name: "options", value: "<options>", help: "gate options", required: true},
					{name: "default", value: "<answer>", help: "default answer", required: true},
				},
				run: func(c *cli, args []string, o values) error {
					return runStore(c, func(s *store) (gate, error) {
						return s.parkGate(args[0], o["question"], o["options"], o["default"])
					}, func(g gate) string { return g.ID + "\topen" })
				},
			},
			{
				name: "list", summary: "list open decision gates",
				run: func(c *cli, _ []string, _ values) error {
					return runStore(c, (*store).openGates, func(rows []gate) string {
						return compactRows(rows, gateLine, "(no open gates)", displayLimit)
					})
				},
			},
			{
				name: "resolve", args: []string{"id"}, summary: "resolve a decision gate",
				options: []option{{name: "answer", value: "<answer>", help: "chosen answer", required: true}},
				run: func(c *cli, args []string, o values) error {
					return runStore(c, func(s *store) (gate, error) {
						return s.resolveGate(args[0], o["answer"])
					}, func(g gate) string { return g.ID + "\tresolved\t" + *g.Answer })
				},
			},
		}},
		{name: "frontier", summary: "manage the stack frontier", subs: []*command{
			{
				name: "set", summary: "resolve the pull request base chain and set the frontier",
				options: []option{
					{name: "repo", value: "<dir>", help: "repository directory (or ORCH_REPO)"},
					{name: "forge", value: "<forge>", help: "pull request forge: gh (default) or origin"},
					{name: "prs", value: "<n,...>", help: "optional expected pull request order pin"},
				},
				run: func(c *cli, _ []string, o values) error {
					forge := "gh"
					if value, ok := o["forge"]; ok {
						forge = value
					}
					if forge != "gh" && forge != "origin" {
						return c.usagef("invalid --forge '%s': allowed choices are gh, origin", forge)
					}
					var prs []int64
					if value, ok := o["prs"]; ok {
						var err error
						if prs, err = c.prList(value); err != nil {
							return err
						}
					}
					repo := validUTF8(os.Getenv("ORCH_REPO"))
					if value, ok := o["repo"]; ok {
						repo = value
					}
					return runStore(c, func(s *store) (frontier, error) {
						if strings.TrimSpace(repo) == "" {
							return frontier{}, c.usagef("set --repo <dir> or ORCH_REPO")
						}
						return s.setFrontier(repo, &forge, prs)
					}, frontierLine)
				},
			},
			{
				name: "show", summary: "show the frontier",
				run: func(c *cli, _ []string, _ values) error {
					return runStore(c, (*store).showFrontier, frontierLine)
				},
			},
		}},
		{name: "status", summary: "render status.md and print a summary", run: func(c *cli, _ []string, _ values) error {
			return runStore(c, (*store).renderStatus, statusLines)
		}},
		{name: "standing", summary: "manage standing orders", subs: []*command{
			{
				name: "show", summary: "show standing orders",
				run: func(c *cli, _ []string, _ values) error {
					return runStore(c, (*store).showStanding, func(rows []standingLine) string {
						return compactRows(rows, standingText, "(no standing orders)", displayLimit)
					})
				},
			},
			{
				name: "add", args: []string{"line"}, summary: "add a standing order",
				run: func(c *cli, args []string, _ values) error {
					return runStore(c, func(s *store) (standingLine, error) { return s.addStanding(args[0]) }, standingText)
				},
			},
		}},
	},
})
