---
name: tdd
description: Red-green-refactor discipline — write a failing test before any production code, watch it fail for the right reason, write the minimum code to pass, then refactor with the suite green. Use when implementing new behavior, fixing a bug (reproduce it as a failing test first), or when the user asks for TDD/test-driven development.
---

# TDD

Mantra: no production code without a failing test demanding it first.

## Cycle

1. **Red** — write the smallest test that fails for the reason you expect. Run it. Confirm it fails on the assertion, not a typo or import error.
2. **Green** — write the least code that passes it. Don't also handle the next case; that's the next test's job.
3. **Refactor** — clean up with the suite green as the safety net. Re-run after each refactor step, not once at the end.

Repeat. One test, one cycle, at a time.

## Bug fixes

Reproduce the bug as a failing test before touching the fix. A fix with no regression test is a bug that returns.

## Rules

- Never write test and implementation in the same step — a test that never failed proves nothing.
- A test that can't fail isn't a test. Delete or fix it.
- Smallest reasonable step. A test needing ten new methods just to compile means the step is too big.
- Refactor only under green. A red refactor is two changes at once, and you can't tell which broke what.

## Anti-patterns

| Pattern | Why it fails |
|---|---|
| Implementation first, tests after | Tests confirm what exists, never catch a design mistake |
| One giant test for the whole feature | No cycle, no feedback loop — same as skipping TDD |
| Skipping "watch it fail" | Can't tell a real assertion from a typo |
| Refactoring with red tests | Can't isolate which change broke what |

## See also

- [`code-quality`](../code-quality/SKILL.md) — the standard the resulting code should meet.
- [`check-work`](../check-work/SKILL.md) — independent verification once the cycle is done.
