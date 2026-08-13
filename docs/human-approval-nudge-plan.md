# Plan: nudge the model when a human had to approve a command

## Problem

When a bash/file/sandbox action needs a live human approval, the human
answers it — but the model never learns that happened. It has no signal to
prefer a lower-friction path next time, so it can keep tripping the same
prompt turn after turn.

## Feature impact analysis

### The seam

Every approval path in poisson (bash risk gate, sensitive-path file check,
sandbox mount/env check, subagent relay) already funnels through one choke
point: `tui.TUI.Approve`, which calls `tools.RecordApproval(ctx, allowed)`
on the `*tools.ApprovalRecord` attached to that call's context via
`tools.WithApprovalRecord` (see `internal/tools/context.go`). That record is
already read back in `agent.go`'s dispatch loop to set `OutputEvent.HumanApproval`
("approved"/"denied"/"" for never-asked), which drives the TUI's ✓/✗ glyph.

This plan adds one more consumer of the exact same record: instead of only
turning it into a UI glyph, also append a short note to the **wire-format
tool_result text sent back to the model** (never to the UI-facing
`ToolResultContent`/`ToolError` fields, which stay exactly as they are today).

### Inventory — every existing approval path through this seam

| Entry | Classification | Evidence | Test coverage |
|---|---|---|---|
| Main-turn bash approval (`agent.go` dispatch loop, `runTool`) | **wired** by this plan | `agent.go:1404-1470`, persistence loop `agent.go:1522-1600` | new test: nudge text present when `Asked=true` |
| Main-turn sensitive-file approval (read/write/edit/glob/grep via `FileApprovalFn`) | **works unchanged** (same choke point/ctx as bash) | `checkSensitivePath` → same `humanApproval` → `tui.TUI.Approve` → same `ApprovalRecord` read in `runTool` | covered by same dispatch-loop test, tool-agnostic |
| Main-turn `create_sandbox` mount/env approval (`SandboxApprovalFn`) | **works unchanged** (same choke point) | `main.go:474-476` (`sandboxApprovalFn` calls `humanApproval` with the call's own `ctx`) | covered by same dispatch-loop test, tool-agnostic |
| `batch` tool nested calls (bash/edit/write/create_sandbox/sandbox_cp) | **works unchanged** — `batch.go:231` reuses the outer `ctx` unmodified, so a nested approval marks the *outer* `batch` call's record, which the persistence loop reads the same way | `batch.go` doc comment + existing `HumanApproval` marker behavior for batch | existing behavior, unchanged by this plan |
| Subagent-internal bash/file approval, relayed to parent (`subApprovalFn`) | **deliberately unsupported**, pre-existing and documented | `main.go:462-467`: explicit comment — no in-flight ctx tied to a toolCallID in the parent process, so `RecordApproval` is already a no-op; the parent's own "subagent" tool_result never gets a marker either | pre-existing gap, not introduced or widened here |
| Subagent child's *own* internal dispatch loop (child process, its own bash/file calls) | **was unknown → now wired** | Child's `humanChildApproval` (`main.go:904-925`) never called `tools.RecordApproval`, unlike the parent's `tui.TUI.Approve` — so the child's own `ApprovalRecord` stayed `Asked=false` even when a human really was asked via the relay. Fixed below. | new unit test on the extracted helper (see §4) |
| Headless `-p`/`--yolo` mode's own approval callback (`main.go:320-322`, `runPrint`) | **works unchanged, by design** | Returns `(yolo, "")` directly, never calls `RecordApproval` — comment already states why: "no live human to mark". `Asked` stays `false`, so no nudge — correct, since no human was actually interrupted. | pre-existing, unaffected by this plan |
| `/btw` side-question tool dispatch (`quickanswer.go`) | **was unknown → now wired** | `runQuickAnswerLoop` builds `callCtx` without `tools.WithApprovalRecord` at all (`quickanswer.go:161`), so `RecordApproval` was already a documented no-op (ephemeral, no tool card). Since /btw allows `read` (sensitive-path gated) and `bash` (risk gated), it can still trigger a real human prompt. Fixed by wrapping its `callCtx` the same way and appending the nudge to its local `block.ToolResult`. | new test in `quickanswer_test.go` |

### Reverse direction — can this break the *old* path?

The nudge only appends text to the wire `tool_result` block already built by
existing code (`toolBlock.ToolResult += ...`), the same mechanism
`contextInjectionForFile`, `expediteNudge`, and the queued-user-message
already use on that exact field. It never touches `OutputEvent.ToolResultContent`/
`ToolError` (UI/store display) or `ApprovalRecord` itself (read-only here). No
shared key, cache, or counter is touched. Risk of regressing the existing
approved/denied UI glyph: none — this plan only adds a reader of the same
record, after the glyph's own reader already ran.

Two real consequences, surfaced by review, that need an explicit decision
rather than silence:

1. **Resume replays the nudge as part of the tool card's content.**
   `tui/hydrate.go`'s resume path reads the tool card's displayed content
   straight from the stored wire `tool_result` (`parseHydratedToolResult`),
   the same field the nudge is appended to. It deliberately blanks the
   *marker* (`completeToolCall(..., "", 0)` — "a resumed session never
   replays the live approval prompt") but content text is a single field
   shared between wire and display, and `contextInjectionForFile` and
   `expediteNudge` already ride along on resume today for the same
   structural reason. **Decision: accept this**, consistent with existing
   precedent — the nudge is plain, non-sensitive text, and treating it
   differently from the other three appends already on this field would be
   the actual inconsistency. Not a regression this plan introduces; it is
   the pre-existing shape of that field, extended to one more case.
2. **A `batch` call with more than one approval-gated step**: `batch.go`
   reuses the outer `ctx` for every nested call, so `RecordApproval` last-
   write-wins onto the *one* `ApprovalRecord` attached to the outer `batch`
   tool call — mirroring how the existing approved/denied glyph already
   only ever shows one verdict for the whole batch. A worded nudge that
   branches on `Allowed` ( "prefer an alternative" vs. "don't just retry" )
   would then risk describing the wrong outcome when two gated steps inside
   one batch disagree (one approved, one denied). §1 below removes this by
   construction: the nudge text does not branch on outcome at all, so
   whichever step's `RecordApproval` call wins the race, the text is
   equally true ("something in this call needed a human") regardless of
   which specific step it was. No batch-specific code needed.

## Design

### 1. One nudge string, reused everywhere (no per-call-site duplication)

New file `internal/agent/approval_nudge.go`:

```go
package agent

import "github.com/mq37/poisson/internal/tools"

// humanApprovalNudge is appended to a tool_result's wire content when a
// human was actually asked to approve this call (see tools.ApprovalRecord)
// — "" when never asked, so the vast majority of tool calls (guard/LLM
// auto-approved) are unaffected.
//
// Deliberately the same text whether approved or denied, and deliberately
// not naming the specific command: a denied call's own ToolError already
// carries the specific rejection reason, and a `batch` call's nested
// approval-gated steps all share one ApprovalRecord (see batch.go) — a
// wording that branched on outcome could describe the wrong outcome when
// a batch's gated steps disagree. "Something here needed a human" is true
// regardless of which step or which verdict.
const humanApprovalNudge = "\n\n[This required human approval before running. " +
	"Prefer an approach that doesn't need manual approval when a safe " +
	"equivalent exists, so the user isn't interrupted unnecessarily. If this " +
	"action genuinely has no safer alternative — e.g. a real destructive or " +
	"critical host operation, a genuinely necessary sandbox mount — asking " +
	"was correct and no change is needed.]"

// humanApprovalNudgeFor returns humanApprovalNudge when rec shows a human
// was actually asked, else "".
func humanApprovalNudgeFor(rec *tools.ApprovalRecord) string {
	if rec == nil || !rec.Asked {
		return ""
	}
	return humanApprovalNudge
}
```

This is the single source of the wording — every call site below just calls
`humanApprovalNudgeFor(rec)` and appends the (possibly empty) result.
Mirrors the existing `expediteNudge` const pattern one function up in the
same file (`agent.go:949`).

### 2. Wire it into the main-turn dispatch loop (`agent.go`)

- Add `approvalRecs := make([]*tools.ApprovalRecord, len(toolCalls))` next to
  the existing `results := make([]tools.ToolResult, len(toolCalls))`.
- In `runTool`, right after `callCtx, approvalRec = tools.WithApprovalRecord(callCtx)`,
  add `approvalRecs[idx] = approvalRec` (safe: distinct index per goroutine,
  same pattern already used for `results[idx]`).
- In the persistence loop (building `toolBlock`), after the whole
  error/content if/else (so it applies to both the success branch, which
  also runs `contextInjectionForFile`, and the error/denied branch, which
  doesn't), add:
  ```go
  toolBlock.ToolResult += humanApprovalNudgeFor(approvalRecs[i])
  ```
  Ordering relative to the other two appends (`expediteNudge`,
  queued-user-message) doesn't matter functionally; placing it right after
  the if/else keeps "about this specific call" text together, before the
  turn-level nudges.

No change to `OutputEvent`, `HumanApproval` field, or `TrimToolResult` —
the nudge is appended to the wire content built for the *next* provider
request, same place `expediteNudge` already lives, entirely after
`TrimToolResult` already ran on `result.Content`/`result.Error`.

### 3. Wire it into `/btw` (`quickanswer.go`)

- Change `callCtx := WithApprovalOrigin(...)` to also wrap with
  `tools.WithApprovalRecord`, capturing the returned record.
- After building `block.ToolResult`/`block.ToolIsError` (both branches),
  append `humanApprovalNudgeFor(rec)`.

This is intra-request only (never persisted — `/btw` never touches the
store) but still helps a multi-round `/btw` answer (up to `btwMaxToolRounds`)
avoid re-tripping the same prompt within the same side question.

### 4. Fix the child-mode gap (`cmd/px/main.go`)

`humanChildApproval` is a closure defined inline inside `runChildMode`, not
a standalone function — not unit-testable as-is. Extract the one line that
needs to change into its own function, next to `childApprovalBroker` in
`child_approval.go`, so the fix is directly testable without spinning up
the whole child-mode stdin/stdout protocol:

```go
// approveViaChildBroker asks the parent for approval through broker and
// records the outcome on ctx's ApprovalRecord (see tools.RecordApproval),
// mirroring what tui.TUI.Approve does for the parent's own direct prompts —
// without this, the child's own dispatch loop (same agent.go code, running
// as its own process) never saw Asked=true even when a human really was
// asked via the relay, so its own tool_results never got the human-approval
// nudge (see agent.humanApprovalNudgeFor).
func approveViaChildBroker(ctx context.Context, broker *childApprovalBroker, event map[string]interface{}) (bool, string) {
	allowed, reason := broker.emitAndWait(event)
	tools.RecordApproval(ctx, allowed)
	return allowed, reason
}
```

`humanChildApproval` changes its one-line body from
`return approvalBroker.emitAndWait(event)` to
`return approveViaChildBroker(ctx, &approvalBroker, event)`. Everything else
(building `event`, banking usage) is unchanged.

This makes the child's *own* dispatch loop correctly see `Asked=true`, so
its own bash/file tool_results get the nudge too — helping a long-running
subagent avoid repeating an approval-triggering pattern within its own
turns. It does **not** change the parent's view of the subagent (that gap
stays, deliberately, per the inventory above).

### 5. Explicitly out of scope

- Parent-side subagent relay marker (`subApprovalFn`) — pre-existing,
  documented, unrelated to this plan; fixing it would need a real
  cross-process `ApprovalRecord` correlation that doesn't exist today and is
  a separate, larger change.
- Any system-prompt wording change — the nudge is self-explanatory injected
  text, not a system-prompt policy. No change needed there.
- Persistence/TTL/cleanup — the nudge is plain text appended to a tool
  result already going through the same store/compaction path as everything
  else; no new resource, no new cleanup surface.

## Test plan

1. `internal/agent/human_approval_marker_test.go` (or a new
   `approval_nudge_test.go`) — extend `TestDispatchMarksHumanApproved`/`Denied`
   assertions, or add new tests, to check the *next* request's tool_result
   wire content (not just `OutputEvent.HumanApproval`) contains the nudge
   when asked, and `TestDispatchNoMarkerWhenNeverAsked`'s case contains no
   nudge text.
2. Unit test for `humanApprovalNudgeFor` directly: nil record, `Asked=false`,
   `Asked=true` (either `Allowed` value returns the same constant).
3. `quickanswer_test.go` — new test: an approval-triggering tool call inside
   `/btw` produces a tool_result (in the request sent for the next round)
   carrying the nudge.
4. `cmd/px/child_approval_test.go` — new test directly on
   `approveViaChildBroker`: attach `tools.WithApprovalRecord` to a
   background ctx, drive the broker's queue/response like the existing
   broker tests do, and assert the record's `Asked`/`Allowed` match the
   broker's answer.

## Rollout

Single PR-sized change, no config flag, no migration. Purely additive text
in an existing wire-content append chain — the kind of change the codebase
already has four instances of on that exact field.
