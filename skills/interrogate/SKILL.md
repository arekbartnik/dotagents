---
name: interrogate
description: 'Use for "interrogate", "adversarial review", "multi-model review", "challenge this", "stress test this code", "find blind spots", or "tear this apart". Multiple LLM reviewers challenge changes from independent angles.'
disable-model-invocation: true
---

# Interrogate

Spawn one reviewer per configured model to adversarially review code changes. The adversarial signal comes from model diversity, not assigned personas. The deliverable is a synthesized verdict. Do NOT auto-apply changes.

## Step 1, Scope

Identify what to review from context:

- If the user points at specific files or a diff, use that.
- If on a feature branch, run `git diff main...HEAD` (or the appropriate base branch) for the full changeset.
- If the user's message references recent work, gather the relevant files.

Package the diff or file contents with any surrounding context files the reviewers need to understand the code.

## Step 2, Intent

Before spawning reviewers, state the intent in one clear paragraph from the user's message, commit messages, the PR description if one exists, and the code. If you're unsure about the intent, ask the user before proceeding.

## Step 3, Spawn Reviewers

Launch all reviewers in a single message so they run concurrently. Default to three, labeled Reviewer A, B, and C. Use more or fewer when the user asks.

Reviewers from different providers disagree in more useful ways than reviewers on one model. Get that diversity from provider CLIs first:

1. **External reviewers.** Run one reviewer through each other provider's CLI that is available (`claude`, `codex`), per `references/external-reviewers.md`.
2. **Native subagents.** Fill the remaining slots with general-purpose subagents. Pick from the most capable models the harness offers, and prefer a model no other reviewer is on.
3. **No diversity available.** With no other provider's CLI and no model choice, run every reviewer on the parent's model. Independent contexts still produce independent findings. Say in the verdict that the reviewers shared one model.

Every reviewer has read-only access. Reviewers never edit files.

If a reviewer fails or the harness rejects a chosen model, replace it and say so in the verdict. Do not block the review on it.

Read `references/reviewer-prompt.md` and fill in the same template for every reviewer with the stated intent, the diff or file contents, the rubric from `references/rubric.md`, and the code-quality lens from `references/code-quality-review.md`. Pick each subagent's reasoning level and fast or standard speed for the job, per the Subagents section of the **poteto-mode** skill.

## Step 4, Synthesize

As results come back, build a unified picture:

1. **Parse all findings** from the reviewers
2. **Identify consensus**. Findings raised by 2+ reviewers independently are highest signal.
3. **Identify lone-reviewer findings**. Still worth reading, but weight accordingly.
4. **Deduplicate**. Different reviewers may describe the same issue differently. Merge these and note which reviewers raised it.
5. **Note disagreements**. If one reviewer flags something and another explicitly says the opposite, that's useful context for the verdict.

## Step 5, Lead Judgment

You are the lead reviewer, a pragmatic senior engineer, not a neutral aggregator. Read `references/lead-judgment.md` for the full framework. Put every finding in one of four buckets, with the model(s) that raised it and a one-line rationale.

- **Act on**. Real issues affecting correctness, security, or maintainability given the actual goals. They would block a real PR.
- **Consider**. Legitimate, but you're not sure they outweigh the cost of addressing them now. Worth the user's attention.
- **Noted**. Technically valid but not actionable. Context-dependent, premature optimization, or low-impact at this stage.
- **Dismissed**. Wrong, nitpicky, or missing context, with a brief reason.

## Output Format

### Intent

> [The stated intent paragraph from Step 2]

### Reviewers

- Reviewer [label]: [model name], [N findings] (one bullet per reviewer)

### Act On

[Each: description, which models raised it, why it matters.]

### Consider

[Each: description, which models raised it, the tradeoff.]

### Noted

[Brief list.]

### Dismissed

[Each with a brief rationale.]

### Agreement Map

[Where models agreed and diverged, and what that pattern tells us.]
