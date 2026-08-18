---
name: code-quality
description: Universal code-quality principles for any language or codebase — simplicity, clarity, and minimalism in the spirit of the suckless manifesto, Linus Torvalds' "good taste", a hacker's distrust of complexity, and the conviction that one mind can understand the whole machine. Use proactively before writing functions, adding comments, handling errors, naming things, allocating resources, or opening a PR. Catches bloat, over-abstraction, AI-slop comments, over-defensive error handling, magic literals, deep nesting, leaked resources, needless dependencies, and code nobody fully understands.
---

# Code Quality

> *"An idiot admires complexity, a genius admires simplicity."* — Terry A. Davis
>
> *"Bad programmers worry about the code. Good programmers worry about data structures and their relationships."* — Linus Torvalds
>
> *"Complexity is the enemy."* — George Hotz
>
> *"The more code lines you have removed, the more progress you have made."* — suckless manifesto

**Cardinal rule.** Every line of code, every comment, every commit message must carry information the reader cannot trivially derive from the rest. If it does not, delete it. The best code is the code that was never written.

This skill is language-agnostic on purpose. There are no library names, no framework APIs, no SDK calls — those rot. The principles below are the ones that held in C in 1972 and still hold today. Apply them whether you are writing assembly, C, Python, or a shell script.

---

## 0. The Prime Directive — Simplicity

You are not paid by the line. Every abstraction, branch, parameter, dependency, and config flag must *earn its place*. The default answer to "should I add this?" is **no**.

- **Delete before you add.** Removing code is progress. A diff that is net-negative and still passes is usually a good diff.
- **Solve the problem in front of you**, not the six hypothetical ones you imagine. Speculative generality is the most expensive code there is.
- **A feature you don't build has no bugs, no tests, no docs, and no maintenance cost.** Push back on scope.
- **Scope-cutting has a floor.** Input validation at trust boundaries, error handling that prevents data loss, security, and accessibility are never the corner you cut to shrink a diff — regardless of how minimal the rest of the change is.
- **New coupling is a one-way door.** Sharing code, a schema, or a release cycle across repos, services, or teams binds their future changes together long after this diff merges. Flag it and get a nod on the approach before you build the full implementation — a big diff resting on an unconfirmed premise is a sunk cost the reviewer now has to unwind, not review.
- **If you can't hold the whole thing in your head, it's too complex.** Terry Davis wrote an operating system he understood end to end. You should at least understand the function you just wrote, top to bottom, with no magic.
- **Make it work, make it right, make it small** — in that order, but never skip the last step before you ship.

If you feel clever, you have probably made it worse. Clever is a code smell. Boring, obvious, linear code is the goal.

### The Ladder

Before writing a line, stop at the first rung that holds:

1. **Does this need to exist at all?** Speculative need → skip it, say so in one line. (YAGNI)
2. **Already in this codebase?** A helper, util, or pattern that already lives here — reuse it, don't rewrite it.
3. **Does the standard library do it?** Use it.
4. **Does a native platform feature cover it?** A DB constraint, a builtin, a native element — before framework code.
5. **Does an already-installed dependency solve it?** Use it. Don't add a new one for what one you already have can do.
6. **Can it be one line?** One line.
7. **Only then:** the minimum code that works.

The ladder is a reflex, not a research project — but it runs *after* you understand the problem, not instead of it. Read the code the change touches and trace the real flow first; laziness that skips comprehension to ship a small diff just ships a confident wrong fix, faster. Two rungs work? Take the higher one and move on.

Tie-breaker: if two options at the same rung cost the same, take the one that's correct on the edge cases. Simplicity means less code, never a flimsier algorithm.

---

## 1. Good Taste — Data Structures First

The single highest-leverage move in programming is choosing the right data structure so the code has no special cases. This is what Torvalds means by "good taste": the bad version has an `if` to handle the edge; the good version restructures the data so the edge does not exist.

- **Eliminate the special case, don't handle it.** Before writing `if (firstElement)` / `if (lastElement)` / `if (empty)`, ask whether a different representation (sentinel node, pointer-to-pointer, empty-but-valid object) makes the branch disappear.
- **The shape of the data dictates the shape of the code.** If the code is ugly, the data model is usually wrong. Fix the model, not the symptom.
- **Store a fact once.** Derive everything else. Duplicated state is a bug waiting for the two copies to disagree.
- **Enforce a cross-site invariant once, structurally.** Ordering, mutual exclusion, cleanup-must-run — if it has to hold at several call sites, put it behind one function, assertion, or type that all of them go through. A rule repeated as a comment at each site isn't enforced anywhere; it drifts the moment one site changes and the others don't.
- **Make illegal states unrepresentable.** A structure that cannot hold a bad value needs no validation for that value.
- **An assumed invariant about input data is a claim, not a fact.** Uniqueness, a reserved separator/sentinel never appearing in real values, ordering, disjoint ranges — verify it against the actual producer's real output, not its name or apparent shape. An unproven invariant baked into a data structure or algorithm is a latent bug waiting for the one input that breaks it.

> Rule of thumb: when you find yourself adding the third `if` to a function, stop and ask what data structure would have made all three unnecessary.

---

## 2. Control Flow — Shallow and Straight

> *"If you need more than 3 levels of indentation, you're screwed anyway, and should fix your program."* — Linux kernel coding style

- **Return early.** Handle invalid input and error conditions at the top, then let the happy path run unindented at the bottom. Guard clauses beat nested `if`/`else`.
- **Max 2–3 levels of nesting.** Deeper than that means a function is hiding inside your function — extract it.
- **Keep functions short and single-purpose.** The maximum reasonable length of a function is inversely proportional to its nesting and branching. A function that validates *and* transforms *and* persists is three functions.
- **One concern per function, one job per module.** Unix philosophy: do one thing, do it well, and compose.
- **No nested ternaries, no clever one-liners.** A four-line `if`/`else` a stranger can read at 3 a.m. beats a dense expression you are proud of.
- **No dead weight.** A variable assigned once and immediately returned, a flag set and checked in the same block, an intermediate collection built then immediately consumed — inline or delete it.

---

## 3. Names, Constants, and Magic

- **A name describes what the thing IS**, using the same word the rest of the system already uses. If the surrounding contract calls it `count`, do not call it `total` here.
- **Name a boolean or predicate for its effect, not the condition it inspects.** This bites hardest on skip/exclude/negate flags — a name framed as an allow-list that's actually read as "skip when true" reads backwards from what it does. Read the call site out loud; if the sentence says the opposite of what runs, rename it.
- **No fancy verbs that hide the concept** — `enrich`, `process`, `handle`, `transform`, `prepare`, `manage`. Say what actually happens: `appendTimestamp`, `parseHeader`, `retryOnce`.
- **No vague nouns** — `data`, `info`, `obj`, `thing`, `config`, `manager`, `util`. Name the content.
- **No `tmp` / `new` / `old` prefixes** unless the thing is genuinely temporary. They age into lies the moment a `newer` one appears.
- **No magic literals.** Every literal with semantic meaning is a named constant. The name explains *what*, not the value (`MAX_RETRIES = 3`, never `THREE = 3`).
- **Numeric constants carry their unit in the name** — `FLUSH_INTERVAL_MS`, `SESSION_TTL_SECONDS`, `MAX_PAYLOAD_BYTES`. A bare `TIMEOUT = 30` is a future incident.
- **Don't hardcode environment-dependent values** — addresses, IDs, paths, region/host names. Funnel them through one place (config, a builder, an injected parameter). Library/shared code must *receive* them, never assume them.

---

## 4. Comments — Why, Not What

> Explain something the next reader cannot get from the code itself. Anything else is noise that rots.

| Keep | Strip |
|---|---|
| **WHY** a non-obvious decision was made | Restating what the line plainly does |
| Business / domain / spec rules and their source | Self-justifying narration ("done intentionally", "for safety") |
| Correctness / security / concurrency invariants | Caller-context guesses ("called by X after auth") — a function can't know its caller |
| The reason an edge case exists at all | Filler: "deliberately", "explicitly", "for clarity", "of course" |
| Links to a spec, ticket, or standard | Hedge prose: "this allows us to", "in order to ensure", "we simply" |
| A genuine TOCTOU / race window worth flagging | Claiming "atomic / thread-safe" when only an inner block is |
| | Decorative banners, ASCII art, emoji in source |
| | Line-number references inside comments — they rot in one edit |

- **A comment that restates the type signature or the function name adds nothing — delete it.**
- **Every `TODO` needs an owner or a ticket link.** A bare `// TODO: handle errors` is conversational filler with no intent behind it; it is a merge blocker, not documentation.
- **Mark a deliberate shortcut with its ceiling, not a bare TODO.** A known corner cut on purpose — a global lock, an O(n²) scan, a naive heuristic — needs the limit and the trigger to revisit named right in the comment: `// shortcut: global lock, move to per-key locks if throughput matters`. Naming the ceiling is what stops "later" from quietly becoming "never".
- **A comment justifying a value must still match that value.** A stale justification next to a flag or config setting that was flipped since is worse than no comment — it actively lies to the next reader. Check the two agree before shipping.
- If the code needs a paragraph to explain *what* it does, the code is wrong — rewrite the code, don't annotate it.

---

## 5. Errors — Let It Crash

A crash at the point of a bug is a gift: it has a stack trace and a clear cause. A silently swallowed error is a debugging session three weeks from now.

- **Fix the root cause, not the symptom.** A bug report names a symptom — the failure someone saw, not where it originates. Before you patch, grep every caller of the function you're about to touch. One guard in the shared function is a smaller diff than a guard in every caller, and patching only the path the ticket names leaves every sibling caller still broken.
- **Don't catch what signals a real bug.** If data was validated where it was written, don't re-validate it on read "just in case". If an invariant is broken, you *want* the loud failure.
- **No catch-and-continue that hides corruption.** Skipping a "maybe malformed" record that should never be malformed only delays and disguises the problem.
- **Never swallow.** A bare `catch {}` is forbidden. Always bind the error and log it with context. If you truly can recover, say *why* in one line.
- **Silent degradation is a swallowed error with extra steps.** Code that meets input violating an assumption and computes a plausible-looking wrong result (`NaN`, empty, a quiet default) instead of surfacing the violation crashes nothing and logs nothing — the feature just quietly stops working. Make the violation visible — throw, return `null`, assert — anything but continuing on a value you know is wrong.
- **Never leak raw internals to the outside.** Stack traces, queries, internal addresses, tokens — log the full error on your side, return a sanitized, generic message to the caller.
- **Don't copy a guard from one path into another** without re-deriving the reason it exists. If you can't state in one plain sentence what *this* path is being protected from, the guard doesn't belong here.
- **Don't over-defend.** A wall of `if (!x) return` checks for conditions that cannot occur is noise that hides the checks that matter.

**Legitimate places to be defensive** (and you must say why in a comment): a best-effort bulk loop where one bad item must not kill the batch; data from a human-edited source (hand-edited config, manual DB rows, env vars) that carries no write-time invariant; a genuine trust boundary (untrusted network/user input).

---

## 6. Resources — Free What You Allocate

This is the C programmer's discipline, and it applies everywhere, garbage collector or not.

- **Every acquire has a matching release on every path** — success *and* error. Every open file gets closed, every lock unlocked, every timer/interval cleared, every listener removed, every connection/stream closed.
- **Pair them visibly.** Allocation and cleanup should be obvious to a reader scanning the function. If the release is far away or conditional, it will eventually be skipped.
- **Bound everything that can grow** — buffers, caches, queues, retry loops, recursion depth. Unbounded growth is a memory leak or a denial-of-service with a delay.
- **Exclusive ownership, not just bounded growth.** Two processes racing to write the same target — build watchers, cron jobs, migrations — corrupt it silently even when each one is individually correct. Before starting a new concurrent/background process, check nothing else already owns its output.
- **Respect the machine.** Cycles, memory, file descriptors, and bandwidth are finite. Frugality is not premature optimization; it is basic respect for the hardware you don't own.

---

## 7. Dependencies & the Whole Stack — No Magic

> A hacker understands the layer below the one they work in. If you can't read it, you can't fix it, and you don't really control it.

- **A dependency is a liability you don't maintain.** Before adding one, ask: can I do this in a few lines myself? Is the dependency bigger than my problem? Three lines of code you own beat a transitive tree of forty packages you don't.
- **Depend on public interfaces, never on internals** — not on another module's private helpers, not on build output, not on undocumented behavior. If you need something that isn't exposed, expose it deliberately; don't reach behind the curtain.
- **Default to unexported.** A new function, type, method, or field starts private/package-scoped. Export it only when a real caller outside the module needs it *now* — speculative exporting turns an internal detail into a public contract you must keep stable forever, and one you now have to grep the whole tree for before you can safely change.
- **Re-declaring a shared constant in two places invites drift.** Import it from its one canonical home.
- **Pin and vet what you pull in.** Don't run code you've never looked at on a whim, and don't trust a brand-new release blindly — fresh packages carry both bugs and supply-chain risk.
- **Verify every import resolves and every API you call actually exists.** Hallucinated package names, methods removed two versions ago, typosquatted lookalikes — build/lint/type-check *before* you reason about logic. Code that doesn't compile isn't a starting point for review.
- **Scope a suppression to the exact offender.** A lint-disable, type-ignore, or rule override belongs on the line or block that needs it, not a whole file or directory — widen it only when you've shown the false positive recurs throughout that scope.
- **Verify a boundary the way its real consumer will cross it.** If the change alters what gets exported, published, or packaged, run the actual built artifact the way a consumer installs it, not the source tree through a dev symlink or workspace link. A linked dev environment resolves paths a shipped package can't — that gap is exactly where packaging bugs hide until a real consumer hits them.

---

## 8. Don't Reinvent, Don't Duplicate

- **Grep before you write.** Before adding a helper for a known job (build an address, parse a path, generate an ID, check a permission, deep-clone, log), search for the one that already exists. A ten-second search beats a thirty-minute review argument.
- **A `@deprecated` marker on an existing helper is a signpost** to the new name — follow it instead of forking a third variant.
- **Inline single-use helpers.** A helper earns its existence at 3+ call-sites or when it hides genuinely non-trivial logic. A five-line wrapper around one constructor, called once, is noise.
- **Don't fork a function just to re-shape its errors.** If a bulk routine already does the fetch-and-check work and a caller needs to know *why* one item failed, surface that through the bulk routine's return value — don't write a parallel single-item twin that redoes the same work.
- **One validator per domain.** If a schema/validator already governs a kind of data, every write path uses it. A second, hand-rolled validator beside it guarantees the two will disagree.
- **Recurse over nested data.** Any strip-secrets / mask / sanitize / clone that walks a structure must descend into nested objects and arrays. Top-level-only is a leak.
- **Plumb a setting end to end.** A flag accepted at the entry point must be honored on *every* path it touches — background loops, retries, cached lookups — not silently dropped back to a default halfway down.

---

## 9. Obey the Local Idiom

The codebase you're editing already made its choices. Consistency beats your personal preference.

- **Before adding any structural thing** — a test, a module, a command, a config entry — find the one or two closest existing examples and copy their shape: same layout, same naming, same scaffolding. Conventions that look cosmetic are usually load-bearing.
- **Diverge only with a stated reason.** "I like mine better" is not one.
- **Prefer the standard shape over a bespoke one.** If a well-known format already expresses what you need, trim it down; don't invent a parallel structure that everyone then has to learn.
- **Match the surrounding style** — indentation, bracing, error-handling pattern, the project's logger over ad-hoc prints. A diff that is half your style and half theirs is harder to read than either.

---

## 10. Tests — Prove It Breaks

- **A test that can't fail is a lie.** Stub the implementation (`return null` / `return []` / throw). Any test that still passes was asserting the code's current shape, not the required behavior — rewrite it.
- **Test behavior, not implementation.** Assert the observable contract, not the internal call sequence; the latter breaks on every harmless refactor and protects nothing.
- **One reason to fail per test.** When it goes red, the name alone should tell you what broke.
- **Cover the edges you actually have** — empty, one, many, boundary, malformed-from-a-trust-boundary — not a pile of near-identical happy-path cases.
- **No fixed sleeps to dodge a race or a rate limit.** Wait on the actual signal — an event, a polled condition, a documented retry-on-error contract — with a timeout, not a guessed delay. A guessed delay is either too short (flaky) or always fully paid (slow), and only covers the case it was tuned against.
- **A protective or cleanup wait belongs in `finally`.** One that only runs after a successful assertion isn't protecting anything — the one time it's needed most, a failure upstream skips it.
- **Load-bearing behavior verified only by hand or locally, never in CI, is one merge from silently regressing.** A security boundary, a cross-service integration, a concurrency invariant — if nothing automated re-checks it, the next change to touch that area has no signal until it breaks in production. Run it in CI, even a scaled-down version, or name in the PR exactly what stays unverified and why.

---

## 11. Simplify, but Don't Over-Simplify

Minimalism is the goal; mutilation is not.

- **Keep an abstraction that names a real, non-obvious concept** even if it's used once — a good name is documentation.
- **Don't merge unrelated concerns** to save lines. Validation, transformation, and I/O crammed into one block is harder to reason about, not easier.
- **Explicit beats compressed.** A 20-line function that reads straight down beats a 10-line one dense with chained ternaries.
- **Simplification is structural only.** If you changed behavior while "cleaning up", you went too far. Re-run the tests; the output must be identical.
- **A mechanical conversion is a draft, not the diff.** Reshaping code's container (statement list to array, class to functions) while preserving old scaffolding — wrapper closures kept only for block-scoping, adapter shims, redundant IIFEs — leaves dead weight that made sense in the old shape and not the new one. Follow the mechanical pass with a cleanup pass.

---

## ✅ Pre-flight Checklist

Walk these before you call it done.

**Before writing a function**
- [ ] Is there a data structure that removes the special cases entirely?
- [ ] Does the name say what it IS, with the system's existing vocabulary?
- [ ] 4+ parameters → group them; the call-site should read clearly.
- [ ] Early returns planned; nesting stays at 2–3 levels.
- [ ] Magic literals named; numeric constants carry their unit.
- [ ] Did I grep for an existing helper? If used 1–2 times, inline it.
- [ ] New function/type/field defaults to unexported; made public only because a real external caller needs it now.

**Before writing a comment**
- [ ] Does deleting it lose information the code doesn't carry? (No → delete.)
- [ ] Is it self-justifying, caller-context, a restated signature, or filler? → delete.
- [ ] Does every `TODO` have an owner or ticket?

**Before catching an error or adding a guard**
- [ ] What exact failure am I catching? Say it in one sentence.
- [ ] Is it a real bug I should let crash? Is the data already invariant-protected?
- [ ] Bound to `catch (error)` and logged with context — never bare, never swallowed?
- [ ] If copied from another path, does the protected-against reason apply *here*?
- [ ] Returning a sanitized message outward, full error only in the log?

**Before shipping**
- [ ] Every acquire (file, lock, timer, listener, connection) has a matching release on all paths.
- [ ] Everything that can grow is bounded (buffers, caches, queues, retries, recursion).
- [ ] No new dependency I could have written in a few lines; nothing imported from internals/build output.
- [ ] It builds / lints / type-checks; every import resolves to something real.
- [ ] I matched the local idiom; any new file mirrors an existing analog.
- [ ] The diff is as small as it can be. Could I delete more and still pass?
- [ ] I can explain every line I added with no "magic" hand-waving.
- [ ] Renamed, moved, or deleted something referenced by name elsewhere? Grepped the whole tree — code, docs, config, comments — not just what one linter happens to cover.
- [ ] Any claim about how a tool, dependency, or published artifact behaves — including the *shape* of data it produces (ID formats, naming schemes, encodings) — is checked against the real thing, not assumed from a name or recalled from memory.

---

## See also

- [`create-pr`](../create-pr/SKILL.md) — commit format, PR title/description template.
- [`create-issue`](../create-issue/SKILL.md) — issue-writing conventions.

> Talk is cheap. Show the code. Then show *less* code.
