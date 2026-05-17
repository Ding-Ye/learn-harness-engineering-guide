# git

> Inspect working tree status and diffs for the current repository.

## When to Use

- The user asks "what changed?", "what's staged?", or "show me the diff".
- You need to confirm a clean working tree before suggesting a commit.

## Available Tools

- `git_status` — short porcelain status of the working tree.
- `git_diff(staged?)` — diff of unstaged changes, or staged with `staged: true`.

## Conventions

- Run `git_status` before `git_diff` so you can describe scope before showing detail.
- Never invent file paths — only mention what the tools actually return.
- The diff output is patch format; quote it sparingly to save tokens.

## Example

To answer "what's on my branch?":

1. `git_status` → list of changed/untracked files.
2. `git_diff(staged: false)` → patch for the unstaged hunks.
