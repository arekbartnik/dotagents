# Harness reference

poteto-mode and its bundled skills use harness-neutral terms. This file maps each term to the concrete tool, field, or path in each harness. Read it once per session, before the first spawn.

The bundled tools run as `scripts/poteto <command>` in every harness. The wrapper needs Go and builds the binary on first use.

Tell your harness from your tool list. A `Task` spawn tool means Cursor. An `Agent` spawn tool means Claude Code. For any other harness, use the closest equivalent of each row. When a harness has no equivalent, take the row's fallback and say so in one line in the reply. Never pass a field or value the tool schema rejects.

## Tools

| Term | Cursor | Claude Code | Fallback |
| --- | --- | --- | --- |
| Spawn a subagent | `Task` | `Agent` | Do the work inline and say so. |
| Ask a structured question | `AskQuestion` | `AskUserQuestion` | Ask in plain text with numbered options. |
| Task list | The todolist tool | The todo or task tool, when the session has one | Keep the list in the reply and update it at each phase. |
| Recurring wake | `/loop <interval>` | `/loop <interval>`, or `ScheduleWakeup` in a self-paced loop | A watcher subagent that sleeps and reports. |
| Event wake | A watcher subagent | `Monitor`, or a background `Bash` command that exits on the event | Poll on a long heartbeat. |
| Run a bundled skill | `/<name>`, or read its `SKILL.md` | `Skill`, or read its `SKILL.md` when `Skill` refuses it | Read its `SKILL.md` and follow it. Bundled skills sit beside poteto-mode, at `../<name>/SKILL.md` from poteto-mode's `SKILL.md`. |
| Author a skill | The built-in `create-skill` skill | The `skill-creator` skill | Follow `playbooks/authoring-a-skill.md` without it. |
| Built-in babysit skill | Exists. Never route to it, per `playbooks/babysit.md`. | None | None |

## Subagent fields

| Term | Cursor | Claude Code |
| --- | --- | --- |
| Type `poteto-agent` | `subagent_type: "poteto-agent"` | `subagent_type: "poteto-agent"` |
| General-purpose type | `subagent_type: generalPurpose` | `subagent_type: "general-purpose"` |
| Read-only | `readonly: true`. It strips MCP. | `subagent_type: "Explore"`. It keeps `Bash`, so the brief also says "do not edit files", and you check `git status` afterward. |
| Full access, MCP included | `readonly: false` | `general-purpose` or a custom type. Both keep the session's MCP servers. |
| Background | `run_in_background: true` | Default. Subagents run in the background and notify you when they finish. |
| Cloud worker | `environment: "cloud"`, plus `cloud_base_branch` for a non-default base | `isolation: "remote"` where the session offers it, otherwise `isolation: "worktree"`. Name the branch or commit the worker checks out. |
| Local worker | `environment: "local"` | Omit `isolation`. |
| Resume | `resume` | `SendMessage` to the agent. Prefer a fresh agent per the Subagents section. |
| Attach files | `file_attachments` | List the paths in the brief. |

A cloud worker has its own checkout and cannot read this machine's transcripts, simulators, IDE state, or local auth. When the harness has no cloud workers, a local subagent in its own git worktree is the closest equivalent.

## Models

The Subagents section of `SKILL.md` names two tiers. Each harness fills them from the models it offers. These are examples, not requirements. If the user's always-on instructions name a model for a tier, use that model.

| Tier | Cursor | Claude Code |
| --- | --- | --- |
| Strongest model | `claude-opus-5-5-xhigh` | `model: "opus"`, `effort: "xhigh"` |
| Fast code model | `grok-4.7-xhigh-fast` | `model: "sonnet"` |
| Inherit the parent | Omit `model` | Omit `model` |

Effort and speed:

- Cursor encodes effort and speed in the slug, as in `<family>-<effort>[-fast]`. Change only those tokens and keep the family.
- Claude Code takes effort as the `effort` field (`low`, `medium`, `high`, `xhigh`, `max`). It has no per-subagent fast mode, so drop any speed choice.

For a second model family, such as a cross-judge, an interrogate reviewer, or a verifier on a family other than the worker's, run another provider's CLI per `../../interrogate/references/external-reviewers.md`. A spawn tool that offers one provider cannot give model diversity alone.

## Paths

| Term | Cursor | Claude Code |
| --- | --- | --- |
| Project skills | `.cursor/skills/` | `.claude/skills/` |
| User skills | `~/.cursor/skills/` | `~/.claude/skills/` |
| User agents | `~/.cursor/agents/` | `~/.claude/agents/` |
| Plugin cache, never edited | `~/.cursor/plugins/` | `~/.claude/plugins/` |
| Always-on instructions | `.cursor/rules/*.mdc`, `AGENTS.md` | `CLAUDE.md`, or `AGENTS.md` when no `CLAUDE.md` exists |
| Transcript store | `~/.cursor/projects/<slug>/agent-transcripts/`. The slug is the workspace path with the leading `/` dropped and each `/` changed to `-`. | `~/.claude/projects/<slug>/<session>.jsonl`. The slug is the workspace path with each `/` and `.` changed to `-`. The newest `.jsonl` is the current session, and `<session>/subagents/` holds its subagent transcripts. |

Read only the active workspace's transcript folder. Never glob across every project folder in the store. That crosses workspace boundaries and reads private chats from unrelated projects. `scripts/poteto worktree-audit` checks both stores, and `--transcripts <dir>` overrides them.

## Harness-only features

Some steps name a product that only one harness has. When the current harness lacks it, put one line in the reply, `Skipped in <harness>: <step>. <reason>.`, and go on to the next step. Do not stop, ask, or retry.

| Feature | Where it exists | Elsewhere |
| --- | --- | --- |
| Bugbot | A GitHub app. Its comments are ordinary review threads in every harness. | Triage per `bugbot-triage.md`. Skip a step that starts, waits for, or configures Bugbot when the PR has no Bugbot check or comment. |
| Cloud agent dashboard | Cursor | Probe state through the ledger, `gh`, and pushed branches. |
| Automations, routines, webhook wakes | Cursor | Skip with the line. |
| Built-in PR tool | Cursor | Use the forge command the step gives, such as `gh pr create`. That is not a skip, so write no line. |
