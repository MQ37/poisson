---
name: stacked-diff-review
description: Output format for presenting a code review as a stacked, risk-tiered summary — every file classified 🔴 critical / 🟡 notable / 🟢 routine, critical-first ordering, OLD/NEW shape diffs, numbered human explanations with explicit risks, and a leading TL;DR. Use when producing the written summary of a PR or branch-diff review. `review-pr` gathers the diff and `code-review` finds the issues; this skill formats the result.
---

# Stacked Diff Review

> The presentation half of a review. [`review-pr`](../review-pr/SKILL.md) gathers the diff;
> [`code-review`](../code-review/SKILL.md) finds and verifies the issues against
> [`code-quality`](../code-quality/SKILL.md); this skill owns how the result is **classified and
> written up**.

A good review summary is *stacked*: ordered by risk, not by file path. The reviewer reads the
dangerous changes first and the boilerplate last — or skips it. Lead with intent, surface risk,
group the noise.

---

## 1. Classify every file

Walk the diff and put each file in a tier. Critical and notable get detailed treatment; routine gets one-liners.

| Tier | What belongs here | Treatment |
|---|---|---|
| 🔴 **Critical** | Data-access queries, auth/security, permission checks, schema/data-shape changes, public API request/response shape, secrets/env, core business logic, **cache writes without expiry**, **external calls without a timeout**, **anything that can grow unbounded** (buffers, queues, retries, recursion) | Numbered human explanation + before/after code. Flag risks. |
| 🟡 **Notable** | New entry points / endpoints, config changes, dependency adds/bumps, error handling, new middleware, public type changes, **new constants without a unit in the name** | Summarize with the specifics that matter — names, values, limits |
| 🟢 **Routine** | Imports, renames, formatting, boilerplate, internal types, test scaffolding, comments, log messages | One-liner; group 5+ similar files into a single bullet |

---

## 2. Special rules

- **Data-access queries, auth/permission changes, and schema/data-shape changes are ALWAYS 🔴** — never downgrade these, no matter how small the diff.
- **Shape changes**: show OLD and NEW side-by-side so the reviewer sees exactly what changed.
- **Mixed-importance files**: split the critical part from the routine part within the same file block.
- **Every 🔴 code block gets a numbered human explanation** above it — *what* changed and *why it matters* — not just the code.

---

## 3. Produce the summary

Critical first, then notable, then routine grouped. Lead with a TL;DR that captures the overall intent in 1–3 sentences.

---

## 🎯 Example output

> Language-agnostic on purpose — the shape of a good review, not the syntax of one stack. Use pseudocode in the report when the real snippet would drown the point in noise.

```markdown
## TL;DR
Broadened user search to a case-insensitive match with an optional status filter,
added API-key rotation with a grace window, and changed `rating` from a number to
a nested object. Two of these have correctness/perf risks; see below.

---

### 🔴 `user_service` — broadened the user lookup
**What changed**: exact-match lookup became a case-insensitive match plus an optional `status` filter.

**1. Case-insensitive match bypasses the index on `email`**
The previous query hit the index directly; the new one forces a full scan.

​```
// BEFORE
users = db.find(users, { email: email })

// AFTER
users = db.find(users, { email: caseInsensitiveMatch(email), status: status? })
​```

**2. `status` is only sometimes present**
When it's absent, the composite index over (email, status) is only partly usable.

**⚠️ Risks**
- Case-insensitive match → full scan on a large table
- No timeout / row limit on the query — an unbounded external call (code-quality §6)

---

### 🔴 `auth` — API-key rotation grace window
**What changed**: key validation now accepts the current OR the previous key within a time window.

​```
// BEFORE
if key != current_key: reject()

// AFTER
valid    = key == current_key or key == previous_key
in_window = now() - rotated_at < ROTATION_GRACE
if key == previous_key and not in_window: reject()
​```

**⚠️ Risks**: no upper bound on `ROTATION_GRACE` — a misconfigured value keeps revoked keys valid forever.

---

### 🔴 `output_schema` — data-shape breaking change
**What changed**: `rating` went from a flat number to a nested `{ average, count }` object.

​```
// OLD
rating: number

// NEW
rating: { average: number, count: number }
​```

**⚠️ Risks**: breaking for every consumer that reads `rating` as a number — a comparison like `rating > 4` silently becomes always-false against an object.

---

### 🟡 `routes` — new search entry point
Added a search endpoint taking `email`, `status`, `limit`. Input validated; rate-limited to 10/min.

---

### 🟢 Routine
- `types` — new search-params type
- `format` — auto-format only, no logic
- `user_service_test` — 3 new cases for the search path
- manifest — added a validation lib, bumped the DB driver one minor version
```

---

## ✅ Checklist

Before posting the summary:

- [ ] Every file classified 🔴 / 🟡 / 🟢.
- [ ] Data-access / auth / schema changes flagged 🔴 regardless of size.
- [ ] Shape changes shown OLD + NEW side-by-side.
- [ ] Every 🔴 block has a numbered human explanation and explicit risks.
- [ ] Routine files grouped (5+ similar → one bullet).
- [ ] TL;DR captures overall intent in 1–3 sentences.

---

## 📚 See also

- [`review-pr` skill](../review-pr/SKILL.md) — gathers the diff.
- [`code-review` skill](../code-review/SKILL.md) — finds and verifies the issues this skill formats.
- [`code-quality` skill](../code-quality/SKILL.md) — the content rules for what to flag.
