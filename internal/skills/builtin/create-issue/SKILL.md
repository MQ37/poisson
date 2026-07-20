---
name: create-issue
description: Workflow for drafting a tight, free-form issue — short, simple, focused on one thing. For bugs, captures what's broken and how to reproduce it (or where it was observed when it can't be reproduced). Use when the user asks to file, open, write, or draft an issue, bug report, or feature request.
---

# Create an Issue

> *"Debugging is twice as hard as writing the code in the first place."* — Brian Kernighan

An issue exists so someone can **act on it**. Give them exactly what they need to act — no more. Write it free-form: there is no section ritual to satisfy. Use headings only when they help a reader skim; a good issue is often just a short paragraph.

**One issue, one thing.** If you're describing two problems, file two issues.

**Keep it short.** ≤30 lines for non-trivial, a few lines for the rest. The right size is where cutting one more sentence would lose information the fixer needs.

---

## 🐛 Bugs

A broken thing is self-justifying — don't write a business case for why a bug matters. A bug report needs exactly two things:

1. **What is broken** — the wrong behavior, in one or two sentences. Point at the offending location (`file:line`), and paste the *minimal* snippet, error, or log line if you have it. The evidence is the snippet; don't restate it in prose.
2. **How to reproduce it** — the shortest sequence of steps that triggers it. Only the steps you actually ran — never invent or guess them.

**If you can't reproduce it**, say so plainly and give **where it was observed** instead: the environment, the run/request/timestamp, a log excerpt, a link. "Seen once in prod, log attached, couldn't reproduce locally" is a complete, honest report — far more useful than a fabricated repro.

Do **not** pad a hard-to-reproduce bug with a speculative root-cause essay. A guess dressed up as analysis sends the fixer down the wrong path. If you have a real hunch, mark it as a hunch in one line and move on.

---

## 💡 Features, changes, tasks

Say what you want, plainly. Add *why* only if it's non-obvious — and keep it to a sentence. If you propose a fix or approach, **pick one**; don't hand the reader a menu of options to choose from. Point at the relevant code so they can start.

---

## ❌ Cut on sight

| Pattern | Why |
|---|---|
| A justification essay for an obvious bug | The breakage is the justification |
| Invented / guessed reproduction steps | Worse than none — sends the fixer the wrong way |
| Speculative root-cause analysis padding a hard-to-repro bug | A guess dressed as fact wastes time |
| Multi-option fix menus ("Option 1… Option 2…") | Decide, or it's not actionable |
| "Acceptance criteria" on simple work | Implicit from a clear ask |
| Restating the snippet's meaning in prose | The reader can see the code |
| Marketing fluff ("improves developer experience") | Doesn't help the fixer |
| AI hedges ("this allows us to", "it should be noted") | Reads as slop |

---

## 🎯 Examples

**Reproducible bug:**

```markdown
`parseConfig` (`config.ts:42`) swallows parse errors and returns null, so a
malformed config looks identical to an empty one.

Repro: start with `config.json` = `{ "port": }` → server boots with defaults,
no error logged.
```

**Bug that can't be reproduced:**

```markdown
Checkout intermittently double-charges. Seen twice in prod (orders #4471,
#4488; logs attached) — couldn't reproduce locally. Both hit the retry path
in `payment.ts` within ~2s of a gateway timeout. Hunch: retry fires before
the first charge settles, but unconfirmed.
```

**Feature / change:**

```markdown
Add a `--dry-run` flag to the import command that prints what would change
without writing. Entry point is `import.ts:20`.
```

---

## ✅ Before filing

- [ ] One issue = one problem.
- [ ] Bug: states **what's broken** + **how to reproduce**, or **where it was observed** when it can't be reproduced.
- [ ] No reproduction steps I didn't actually run.
- [ ] No justification padding — the bug speaks for itself.
- [ ] One proposal, not a menu.
- [ ] Short, focused, no marketing or AI-slop language.

---

## 📚 See also

- [`create-pr` skill](../create-pr/SKILL.md) — when you're opening the fix, not reporting it.
- [`code-quality` skill](../code-quality/SKILL.md) — the principles a fixer will hold the change to.
