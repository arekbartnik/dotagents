# External reviewers

How to run a reviewer, judge, or trail reviewer on another provider's model through that provider's CLI. The **interrogate**, **arena**, and **show-me-your-work** skills all use this.

## Which CLI to use

Supported provider CLIs: `claude`, `codex`.

1. Skip the CLI for the provider the parent already runs on. A native subagent covers that family.
2. Check each remaining CLI is installed with `command -v <cli>`.
3. Run one external reviewer per available CLI.
4. If none is available, use the fallback order in the calling skill.

Don't pass a model flag. Each CLI uses its own configured default, so this file names no models.

## Before running

- **Tell the user the code leaves for another provider** the first time in a run. If the user has said the code must stay with one provider, skip external reviewers.
- **Write the filled prompt to a file.** Diffs break shell quoting. Pass the prompt on stdin.
- **Write outputs outside the repo**, in a temp or scratch directory, one file per reviewer.

## Commands

Run from the repo root. Both run non-interactively and read-only.

```bash
# codex
codex exec --sandbox read-only --ephemeral -C "$PWD" -o <out-file> - < <prompt-file>

# claude
claude -p --tools "Read,Grep,Glob" --no-session-persistence < <prompt-file> > <out-file>
```

Flags change between CLI versions. If a command fails on an unknown flag, check `<cli> --help`, keep the run non-interactive and read-only, and fix this file.

## Running

- Launch external reviewers in the background, in the same message as the native subagents, so all run concurrently.
- Set a timeout of 10 minutes per external reviewer.
- After each run, check `git status`. An external reviewer that changed the working tree broke the read-only rule. Discard its changes and its findings, and report it.

## Failures

A missing CLI, an auth failure, a timeout, or an empty output file drops that reviewer. Don't retry more than once. Don't invent findings. Replace the dropped reviewer with a native subagent and name the failure in the final output.

## Reporting

Label each external reviewer by its CLI, such as `codex CLI` or `claude CLI`. Add the model name only when the CLI's output states it.
