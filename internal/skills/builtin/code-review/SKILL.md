---
name: code-review
description: General code review process — scan lenses (correctness, performance, edge cases, test coverage, security, API design, boundary/type cleanliness, orchestration/atomicity, ambitious simplification with a file-size ceiling and spaghetti-growth check, comment hygiene), verify every finding against the real code with subagents, re-evaluate severity against a presumptive-blockers bar, and optionally auto-apply real fixes (apply/escalate/skip). Diff-source-agnostic — works on a PR diff, a local branch diff, or a pasted change. Use when you want a thorough code review, not just a stacked-diff write-up.
---

# Code Review

> Where the finding actually happens. [`review-pr`](../review-pr/SKILL.md) gathers the diff (git/gh mechanics, /tmp file). This skill is the process in between — what to look for, how far to trust a finding, when to just fix it. [`stacked-diff-review`](../stacked-diff-review/SKILL.md) formats the result.

## 0. Mode: review or apply?

Check the invocation for an explicit go-ahead — the word "apply" in the `skill` tool's `args` string if invoked that way, or an explicit instruction in the user's message otherwise ("fix it", "apply the fixes", "go ahead"). State the parsed mode as your first line: `Mode: apply` or `Mode: review only`. This is a commitment — don't re-derive or second-guess it later in the same pass.

## 1. Identify potential issues

Lenses to scan in parallel:

- **Correctness, performance, edge cases, test coverage, security, API design** — the standard surface. An invariant assumed about data from an external dependency (uniqueness, format, ordering) is not verified by reading its name — check the dependency's actual source or a real run.
- **Feature impact (blast radius)** — when the diff adds a path beside an existing one or touches a shared entry point, run [`feature-impact`](../feature-impact/SKILL.md) and demand its inventory. This is the lens that catches what the diff *doesn't* show.
- **Boundary and type cleanliness** — unnecessary `any`/`unknown`/casts where a clearer type boundary could exist; a silent fallback papering over an invariant that should instead be made explicit. Question every optionality that isn't a real absence.
- **Orchestration and atomicity** — independent work serialized for no reason (should it run in parallel?); related updates that can leave state half-applied (should the change be more atomic?). Don't over-index on micro-optimization, but flag avoidable orchestration complexity that makes the implementation more brittle.
- **Simplification** — don't stop at "this could be a bit cleaner." Actively search for the reframing that deletes a whole branch, helper, mode, or conditional layer rather than just tidying it — the restructuring that uses the existing architecture better and makes the change simpler, not just neater. Concretely, flag:
  - Dead or unreachable code: unused params, branches, imports, exports.
  - Over-abstraction: a helper/wrapper/options bag for a single call site or a hypothetical future.
  - Redundancy: a variable/method extracted once with no naming benefit; duplicated logic that could share a path.
  - Defensive code for impossible scenarios: checks for cases the type system or call graph already prevents (validate only at real trust boundaries).
  - Backwards-compat cruft with no live consumers: `// removed X`, unused re-exports, compat shims nobody calls.
  - **Spaghetti growth in existing code** — a new ad-hoc conditional or one-off branch bolted onto a flow that was clean before this change, not just special-casing in new code. Treat this as a design problem, not a style nit, even if it technically works.
  - **File-size ceiling** — a file crossing ~1000 lines because of this diff is a default code-quality smell; ask explicitly whether it should be decomposed first. Waive only with a compelling structural reason and a result that's still clearly organized.
- **Comment hygiene** — for every comment the change added or modified: does it restate the code (drop it)? reference the current task/callers (belongs in the commit message, not the code)? leftover commented-out code or stray TODO? Keep only comments that explain *why* — a hidden invariant, a workaround, a non-obvious constraint. Full rules: [`code-quality`](../code-quality/SKILL.md) §4.

Assign an **initial** severity per finding: `critical` / `high` / `medium` / `low` / `nit`. Simplification and comment-hygiene findings are usually `low`/`nit` but earn higher severity when the surplus code or misleading comment actively hides a bug.

## 2. Verify every finding

Use subagents to check each finding against the actual code — read the lines, trace the call path, don't take the first read at face value. **Discard** anything that can't be verified. Three solid findings beat ten speculative ones.

Reviewing several PRs/branches in parallel (one subagent per PR): per [`sandbox`](../sandbox/SKILL.md) §8, create one sandbox per subagent first, mount/worktree what each needs, then pass `sandboxIds` — otherwise every git/build/lint command each parallel child runs queues its own approval prompt.

## 3. Re-evaluate severity (important)

Initial severities are guesses made under uncertainty. After verification, reconsider each:

- Does it actually break correctness, or is it stylistic/cosmetic?
- Blast radius — one call site or a whole subsystem? Hot path or one-shot setup?
- Existing safeguard — does a test, type, or runtime check already cover it?
- Public API boundary, or internal-only?
- Silent failure/data corruption (bump up) or a loud crash already caught by tests (bump down)?

Record the delta when severity changes — `high → medium`, one short reason.

### Presumptive blockers

Treat these as blocking regardless of how the rest of the diff looks — a passing build and a working feature don't excuse them. The author can still argue one down, but the burden is on them to justify it, not on the reviewer to prove it:

- A plausible reframing exists that would delete a whole layer of incidental complexity, and the diff took the more complex path instead.
- The diff pushes a file from under ~1000 lines to over.
- Ad-hoc branching added to a flow that was clean before this change.
- A local problem solved by scattering feature-specific checks across shared/canonical code instead of behind its own boundary.
- An unnecessary wrapper, cast, or optional field that makes a contract more indirect than it needs to be.
- Logic duplicated where an existing canonical helper already does the job.
- An `unknown` entry in the [`feature-impact`](../feature-impact/SKILL.md) inventory, or an existing test gated to the old path without naming the mechanism that makes the case impossible on the new one.
- An existing test's expected value edited to match new output, with no argument given for why the new output is the *right* one — "the test now matches the code" is not that argument.
- A defensive/redundant check removed or loosened on the grounds that its one known trigger was fixed at the source, without checking whether other producers can still reach it.
- A new flag or mode that gates which code runs, with "tests pass" as the only evidence and no sign the gated path itself was exercised.
- A change to what a module exports, publishes, or packages, verified only through a dev-linked/workspace path rather than the artifact a real consumer installs.
- A boolean/skip/exclude predicate whose name reads the opposite of what it does when you read the call site out loud.

## 4. Report

Hand the verified, re-scored findings to [`stacked-diff-review`](../stacked-diff-review/SKILL.md) for the write-up (risk-tiered, critical first, TL;DR leading). Order simplification/architecture findings by how much they cost the reader, not by where they sit in the file: a missed dramatic-simplification opportunity outranks a spaghetti-branch flag, which outranks a boundary/type nit, which outranks a bare file-size note. Don't flood the report with low-value nits when a structural finding is present — fewer high-conviction findings beat a long list of cosmetic ones.

Include a verification table:

| # | File:line | Issue | Initial | Final | Δ reason (if changed) |
|---|-----------|-------|---------|-------|----------------------|

## 5. Apply (only in apply mode)

Before editing anything, if this review is of a PR/branch someone else's tooling might also push to, confirm you're on the diff's actual branch (re-check `git branch --show-current` against the PR's `headRefName`). Never create a synthetic ref (`git fetch origin pull/<n>/head:pr-<n>`) to edit against — that leaves your changes divorced from the real head.

For every verified finding, in order of preference:

1. **Apply** — the smallest surgical fix, regardless of severity. Nit and low count too — that's the point of apply mode.
2. **Escalate** — when the right fix is genuinely bigger than a surgical edit (multi-file refactor, breaking API change, migration). Don't wedge an oversized fix into a small diff: state explicitly what the real fix is, leave the code untouched for that finding, and recommend a follow-up PR/issue.
3. **Skip** — only when the fix is unnecessary (the finding was taste, current code is fine) or disproportionately complex for its severity. Name the specific reason — "low severity" alone is not one.

Run the relevant tests/lint/build for every file you touched. Report what was applied, what was escalated (with the recommended follow-up), what was skipped (with the specific reason), and any test/build failures.

---

## See also

- [`review-pr`](../review-pr/SKILL.md) — gathers the diff this skill reviews (git/gh mechanics, /tmp file).
- [`stacked-diff-review`](../stacked-diff-review/SKILL.md) — the output format.
- [`code-quality`](../code-quality/SKILL.md) — the content rules every lens above points back to.
- [`feature-impact`](../feature-impact/SKILL.md) — the blast-radius inventory the lens above requires.
- [`council`](../council/SKILL.md) — multi-persona alternative when one lens isn't enough.
- [`sandbox`](../sandbox/SKILL.md) — §8 covers staging sandboxes for parallel review subagents.
