---
name: create-pr
description: Workflow for opening a pull request — picking a conventional-commit title, writing a tight What / Why / Testing description, and self-reviewing the diff before pushing. Language-agnostic and minimal in the spirit of suckless and Torvalds' "show me the code". Use when the user asks to open, draft, push, or submit a PR, or to write a PR description or title.
---

# Create a PR

> *"Talk is cheap. Show me the code."* — Linus Torvalds

A PR description is not an essay. It is the **minimum context a reviewer needs that the diff cannot give them**. The diff already shows *what changed* line by line — your job is to say it in one breath, explain *why*, and prove *you tested it*. Three sections. Nothing else.

> **Two skills, one job.** This skill is the **workflow** for producing the PR. The [`code-quality`](../code-quality/SKILL.md) skill is the **content rules** for what's inside the diff. Load both for a non-trivial PR.

**Never manually wrap lines.** Write each paragraph in the description as one long line — GitHub soft-wraps it for the reader. Hard-wrapping at ~70–80 chars only deforms the rendered body and the raw markdown.

---

## 🧪 Workflow

### 1. Dump the diff to a file

```bash
git diff main...HEAD > /tmp/diff_output.diff   # or whatever the base branch is
gh pr diff <number> > /tmp/diff_output.diff    # once the PR already exists
```

Read it with the `Read` tool using offset/limit for large diffs — **never trust truncated terminal output**.

### 2. Self-review against `code-quality`

Walk the whole diff once before you open the PR. The bar, in plain terms:

- Every comment carries information the code doesn't; no filler, no AI hedge prose.
- No swallowed errors, no debug prints left behind, no over-defensive guards.
- Every acquire (file, lock, timer, listener, connection) has a matching release on all paths.
- Every external call is bounded (timeout / limit); everything that can grow is bounded.
- No magic literals; names use the system's existing vocabulary.
- The diff is as small as it can be — could you delete more and still pass?

Full list: the ✅ Pre-flight Checklist in [`code-quality/SKILL.md`](../code-quality/SKILL.md).

### 3. Title — conventional commit

Format: `type(scope): description` — lowercase, imperative, no trailing period. Append `!` for a breaking change.

Types: `feat | fix | chore | docs | refactor | perf | test | ci | build | style | revert`

```text
✅ feat(auth): add API key rotation with grace period
✅ fix(db): prevent race condition in user upsert
✅ perf(index): add compound index on user lookups
✅ feat!: drop support for the legacy config format

❌ Add API key rotation                     # no prefix
❌ fix: fixed the race condition            # past tense
❌ feat(api): Add new search endpoint.      # capitalized, trailing period
❌ Update deps                              # vague, no scope
```

### 4. Description — What / Why / Testing

The only three sections. If a sentence describes *what the code does* line by line, delete it — the diff shows that. Keep the *why* when it is non-obvious. Always say *how you verified it*.

```markdown
## What
[1–3 sentences: the change, at the level a reviewer skims before reading code.]

## Why
[1–3 sentences: the reason it's needed and any non-obvious decision worth flagging. Link the ticket / issue / related PR. This is the only part the diff can't show.]

## Testing
[How you verified it: tests added/run, manual steps, edge cases checked. "Didn't test" is an honest answer that tells the reviewer where to look.]
```

**Size:** 5–30 lines. A trivial fix is three short bullets. If you're past ~40 lines you're explaining the diff back to the reviewer — stop.

### 5. Verify before pushing

- [ ] Every factual claim in the description matches the diff **at the moment of push**.
- [ ] Title has a conventional-commit prefix; breaking change marked with `!`.
- [ ] No stray TODOs or debug code you forgot (`git diff` one last time).

---

## ❌ Anti-patterns — delete on sight

A PR description rots the same way code does: padding hides signal. Cut anything in this list.

| Pattern | Why it's noise |
|---|---|
| Before/after behavior tables | The diff *is* the behavior |
| Benchmark numbers, query plans, stat tables | Belongs in a comment next to the code, not the PR |
| Field-by-field list of what was added/removed | The diff already enumerates it |
| A heading with one sentence under it | Fold it into a bullet |
| Pre-emptive defense of choices nobody questioned | Wait for the question |
| Marketing prose ("improves developer experience") | Show the change, don't sell it |
| AI hedge phrases ("this allows us to", "it should be noted") | Reads as slop |
| Restating the title in paragraph form | Say it once |

---

## 📐 Keep PRs small

> The best PR is one a reviewer can hold in their head in one sitting.

For non-trivial work, split *before* opening PR #1, in dependency order:

1. **Shared types / interfaces / constants** — tiny, near-zero risk, merges fast.
2. **Core implementation** — one logical unit per PR.
3. **Wiring / integration** — connect it up.
4. **Launch** — flip the flag.

If a single PR is doing **two of {types, core, wiring, UI, infra, tests-only}**, it's probably too big — split it. If the work will become a multi-PR stack, sketch the stack in PR #1's description so reviewers see where it's going.

---

## ✅ Final checklist (before "Ready for review")

- [ ] Title: conventional-commit prefix, lowercase, imperative, no trailing period.
- [ ] Description is only **What / Why / Testing**, ≤30 lines (≤10 for a trivial fix).
- [ ] No behavior tables, perf tables, or sections that just narrate the diff.
- [ ] Every claim matches the diff; ticket / related PRs linked under Why.
- [ ] Walked the [`code-quality`](../code-quality/SKILL.md) pre-flight checklist.
- [ ] Breaking change marked with `!` in the title.
- [ ] The diff is as small as the change allows.
- [ ] No manually wrapped lines — each paragraph is one long line.

---

## 📚 See also

- [`code-quality` skill](../code-quality/SKILL.md) — the content rules for what's inside the diff.
- [`review-pr` skill](../review-pr/SKILL.md) — for the other side of the table, reviewing someone else's PR.
