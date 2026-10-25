package watchpr

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

type cliOptions struct {
	owner      *string
	repo       *string
	pr         *PrNumber
	mode       string
	stackPrs   []PrNumber
	statusOnly bool
	pretty     bool
	polling    PollingOptions
}

const helpText = `Usage: watch-pr [options]

Watch one pull request, a connected stack, or an immutable queued stack.
JSON (NDJSON while polling) is the default; --pretty renders human text.

Options:
  --owner <owner>             GitHub repository owner
  --repo <repo>               GitHub repository name
  --pr <number>               pull request number
  --stack                     watch the connected open stack
  --queued-stack              watch the captured stack until all PRs merge
  --stack-prs <n,...>         frozen bottom-to-top queue (queued mode only)
  --interval <seconds>        poll interval (default: 60)
  --sweep-interval <seconds>  whole-stack sweep interval (default: 300)
  --timeout <seconds>         deadline; 0 disables it (default: 0)
  --max-query-errors <count>  consecutive query-error budget (default: 5)
  --status-only               print one status table and exit 0
  --allow-draft               do not treat a draft as a merge gate
  --pretty                    render human text instead of JSON
  -h, --help                  show this help
`

// usageError makes the CLI exit 64 with its message on stderr. An empty
// message is a request for help, which exits 0.
type usageError struct{ message string }

func (e *usageError) Error() string { return e.message }

func usagef(format string, args ...any) error {
	return &usageError{"error: " + fmt.Sprintf(format, args...)}
}

func positiveNumber(value string) (float64, error) {
	f, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) || f <= 0 {
		return 0, errors.New("must be greater than zero")
	}
	return f, nil
}

func nonNegativeNumber(value string) (float64, error) {
	f, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) || f < 0 {
		return 0, errors.New("must be zero or greater")
	}
	return f, nil
}

func positiveInteger(value string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || n <= 0 {
		return 0, errors.New("must be a positive integer")
	}
	return n, nil
}

func prNumberArg(value string) (PrNumber, error) {
	number, err := parsePrNumber(strings.TrimPrefix(value, "#"), "PR number")
	if err != nil {
		return 0, errors.New("must be a positive integer")
	}
	return number, nil
}

func stackPrList(value string) ([]PrNumber, error) {
	var numbers []PrNumber
	for _, part := range strings.Split(value, ",") {
		number, err := prNumberArg(strings.TrimSpace(part))
		if err != nil {
			return nil, err
		}
		numbers = append(numbers, number)
	}
	seen := map[PrNumber]bool{}
	for _, number := range numbers {
		if seen[number] {
			return nil, errors.New("contains a duplicate PR")
		}
		seen[number] = true
	}
	return numbers, nil
}

var valueFlags = map[string]func(o *cliOptions, value string) error{
	"owner": func(o *cliOptions, v string) error { o.owner = &v; return nil },
	"repo":  func(o *cliOptions, v string) error { o.repo = &v; return nil },
	"pr": func(o *cliOptions, v string) error {
		number, err := prNumberArg(v)
		o.pr = &number
		return err
	},
	"stack-prs": func(o *cliOptions, v string) (err error) {
		o.stackPrs, err = stackPrList(v)
		return err
	},
	"interval": func(o *cliOptions, v string) (err error) {
		o.polling.Interval, err = positiveNumber(v)
		return err
	},
	"sweep-interval": func(o *cliOptions, v string) (err error) {
		o.polling.SweepInterval, err = positiveNumber(v)
		return err
	},
	"timeout": func(o *cliOptions, v string) (err error) {
		o.polling.Timeout, err = nonNegativeNumber(v)
		return err
	},
	"max-query-errors": func(o *cliOptions, v string) (err error) {
		o.polling.MaxQueryErrors, err = positiveInteger(v)
		return err
	},
}

var boolFlags = map[string]bool{"stack": true, "queued-stack": true, "status-only": true, "allow-draft": true, "pretty": true}

// parseArgs fails at once on a bad option value. Help outranks every other
// mistake, which is reported after the whole command line is read.
func parseArgs(argv []string) (cliOptions, error) {
	options := cliOptions{polling: PollingOptions{Interval: 60, SweepInterval: 300, Timeout: 0, MaxQueryErrors: 5}}
	set := map[string]bool{}
	help := false
	var mistake error
	report := func(err error) {
		if mistake == nil {
			mistake = err
		}
	}
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		switch {
		case arg == "-h" || arg == "--help":
			help = true
			continue
		case arg == "--":
			if i+1 < len(argv) {
				report(usagef("unexpected argument %q", argv[i+1]))
			}
			i = len(argv)
			continue
		case !strings.HasPrefix(arg, "-"):
			report(usagef("unexpected argument %q", arg))
			continue
		case !strings.HasPrefix(arg, "--"):
			report(usagef("unknown option %s", arg))
			continue
		}
		name, value, hasValue := strings.Cut(arg[2:], "=")
		apply, takesValue := valueFlags[name]
		switch {
		case takesValue:
			if !hasValue {
				if i+1 >= len(argv) {
					return cliOptions{}, usagef("--%s needs a value", name)
				}
				i++
				value = argv[i]
			}
			if err := apply(&options, value); err != nil {
				return cliOptions{}, usagef("invalid --%s %q: %v", name, value, err)
			}
		case boolFlags[name] && hasValue:
			report(usagef("--%s takes no value", name))
		case boolFlags[name]:
			set[name] = true
		default:
			report(usagef("unknown option %s", arg))
		}
	}
	switch {
	case help:
		return cliOptions{}, &usageError{}
	case mistake != nil:
		return cliOptions{}, mistake
	case set["stack"] && set["queued-stack"]:
		return cliOptions{}, usagef("--stack cannot be used with --queued-stack")
	case options.stackPrs != nil && !set["queued-stack"]:
		return cliOptions{}, usagef("--stack-prs requires --queued-stack")
	}
	options.mode = "single"
	if set["queued-stack"] {
		options.mode = "queued-stack"
	} else if set["stack"] {
		options.mode = "stack"
	}
	options.statusOnly = set["status-only"]
	options.pretty = set["pretty"]
	options.polling.AllowDraft = set["allow-draft"]
	return options, nil
}
