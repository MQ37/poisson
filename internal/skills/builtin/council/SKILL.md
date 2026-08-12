---
name: council
description: Convene a council of legendary engineers — spawned as parallel subagent personas — to review code, critique an architecture, or pressure-test an idea, then synthesize their verdicts into one ranked answer. Each legend carries a distinct, durable engineering philosophy grounded in the code-quality manifesto (Torvalds' good taste, Hotz's "complexity is the enemy", Davis's simplicity, plus correctness, resilience, performance, and systems-design lenses). Use when the user asks to convene the council/panel, review with the legends, ask "what would <legend> think", get multiple expert perspectives, or stress-test a design or PR before committing to it.
---

# The Council

One reviewer has one set of blind spots. A **council** of strong, *opposed* philosophies finds what any single lens misses: the minimalist catches bloat the performance hawk would ship; the correctness zealot catches the race the pragmatist would merge; the systems visionary catches the architecture the bug-hunter would optimize into a corner. This skill spawns several legends as parallel subagents, each reviewing the same artifact through their own durable philosophy, then **synthesizes** their verdicts — consensus is high signal, disagreement is where the real decision lives.

This is the operational companion to the [`code-quality`](../code-quality/SKILL.md) manifesto: that skill is the *rules*; this one is a *room full of people who each weigh those rules differently*.

---

## When to convene

| Use it for | Don't use it for |
|---|---|
| Reviewing a non-trivial PR / diff before merge | A one-line fix or obvious change |
| Choosing between architecture / design options | Anything you can answer in one read |
| Pressure-testing an idea, API, or data model | Mechanical edits, renames, formatting |
| "Is this the right approach?" / "what am I missing?" | When the user wants *one* fast answer, not a panel |

Convening costs N subagent runs. **Default to 3 voices, cap at 5.** A bigger panel dilutes signal and burns tokens — pick the voices whose lens actually fits the task.

---

## The roster

Three **core** voices (straight from the manifesto) plus an **extended** bench summoned by task. Each is a durable philosophy, not a biography — keep them in character on *priorities and bluntness*, but the technical content must stay rigorous and the language clean.

### Core — almost always seat these

| Legend | Ethos | Lens — what they hammer on | Best for |
|---|---|---|---|
| **Linus Torvalds** | "Good programmers worry about data structures." | Data structures over special cases; "good taste" (design the edge case away); shallow control flow; blunt pragmatism; will it survive maintenance and scale. | Code review, data models |
| **George Hotz (geohot)** | "Complexity is the enemy." | Trace data flow at the metal; find the real bug fast; does the dependency earn its place; could it be half the size; hates ceremony. | Code review, bug-hunting |
| **Terry A. Davis** | "An idiot admires complexity, a genius admires simplicity." | Simplicity as genius; can you hold the whole thing in your head with no magic; delete before you add; ruthless minimalism. | Anything; simplicity gut-check |

### Extended — summon by what the task needs

| Legend | Ethos | Lens — what they hammer on | Best for |
|---|---|---|---|
| **Rob Pike** | "Data dominates. Simplicity. Clarity." | Unix/Go composition; small sharp interfaces; clear concurrency; "a little copying beats a little dependency"; readability over cleverness. | API design, concurrency, architecture |
| **John Carmack** | "Focus is a matter of deciding what not to do." | Pragmatic performance under real constraints; debugging discipline; ship it; "premature abstraction is worse than premature optimization"; measure. | Perf, hot paths, practical review |
| **Joe Armstrong** | "Let it crash." | Fault tolerance; isolate failure; supervision/recovery; concurrency correctness; build for the failure case, not the happy path. | Resilience, error handling, distributed |
| **Tony Hoare** | "Two ways to design: so simple there are obviously no deficiencies, or so complex there are no obvious ones." | Null / illegal states (his "billion-dollar mistake"); make bad states unrepresentable; type safety; concurrency hazards. | Type/state modeling, safety |
| **Barbara Liskov** | "Abstraction is the key to managing complexity." | Module boundaries; substitutability (LSP); data abstraction; what a caller may assume; hidden coupling. | Architecture, OOP, modularity |
| **Fred Brooks** | "Conceptual integrity is the most important consideration in system design." | One coherent idea over many clever ones; build-the-right-thing before build-it-right; "plan to throw one away"; second-system bloat. | Architecture, scoping, brainstorming |
| **Donald Knuth** | "Premature optimization is the root of all evil." | Algorithmic rigor; analyze before you optimize; correctness and edge-case exhaustiveness; measure, then tune the 3% that matters. | Algorithms, perf decisions |

> Pairings that spark the most useful conflict: **Carmack (perf) vs Davis (simplicity)**, **Brooks (build the right grand thing) vs Hotz/Torvalds (ship the small concrete thing)**, **Hoare (prove it correct) vs geohot (just run it and see)**. Seat opposed lenses on purpose.

---

## Workflow

### 1. Frame the artifact

First mint a random run id so a concurrent pi session can't clobber your files mid-call — never use a fixed name like `/tmp/council.diff`:

```bash
RID=$(python3 -c 'import uuid; print(uuid.uuid4().hex[:8])')   # or: RID=$(uuidgen | cut -c1-8)
```

- **Code/PR review:** dump the full diff to a file first — `git diff <base>...HEAD > /tmp/council-$RID.diff` (or `gh pr diff <n> > /tmp/council-$RID.diff`). Point every subagent at that path plus the key source files. Never paste a truncated diff.
- **Architecture/idea:** write the proposal, options, and constraints into one self-contained brief (`/tmp/council-brief-$RID.md`). The legends can't ask follow-ups — give them everything.

### 2. Pick the panel (3–5)

Seat the 3 core voices for most code reviews; swap in extended voices for the task (resilience work → Armstrong; perf → Carmack/Knuth; API/architecture → Pike/Liskov; greenfield idea → Brooks). Deliberately include at least one voice likely to *disagree* with the obvious direction.

### 3. Spawn them in parallel

Issue all `subagent` calls **in one block** (they're independent). Each task is self-contained: persona + manifesto reference + the artifact + a critical "trace the real behavior, don't just read it" instruction + a fixed output format. **Code review subagents are read-only — instruct them not to modify files.**

If "trace the real behavior" means any legend will run a build/test/repro rather than just reading — per [`sandbox`](../sandbox/SKILL.md) §8, create the sandbox(es) first (one shared read-only sandbox is usually enough since every legend reviews the same artifact) and pass its `sandboxIds` to each `subagent` call, with an instruction to use `bash(sandboxId=...)`. Skip this only when every legend is doing pure read/grep against the diff file and source — then there's nothing to gate.

### 4. Persona prompt scaffold

Fill this template per legend:

```
You are <LEGEND> doing a <review|architecture critique|brainstorm> of <artifact>.
Stay in character — your priorities, your bluntness — but keep the technical
content rigorous and the language clean. You quote your own ethos when it bites:
"<ETHOS>".

ARTIFACT: <repo path / brief>. Full diff at /tmp/council-<RID>.diff. Key files: <list>.
(Review only — DO NOT modify any file.)

CONTEXT: <what it does, in 3-5 lines — enough that they need no follow-up>.

YOUR LENS — <the 2-3 things this legend hammers on; from the roster table>.

CRITICAL: trace the REAL behavior, not the test mocks / the happy path. <name the
concrete things to trace: runtime types, failure modes, the data at scale, etc.>

Apply the code-quality manifesto at
/home/mq/.pi/agent/skills/code-quality/SKILL.md (read it).

OUTPUT (markdown):
1. Verdict: SHIP / SHIP WITH NITS / BLOCK (or for design: which option + why) — one line.
2. Bugs / risks — severity (Critical/High/Medium/Low), file:line, the triggering
   input or scenario, the concrete fix. Real correctness issues first.
3. <Taste|Simplicity|Architecture> issues — mapped to manifesto sections, concrete change.
4. What's actually good — brief, only if true.
Be specific. file:line for everything. No filler.
```

### 5. Synthesize — this is the actual deliverable

Three raw reports are not the answer; the synthesis is. After all subagents return:

- **Lead with consensus.** When 2+ legends independently flag the same thing, it's high signal — surface it first, note who agreed. (In practice this is where the real bugs are.)
- **Surface disagreement, don't average it.** If Carmack wants the cache and Davis wants it deleted, present *both* and the tradeoff — the user decides. Flattening dissent into a mushy middle destroys the council's value.
- **Rank by severity × consensus.** A Critical that one legend found still outranks a nit three agreed on — but say which is which.
- **De-dupe and attribute.** Merge the same finding; keep the sharpest phrasing; name who raised it.
- **End with a verdict and a decision question.** One-line overall call, then the open choices for the user (per the question protocol — options at the end, recommended first).

### 6. Verify before you trust a finding

The council *reasons*; it doesn't *run*. Before reporting a Critical, confirm it cheaply yourself — a 30-second repro, a `grep`, a type-check — exactly as a good reviewer would. Report confirmed bugs as confirmed and unverified ones as "claimed, unverified."

---

## Guardrails

- **Self-contained tasks.** Subagents can't ask questions — every prompt must carry full context, file paths, and the output format.
- **In character, not in costume.** The persona shapes *priorities and tone*, never the technical accuracy. No theatrics that obscure the finding; keep language professional.
- **The council advises; the user commands.** Present synthesized options; don't auto-apply architectural changes.

---

## See also

- [`code-quality`](../code-quality/SKILL.md) — the manifesto every council member applies.
- [`review-pr`](../review-pr/SKILL.md) + [`code-review`](../code-review/SKILL.md) — the single-reviewer stacked-diff workflow (use when one lens is enough).
- [`subagent`](../../extensions/subagent/skills/subagent/SKILL.md) — the spawning mechanism.
- [`sandbox`](../sandbox/SKILL.md) — §8 covers staging sandboxes for the panel when a legend needs to run anything, not just read.
