---
name: grilling
description: Relentless one-question-at-a-time interview to stress-test a plan, design, or decision before acting on it — maps it as a design tree, works the open frontier each round, dispatches subagents for anything discoverable instead of asking the user. Use when the user wants to be grilled, asks to stress-test an idea/architecture, or before implementing anything non-trivial where assumptions haven't been surfaced yet.
---

# Grilling

Interview the user until you both share the same understanding of a plan. Never act on a half-settled one.

## Design tree

Map the plan as a tree: each decision branches into the decisions hanging off it. The **frontier** is every decision whose prerequisites are already settled — askable now, without guessing at an answer not yet heard.

## Rounds

Ask the whole frontier at once, numbered, each with your recommendation:

```
❓ Q1 — <title>: <question, options if relevant>
➡️ recommendation: <your answer>
```

Wait for the answers before the next round. Each answer reshapes the tree — settled decisions push the frontier outward, unblocking what depended on them. A question whose answer depends on another still-open question belongs to a later round, not this one.

## Facts vs decisions

Finding facts is the agent's job, never the user's. A frontier question answerable from the codebase or tools: dispatch a subagent to find it, don't ask. Don't block the round on it — only the questions downstream of that fact wait; ask the rest of the frontier now. Decisions are the user's — put each to them, wait.

## Done

Frontier empty, every branch visited, nothing silently assumed. Confirm shared understanding before writing a line of code.

## Anti-patterns

| Pattern | Why it fails |
|---|---|
| Serial questions that could've been one round | Wastes turns; user re-answers a moving target |
| Asking the user something a grep would answer | Wastes their time on lookup, not decision |
| Proceeding on "probably what they meant" | Defeats the point of grilling |
| Dumping questions ignoring dependency order | Later ones get answered before their prerequisites are known |

## See also

- [`council`](../council/SKILL.md) — critiques finished code/architecture; grilling stress-tests a plan before it's written.
- [`check-work`](../check-work/SKILL.md) — verifies the result after; grilling reduces how much needs fixing.
