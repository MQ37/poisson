---
name: check-work
description: Self-verification — spawn a fresh-context subagent to independently check that finished work actually satisfies the original request. Re-derives the task from a briefing you write (poisson subagents start with a blank conversation, not a copy of this one), re-reads the diff, runs build/tests, and returns a PASS/FAIL verdict. Use before declaring non-trivial work done, or when asked to check/verify/self-verify.
---

# Check Work

Poisson's `subagent` tool spawns a **blank-context child** — its conversation is ephemeral and never inherits this one (see `subagent.go`: "the child's conversation is ephemeral... its internal steps never enter the parent's conversation"). So the verifier cannot see what the user asked for, what you did, or why, unless you write it down for it. Skipping the briefing step below produces a verifier that's guessing, not verifying.

The verifier is going to run builds/tests/greps against real code — that's real bash, real approval-gate exposure. Per [`sandbox`](../sandbox/SKILL.md) §8: before spawning, create a sandbox mounting the workdir/worktree(s) it needs to inspect, then pass its id via `sandboxIds` and tell it to use `bash(sandboxId=...)`. Skipping this puts every build/test/lint command the verifier runs on the human approval queue.

## 1. Write a self-contained briefing

Before spawning, write out — don't paraphrase this away as "verify my work":

- **Original request** — the user's ask, faithfully, including any follow-up corrections or clarifications across the session (not just the first message).
- **What you did** — files touched, commands run, key decisions, anything you explicitly deferred to the user instead of doing yourself.
- **Focus area** (optional) — if the user asked to focus on one part, name it; otherwise the verifier checks everything above.

This briefing is the *only* thing the verifier will ever know about this session. If it's thin, the verification is thin.

## 2. Spawn the verifier

If the work touched code, create a sandbox first (mount the workdir/worktree that was actually changed), per [`sandbox`](../sandbox/SKILL.md) §8. Call the `subagent` tool with `sandboxIds` set to it, and `task` set to the VERIFIER PROMPT below, with all four placeholders filled from step 1 (SANDBOX gets the same id passed to `sandboxIds`). Give it a clear name (e.g. "verify: fix auth token expiry").

### VERIFIER PROMPT (fill in and pass verbatim as the subagent's task)

```
You are an independent verifier. You do NOT have the conversation that produced
this work — everything you need is below. Don't take it on faith either:
confirm it against the actual code and environment yourself.

=== ORIGINAL REQUEST ===
<the user's ask, faithfully, including follow-ups>

=== WHAT WAS DONE (per the implementer — unverified, confirm it) ===
<files touched, commands run, decisions made, anything deferred>

=== FOCUS AREA ===
<a specific area to concentrate on, or "none — verify everything above">

=== SANDBOX ===
<the sandboxId passed to this call, if any — "none, use plain bash" otherwise>

=== YOUR JOB ===

Phase A — Trace review (always):
1. Restate the request as a concrete checklist of deliverables.
2. Check each deliverable was actually attempted, not just claimed — anything
   the implementer said they'd do but the state doesn't back up?
3. Verify current state yourself: read the modified files, don't trust the
   "what was done" summary. If actions had external effects (jobs submitted,
   configs changed, resources created), confirm those effects actually exist.
   If anything was renamed, moved, or deleted, grep the whole tree yourself for
   its old name — don't trust the implementer's summary or one linter's coverage.

Phase B — Code review (when the task involved code):
4. Collect the diff: `git diff`, `git diff --cached`, `git log --oneline -5`.
5. Read the changed files and enough surrounding context to judge them.
6. Evaluate: correctness (compiles/runs/passes tests — broken build or failing
   tests is an automatic FAIL), adequacy (does it fully address the request),
   excess (unnecessary refactors or scope creep beyond what was asked), edge
   cases (missing ones vs. over-engineered ones).
7. Build and test: read the repo's AGENTS.md/README for the actual commands,
   run them — via bash(sandboxId="<SANDBOX_ID>", ...) if one was given below,
   plain bash otherwise. A broken build or failing test is an automatic FAIL
   regardless of anything else. If the diff adds or flips a flag that gates
   specific behavior (a live-only path, a feature flag, a new mode), run with
   it enabled — a passing default-path suite verifies nothing about a path
   it never touched. If the diff changes what gets exported/published, verify
   the built artifact directly, not just the source tree.
8. Diff every changed test's expected value, not just its name or setup. An
   assertion whose expected output moved (`toEqual(['query'])` becoming
   `toEqual([])`) is a behavior change wearing test-cleanup clothes — confirm
   it against the issue/spec being closed, not just against the code that now
   produces it. A test passing because its expectation was edited to match
   new output proves internal consistency, not correctness.
9. When the diff removes or loosens a defensive/redundant check tied to a
   past bug (regression guard, workaround comment, `// see #NNN`), find every
   producer of the value that check guards against — not just the one this
   diff fixed at the source. A stricter check often exists to survive
   producers the diff doesn't touch (hand-built inputs, other call sites, a
   sibling implementation in a related repo). If a related codebase or spec
   already encodes the same restriction, that's a signal to match it, not
   override it.
10. Look for bugs, security issues, missing validation at trust boundaries,
    regressions, and low-quality tests (circular, over-mocked, happy-path-only).

=== OUTPUT ===

## Checklist
Requirements restated as a numbered list.

## Action Trace
Per checklist item: what was done, how you confirmed it, pass/fail.

## Build & Test Results (if code was involved)
Exact commands run and their output.

## Issues
Skip this section if none. Otherwise, per issue: file:line (if code),
description, evidence, suggested fix.

End with exactly one of:
VERDICT: PASS
VERDICT: FAIL
```

## 3. Act on the verdict

- **PASS** — summarize what the verifier confirmed. Done.
- **FAIL** (or no verdict line found) — fix the issues it identified, then go back to step 1 with an updated briefing (what changed since the last attempt). Repeat up to 3 times; if still failing after 3, stop and report the remaining issues to the user instead of looping forever.

---

## See also

- [`code-review`](../code-review/SKILL.md) — reviews a diff on its own terms; this skill verifies a diff *against a specific request* someone claims it satisfies.
- [`code-quality`](../code-quality/SKILL.md) — the standard the code review phase applies.
- [`sandbox`](../sandbox/SKILL.md) — §8 covers staging a sandbox for the verifier before spawning it.
