---
name: feature-impact
description: Scout the blast radius of a change before or after writing it — name the shared seam it touches, enumerate from the code every mode, flag, header, and consumer already flowing through that seam, then classify each as works-unchanged / wired / deliberately-rejected-loudly / unknown, with evidence and test parity. Catches the classic miss where a second path is added beside an existing one and pre-existing behavior is silently left unhandled or gated out of the tests. Use when adding a parallel implementation, forking a shared entry point, introducing a new protocol/API version, transport, mode, or tier, or when reviewing any of those.
---

# Feature Impact

> The risk in a change that adds a path beside an existing one is almost never in the new code. It is in the behavior that already flowed through the old path and nobody enumerated. This skill turns that inventory into an explicit, evidence-backed artifact instead of a memory exercise.

Works in both directions: run it **before** implementing to size the real job, and **as a lens during review** to catch what the author missed. [`code-review`](../code-review/SKILL.md) scans the diff on its own terms; this skill scans everything the diff *didn't touch* but now affects.

## When it applies

- A second implementation of an existing capability (new transport, protocol revision, API version, storage backend, auth scheme).
- Any change to a shared entry point: route handler, dispatcher, factory, middleware chain, store, client wrapper.
- A new mode, flag, tier, or tenant class that rides the same code as the existing ones.
- Extending a shared config/options/context type that already flows into multiple implementations of one interface.
- Splitting a shared setup/registration routine that several entry points assembled from into pieces each entry point now reassembles on its own.
- Removing or loosening a defensive/redundant check because the one known trigger was fixed at its source — the check is a seam if more than one producer can still reach it.
- Reviewing any of the above.

If a change only touches leaf code with one caller, skip this — it has no blast radius to scout.

## Procedure

### 1. Name the seam

One line: the shared entry point(s) this change touches, and which side of it is new. Everything below is scoped to that seam.

### 2. Enumerate what already flows through it — from the code, not memory

Grep the seam and what it calls. Build the inventory from what you find, not from what you remember being there:

- Query params, headers, and body fields the handler branches on.
- Feature flags, env switches, config toggles.
- Operating modes (and their combinations — modes usually multiply, not add).
- Auth and identity variants: authenticated, unauthenticated, impersonated, service-to-service, payment- or capability-scoped.
- Tenant, plan, and tier branches.
- Shared keys and buckets: rate limits, caches, session and event stores, idempotency keys.
- Observability: counters, spans, log fields that consumers filter on.
- Persistence and cross-node/shared state.
- Downstream consumers reading the output shape: other services, UIs, SDK clients, dashboards, alerts.
- Every field on a shared options/context object passed across multiple implementations — does each one read it, or does one silently fall back to an ambient default instead?

Every branch you find is an inventory entry. A branch you can't explain is an inventory entry too.

### 3. Classify every entry, with evidence

Evidence is a `file:line`, a traced call path, a test name, or a command and its output. Not an adjective.

| Classification | Bar it has to clear |
|---|---|
| **works unchanged** | Proven — a test exercises it on the new path, or the call path demonstrably never diverges |
| **wired** | This diff handles it explicitly |
| **deliberately unsupported** | Fails *loudly* on the new path, and a test asserts the rejection |
| **unknown** | Nothing above is true yet |

### 4. Silence is the bug

"Probably fine", "nothing calls that here", and "the old path handled it" are not classifications. An entry stays **unknown** until a test, a type, or a traced call path settles it — and an `unknown` entry is a defect in the change, not a documentation gap.

### 5. Test parity

For a parallel path, every existing test covering an inventory entry needs a decision: run it against the new path, or state why it cannot apply.

**Gating a test to the old path is not a decision.** It converts unknown behavior into a green suite, which is strictly worse than a failing test — the failure was information. Every gate needs a one-line reason naming the *mechanism* that makes the case impossible on the new path (a method that doesn't exist there, a capability the protocol lacks, a field the schema drops). "It fails on the new path" is a finding to investigate, never a reason to gate.

Count the gates. A diff that gates many existing tests to the old path has usually shipped a fraction of the feature it claims.

### 6. Check the reverse direction

The new path can break the old one through anything shared: rate-limit buckets and their keying, cache and session keys, counters and their attributes, connection pools, global config, singletons, migration state. Ask what the old path now shares with a stranger.

### 7. Output

One table. Nothing else is required.

| Entry | Classification | Evidence | Test coverage |
|---|---|---|---|

Then the verdict: every `unknown`, and every gate without a stated mechanism, is a **blocker** — not a nit. Report them as such, and in review mode order them above cosmetic findings.

## Anti-patterns

| Pattern | Why it fails |
|---|---|
| Inventory built from memory or the PR description | The branches you forget are exactly the ones that break |
| "The new path is opt-in, so nothing else is affected" | Shared state, keys, and counters don't care that it's opt-in |
| Gating existing tests until the suite is green | Trades information for the appearance of progress |
| Classifying an entry from the happy path alone | Modes combine; the break is usually in a pair, not a single |
| Treating an `unknown` as a follow-up ticket | A follow-up ticket is a decision made by whoever gets paged |
| Fixing root cause and assuming a downstream guard is now dead weight | The guard protects against every producer that reaches it, not just the one fixed |

## See also

- [`code-review`](../code-review/SKILL.md) — reviews the diff itself; this skill covers what the diff omits.
- [`code-quality`](../code-quality/SKILL.md) — the content rules for the code you end up writing.
- [`check-work`](../check-work/SKILL.md) — independent verification that the work matches the request.
