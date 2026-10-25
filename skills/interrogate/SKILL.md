---
name: interrogate
description: 'Use for "interrogate", "adversarial review", "multi-model review", "challenge this", "stress test this code", "find blind spots", or "tear this apart". Multiple independent reviewers challenge changes from different angles.'
disable-model-invocation: true
---

# Interrogate

Spawn several independent reviewers to adversarially review code changes. Each reviewer gets the same prompt and rubric. The adversarial signal comes from reviewer independence and, where the harness offers it, model diversity. It does not come from assigned personas.

The deliverable is a synthesized verdict. Do NOT auto-apply changes.

## Step 1, Determine Scope

Identify what to review from context:

- If the user points at specific files or a diff, use that
- If on a feature branch, run `git diff main...HEAD` (or the appropriate base branch) for the full changeset
- If the user's message references recent work, gather the relevant files

Package the diff (or file contents) plus any surrounding context files the reviewers need to understand the code.

## Step 2, State the Intent

Before spawning reviewers, state the intent explicitly. Derive this from:

- The user's message
- Commit messages
- PR description if one exists
- The code itself

Write one clear paragraph. If you're unsure about the intent, ask the user before proceeding.

## Step 3, Spawn Reviewers

Launch all reviewers in a single message so they run concurrently. Default to three, labeled Reviewer A, B, and C. Use more or fewer when the user asks.

Reviewers from different providers disagree in more useful ways than reviewers on one model. Get that diversity from provider CLIs first:

1. **External reviewers.** Run one reviewer through each other provider's CLI that is available (`claude`, `codex`), per `references/external-reviewers.md`.
2. **Native subagents.** Fill the remaining slots with general-purpose subagents. Pick from the most capable models the harness offers, and prefer a model no other reviewer is on.
3. **No diversity available.** With no other provider's CLI and no model choice, run every reviewer on the parent's model. Independent contexts still produce independent findings. Say in the verdict that the reviewers shared one model.

Every reviewer has read-only access. Reviewers never edit files.

If a reviewer fails or the harness rejects a chosen model, replace it and say so in the verdict. Do not block the review on it.

Read `references/reviewer-prompt.md` and fill in the template with:

1. The stated intent
2. The diff or file contents
3. The review rubric from `references/rubric.md`
4. The code-quality lens from `references/code-quality-review.md`

The same filled template goes to all reviewers, so every reviewer applies the code-quality lens.

## Step 4, Synthesize

As results come back, build a unified picture:

1. **Parse all findings** from the reviewers
2. **Identify consensus**. Findings raised by 2+ reviewers independently are highest signal.
3. **Identify lone-reviewer findings**. Still worth reading, but weight accordingly.
4. **Deduplicate**. Different reviewers may describe the same issue differently. Merge these and note which reviewers raised it.
5. **Note disagreements**. If one reviewer flags something and another explicitly says the opposite, that's useful context for the verdict.

## Step 5, Lead Judgment

You are the lead reviewer, a pragmatic senior engineer, not a neutral aggregator.

Read `references/lead-judgment.md` for the full framework.

Categorize every finding using these buckets:

- **Act on**. Real issues affecting correctness, security, or maintainability given the actual goals. These would block a real PR.
- **Consider**. Legitimate points, but you're not sure they outweigh the cost of addressing them right now. Worth the user's attention.
- **Noted**. Technically valid but not actionable. Context-dependent, premature optimization, or low-impact given the current stage.
- **Dismissed**. Wrong, nitpicky, or missing context. Brief explanation why.

For each finding, include:

- Which reviewer(s) raised it
- The category (act on / consider / noted / dismissed)
- A one-line rationale for the categorization

## Output Format

Present the verdict in this structure:

### Intent

> [The stated intent paragraph from Step 2]

### Reviewers

- Reviewer [label]: [provider CLI or model it ran on], [N findings] (one bullet per reviewer)

### Act On

[Findings that should be addressed. For each: description, which reviewers raised it, why it matters.]

### Consider

[Findings worth thinking about. For each: description, which reviewers raised it, tradeoff involved.]

### Noted

[Valid but low-priority. Brief list.]

### Dismissed

[Rejected findings with brief rationale.]

### Agreement Map

[Where did reviewers agree, where did they diverge, and what does the pattern of agreement/disagreement tell us?]
