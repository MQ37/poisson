---
name: review-pr
description: Gather a PR or branch diff for review — decision tree for local working tree vs GitHub PR vs a fresh /tmp checkout, dumped to a file so nothing is lost to terminal truncation. Use when the user asks to review a PR, summarize a diff, or audit changes; hands off to `code-review` for the actual review and `stacked-diff-review` for the write-up.
---

# Gather a Diff for Review

> **Three skills, one job.** This skill gathers the diff. [`code-review`](../code-review/SKILL.md) is the review process — what to look for, how to verify a finding, when to auto-apply. [`stacked-diff-review`](../stacked-diff-review/SKILL.md) is the output format. Load all three for a non-trivial PR.

---

## 1. Decide where the diff comes from

**Prefer the local working tree whenever possible.** A fresh checkout under `/tmp` is a last resort — only reach for it when the current directory genuinely cannot serve the review (wrong repo, or you need a clean tree and the user has unrelated uncommitted changes).

Decision order:

1. **No PR reference, in a git repo** — review the local branch. Detect an open PR via `gh pr view --json number,url` for context, but read the diff from the working tree: `git diff <base>...HEAD` (`<base>` = PR base or `origin/HEAD`), plus `git diff` and `git diff --cached` for any uncommitted changes. Mention uncommitted changes explicitly in the report.
2. **PR reference provided, and we are inside the matching repo** — verify by comparing `gh repo view --json nameWithOwner` to the PR's repo. If they match:
   - If the PR's head branch is currently checked out (`git branch --show-current` matches `gh pr view <n> --json headRefName`), review the local tree as in (1).
   - Otherwise, read the diff via `gh pr diff <number>` and PR metadata via `gh pr view <number>` — no checkout, no `/tmp`.
3. **PR reference provided, and we are NOT in the matching repo (or not in a git repo at all)** — only then fall back to a fresh checkout under `/tmp` (`gh pr checkout` into a temp clone). State this up front so the user knows why you're leaving the current directory.

Never `cd /tmp` or clone anywhere when steps (1) or (2) apply. If unsure which case applies, run the detection commands above before fetching anything.

## 2. Dump the full diff to a file

```bash
gh pr diff <number> > /tmp/diff_output.diff      # PR by number
git diff main...HEAD > /tmp/diff_output.diff     # current branch
git diff <base>...<head> > /tmp/diff_output.diff # arbitrary range
```

**Never trust truncated terminal output.** Read with the `read` tool using `offset`/`limit` for large diffs.

## 3. Hand off

Once the diff is gathered and saved, run it through [`code-review`](../code-review/SKILL.md) (lenses, verification, severity, optional apply), then write it up per [`stacked-diff-review`](../stacked-diff-review/SKILL.md).

---

## ✅ Checklist

- [ ] Correct source picked — local tree preferred over PR fetch preferred over `/tmp` checkout.
- [ ] Full diff dumped to a file — not relying on terminal truncation.
- [ ] Uncommitted changes called out explicitly if present.
- [ ] Handed off to `code-review` and `stacked-diff-review`.

---

## 📚 See also

- [`code-review`](../code-review/SKILL.md) — the review process this hands off to.
- [`stacked-diff-review`](../stacked-diff-review/SKILL.md) — the output format.
- [`code-quality`](../code-quality/SKILL.md) — the content rules.
- [`create-pr`](../create-pr/SKILL.md) — the other side of the table, when you're the author.
