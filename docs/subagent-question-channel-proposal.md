# Proposal: subagent → main-agent question channel (not implemented)

Status: **parked, documented for future reference.** Scoped out as
"super complicated right now" — revisit if a concrete need for it shows up.

## The ask

A subagent hits something it needs more context/judgment on. It calls a new
tool, blocking, that relays the question to the main agent's session. The
main agent decides how to answer — possibly escalating to the human user for
genuinely important questions — then a separate tool call resolves it,
unblocking the subagent. Multiple subagents can ask concurrently; only one
question is surfaced/answered at a time, the rest queue.

## Feasibility

The wire mechanism already exists in a near-identical shape: bash-approval
requests. Child and parent talk JSON-over-stdio (see `subagent.ChildEvent`
and `cmd/px/main.go`'s `approveViaChildBroker`/`writeChildEvent`); the child
emits an event and blocks reading its own stdin for the reply; the parent's
`SubagentTool.Execute()` reads that event in its select loop and calls back
(`child.SendApprovalSafe`). A `"question"`/`"answer"` event pair, plus a new
`child.SendQuestionAnswer`, is the same protocol — cheap to build.

## The real blocker: the main agent can't be paused mid-flight

The main agent's conversation is strictly request/response: every `tool_use`
block in a round **must** get a `tool_result` in the very next request before
the provider continues at all. The subagent's question arrives while the
main agent is already waiting on this exact tool call's result (`subagent`
is itself one of the tool calls in the main agent's current round). Asking
the *live* main-turn LLM for a fresh opinion mid-round, before that
tool_result exists, is a genuine protocol deadlock — not an engineering
inconvenience to be optimized around.

So "the main agent decides" can only mean one of three different things:

| Option | Mechanism | Genuinely "main agent decides"? | Cost |
|---|---|---|---|
| **A. Direct to human** | Reuse `approval_request` as-is — TUI popup, human answers. | No — skips LLM judgment | Free in $, interrupts human every time |
| **B. Ephemeral advisor call** | Separate one-shot inference (no shared history/cache with the live session) gets the question + context (task, recent messages/compaction summary, cwd), answers directly or escalates to human if it judges the question important. | Yes, via a stand-in call | One small side inference per question (cheap-tier model) + rare human interrupt |
| **C. Deferred real injection** | Question queues; injected into the main agent's own *next natural round* once its current one finishes; the live agent answers with full context, can itself message the human. | Yes, most faithful | Free in $, but unbounded block — could be seconds to minutes depending on what the main agent's current round is doing |

**Recommendation if this gets built: B**, with escalation-to-human as an
explicit fallback the advisor call can trigger — matches "only for super
important questions" (cheap default path, rare interrupt) without the
unbounded-block risk of C.

## Concrete pieces (option B), for whenever this gets picked back up

- New **child-only** tool, e.g. `ask_main_agent(question)` — registered only
  when `opts.Child` is true (inverse of the current subagent-tool exclusion
  in `BuildRegistry`). Blocks via a broker mirroring `childApprovalBroker`.
- `ChildEvent` gains a `"question"` type; new `child.SendQuestionAnswer(...)`
  mirrors `SendApprovalSafe`.
- `SubagentTool.Execute()`'s event switch gains a `"question"` case, parallel
  to `"approval_request"`.
- **Cross-child queueing** (the genuinely new part — everything above is
  reused from the approval flow): today's `subagentSlots` channel caps how
  many children run concurrently; this needs a second, session-wide
  single-flight broker capping how many questions are being *answered* at
  once, shared across every `SubagentTool.Execute()` goroutine in one parent
  session (wired like `usageFn`/`progressFn` — a setter called once at
  session setup).
- The advisor call itself: a side `provider.Stream` request, default
  cheap-tier model, given the question + a context bundle. Its own
  "escalate to human" path (structured output or a dedicated tool call
  within that one-shot request) falls through to option A's existing
  TUI-popup mechanism only when triggered.
- TUI: at minimum a "waiting on an answer" indicator on the subagent widget;
  the existing approval-popup pattern covers the rare escalation case.

## Open questions, unresolved

- Default advisor model/effort — hardcode cheap, or configurable like the
  bash-risk classifier's `/classifier-model` pin?
- What forces escalation to human — left entirely to the advisor's
  judgment, or does the subagent's own call carry an `importance` field
  that forces it past some threshold regardless of the advisor's opinion?
- Timeout/abandon behavior for an unanswered escalated question.
- Whether C is worth building later as a "true main agent" upgrade path.
