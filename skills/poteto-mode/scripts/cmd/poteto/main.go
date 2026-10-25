package main

import (
	"fmt"
	"io"
	"os"

	"poteto/internal/checkplan"
	"poteto/internal/harnesslint"
	"poteto/internal/orch"
	"poteto/internal/watchpr"
	"poteto/internal/worktreeaudit"
)

type command struct {
	name    string
	summary string
	run     func(args []string, stdout, stderr io.Writer) int
}

var commands = []command{
	{"watch-pr", "watch a GitHub PR or stack until it is merge-ready", watchpr.Main},
	{"orch", "plain-file orchestrate bookkeeping", orch.Main},
	{"check-plan", "check a multi-phase plan's structure", checkplan.Main},
	{"check-harness", "flag harness-specific names in skill markdown", harnesslint.Main},
	{"worktree-audit", "classify git worktrees for pruning", worktreeaudit.Main},
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "Usage: poteto <command> [args]")
	fmt.Fprintln(w)
	for _, c := range commands {
		fmt.Fprintf(w, "  %-15s %s\n", c.name, c.summary)
	}
}

func main() {
	if len(os.Args) < 2 || os.Args[1] == "-h" || os.Args[1] == "--help" {
		usage(os.Stdout)
		if len(os.Args) < 2 {
			os.Exit(2)
		}
		return
	}
	for _, c := range commands {
		if c.name == os.Args[1] {
			os.Exit(c.run(os.Args[2:], os.Stdout, os.Stderr))
		}
	}
	fmt.Fprintf(os.Stderr, "poteto: unknown command %q\n\n", os.Args[1])
	usage(os.Stderr)
	os.Exit(2)
}
