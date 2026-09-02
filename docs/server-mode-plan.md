# Server Mode Plan

`px serve`: run poisson as a long-lived HTTP server so sessions can be
driven from a phone browser over a web UI, with multiple sessions live in
parallel and one focused at a time — single Go binary, single codebase, no
native app, no separate frontend project.

No new Go dependency without explicit approval (repo policy, see AGENTS.md).
Design below needs none: stdlib `net/http` + Go 1.22 `ServeMux` pattern
routing, Server-Sent Events for agent -> browser streaming, plain POST for
browser -> agent. No WebSocket (see "Wire protocol" for why SSE+POST covers
it).

## Existing architecture this builds on

- `internal/agent.Agent` already runs the turn loop for exactly one session,
  decoupled from its renderer: it emits `agent.OutputEvent` (text |
  tool_start | tool_result | status | approval | error | compacting) on an
  output channel, and calls an `approvalFn` callback for risk-gated bash.
  `internal/tui.TUI` is just the first consumer of that channel+callback
  pair — a web frontend is a second one, not a fork of the TUI.
- `internal/store.Store`: SQLite, WAL, `busy_timeout=30000`,
  `MaxOpenConns(1)` per handle. Already stores every message/tool-call/
  api-call with FTS5 full-text search and session listing. One server
  process must own one shared `*store.Store` driving N `Agent`s — never N
  processes/handles against the same file (that reintroduces the
  cross-process writer race the current code only tolerates for the rare
  simultaneous-first-run schema race).
- Subagents already prove an "agent emits JSON event stream, something else
  consumes and can inject responses back" pattern — over child-process
  stdin/stdout (`internal/subagent/spawn.go`, `POISSON_SUBAGENT_CHILD=1`).
  Prior art for the shape, different transport — not directly reused.
- No HTTP server, no auth layer, no multi-session registry, no web UI exist
  today. `runREPL`/`runPrint` (cmd/px/main.go) each wire exactly one
  Agent for one process. `runServe` is new, additive — TUI/`-p` keep working
  standalone.

## A. Process / session model

```
px serve [--addr 127.0.0.1:7717] [--max-live 8] [--max-turns 2]
         [--idle-timeout 30m] [--approval-timeout 0] [--behind-proxy]
         [--new-token] [--web-dir DIR]
```

Default bind: loopback. Non-loopback without `--behind-proxy` logs a
startup warning and refuses plaintext login.

One process owns one `*store.Store`, one `provider.Provider` (shared iff
stateless, else per-session), the embedded web UI, and a registry:

```go
type liveSession struct {
    id       string
    agent    *agent.Agent
    out      chan agent.OutputEvent
    hub      *hub           // fan-out to SSE subscribers
    ring     *ring          // last 512 wire events, Last-Event-ID replay
    gate     *approvalGate  // pending approvals, see C
    tools    *tools.Registry
    status   atomic.Int32   // idle | queued | running | awaiting_approval
    lastSeen atomic.Int64
}

type registry struct {
    mu      sync.Mutex
    live    map[string]*liveSession
    store   *store.Store
    maxLive int
    turnSem chan struct{} // caps concurrent in-flight turns
}
```

Two independent caps: `--max-live` (default 8 — memory/goroutines/sandbox
count) vs `--max-turns` (default 2 — provider rate limits and spend). A
turn that can't acquire the semaphore goes `queued` and emits a `status`
event saying so, never silently stalls. `--max-live` overflow evicts the
least-recently-seen *idle* session; if none is evictable, session creation
returns `503` naming which sessions are busy.

Lifecycle: created lazily on first message/SSE attach; client disconnect has
**no effect** on the agent (turn keeps running, events keep landing in
store + ring buffer); idle eviction only applies when status==idle and zero
subscribers — running/awaiting-approval sessions are never evicted (a
locked phone screen must not kill a turn); server restart loses in-flight
turns but transcript in store is truth, sessions rehydrate on demand.

`~/.poisson/serve.lock` (PID + addr) warns a local TUI it would be a second
writer to the same DB while serve is running. `px attach` (TUI as an HTTP
client of serve, phase 6) is the real fix, deferred.

## B. Wire protocol / HTTP API

Stdlib `net/http.ServeMux`, Go 1.22 pattern routing
(`"POST /api/sessions/{id}/cancel"`). Same-origin only, no CORS.

| Method | Path | Purpose |
|---|---|---|
| GET | `/api/meta` | version, models, caps, allowed cwd roots |
| POST | `/api/login` | `{token}` -> session cookie |
| POST | `/api/logout` | invalidate cookie |
| GET | `/api/sessions?limit&offset` | list (existing store listing) |
| GET | `/api/sessions/search?q=` | FTS5 search (existing) |
| POST | `/api/sessions` | create -> `{sessionId}` |
| GET | `/api/sessions/{id}` | meta: title, status, cost, context pct |
| PATCH | `/api/sessions/{id}` | rename |
| DELETE | `/api/sessions/{id}` | archive + evict |
| GET | `/api/sessions/{id}/messages?before=&limit=` | paged transcript |
| POST | `/api/sessions/{id}/messages` | send prompt, `202 {turnId}`, output via SSE |
| GET | `/api/sessions/{id}/events` | SSE stream, honors `Last-Event-ID` |
| GET | `/api/sessions/{id}/approvals` | pending approvals (reconnect recovery) |
| POST | `/api/sessions/{id}/approvals/{approvalId}` | `{decision, scope, reason?}` |
| POST | `/api/sessions/{id}/cancel` | cancel turn + deny its pending approval |
| GET | `/api/events?session=a&session=b` | multiplexed stream (phase 6) |
| GET | `/`, `/static/*` | embedded web UI |

`agent.OutputEvent` stays the single render contract — add `json:` tags
(no behavior change), wrap in an envelope carrying what the bare struct
doesn't:

```go
type WireEvent struct {
    Seq        uint64 `json:"seq"`       // per-session monotonic, SSE `id:`
    SessionID  string `json:"sessionId"`
    TurnID     string `json:"turnId,omitempty"`
    At         int64  `json:"at"`
    ApprovalID string `json:"approvalId,omitempty"`
    agent.OutputEvent `json:"e"`
}
```

Backpressure: a per-session pump goroutine drains `out` into the hub, which
must never block the pump. Per-subscriber buffered channel (256); overflow
closes that subscriber, which reconnects via `Last-Event-ID`. Safe because
the agent already persists everything to store — a dropped SSE frame is a
display gap, not data loss. Heartbeat comment frame every 15s survives
mobile-carrier idle timeouts.

**Why not WebSocket**: browser->server traffic is discrete request/response
(send, approve, cancel, rename) — POST fits natively, gets real status
codes. Server->browser is a pure one-way append-only stream — SSE's
`Last-Event-ID` replay is free reconnect logic WebSocket would need
hand-rolled framing for. Multiplexed `/api/events` (phase 6) covers the one
real SSE limit (~6 connections/origin on HTTP/1.1) if more than a handful
of sessions are open at once.

## C. Approval handoff

```go
type pendingApproval struct {
    ID, SessionID, Command, Description, Workdir string
    Risk    agent.BashRisk
    Origin  agent.ApprovalOrigin
    Created time.Time
    resp    chan approvalDecision // buffered 1
    once    sync.Once
    decided *approvalDecision     // set after resolution, for 409 replay
}
```

Turn goroutine: mints ID, registers pending, sets session status
`awaiting_approval`, publishes an `approval` wire event, blocks on
`select { resp | ctx.Done() -> deny("turn cancelled") | shutdown -> deny("server shutting down") }`.
`POST /approvals/{id}` resolves it exactly once (`sync.Once`); a second POST
for a resolved ID returns `409` with the recorded decision (mobile
double-tap safety).

**Default `--approval-timeout` is `0` — block indefinitely.** This is the
security-relevant decision: auto-approving after a timeout is unacceptable
(executes risk-gated shell commands unattended); auto-denying after a short
timeout is also bad (model silently takes a worse, undebuggable path). So
the gate waits, durably, re-served on `GET /approvals`, with an
`awaiting_approval` badge in the session list so a forgotten gate is visible
rather than mysterious. If a timeout is configured it only ever denies, with
an explicit reason string that lands in the transcript.

Disconnect never resolves a gate. `POST /cancel` denies the pending gate
first, then cancels the turn context. Shutdown denies all pending, cancels
all turn contexts, waits bounded (5s), exits.

## D. Auth

Token is equivalent to host shell access — handle like an SSH key: never in
a URL/query string, never logged.

- Source: `POISSON_WEB_TOKEN` env var if set, else 32 bytes from
  `crypto/rand` (base64url) generated on first `px serve` and **printed
  once**. Only its SHA-256 hash is persisted, in `~/.poisson/server.json`
  (mode 0600) — compared via `crypto/subtle.ConstantTimeCompare`.
- Persisted (not in-memory-only) deliberately: a restart (crash, deploy,
  reboot) must not force retyping a 43-char token on the phone. Trade-off:
  needs an explicit revoke path, hence:
- `--new-token`: rotates the token and invalidates every cookie session
  tied to it — the only way to revoke a leaked token, since restart alone
  won't rotate a persisted one.
- Two credentials, one secret: scripts send `Authorization: Bearer <token>`
  on every request; the browser does one-time `POST /api/login {token}` ->
  `HttpOnly; SameSite=Strict` cookie (cookie map persisted so a server
  restart doesn't log the phone out). Cookies exist solely because
  `EventSource` can't set request headers.
- CSRF: `SameSite=Strict` + every non-GET request must carry a custom
  `X-PX-Auth: 1` header (blocks plain cross-site form POST, no CORS headers
  are ever emitted) + `Origin`/`Referer` check against expected host.
- No TLS in px. Recommended: Tailscale/WireGuard, bind to tailnet IP only.
  Alternative: Caddy/nginx terminating TLS in front, `--behind-proxy` trusts
  `X-Forwarded-Proto` for the cookie's `Secure` flag only from a
  loopback/configured peer. Raw public bind is not supported by design —
  loopback default, warning + plaintext-login-refusal otherwise.
- `/api/login` gets a per-IP failure counter with backoff; failures logged
  with source address.

### Dependency decision points (flagged, not decided)

| Dependency | Verdict | Note |
|---|---|---|
| WebSocket lib / hand-rolled framing | reject | SSE+POST covers everything, see B |
| `golang.org/x/crypto/acme/autocert` | reject | Caddy solves this for free |
| JS framework/bundler | reject | hand-rolled, matches TUI precedent |
| vendored `marked.js` (~50KB JS) | owner decides | default: hand-rolled fenced/inline-code subset |
| `github.com/skip2/go-qrcode` | owner decides | only saves typing the token once; recommend reject |

## E. Web UI

`internal/web/static` via `//go:embed all:static`: plain HTML/CSS/
vanilla-JS ES modules, no bundler, no framework — same posture as the
hand-rolled TUI. `--web-dir DIR` serves from disk instead, for editing
without a binary rebuild.

Mobile-first layout: off-canvas drawer (session list + FTS search, status
dot per session: idle/queued/running/awaiting-approval, cost) + top chip
strip (open sessions, unseen-event badge, one focused) + main transcript
pane + composer, with an approval sheet that slides up when the focused
session has a pending gate.

Client keeps every *open* session's SSE stream alive in the background
(`sessions[id] = { meta, events, lastSeq, stream, pending, unseen }`) even
when not focused — switching focus is instant, no refetch. That's "open
multiple, switch between them, one active" with zero server-side notion of
focus. Event rendering mirrors the TUI's event->block mapping so both
frontends stay conceptually identical.

Known gaps: no push notification if the browser tab is fully closed
(interim: title badge + beep while open; real fix is Web Push, deferred);
long tool results need history virtualization/capping on a phone.

## F. Reuse vs net-new

| Component | Verdict |
|---|---|
| `agent.Agent`, `store.Store`, FTS/session listing, `sandbox.Manager` | reuse as-is |
| `agent.OutputEvent` | adapt: `json:` tags, wrap in `WireEvent` |
| `tools.Registry` | adapt: constructed per session (sandbox manager already sessionID-tagged) |
| `provider.Provider` | reuse as-is if stateless, else per-session |
| `approvalFn`/`humanApproval` signature | reuse signature, new server-side implementation |
| `internal/tui` | not used — web is a second consumer of the Agent pattern, not a TUI fork |
| subagent `ChildEvent` stdin/stdout protocol | prior art only, different transport |
| `runREPL` session wiring | adapt: extract shared constructor (below) |
| HTTP server, router, SSE hub, ring buffer, registry, auth, web UI, `serve.lock` | build from scratch |

Recommended refactor before phase 1: extract `runREPL`'s Agent/tools/store
wiring into `internal/session.New(...)`, shared by `runREPL`, `runPrint`,
and the new `runServe` — otherwise serve-mode wiring drifts from TUI-mode
wiring within a month.

## G. Build order

Each phase independently shippable/testable with `net/http/httptest`.

1. **Refactor + single-session HTTP/SSE.** Extract `internal/session.New`,
   confirm TUI unchanged. `px serve` loopback, no auth, one session:
   create/messages/events/history. Approvals hard-denied
   (`"approvals not supported over HTTP yet"`) — nothing risk-gated runs
   unattended before phase 3.
2. **Registry + multi-session.** `liveSession` map, `--max-live`/
   `--max-turns` with `queued` status, cancel, idle eviction (with running/
   awaiting-approval exemptions), list/search/delete/rename, ring buffer +
   `Last-Event-ID` replay, `serve.lock`.
3. **Approval gate over HTTP.** `approvalGate`, pending-list endpoint,
   idempotent resolve, cancel-denies-pending, shutdown-denies-all, optional
   deny-only `--approval-timeout`.
4. **Auth.** Token mint/hash/rotate via `POISSON_WEB_TOKEN`/`--new-token`,
   `/api/login` + cookie, bearer for scripts, CSRF header + Origin check,
   bind-policy warnings, `--behind-proxy`. **Gate: no non-loopback bind
   before this phase lands.**
5. **Web UI.** Embedded bundle, drawer + tab strip + transcript + composer
   + approval sheet, multi-open/single-focus state, SSE reconnect+resync.
6. **Polish (optional).** Multiplexed `/api/events`, markdown subset,
   transcript virtualization, `px attach` (TUI as HTTP client, kills the
   two-writer problem), Web Push, QR pairing.

## Risks

| Risk | Mitigation |
|---|---|
| Token leak = host RCE | handling rules above, Tailscale-first deployment, `--new-token` rotation |
| `--addr 0.0.0.0` footgun | loopback default, startup warning, plaintext login refused remotely |
| Parallel sessions burn budget fast | `--max-turns` cap, per-session cost visible, `queued` status shown |
| SQLite single-conn serialization under N turns | measure at phase 2; fix is a serializing store actor/reader-pool, not multiple handles |
| Process crash kills all live turns | per-message persistence means history survives; in-flight turns don't — documented |
| Two writers (serve + local TUI) | `serve.lock` + warning now, `px attach` later |
| Forgotten approval stalls a session forever | `awaiting_approval` badge, explicit cancel, opt-in deny-only timeout |

## Open decisions

- Markdown rendering in web UI: hand-rolled JS subset (recommended) vs
  vendored `marked.js`.
- QR pairing for phone: skip, type token once (recommended) vs
  `go-qrcode` dep.
- `--approval-timeout` default: block forever (recommended, current design)
  vs deny-only default (e.g. 30m).
- TLS: Tailscale-only (recommended) vs Caddy/nginx reverse proxy.
