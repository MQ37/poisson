# px orchestrate: Box Yolo Mode + Host-Direct Instances — Design + Implementation Plan

Addendum to `docs/orchestrator-plan.md` (read that first — this doc assumes
its architecture, terminology, and §5 threat model as given). Covers two
related changes requested after the orchestrator went live on orchestrator-host:

1. **Box instances run yolo, and don't see `set_title`.** A box instance
   (nspawn container, the only kind that exists today) blocking every risky
   bash command on a Telegram round-trip defeats the point of an isolated,
   disposable sandbox — and `set_title` (renames a TUI window that doesn't
   exist here) is dead weight in the tool list.
2. **A second instance kind: host-direct.** `/new-host` creates an instance
   that runs `px` directly on the orchestrator host itself — no container,
   no isolation, real root, real approvals (never yolo). `/new-box` (and
   plain `/new`, kept as an alias) keeps today's nspawn behavior.

No code has been written for this yet — this is a planning document only,
same status as `orchestrator-plan.md` was before its own implementation
began.

---

## 1. Requirements (as given by the user, verbatim intent preserved)

> since the agent is running in the sandbox in the telegram mode we should
> hide the set title tool and also make it run in the yolo mode. Then I am
> thinking that we should also add support for the orchestrate to run on
> the host directly with the approvals without yolo mode so there would be
> command /new-host and /new-box.

Decomposed:

- Box instances (existing nspawn kind): `set_title` hidden from the tool
  registry, turns run with `Yolo: true`.
- A new host-direct instance kind: turns run directly on the orchestrator
  host (orchestrator-host), no nspawn container at all, `Yolo: false` always (real
  Telegram approval round-trip, same mechanism `orchestrator-plan.md` Step
  28 already built).
- Two explicit commands: `/new-box` (today's behavior) and `/new-host`
  (the new kind). Bare `/new` stays a working alias for `/new-box` — no
  behavior change for anyone already using it.

---

## 2. Why host-direct instances are a different trust tier, not a variant

A box instance's entire safety story (§5 of `orchestrator-plan.md`) rests
on the nspawn boundary: real root, but confined to a disposable rootfs, a
capability set missing `CAP_SYS_MODULE`, and credentials scoped to exactly
what that one instance was authorized for. Yolo mode is acceptable there
specifically *because* a mistake's blast radius is "this one throwaway
container," not the host.

A host-direct instance has **none of that**. It is, precisely and without
euphemism, the same thing as SSHing into orchestrator-host and running `px` yourself:

- Full, unconfined host root — no rootfs boundary, no capability
  restriction, no cgroup, nothing.
- Uses the host's own real `~/.poisson/auth.json` directly (there is no
  separate rootfs to bind-mount a scoped copy into).
- Every command it runs is a real command on real orchestrator-host — it can modify
  `px-orchestrate.service` itself, read every other box instance's
  bind-mounted secrets, touch vaultwarden's data, reconfigure the
  firewall, anything root on that box can do.

This is why yolo is categorically wrong for this kind, and why the plan
below gates it far more conservatively than a box instance: opt-in at the
config level, a hard low concurrency cap, and no shortcuts on the approval
path anywhere.

---

## 3. Architecture

### 3.1 `InstanceMeta.Kind`

Add `Kind string` to `InstanceMeta` (`"box"` or `"host"`), persisted like
every other metadata field. Drives:

- `turn.go`'s `runTurn`: `TurnSpec.Yolo = (kind == "box")`.
- Which `Runtime` backend a given instance's calls route to (see 3.3).
- The env var `buildPrintAgent` (cmd/px) checks to hide `set_title` — see
  3.2 below, gated by kind only for box today, but written so a future
  third kind can independently decide.

### 3.2 Hiding `set_title` for orchestrator turns

`tools.BuildRegistry` currently always registers `set_title` when
`opts.Store != nil` (`internal/tools/build.go:131-136`). Add:

```go
// NoSetTitle omits the set_title tool -- an orchestrator instance turn has
// no TUI window to rename; the tool is just dead weight in its tool list.
NoSetTitle bool
```

to `tools.BuildOptions`, checked alongside the existing `opts.Store != nil`
condition before registering `NewSetTitleTool`.

`cmd/px/print_session.go`'s `buildPrintAgent` sets `NoSetTitle: true` when
`os.Getenv("POISSON_ORCHESTRATE_INSTANCE") == "1"` — a new env var, set by
whichever `Runtime.StartTurn` implementation launches the turn (both
`nspawn.turnRunArgs` and the new host-mode equivalent add
`--setenv=POISSON_ORCHESTRATE_INSTANCE=1`, the same pattern already used
for `--setenv=HOME=/root`). Scoped narrowly to orchestrator-launched turns
— a human typing `px -p --print-json` directly still gets `set_title`,
since nothing else sets this env var.

### 3.3 `CompositeRuntime`

`Core` holds a single `rt Runtime` field today. Two backends (nspawn for
box, a new one for host) need dispatch without touching `Core`,
`commands.go`, or `turn.go` at all — they only ever call methods on `c.rt`,
never caring which concrete backend answers.

New file `internal/orchestrator/composite_runtime.go`:

```go
// CompositeRuntime implements Runtime by dispatching each call to the
// Runtime registered for that instance's kind. Core is entirely unaware
// this indirection exists -- it just calls Runtime methods as always.
type CompositeRuntime struct {
    mu      sync.Mutex
    byKind  map[string]Runtime      // "box" -> nspawn.Runtime, "host" -> hostrt.Runtime
    kindOf  map[string]string       // instance name -> which kind owns it
}

func NewCompositeRuntime(byKind map[string]Runtime) *CompositeRuntime

// Create is the ONE method that doesn't just look up kindOf[name] (nothing
// there yet) -- it takes an explicit kind, checks the name isn't already
// used under ANY kind (not just this one), then records kindOf[name] =
// kind on success. This is what stops "/new-box x" and "/new-host x" from
// silently colliding in Core's single flat byName map.
func (c *CompositeRuntime) CreateKind(ctx context.Context, kind string, spec InstanceSpec) error

// Every other Runtime method looks up kindOf[name] and forwards.
func (c *CompositeRuntime) Start(ctx context.Context, name string) error
func (c *CompositeRuntime) Stop(ctx context.Context, name string) error
// ... Terminate, Destroy, Exec, StartTurn, StopTurn ...

// List concatenates every backend's List() -- a box and a host instance
// both show up in one combined report.
func (c *CompositeRuntime) List(ctx context.Context) ([]InstanceInfo, error)
```

`CreateKind` is not part of the `Runtime` interface itself (the interface's
plain `Create(ctx, spec)` has no kind parameter, and every other backend
doesn't need one). `Core.handleNew` is updated to accept a `kind` param and
type-assert `c.rt.(*CompositeRuntime)` to call `CreateKind` — the one place
in `Core` that has to know `CompositeRuntime` exists, documented inline as
a deliberate, narrow exception to "Core only calls Runtime interface
methods."

**Verify.** Unit tests with two `FakeRuntime`s registered under `"box"`/
`"host"`: creating `"x"` under box then attempting `"x"` under host fails
with a clear "name already in use" error, not a silent overwrite; `List`
returns entries from both backends; `Destroy` routes to the correct
backend and clears `kindOf`.

### 3.4 Host-mode `Runtime`: `internal/orchestrator/hostrt`

New package, small — most of `Runtime`'s surface is near-trivial for a
kind with no container to manage:

```go
type Runtime struct {
    cfg Config // PxBinPath, StateRoot -- no MachinesRoot/GoldenImage (no rootfs), no resource-limit fields (decided: host turns are never capped -- see §6.2)
}

// Create just builds the instance's state-dir layout (via
// orchestrator.CreateInstanceLayout, same as nspawn -- secrets/ stays
// unused and empty, work/ becomes this instance's real host-side scratch
// cwd, deliberately reused rather than inventing a second layout shape).
// No rootfs, no clone, no bind mounts -- there's nothing else to create.
func (r *Runtime) Create(ctx context.Context, spec orchestrator.InstanceSpec) error

// Start/Stop/Terminate: no process runs between turns (matches
// orchestrator-plan.md assumption 10 -- "one process per turn, not a
// resident daemon" -- already true for box instances too). These just
// flip an internal enabled/disabled bookkeeping bit persisted alongside
// the instance's own metadata (reusing InstanceMeta.DesiredState, already
// exactly this concept) so StartTurn can refuse to run against a
// suspended host instance. Idempotent, same as every other Runtime.
func (r *Runtime) Start(ctx context.Context, name string) error
func (r *Runtime) Stop(ctx context.Context, name string) error
func (r *Runtime) Terminate(ctx context.Context, name string) error

// Destroy removes the instance's state directory. No rootfs to remove, no
// secrets to shred (host mode never wrote any -- it uses the host's own
// auth.json directly, never a copy).
func (r *Runtime) Destroy(ctx context.Context, name string) error

// List reports every known host instance from local state-dir bookkeeping
// (there's no machinectl/systemctl entity representing "this host
// instance exists" the way a box instance's own unit does) -- State is
// "running"/"stopped" from the DesiredState bit above, MemoryCurrent is
// always 0: host-mode turns are deliberately NOT resource-limited (decided
// -- see §6.2), so there is no cgroup accounting scoped to just this
// instance to report in the first place.
func (r *Runtime) List(ctx context.Context) ([]orchestrator.InstanceInfo, error)

// Exec runs argv directly on the host, no wrapping at all.
func (r *Runtime) Exec(ctx context.Context, name string, argv []string) (stdout, stderr string, code int, err error)

// StartTurn is nspawn.turnRunArgs with --machine=<name> removed -- exactly
// that and nothing else. Everything downstream (the transient
// px-turn-<name> unit, StopTurn/StopOrphanedTurn stopping it by that same
// name, the --pipe/--collect/--wait streaming contract) is identical to
// the box case, because it's the same systemd-run mechanism minus one
// flag.
func (r *Runtime) StartTurn(ctx context.Context, name string, spec orchestrator.TurnSpec) (*orchestrator.Turn, error)
func (r *Runtime) StopTurn(ctx context.Context, unitName string) error
func (r *Runtime) StopOrphanedTurn(ctx context.Context, name string) error
```

**Edge cases.**
- `StartTurn` must refuse (clear error, not a confusing systemd-run
  failure) if the instance's own `DesiredState` is suspended — nspawn
  relies on the container itself being stopped to make this true
  implicitly; host mode has no container to rely on, so this check must be
  explicit here.
- Two host instances running turns concurrently both have **unconfined**
  access to the same real filesystem — no locking, no coordination. Not
  solved by this plan (see §4's concurrency cap instead of trying to solve
  concurrent-host-mutation safely).
- `PxBinPath` for host mode's `StartTurn` is the same binary already
  running the orchestrator itself (`/usr/local/bin/px`) — no bind-mount
  needed, it's already on the host's own `PATH`.
- Unlike nspawn's `Create`, there is no disk-space precheck, no
  capability/boot verification, nothing to fail in an interesting way —
  `hostrt.Create` is close to unconditionally successful once the state
  directory writes.

**Verify.** Pure unit tests (no real systemd-run needed for the
argv-builder — same `turnRunArgs`-minus-`--machine=` pure function pattern
as nspawn's own `argv_test.go`). One gated live test on orchestrator-host: `Create` +
`StartTurn` a real host-mode turn, confirm it runs as a real host process
(not inside any container — `machinectl list` shows nothing new), confirm
`StopTurn` actually stops it.

### 3.5 Command routing

Add `CmdNewBox`/`CmdNewHost` to the `CommandKind` enum (keep `CmdNew` as a
literal alias resolving to box, for the Telegram parser and for anyone with
muscle memory). `telegram/frontend.go`'s `commandVerbs` map gains `/new-box`
and `/new-host` entries; `/new` keeps mapping to `CmdNewBox`, unchanged
behavior.

`commands.go`'s `handleNew` gains a `kind string` parameter. The two
Telegram verbs dispatch to the same function with `"box"`/`"host"`. Host
mode adds two refusal/friction paths before anything else, in order:

1. If `!cfg.AllowHostInstances`, refuse explicitly ("host instances are
   disabled on this orchestrator — see `[orchestrator] allow_host_instances`")
   rather than silently falling through to box behavior or a confusing
   runtime error.
2. **Explicit second confirmation, required every time** (decided: yes —
   see §6). `/new-host <name>` with no confirmation token never creates
   anything — it replies with the risk warning (§2, verbatim: no isolation,
   real host root, real credentials) and the *exact* re-invocation to
   confirm: `/new-host <name> --confirm-unconfined-host`. Only that exact
   second form actually calls `CreateKind(ctx, "host", spec)`. Deliberately
   **stateless** — no pending-confirmation registry, no timeout to manage,
   no risk of a stale confirmation firing minutes later against a
   different intent — the confirmation is encoded entirely in what the
   user types back, the same "resolve exactly once via what's actually
   sent" simplicity already used for `/approve`/`/deny`. A bare `/new-host`
   with the wrong or missing flag always re-shows the warning; it never
   partially creates anything.

---

## 4. Config changes

`config.OrchestratorConfig` gains:

```go
// AllowHostInstances gates /new-host entirely -- false (the default)
// means the command is refused outright, not just hidden. A host instance
// has no isolation boundary at all (see §2) -- this must be an explicit,
// conscious opt-in, never silently available just because [orchestrator]
// is configured for box instances.
AllowHostInstances bool
// MaxHostInstances caps concurrent host-direct instances, independent of
// MaxInstances (confirmed: MaxInstances counts box instances only -- see
// §6). Default 0 means unlimited, by explicit decision: host instances
// are gated by AllowHostInstances + the per-invocation confirmation step
// (§3.5) instead of a count ceiling. Set to a positive number to also cap
// the count if wanted later.
MaxHostInstances int
```

Parsed the same `lookup()`-based way as every other `[orchestrator]` field,
same file. `renderOrchestratorConfig`/`--config-check` prints both.

---

## 5. Implementation steps

State dir/layout, naming, and everything from `orchestrator-plan.md`
carries over unchanged — these steps are additive.

### Step H1. `tools.BuildOptions.NoSetTitle`
**What.** Add the field (§3.2), check it in `BuildRegistry` before
registering `set_title`.
**Why here.** Independent of everything else in this plan — buildable and
testable in isolation first.
**Edge cases.** Must not affect `list_sessions`/`read_messages`/`recall` —
only `set_title` is gated by this flag.
**Verify.** `BuildRegistry(BuildOptions{Store: st, NoSetTitle: true})`
registry has no `set_title` entry; `NoSetTitle: false` (or omitted, the
zero value) is unchanged from today.

### Step H2. `POISSON_ORCHESTRATE_INSTANCE` env var + `buildPrintAgent` wiring
**What.** `buildPrintAgent` checks the env var, passes `NoSetTitle: true`
into `tools.BuildOptions` when set.
**Why here.** Depends on H1.
**Verify.** `POISSON_ORCHESTRATE_INSTANCE=1 px -p --print-json -- "list your tools"`
(against a fake/test provider) never mentions `set_title`; unset, it does.

### Step H3. `InstanceMeta.Kind` + box yolo wiring
**What.** Add the field to `state.go`. `turn.go`'s `runTurn` sets
`TurnSpec.Yolo = (meta.Kind == "box")`. Existing (pre-this-plan) instances
have `Kind == ""` (zero value) after an upgrade — treat empty the same as
`"box"` everywhere (`Kind == "" || Kind == "box"`), so an orchestrator
upgrade doesn't silently flip already-running instances to non-yolo host
-like behavior or, worse, crash on an unrecognized kind.
**Why here.** Independent of H1/H2; can be built and tested in parallel.
**Edge cases.** The empty-string-means-box compatibility shim above is the
one genuinely load-bearing edge case — get a test on it specifically, not
just the two explicit values.
**Verify.** `FakeRuntime`-backed test: a box-kind instance's `TurnSpec.Yolo`
is `true`; a host-kind instance's is `false`; an instance loaded with
`Kind: ""` (simulating pre-upgrade metadata) also gets `Yolo: true`.

### Step H4. `internal/orchestrator/hostrt` package
**What.** The `Runtime` implementation from §3.4: `Config`, pure
argv-builder functions (`turnRunArgs` minus `--machine=`, following
`nspawn/argv.go`'s exact separation-of-concerns pattern), and the real
methods.
**Why here.** Depends on H3 existing (needs `InstanceMeta.Kind` to make
sense of what it's for) but is otherwise self-contained — buildable and
fully unit-testable against fakes/pure functions before `CompositeRuntime`
exists.
**Edge cases.** See §3.4's own edge-case list.
**Verify.** Pure argv-builder tests (no real systemd-run). Gated live test
on orchestrator-host per §3.4.

### Step H5. `CompositeRuntime`
**What.** The dispatcher from §3.3.
**Why here.** Depends on both nspawn.Runtime (already built) and
`hostrt.Runtime` (H4) existing as concrete `Runtime` implementations to
wrap.
**Edge cases.** See §3.3's own edge-case list — the cross-kind name
collision check is the one to get a dedicated test for, not just happy-path
dispatch.
**Verify.** Unit tests entirely against two `FakeRuntime`s (no real nspawn
or host process involved) — this is exactly the kind of pure-dispatch logic
`FakeRuntime` already exists to make cheap to test.

### Step H6. Command routing (`CmdNewBox`/`CmdNewHost`, `/new-box`/`/new-host`)
**What.** §3.5's changes to `types.go`, `telegram/frontend.go`,
`commands.go`.
**Why here.** Depends on H5 (needs `CompositeRuntime.CreateKind` to call)
and the config fields from §4.
**Edge cases.**
- `/new-host` while `AllowHostInstances` is false → explicit refusal
  naming the config knob, not a generic error. Checked *before* the
  confirmation-token check, so a disabled orchestrator never even reveals
  the confirmation incantation.
- `/new-host <name>` (no confirmation flag) → the warning + exact
  re-invocation, never creates anything, regardless of `MaxHostInstances`.
- `/new-host <name> --confirm-unconfined-host` at `MaxHostInstances` (only
  relevant if an admin set it to a positive number — default is unlimited,
  §4) → same "refuse and name what's running" shape `/new`/`/new-box`
  already has at `MaxInstances`, scoped to just the host-kind count.
- `MaxInstances` (box) is never consulted for a `/new-host` call, and
  `MaxHostInstances` is never consulted for `/new-box` — confirmed fully
  independent ceilings (§6).
- Plain `/new` (no suffix) must keep behaving exactly as it does today —
  regression-test this explicitly, not just the two new verbs.
**Verify.** Table-driven dispatch tests (same pattern as
`commands_test.go`'s existing table) covering: `/new-box` succeeds,
`/new-host` refused when disabled, `/new-host` with no confirmation flag
replies with the warning and creates nothing, `/new-host ... --confirm-unconfined-host`
succeeds when enabled, `/new-host` refused at its own cap independent of
the box cap (only when `MaxHostInstances` is explicitly set), plain `/new`
unchanged.

### Step H7. `cmd/px/orchestrate.go` wiring
**What.** Build both backends (`nspawn.NewRuntime(...)` and
`hostrt.NewRuntime(...)`, or `FakeRuntime` for each under `--dry-run`),
wrap in `orchestrator.NewCompositeRuntime(map[string]orchestrator.Runtime{"box": ..., "host": ...})`,
pass to `orchestrator.NewCore` as before (its `rt` field type is unchanged
— `CompositeRuntime` satisfies `Runtime`).
**Why here.** Last wiring step — every piece it assembles is already built
and tested by this point, matching `orchestrator-plan.md`'s own Step 26
positioning.
**Edge cases.** `--dry-run` must fake *both* kinds, not just box — a
dry-run smoke test of `/new-host` should work with zero real host
processes touched either.
**Verify.** `px orchestrate --config-check` prints the two new config
fields. `px orchestrate --dry-run`: `/new-box` and `/new-host` both create
distinct fake instances with no collision; `/list` shows both.

### Step H8. Documentation
**What.** Update `orchestrator-plan.md`'s own §10 (or add a cross-reference
to this doc) noting the two-kind architecture exists; update this doc's own
assumptions/open-questions section below with anything that changed during
actual implementation, same discipline as Step 30 of the original plan.
**Why here.** Last, by definition.
**Verify.** A reader with context on the base orchestrator plan (but not
this addendum) can understand the box/host distinction and set up a host
instance end to end from this document alone.

---

## 6. Decisions (resolved — were open questions, now settled)

1. **`MaxInstances` counts box instances only.** Host instances are
   governed entirely separately: gated by `AllowHostInstances` plus the
   per-invocation confirmation step (§3.5), not by a count ceiling.
   `MaxHostInstances` exists as an optional additional cap (default 0 =
   unlimited) for an admin who wants one later, but is not required and
   is never consulted by `/new-box`.
2. **Host-mode turns are never resource-limited.** No `MemoryMax`/
   `CPUQuota`/`TasksMax` applied to a host instance's transient
   `systemd-run` unit — a host instance is meant to act with the same
   authority an interactive operator has, and operators don't get
   memory-capped either. `hostrt.Config` accordingly carries no
   resource-limit fields at all (contrast `nspawn.Config`, which does).
3. **`/new-host` requires an explicit second confirmation, every single
   time**, on top of the config-level `AllowHostInstances` gate — see
   §3.5's stateless confirmation-token design
   (`/new-host <name> --confirm-unconfined-host`). Not a one-time or
   per-session waiver; the friction is intentionally permanent.

---

## 7. Implementation status (H1–H8, all complete)

Every step landed with clean `go build ./...`, `go vet ./...`, `./test.sh`,
and `go test -race ./internal/orchestrator/...` at each commit. Deviations
found during actual implementation, not anticipated by §3-§6 above:

- **`RehydrateKindOf` — a real gap this plan didn't anticipate.**
  `CompositeRuntime.kindOf` is in-memory only. A freshly constructed
  `CompositeRuntime` after an orchestrator restart starts with an empty
  `kindOf` map, even though every existing instance (and its persisted
  `Kind`) is already on disk — every reconcile-path call
  (`Start`/`Stop`/`StopOrphanedTurn`/`Destroy` on an *existing* instance,
  none of which go through `CreateKind`) would otherwise fail with "unknown
  instance" on every single restart. Fixed by
  `CompositeRuntime.RehydrateKindOf(metas []InstanceMeta)`, called from
  `Core.reconcile` right after `ScanInstances` and before anything else
  touches an existing instance — the second (and only other) narrow,
  documented place `Core` knows `CompositeRuntime` exists. Caught by test
  (`TestReconcile_RehydratesCompositeRuntimeKindOfForHostInstance`), not
  review — the same "found via test" discipline the base plan's M3
  established.
- **`hostrt.Runtime`'s bookkeeping is a marker file, not a separate
  registry.** `Create` writes a `host-state` file (`"running"`/`"stopped"`)
  under the instance's own state directory — the same
  `StateRoot/instances/<name>` tree `nspawn.Runtime.Create` also uses via
  `CreateInstanceLayout`. `List` distinguishes a host instance from a box
  instance sharing that same parent directory purely by that marker file's
  presence, not by any separate directory tree or naming convention. No
  coupling to `orchestrator.InstanceMeta` needed — self-contained.
- **`CompositeRuntime.StopTurn(unitName)` tries every backend.** Unlike
  every other `CompositeRuntime` method, `StopTurn`'s argument is a unit
  name, not an instance name — it can't be looked up in `kindOf` directly.
  Both `nspawn` and `hostrt` derive `"px-turn-<instance>"` the same way, and
  every backend's `StopTurn` is already idempotent (stopping an
  unknown/already-gone unit is success), so `CompositeRuntime.StopTurn`
  just calls it on every registered backend rather than needing a second
  unit-name→kind index.
- **`CompositeRuntime.Create` (the plain, non-kind-aware method) defaults to
  `KindBox`** — satisfies the `Runtime` interface for a caller that knows
  nothing about kinds at all (there isn't one in production: `Core` always
  calls `CreateKind` via `handleNew`'s type assertion). Exists so
  `*CompositeRuntime` type-checks as a drop-in `Runtime` everywhere a plain
  one is expected.
- **`nspawn.turnRunArgs` also gained `--setenv=POISSON_ORCHESTRATE_INSTANCE=1`**
  (§3.2), alongside `hostrt`'s own copy — both box and host turns hide
  `set_title` the same way, not just host ones.
