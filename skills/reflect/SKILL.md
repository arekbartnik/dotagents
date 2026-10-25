---
name: reflect
description: Spawn three parallel review subagents over the active transcript, surface learnings, and route each to a concrete edit on an existing skill. Use when the user says reflect.
disable-model-invocation: true
---

# Reflect

Mine the current conversation for durable learnings, then route them into skill edits.

Invoke when the user says "reflect" or "/reflect". Skip when the conversation is trivial, off-topic, or already covered by an existing skill the parent followed correctly. One-offs are not learnings.

## 1. Locate the active transcript

Before fanning out, find this conversation's transcript in the active workspace's folder of the transcript store. `../poteto-mode/references/harness.md` gives the folder per harness. Never glob across every project folder in the store. That crosses workspace boundaries and reads private chats from unrelated projects.

```bash
ls -t <transcripts>/*.jsonl <transcripts>/*/*.jsonl <transcripts>/*/subagents/*.jsonl 2>/dev/null | head -10
```

That covers the flat (`<id>.jsonl`), nested (`<id>/<id>.jsonl`), and subagent (`<parent>/subagents/<child>.jsonl`) layouts. For each candidate, find the first user message. Take the file whose first user message contains the conversation's opening user prompt. If none matches, pass a tight digest of the session instead.

## 2. Spawn three reviewers in parallel

One message, three general-purpose subagents with full access, not read-only. Reviewers need MCP access to look up the tickets, chat threads, and observability traces the transcript references, and a read-only mode can strip MCP.

Each reviewer and the synthesizer run on a model tier from the Subagents section of the **poteto-mode** skill. If the harness offers no model choice, all of them run on the parent's model. If the harness rejects a model, use the parent's model and say so. Pick each subagent's reasoning level and fast or standard speed for the job, per that same section.

| Lens      | Model tier      | Prompt template                    |
| --------- | --------------- | ---------------------------------- |
| Judgment  | Strongest model | `references/judgment-reviewer.md`  |
| Tooling   | Fast code model | `references/tooling-reviewer.md`   |
| Divergent | Strongest model | `references/divergent-reviewer.md` |

Pass each template verbatim, substituting the transcript path or digest where marked. Reviewers return findings in their final reply.

## 3. Synthesize

One general-purpose subagent on the strongest model, with full access, not read-only. It spot-verifies citations through MCP, and a read-only mode can strip MCP. Pass `references/synthesizer.md` verbatim, with each reviewer's full output inlined where marked. It returns an Accepted / Rejected / Backlog list.

## 4. Structural enforcement check

Move any Accepted item that a lint rule, script, metadata flag, or runtime check would enforce more reliably to Backlog. See the **encode-lessons-in-structure** principle skill.

## 5. Apply

Present the synthesizer's full Accepted / Rejected / Backlog output and wait for explicit approval before applying any Accepted edit. The user picks the subset and may redirect routings. Skill changes affect every future agent in the org. Do not auto-apply.

File each Backlog item to your team's devex or backlog tracker without waiting. Only the Accepted list waits for approval.

Never edit a skill in the harness's plugin cache. Before approval, show its destination, the harness's project or user skill folder, such as `.claude/skills/<skill>/` or `~/.cursor/skills/<skill>/` (`../poteto-mode/references/harness.md`). After approval, copy the skill folder there and edit the copy. Never open a PR or push.

Follow each approved row's Routing exactly:

- Trivial existing-skill edit (a one-line bullet, a tightened sentence, a stale fact corrected): the parent does it directly.
- Substantive existing-skill edit (a new section, a new pattern table, more than ~10 lines): hand to the harness's skill-authoring skill (`create-skill` in Cursor, `skill-creator` in Claude Code) and run its draft / test / iterate loop.
- `tune description: <skill path>` (the skill exists but didn't trigger when it should have): hand to the skill-authoring skill and run its description-optimization loop.
- `new skill via create-skill: <kebab-name>`: hand creation to the skill-authoring skill. Do not invent the shape ad hoc.

If your environment ships a SKILL.md validator, run it on every touched skill before declaring done.

## 6. Summarize for the user

Short list, no preamble:

- Edits applied: `<skill path>`. What changed, one line each.
- New skills created: `<skill path>`. One line each (rare).
- Backlog filed to the devex tracker: `<issue title>` (`<tags>`). One line each.
- Dropped: one line per rejected finding + reason from the synthesizer.
