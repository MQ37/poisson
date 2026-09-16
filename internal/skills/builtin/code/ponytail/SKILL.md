---
name: ponytail
description: Ship the smallest correct diff and say so in three lines flat — what got skipped, when to add it back. Use on any implementation/refactor task, when the user says "ponytail", "be lazy", "lazy mode", "minimal solution", "do less", "shortest path", or flags over-engineering, bloat, boilerplate, or an unneeded dependency. Companion to `code-quality`'s Prime Directive ladder (load that too) — this one covers the response shape and the default-and-flag habit the ladder doesn't. Not for non-coding requests.
---

# Ponytail

Distilled from github.com/DietrichGebert/ponytail. The pre-code ladder
(does this need to exist? already here? stdlib? native? existing
dependency? one line? only then write it) already lives in
[`code-quality`](../code-quality/SKILL.md) §0 — load that first if it
isn't loaded yet. This skill is what's left over: the shape of the
answer, and how to handle an ambiguous ask without stalling on it.

## Output shape

Code first. Then at most three short lines: what got skipped, when to
add it back.

Pattern: `[what shipped]. Skipped: [X]. Add when: [Y].`

No paragraph defending the simplification — a paragraph justifying a
cut is complexity smuggled back in as prose. Explanation the user
explicitly asked for (a walkthrough, a report, per-phase notes) is not
this — give that in full.

## Ambiguous or big ask: ship the lazy version, flag it, move on

Don't stall on a scope question you can default. Build the smallest
thing that satisfies the literal ask, then name the fuller option in
the same response: "Did X; Y covers it. Need full X, say so." One
saved round-trip beats a blocked turn.

## One runnable check, not a suite

Non-trivial logic (a branch, a loop, a parser, a money/security path)
shipped outside a formal TDD cycle still leaves one check behind — the
smallest thing that fails if the logic breaks (one table-test case, one
assert-based self-check). No fixture scaffolding, no per-function
suite, unless asked. A trivial one-liner needs none. Already running
[`tdd`](../tdd/SKILL.md)? Its failing test already is the check —
don't add a second one.

## Boundaries

Governs what ships, not how it's said (the always-on caveman style
covers that). Never the source of a cut corner in trust-boundary
validation, data-loss-preventing error handling, security, or
accessibility — `code-quality`'s floor already rules that out, same
floor here. User sees the lazy version and still wants the fuller
one — build it, no re-arguing.

## See also

- [`code-quality`](../code-quality/SKILL.md) — the ladder this skill assumes: YAGNI, reuse, stdlib, native, existing dependency, one line, minimum code.
- [`tdd`](../tdd/SKILL.md) — when a full red-green-refactor cycle is the right amount of process, use that instead of the one-check shortcut above.
