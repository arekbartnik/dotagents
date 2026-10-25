---
name: repo-explorer
description: Clone and inspect external repositories in reusable local exploration cache. Use this skill when user ask to explore, inspect, investigate, compare, or answer questions about a repository that may not already be in the current workspace.
---

Use this skill to explore repositories without cluttering the active workspace.

## Repository Cache

Use `~/.explore/repos` as local cache directory for repositories being explored.

## Current Cache Contents

```!
mkdir -p ~/.explore/repos
ls -la ~/.explore/repos
```

## Flow

1. List the current repository cache contents before deciding what to use.
   - In hosts that support skill shell injection, use the rendered `Current Cache Contents` section above.
   - Otherwise, run `ls -la ~/.explore/repos` before deciding what to use.

2. Check whether the targeet repository is already present in `~/.explore/repos`.
   - Prefer a stable directory name base on the repository owner and name, such as `owner__repo`.
   - If the repository is already present, use the local checkout for exploration.

3. If the repository is not present, clone it into `~/.explore/repos`, then explore it there.
   - Create `~/.explore/repos` first if it does not exist.
   - Clone with a clear desitnation path, for example:

```bash
mkdir -p ~/.explore/repos
git clone <repo-url> ~/.explore/repos/<owner>__<repo>
```

After opening the repository, inspect its local instructions and project metadata before making assumptions. Prefer `rg`, `rg --files`, and targeted file reads for exploration.
