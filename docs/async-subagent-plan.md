# Async Subagent Plan

Rework `subagent` from a blocking tool call into a spawn/poll/retrieve job:
model calls `subagent`, gets a job ID back immediately, keeps working (or
lets the turn end and the user keep chatting) while the child runs in the
background, then calls `subagent_status`/`subagent_result` later.

No related plan exists in the repo today. Two adjacent-but-different docs:
`docs/server-mode-plan.md` (multi-session HTTP server, unrelated axis) and
`docs/subagent-question-channel-proposal.md` (child asks parent a question
mid-run, parked as "too complicated" — different problem: that one is about
the CHILD blocking on the PARENT; this one is about the PARENT not blocking
on the CHILD).

No new Go dependency (repo policy). Everything below is goroutines,
channels, and a mutex-guarded map — stdlib only.

## Current architecture (what actually happens today)

`internal/tools/subagent.go`'s `SubagentTool.Execute` is a single function
that: validates input → resolves provider/model/effort → spawns a child
process (`internal/subagent.Spawn`) → **blocks in a for-loop reading the
child's stdout events** (text, tool progress, approval requests it relays
to a human, usage) until a `"done"`/`"error"` event → records cost →
returns one `ToolResult`.

That block matters because of two things `internal/agent/agent.go`'s turn
loop does that no other layer can bypass:

1. **A round cannot complete until every `tool_use` in it has a
   `tool_result`.** (`agent.go`'s `runTool`/`emit` — every tool call,
   `subagent` included, gets exactly one `OutputToolResult` right after
   `Execute` returns, and the provider is not called again for the next
   round until all of them have.) So today, spawning a subagent — even as
   one of several parallel tool calls — stalls the *entire* turn, blocking
   the model from replying to the user or doing anything else, for however
   long the child takes.
2. **The `context.Context` passed into `Execute` dies with the turn, not
   the round.** `internal/tui/agent_io.go`'s `startTurn` creates
   `ctx, cancel := context.WithCancel(context.Background())` once per
   `Prompt` call and unconditionally calls `cancel()` in the turn
   goroutine's deferred cleanup the instant `PromptSegmentsWithContext`
   returns (`agent_io.go:94,111`). A background goroutine that outlives
   `Execute` but still holds this ctx would be killed within microseconds
   of the (now-quick) turn ending — this is the load-bearing reason a naive
   "just return early and keep a goroutine running" patch does not work
   without also introducing a longer-lived context.

Everything else already proves the pieces exist and work concurrently:
`maxConcurrentSubagents`/`subagentSlots` caps 8 children process-wide;
`trackLive`/`untrackLive` + `ExpediteAll` (Ctrl+G) already manage a set of
live children independent of any one `Execute` call; `agent.go`'s
`CompleteBatchedSubagent`/`SendSubagentProgress` already push `OutputEvent`s
onto the render channel **from a goroutine that isn't the current round's
tool dispatch at all** (batch.go's nested subagent calls use exactly this
today) — proof the TUI-notification path is already decoupled from "this
tool call's own `Execute` is what completes the widget."

## Proposed design

### A. A session-scoped background context

`SubagentTool.SetBackgroundContext` (implemented) takes the ctx a job's
`runJob` goroutine actually runs on — distinct from the per-call `ctx`
`Execute` receives, which is only used for the synchronous validation +
spawn-attempt part (so `Ctrl+C` during that narrow window still works) and
is never referenced again once a job is registered. **Not wired to
anything in `cmd/px/main.go` yet, deliberately**: this repo has no
graceful-shutdown/signal-handling infrastructure today, so the only
context `main.go` could hand it is `context.Background()` — exactly
`SetBackgroundContext`'s own documented nil-fallback. Wiring it to a real
cancellable context becomes worth doing the day a "kill all my subagents"
action or graceful-shutdown signal handler exists; until then it's a
tested seam (see `subagent_bgctx_test.go`), not dead code — tests inject a
controllable ctx directly via the setter to prove `runJob` really does
survive past `Execute`'s own return and past the spawning turn's own
context being cancelled.

### B. `SubagentTool.Execute` becomes spawn-and-return

Keep all of today's synchronous validation (sandboxIds resolution,
provider/model/effort override + cross-provider approval, config lookups)
— none of that needs to change. What changes is everything after
`subagent.Spawn` succeeds:

```go
type subagentJob struct {
    id, name, task, provider, model, effort string
    status      atomic.Value // "queued" | "running" | "done" | "error"
    child       *subagent.ChildProcess
    output      strings.Builder // guarded by mu
    mu          sync.Mutex
    turns, toolCount, contextTokens, contextWindow int
    tokensPerSec float64
    cost         float64
    costKnown    bool
    err          string
    startedAt, doneAt time.Time
    retrieved   bool // set by subagent_result on first successful "done" fetch
}
```

- `jobID := store.NewSubagentID()` (already the exact generator used for
  the ephemeral child session ID — reuse it, don't invent a second ID
  scheme).
- Try `subagentSlots` **non-blocking** (`select ... default:`). Available
  now → spawn immediately, status `"running"`. Not available → register
  the job as `"queued"` and let the background goroutine block on the slot
  itself; `Execute` still returns instantly either way — a full queue is
  visible via `subagent_status`, not a stall.
- `go t.runJob(t.bgCtx, job)` — this is today's entire event-loop body
  (spawn if not yet, `child.ReadEvent` loop, approval relay via
  `t.approvalFn`, `bankUsage`/`recordUsage` via `t.usageFn`,
  `trackLive`/`untrackLive`, `child.Reap`) moved verbatim into a method,
  writing into `job` instead of building a return value, and firing the
  TUI completion signal (see D) when it reaches `"done"`/`"error"` instead
  of returning it.
- `Execute` returns immediately:
  `ToolResult{Content: fmt.Sprintf("Subagent %q spawned as job %s (status: %s). Use subagent_status to check progress, subagent_result to retrieve the final output once done.", agentName, jobID, initialStatus)}`.

### C. Two new tools

- **`subagent_status`** — input: optional `jobId`. No `jobId`: table of
  every job this session has spawned (id, name, status, turns,
  context %, tokens/sec, elapsed). One `jobId`: same, one row, plus a short
  note when `status == "done"` ("ready — call subagent_result").
- **`subagent_result`** — input: required `jobId`. `status != "done"/"error"`
  → error result ("still {status} — check subagent_status"), no blocking
  (the model can just ask again later; blocking here would reopen exactly
  the round-stall problem this whole rework removes). `status == "done"`
  → the full formatted result text (identical shape to today's synchronous
  return: output + tool/turn counts + cost + "Ran on provider/model"), and
  sets `job.retrieved = true`.
  `status == "error"` → the error text, same one-shot marking. **Decision:
  one-shot.** A second `subagent_result` call on a `jobId` already marked
  `retrieved` returns `ToolResult{Error: "job <id> result already
  retrieved"}` instead of repeating it — `subagent_status` stays a free,
  repeatable read for progress-checking; `subagent_result` is a
  single-use handoff, so the model can't lean on it as a memory jog
  instead of actually using/remembering the answer.

Both are cheap, synchronous, in-memory map reads/writes — no process
spawn, no approval, no cost.

### D. TUI: decouple widget completion from the wrapping tool call

This is the one genuinely new wrinkle, not just "move code to a goroutine."
Today, `agent.go`'s generic per-call `emit(res)` (used by *every* tool,
`agent.go:1532`) is what fires the `OutputToolResult` event the TUI reads
to mark a subagent card done (`tui/subagent_card.go`'s
`completeSubagentCard`, matched by `ProviderCallID`). With async spawn,
that generic event now fires almost instantly, carrying the spawn-ack text
— if left unchanged, the card would flip to "done" the moment the job is
merely *queued*, showing the ack sentence as if it were the real result.

Fix, mirroring the pattern `batch.go`'s nested-subagent handling already
uses (`CompleteBatchedSubagent`, called explicitly, *bypassing* the
generic per-call `emit`): give the ack a distinguishable shape — either a
new `OutputEvent` type (`OutputSubagentSpawned`) that `agent.go`'s `runTool`
emits instead of `OutputToolResult` specifically when `call.Name ==
"subagent"` and the result carries a new `ToolResult.Async bool` marker, or
(simpler, no new field on the shared `ToolResult` type) have the TUI's
subagent-card completion switch treat any `OutputToolResult` for
`ToolName == "subagent"` as a **status update, not a completion** — set
`SubagentJobID` from the ack text (regex-extracted, same pattern
`subagentCostFromResult`/`subagentRanOnFromResult` already use) — and only
`completeSubagentCard` a real `"done"`/`"error"` when the background job
loop separately calls a new `Agent.CompleteSubagentJob(jobID, res)` (a
straight rename/generalization of today's `CompleteBatchedSubagent`, reused
for both the batched-nested case and this one). The second option needs no
change to the shared `provider`/`tools.ToolResult` types at all — prefer it
unless the regex-extraction proves fragile in practice.

Either way: the card must key off the **job ID**, not the tool-call ID,
once the ack has been seen — a job's completion can now arrive turns later,
long after the original `ToolCallID` has scrolled off anyone's mental
model. `appendSubagentCard`/`completeSubagentCard`/`updateSubagentProgress`
(`tui/subagent_card.go`) all currently match on `ProviderCallID` (today
== the tool-call ID); switching that field's *meaning* to "job ID" for the
subagent-card path, while every other card type keeps using the real
tool-call ID, is a small, scoped change — but one that needs its own pass
through `render_v2.go`'s pinned-widget scan and `scrollback.go`'s
`runningSubagentLines`/`hasRunningSubagent`, both of which just filter
`kind == blockSubagent && !ToolDone` and don't care what the ID actually
identifies.

### E. Everything that should need zero changes

- `subagentSlots` concurrency cap — semantics unchanged, just acquired from
  a different goroutine.
- `ExpediteAll`/Ctrl+G — already keyed off `t.live`, populated/depopulated
  independent of `Execute`'s own blocking.
- Usage/cost recording (`usageFn`/`RecordSubagentUsage`) — same call,
  same data, just made from the background goroutine instead of `Execute`'s
  own return path.
- Sandbox authorization, cross-provider approval, model/effort override
  validation — all synchronous, all stay in `Execute` before the job is
  even created.
- `batch.go`'s nested-subagent handling — already async-shaped (see above);
  if anything it's the model the direct path should converge toward, not
  vice versa.

## Feature-impact inventory

Seam: `internal/tools.SubagentTool.Execute` (the shared entry point every
"spawn a child agent" call goes through) — new side: async spawn/poll/
retrieve; old side: today's single blocking call.

| Entry | Classification | Evidence | Test coverage |
|---|---|---|---|
| Global concurrency cap (`subagentSlots`, 8) | wired (by design, §B) | non-blocking acquire + queued status is the explicit replacement | needs new test: queued job surfaces via `subagent_status`, later transitions to running |
| Ctrl+G expedite (`ExpediteAll`, `t.live`) | works unchanged | `trackLive`/`untrackLive` calls just move into `runJob`, same map | existing `subagent_expedite_test.go` should still pass once wired to `runJob`; add one exercising expedite on a job whose spawning `Execute` already returned |
| Usage/cost rollup (`usageFn`, `RecordSubagentUsage`) | wired | recording call relocates verbatim into `runJob`'s completion path (§E) | existing `internal/agent/subagent_usage_test.go` needs a variant that records after `Execute` already returned |
| Cross-provider model/effort override + approval | works unchanged | stays entirely inside `Execute`'s synchronous prefix (§B) | existing `subagent_test.go` cross-provider suite unaffected |
| Sandbox ID authorization | works unchanged | stays entirely inside `Execute`'s synchronous prefix | existing `subagent_sandbox_test.go` unaffected |
| Bash-approval relay from child (`approvalFn`) | **resolved: works unchanged** | `/btw` is direct precedent — its side-question stream already shows an `*approvalOverlay` via the exact same `TUI.Approve`/`runModalApproval` path while the main turn may be fully idle (`agent_io.go:340-358`'s `t.currentBTW` comment block spells this out). Async subagent approvals reuse `Approve` unchanged, same `SubagentOrigin` badge already covered by `approval_origin_test.go` | existing `/btw`-while-idle coverage is the proof; add one test confirming a subagent approval origin renders correctly with `t.status.Thinking == false` |
| Batched nested subagent (`batch.go`'s `subagentDoneFn`/`CompleteBatchedSubagent`) | wired, becomes the shared mechanism | §D proposes reusing/generalizing this exact function for the direct-call path too | existing `TestBatch_ParallelSubagentsRunConcurrently` should keep passing; add coverage that a batched subagent job is ALSO independently pollable via `subagent_status`/`subagent_result`, not just fire-and-forget |
| TUI widget lifecycle (`subagent_card.go`, `ProviderCallID` matching) | wired, but changes meaning | §D — `ProviderCallID` becomes "job ID" for this card type only | needs new tests: card stays open across a turn boundary, completes on a later turn's background event |
| Turn-scoped `ctx` cancellation (`agent_io.go`'s `startTurn`) | wired | §A's session-scoped bg context is the explicit fix; without it every async job dies within microseconds of its own spawn-ack turn ending | needs a regression test proving a job survives past `PromptSegmentsWithContext` returning |
| `/undo`, `/fork`, session resume | **N/A — neither command exists** | Neither is a live slash command (`internal/tui/slash.go`'s dispatch table has no `/undo`/`/fork` case). `/undo` was built once and removed (`CHANGELOG.md`: "Remove abandoned /undo write-path (SoftDeleteMessages)"); `/fork` was only ever planned (`PLAN.md` phase 17) and never actually built — `store.Session` has no `ParentID`/`ForkPoint` fields. Originally analyzed here as a resolved-by-construction risk before this was verified against the real dispatch table; kept as a note so a future re-add of either command starts from "job map is process-lifetime, untouched by store ops" as the design baseline. Session resume alone (no fork/undo involved) still applies: the job map doesn't survive a process restart, by design. | n/a |
| Process exit / crash mid-job | deliberately unsupported (matches today) | jobs are in-memory only, same as today's live children being killed on process exit; child process itself is already reaped/killed the same way `Reap` does today | existing process-exit-kills-children behavior; no new test needed if behavior is genuinely unchanged — verify `Reap` is still called on bg-context cancellation, not skipped |
| `subagent_result` called twice on the same finished job | **resolved: one-shot** | §C — `job.retrieved` flag, second call errors `"already retrieved"` | needs a test: retrieve, retrieve again, expect the error |
| Server mode (`docs/server-mode-plan.md`), if ever built | **unknown**, not yet reachable | that plan's `liveSession` registry is a different, session-level analog of the same "job you can poll" idea — worth designing this job map so it *could* later be exposed the same way (`GET /api/sessions/{id}/subagents`), not guaranteed compatible today | n/a — future work, flagging only |

All three former blockers are now resolved decisions (rows above) — no
remaining `unknown` in this table except server-mode compatibility, which
is out of scope until that plan is picked up.

## Build order

1. **Core async loop, no TUI change — DONE.** Session-scoped bg context
   (§A, `SetBackgroundContext`, not yet wired to a real cancellable ctx in
   `main.go` — see §A's note), `runJob` extraction (§B), `subagent_status`/
   `subagent_result` tools (§C, one-shot retrieval). Covered by
   `subagent_jobs_test.go` (spawn-returns-immediately, job survives its
   spawning turn's ctx cancellation, status/result tool behavior, one-shot
   retrieval) plus every pre-existing subagent test migrated to the async
   shape (spawn → `waitForJob` → assert on the job's result, not `Execute`'s
   own return). Found and fixed along the way: two pre-existing tests
   (`TestSubagentToolSameProviderOverrideNeverAsksCrossProviderApproval`,
   `TestSubagentToolModelWithSlashButNoProviderPrefixStaysSameProvider`)
   let a real, unmocked child spawn happen and relied on synchronous
   execution to contain the fallout — async turned that into a
   cross-test output leak (a detached goroutine spawning the wrong binary
   *after* its own test had already returned and torn down its
   `SetLookupExecutableForTest` fixture); both now use a real fake-child
   script instead. Full suite (`go build`, `go vet`, `go test ./...`, `-race`
   on `internal/tools`) green.
2. **TUI widget decoupling (§D) — DONE.** Ack-vs-final distinguished by
   content (`subagentJobIDFromAck`'s regex marker — no shared-type change,
   as recommended), not a new `OutputEvent` type. `BlockMeta.SubagentJobID`
   is a second correlation key checked alongside `ProviderCallID` by
   `completeSubagentCard`, set (without completing anything) by
   `setSubagentJobID` the moment an ack is seen — so the widget stays keyed
   by its original tool-call id for progress updates (`updateSubagentProgress`
   is untouched, still keyed by tool-call id — the child's progress ticks
   always carry that, never the job id) while ALSO becoming reachable by job
   id for the real completion. `Agent.CompleteSubagentJob` (thin wrapper
   around the pre-existing `CompleteBatchedSubagent`) pushes that completion
   whenever `SubagentTool`'s `jobDoneFn` fires (wired via
   `BindSubagentJobDone` in `runREPL` only — headless/`-p` mode has no
   widget to update, so it's simply never wired there, a no-op). The batched
   nested-subagent path needed zero special-casing: it already converges on
   the identical `OutputToolResult`/`ToolName=="subagent"` wire shape via
   `CompleteBatchedSubagent`, so the same ack-detection in `agent_io.go`'s
   `handleEvent` covers both — proven by
   `TestCompleteSubagentCard_BatchedCallSameAckThenFinalPattern`. Pinned
   region across turn boundaries needed no change either:
   `scrollback.runningSubagentLines`/`hasRunningSubagent` already just scan
   for `kind == blockSubagent && !ToolDone`, which now simply stays true
   correctly longer. Not touched (deliberately, out of scope): `hydrate.go`'s
   resume-time reconstruction still shows a resumed session's subagent call
   as "done" with the ack text if that's the only tool_result stored (no
   live process exists anymore to complete it) — "ugly but correct," matches
   the phase-1 framing, not a regression.

## Open decisions

Resolved: retrieve-twice (one-shot, §C), approval-while-idle (reuse
`Approve` unchanged, proven by `/btw`). `/undo`/`/fork` turned out to be
moot — neither command exists in this codebase (see the inventory table's
N/A row); session resume alone still means the job map is lost, by design
(process-lifetime, in-memory only). Still open:

- Ack-vs-final signal to the TUI: new `OutputEvent` type + `ToolResult`
  field vs. regex-extracted marker in the ack text (recommended — no
  shared-type change, matches existing `subagentCostFromResult` precedent).
- Default job list retention: keep every job for the life of the process
  (simple, matches "ephemeral, in-memory, gone on restart" framing already
  used for the child DB) vs. capping/evicting old finished jobs. Leaning
  toward no cap initially — a session spawning enough subagents to matter
  memory-wise is a later problem, not a v1 one.
- Should `subagent_status` with no `jobId` include jobs from OTHER
  sessions/processes, or only this one? Recommended: this session only —
  jobs are an in-memory field on this process's `SubagentTool`, there is no
  cross-process registry (that's what `docs/server-mode-plan.md` would be
  for, separately).
