// Package watchpr watches one GitHub pull request, a connected stack, or a
// queued stack until it is merge-ready, reading GitHub through the gh CLI.
package watchpr

import (
	"errors"
	"fmt"
	"io"
	"time"
)

type runtime struct {
	reader Reader
	clock  Clock
	stdout io.Writer
	stderr io.Writer
}

type realClock struct{ started time.Time }

func (c realClock) Now() float64 { return time.Since(c.started).Seconds() }

func (realClock) ObservedAt() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

func (realClock) Sleep(seconds float64) error {
	time.Sleep(time.Duration(seconds * float64(time.Second)))
	return nil
}

func Main(args []string, stdout, stderr io.Writer) int {
	// A leading "--" only separates these arguments from the caller's.
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	return run(args, runtime{
		reader: GhReader{Run: execRunner},
		clock:  realClock{started: time.Now()},
		stdout: stdout,
		stderr: stderr,
	})
}

// run exits 64 on a usage error and 1 on an error that is neither a usage
// error nor a query failure.
func run(argv []string, rt runtime) int {
	code, err := runWatch(argv, rt)
	if err != nil {
		fmt.Fprintf(rt.stderr, "error: %s\n", err)
		return 1
	}
	return code
}

func runWatch(argv []string, rt runtime) (int, error) {
	options, err := parseArgs(argv)
	if err != nil {
		var usage *usageError
		if !errors.As(err, &usage) {
			return 0, err
		}
		if usage.message == "" {
			io.WriteString(rt.stdout, helpText)
			return 0, nil
		}
		io.WriteString(rt.stderr, usage.message+"\n")
		return 64, nil
	}
	render := renderJSON
	if options.pretty {
		render = renderPretty
	}
	emit := func(v Verdict) { io.WriteString(rt.stdout, render(v)) }
	contexts, err := watchContexts(rt.reader, options)
	if err != nil {
		var queryErr *QueryError
		if !errors.As(err, &queryErr) {
			return 0, err
		}
		verdict := statusQueryVerdict(newStamp(rt.clock, options.mode), 1, queryErr.Failure)
		emit(verdict)
		return verdict.ExitCode, nil
	}
	deps := dependencies{reader: rt.reader, clock: rt.clock, emit: emit}
	var verdict Verdict
	if options.mode == "queued-stack" && !options.statusOnly {
		verdict, err = runQueued(deps, contexts, options.polling)
	} else {
		verdict, err = runSimple(deps, contexts, options.mode, options.statusOnly, options.polling)
	}
	if err != nil {
		return 0, err
	}
	emit(verdict)
	return verdict.ExitCode, nil
}

func watchContexts(reader Reader, options cliOptions) ([]PrContext, error) {
	pr := options.pr
	if pr == nil && len(options.stackPrs) > 0 {
		pr = &options.stackPrs[0]
	}
	seed, err := resolveContext(reader, options.owner, options.repo, pr)
	if err != nil {
		return nil, err
	}
	if len(options.stackPrs) > 0 {
		contexts := make([]PrContext, len(options.stackPrs))
		for i, number := range options.stackPrs {
			contexts[i] = PrContext{Owner: seed.Owner, Repo: seed.Repo, Number: number}
		}
		return contexts, nil
	}
	if options.mode == "single" {
		return []PrContext{seed}, nil
	}
	return discoverStack(reader, seed)
}
