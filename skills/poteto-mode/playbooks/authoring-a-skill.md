### Authoring or modifying a skill

**You own the skill's voice.**

1. Use the harness's skill-authoring skill (`../references/harness.md`). With none, write the skill by hand to the rules below.
2. Validate the skill: frontmatter has `name` and `description`, referenced files exist, cross-skill links resolve. Run `../scripts/poteto check-harness` and fix every line it prints, so the skill names actions and `../references/harness.md` holds the tool names.
3. Test cases if structural. Skip if subjective.
4. Run **Opening a PR**.

When in doubt, delete. Keep only prose that changes a decision. Tell it to do the thing and skip the reason. Explain only when the rule is confusing without one. Match tone to scope. Point at structural sources (types, READMEs, config) per the **encode-lessons-in-structure** principle skill. Delegate to other skills by path. Don't restate. A workflow you keep hitting but isn't captured → propose a new skill.

**Reply:** summary of the skill, key design decisions, validation notes.
