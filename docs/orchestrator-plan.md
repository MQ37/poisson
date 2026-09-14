# Agent Orchestrator — Design + Implementation Plan

Manage multiple persistent, isolated, headless poisson agent instances remotely
over Telegram. Each instance runs inside its own systemd-nspawn container with
full root access and injected secrets. One Telegram supergroup with Topics
enabled, one topic per instance. Lives inside the poisson monorepo as a new
`px orchestrate` subcommand. **Zero new Go dependencies** (hard repo rule, see
`/home/mq/workdir/poisson/AGENTS.md`).

This doc is a complete, self-contained brief for whoever builds this — read
it in full before writing any code. It merges a feasibility-scout pass and a
step-by-step implementation-plan pass, both done by a fresh Claude Opus
subagent reading the real codebase and probing the real target host
(`orchestrator-host`) live over SSH. Nothing here is speculative; every claim below was
either read directly from a file (path cited) or run directly on the host
(output cited or paraphrased).

Status as of writing: **planning complete, zero code written**. Confirmed
working design decisions below; only the numbered open items at the very end
need a user call before/while building.

---

## 1. Requirements (as given by the user, verbatim intent preserved)

1. Isolation backend: **systemd-nspawn**, not Podman, not full QEMU —
   namespace-based, shares host kernel, own init/systemd inside,
   `machinectl`/`systemd-nspawn` CLI tooling.
2. **Full root access inside the container is required** — genuine root, not
   a user-namespace-remapped unprivileged uid. Explicit, accepted tradeoff:
   shared kernel + real root inside means a container escape is full host
   root (see §5).
3. Host: **orchestrator-host** (aarch64 Raspberry Pi, reachable via `ssh root@orchestrator-host`).
4. **Telegram** as the first transport, via a supergroup with **Topics**
   enabled — one topic per running instance, routed by `message_thread_id`.
   Bot API only (plain HTTPS + JSON, long-polling `getUpdates` — no webhook,
   no third-party SDK).
5. Model selection scoped via a **`/model`** command per topic/instance,
   picking from a curated allow-list (not every model poisson knows about).
6. Lifecycle: instances live **forever until explicitly `/kill`ed**. Also
   support **`/suspend`** (container stopped, not destroyed — resumable via
   `/resume`).
7. **Transport-agnostic core** — the orchestrator's routing/lifecycle/dispatch
   logic must live behind an abstract interface so Telegram is just the first
   of several possible frontends (Discord, web UI, HTTP API later), pluggable
   with minimal new code.
8. Must live **inside the poisson monorepo**, integrated into the `px` CLI.
9. **Minimum/ideally zero new dependencies.**

---

## 2. Architecture

```
┌─ Telegram supergroup (topics) ─┐
│  topic 42 ──┐                  │
│  topic 77 ──┤                  │
└─────────────┼──────────────────┘
              │ Bot API, long-poll getUpdates (net/http)
       ┌──────▼─────────────────────────────┐
       │ internal/orchestrator/telegram      │  implements Frontend
       └──────┬─────────────────────────────┘
              │ Command / Event  (plain Go structs, no transport types)
       ┌──────▼─────────────────────────────┐
       │ internal/orchestrator  (Core)       │  routing, registry, lifecycle,
       │   registry: ChannelKey -> *Instance  │  /model allow-list, authz
       └──────┬─────────────────────────────┘
              │ Runtime
       ┌──────▼─────────────────────────────┐
       │ internal/orchestrator/nspawn        │  implements Runtime
       └──────┬─────────────────────────────┘
              │ os/exec: machinectl, systemctl, systemd-run
       ┌──────▼──────────┐  ┌──────────────┐
       │ px-inst-alpha   │  │ px-inst-beta │   nspawn, --boot, real root
       │  px -p --session│  │              │
       └─────────────────┘  └──────────────┘
```

### 2.1 Illustrative interface shapes (reference sketch, not final code)

These signatures are a design reference to build from — exact method sets are
nailed down per-step in §7, but this is the shape to keep in mind while
reading the plan.

```go
package orchestrator

// ChannelKey is one frontend's opaque routing target for one instance: a
// Telegram message_thread_id, a Discord thread id, an HTTP session path. The
// Core never parses it — it is a map key and nothing more.
type ChannelKey struct{ Frontend, Chat, Topic string }

type CommandKind int
const (
	CmdMessage CommandKind = iota // free text -> one agent turn
	CmdNew; CmdList; CmdStatus; CmdModel
	CmdSuspend; CmdResume; CmdKill
	CmdApprove; CmdCancel
)

type Command struct {
	Key    ChannelKey
	Kind   CommandKind
	Text   string
	Args   []string
	UserID string
	At     time.Time
}

type EventKind int
const (
	EvText EventKind = iota
	EvToolStart; EvToolResult; EvStatus; EvLifecycle; EvApproval; EvError
)

// Event mirrors agent.OutputEvent / subagent.ChildEvent deliberately — the
// adapter is a field copy, not a translation layer (see §4 reuse decision 1).
type Event struct {
	InstanceName string
	Seq          uint64
	At           time.Time
	subagent.ChildEvent // embedded as-is, see §4.1
}

// Frontend is one transport. Telegram is the first; Discord/HTTP/web are
// later implementations of this exact interface and nothing else.
type Frontend interface {
	Run(ctx context.Context, out chan<- Command) error
	CreateChannel(ctx context.Context, title string) (ChannelKey, error)
	CloseChannel(ctx context.Context, key ChannelKey) error
	Send(ctx context.Context, key ChannelKey, msg Message) error
	Edit(ctx context.Context, key ChannelKey, msgID, text string) error // best-effort
}

// Runtime is where an agent instance actually executes. nspawnRuntime is the
// first implementation.
type Runtime interface {
	Create(ctx context.Context, spec InstanceSpec) error
	Start(ctx context.Context, name string) error
	Stop(ctx context.Context, name string) error      // clean poweroff, rootfs kept
	Terminate(ctx context.Context, name string) error // force
	Destroy(ctx context.Context, name string) error    // rm -rf rootfs
	List(ctx context.Context) ([]InstanceInfo, error)
	Exec(ctx context.Context, name string, argv []string) (stdout, stderr string, code int, err error)
	StartTurn(ctx context.Context, name string, spec TurnSpec) (*Turn, error)
}
```

**Adding Discord later = one file implementing `Frontend`, plus one line in
the `--frontend` switch. No Core change, no Runtime change.** That is the
requirement-7 contract, and it holds because `ChannelKey` is opaque and
`Command`/`Event` contain zero transport types.

### 2.2 Why NOT reuse `internal/sandbox.Driver` (the existing Podman interface)

Verified directly against `internal/sandbox/driver.go` and `podman_driver.go`:

| `Driver` fact | Why it doesn't fit |
|---|---|
| `Kill` = stop **and remove**, atomically (`podman_driver.go:407-420`, `forceRemove` → `podman rm -f -t 0`) | No stop-without-remove exists anywhere in the interface — `/suspend` needs exactly that. |
| Discovery via `podman ps --filter label=poisson.sandbox=1` (`podman_driver.go:428-431`) | nspawn has no label concept at all. |
| `createArgs` sets `--userns=keep-id`; exec drops to a bootstrapped non-root uid-mapped user (`podman_driver.go:283-355`) | Exactly the opposite of "genuine root inside." |
| `Driver`/`Manager` exist to gate per-call `sandboxId` routing for the bash tool, with an ownership model (`manager.go:32-45`) | Different purpose — the orchestrator *is* the owner of everything, it doesn't need session-scoped ownership gating on top. |
| `Start` (`podman_driver.go:246-270`) re-runs the whole `bootstrap` (apt-get) path | Unusable as a resume primitive for nspawn — resume must not re-provision. |

**Conclusion: build a separate `Runtime` interface** (§2.1) rather than
forcing nspawn through `Driver`. Leave `sandbox.Driver` completely untouched
— this feature does not modify it.

---

## 3. nspawn semantics (confirmed against upstream `systemd.nspawn`/`systemd-nspawn` man pages, systemd v255 — the version orchestrator-host actually runs)

- `--private-users=` default when invoked **from the command line** is
  **`no`** — "user namespacing is turned off. This is the default." This must
  still be passed **explicitly**, because...
- `machinectl start` runs instances through `systemd-nspawn@.service`, which
  passes **`-U`** (≡ `--private-users=pick --private-users-ownership=auto`)
  plus `--settings=override`. **Do not use `machinectl start`/the stock
  template unit** — own the unit file explicitly (see §7 Step 12) so
  `--private-users=no` is guaranteed, not accidentally overridden.
- `.nspawn` settings files' privileged options (`PrivateUsers=`, `Bind=`,
  `[Network]`) are only honored from `/etc/systemd/nspawn/` or
  `/run/systemd/nspawn/`, under `--settings=override` — **do not rely on
  `.nspawn` files as the primary mechanism**; an explicit unit is one file,
  fully auditable, no precedence reasoning required.
- Relevant `.nspawn`/unit knobs: `[Exec] Boot=`, `PrivateUsers=`,
  `Environment=`; `[Files] Bind=`, `BindReadOnly=`; `[Network] Private=`,
  `VirtualEthernet=`, `Bridge=` (none of the `[Network]` ones are used here —
  see host networking decision below).
- **`--capability=all` alone is not enough for genuine root** — nspawn's
  restricted capability bounding set still applies without it. Both
  `--private-users=no` AND `--capability=all` (with `--drop-capability=` to
  subtract specific ones) are required together.
- Correct nspawn syntax for "all capabilities except one" is **two separate
  flags**: `--capability=all --drop-capability=CAP_SYS_MODULE` (a prior draft
  of this plan wrote `--capability=all,-CAP_SYS_MODULE`, which is **not**
  valid nspawn syntax — corrected here).

### 3.1 Networking decision: host networking, not a private bridge

orchestrator-host's firewall is a three-way tangle already: `ufw` (only 22/tcp inbound
allowed), Docker's own `iptables-nft` rules (with an explicit "do not touch"
warning from `nft list ruleset` itself), and Tailscale's `ts-forward`/
`ts-input` chains. `iptables -P FORWARD DROP` is set. Threading new
bridge/NAT rules through that, **on the box reached only via SSH**, is the
single highest-risk change available — a mistake there can lock you out.

**Decision: omit `--private-network`/`--network-veth` entirely.** Instances
share the host network namespace.

- Zero new firewall rules needed.
- DNS works immediately via `--resolv-conf=bind-host` against the systemd-
  resolved stub.
- Tailscale reachability is free (instances see `tailscale0`).
- Isolation loss is ~nil in context: real root inside the container can
  already reconfigure host networking anyway (see §5).

**Required mitigation**: mask the guest's own network stack in the golden
image, or guest `systemd-networkd`/`systemd-resolved` fight the host's:
```
systemctl mask systemd-networkd systemd-resolved systemd-networkd.socket
```
**Practical failure mode to watch for**: guest processes can bind host ports.
orchestrator-host already runs `vaultwarden` on `127.0.0.1:8081` (Docker) — a port
collision there is the realistic failure, not a security issue but an
availability one.

### 3.2 Nested Podman inside an nspawn instance — confirmed feasible

Asked and answered during planning: **yes, and it's the easy case.** Nested-
container pain (fuse-overlayfs workarounds, `/proc` masking errors) is
specifically a *rootless*-podman-in-nspawn problem, caused by overlayfs
refusing to nest inside a user namespace. Since instances run
`--private-users=no` (real root, no user namespace), rootful podman nests
without any of that.

Requirements, already covered by the plan as written:
- `--capability=all --drop-capability=CAP_SYS_MODULE` — already sufficient,
  podman doesn't need `CAP_SYS_MODULE`.
- `Delegate=yes` on the instance unit — already planned (§7 Step 12),
  required so podman can manage its own cgroup subtree for whatever it
  spawns.
- Native overlayfs storage driver just works — no `fuse-overlayfs`/`/dev/fuse`
  bind needed (that fallback is only for the rootless case).

**Caveat to note in the final docs (§7 Step 30)**: nesting does not add a
security boundary — a container spawned inside an instance is exactly as
privileged as the instance itself (same real host root). It's workflow
convenience only, and it compounds the existing tradeoff rather than
introducing a new one.

---

## 4. Reuse-vs-build decisions (checked against the real code, not assumed)

1. **`subagent.ChildEvent` as the orchestrator's `Event` type — reuse
   as-is, embedded, no translation layer.** It already carries
   `type/text/tool/tool_input/result/error/command/description/cwd/risk/
   usage/cost/turns/contextTokens/success` (`internal/subagent/spawn.go:65-108`).
   Wrap: `orchestrator.Event{InstanceName, Seq, At, subagent.ChildEvent}`.
   Never add orchestrator-only fields to `ChildEvent` itself — that protocol
   is shared with the `subagent` tool and must not fork.
2. **`subagent.ChildProcess`'s stdio framing — reuse via one new
   constructor, not a fork of `Spawn`.** `ReadEvent`/`SendApprovalSafe`/
   `SendExpedite` (`spawn.go`) are exactly the supervisor loop needed for a
   turn process, but the only constructor today is `Spawn`, which hardcodes
   `lookupExecutable`, `POISSON_SUBAGENT_*` env, and `Setpgid` (`spawn.go:232-270`)
   — none of which apply to a `systemd-run`-spawned process. **Add
   `subagent.AttachChild(stdin io.WriteCloser, stdout io.Reader) *ChildProcess`**
   (new exported func, ~5 lines) so the orchestrator owns the actual process
   but reuses the framing/read/approval logic verbatim. Note:
   `ChildProcess.Kill`/`Reap` assume `c.cmd != nil` (`spawn.go:351`) — the
   attached variant must nil-guard these, or the orchestrator must simply
   never call them (prefer nil-guard; the attached child's process lifecycle
   belongs to the caller, not to `ChildProcess`).
3. **`docs/server-mode-plan.md`'s *decisions* — reuse wholesale. Its
   *code* — none exists to reuse** (verified: this doc is design-only prose;
   grepped the repo, no `internal/web`, no `runServe`, no `liveSession` type
   exists anywhere). Decisions worth carrying over directly:
   - Four-state status enum: `idle | queued | running | awaiting_approval`.
   - **Approvals block indefinitely — never auto-approve, never auto-deny on
     disconnect** (`docs/server-mode-plan.md:168-179`).
   - Resolve an approval **exactly once**, replay the recorded decision on a
     duplicate resolve attempt (`:164`).
   - "The store is truth; in-flight turns die with the process" (`:86`) — no
     attempt to resume a turn process across an orchestrator restart, only
     the conversation/session state.
   - Two independent concurrency caps: live instances vs. concurrently-
     running turns.
   - Eviction/cleanup logic must never touch a running or awaiting-approval
     session.
4. **`sandbox.Manager.EnableDiscovery` pattern — reuse the *pattern*, not
   the code.** `manager.go:22-62` + `podman_driver.go:428`: the backend's own
   `List` call is the source of truth across process restarts; the in-memory
   map is a cache, never the other way around. The orchestrator mirrors this
   with `machinectl list` (+ a state-dir scan) as truth, exactly analogous to
   how Podman sandbox crash-recovery already works.
5. **Test scaffolding — copy the existing shape.**
   `internal/sandbox/fake_driver.go` (in-memory double),
   `podman_pure_test.go` (pure argv-builder unit tests, no binary needed),
   `podman_integration_test.go` (gated real-backend suite, opt-in via env
   var). The orchestrator gets `FakeRuntime`/`FakeFrontend`, pure
   `nspawnArgs`-builder tests, and one gated orchestrator-host integration test,
   following this exact three-tier pattern.
6. **`px cost <session>` / `px sessions` — reuse via in-container `Exec`**
   for `/status`, instead of writing new cost-reporting plumbing
   (`cmd/px/main.go:628-677`, `:590-608`).
7. **Static px binary, bind-mounted, never baked into the image.**
   `go.mod` already uses `modernc.org/sqlite` (pure Go, no cgo) — a
   `CGO_ENABLED=0 GOARCH=arm64` build is fully self-contained. Bind-mount
   `/usr/local/bin/px` read-only into every instance so upgrading px for
   every instance is one atomic host-side file swap, and the golden image
   never needs to carry (or be rebuilt to update) px at all.
8. **HTTP client idiom**: model the Telegram client on
   `internal/mcpclient/client.go:97-145` (stdlib `net/http`, context timeout,
   `io.LimitReader`, `encoding/json`). **Do NOT reuse `provider.DoWithRetry`**
   — it's modeled on LLM streaming-request semantics and its
   `RetryTrace`/`MaxElapsed` contract doesn't fit an infinite Telegram
   long-poll loop.
9. **Instance naming — do NOT reuse `sandbox.ResolveSandboxName`
   verbatim.** Checked directly: `sanitizeSandboxName` (`driver.go:81`)
   permits underscores and caps length at 40. nspawn machine names must be
   valid hostnames (RFC1123: no `_`, ≤64 chars, no leading/trailing `-`).
   Copy the *shape* (centralized resolver + sanitizer + random fallback), not
   the function itself — see §7 Step 13 for the actual rules.

---

## 5. 🚨 Security tradeoff — full root, shared kernel, host networking (flagged explicitly, not buried)

`--private-users=no` + `--capability=all` (minus `CAP_SYS_MODULE`, see
assumption 9 below) means uid 0 inside an instance is genuinely uid 0 on
orchestrator-host, holding nearly the full capability set, against a **shared host
kernel**. systemd-nspawn is a namespace-and-cgroup wrapper, not a hypervisor
— there is no second kernel and no virtualization boundary between an
instance and the host.

Concrete consequences:

1. **Any container escape is immediate, complete host root.** No unprivileged-
   uid speed bump exists, because the uid mapping that would provide one is
   exactly what's disabled by design.
2. Even with `CAP_SYS_MODULE` dropped, the remaining capability set (notably
   `CAP_SYS_ADMIN`) is still very powerful — this is a mitigation, not a fix.
3. **Bind mounts are host filesystem writes** — `/work`, and any per-instance
   secrets bind, are real host paths written by real host root.
4. **With host networking, an instance is on the Tailscale tailnet as
   orchestrator-host.** It can reach every tailnet peer (`bastion`, `ubuntu-1`,
   `pixel-8a`, etc.) with orchestrator-host's own identity. **The blast radius is not
   one Raspberry Pi — it is the whole tailnet.**
5. **The threat model is not hypothetical**: the thing running as root
   inside is an LLM agent executing model-generated commands, driven by a
   Telegram chat. Prompt injection reaching that agent (a fetched web page,
   a cloned repo's README, a file it reads) is a direct path to host root.

**This configuration is appropriate for a single-operator personal box where
power/flexibility is explicitly preferred over defense-in-depth, and where
the operator accepts that a compromised instance equals a compromised host
and tailnet. The user has stated that preference; this design implements it.
Recorded here so the acceptance is explicit and dated, not implicit.**

Cheap mitigations that do **not** compromise "full root inside", worth taking
regardless:
- `MemoryMax=`/`CPUQuota=`/`TasksMax=` per instance (already planned, §7
  Step 12) — bounds accidental host DoS. **Important given orchestrator-host has 0B
  swap**: without a cap, one instance's OOM is a host-wide OOM kill that may
  pick vaultwarden or sshd instead of the offending instance.
- Strict Telegram `allowed_user_ids` allow-list — it is the sole
  authentication boundary in front of host root.
- Telegram bot token stored `0600`, never bind-mounted into an instance.
- Per-instance secrets, never a shared baked-in credential set (§7 Step 15).
- `CAP_SYS_MODULE` dropped by default (assumption 9) — closes the trivial
  "load a kernel module, own the host" path at near-zero cost to practical
  root functionality. Overridable back to plain `--capability=all` if module
  loading is genuinely needed for some workload.

---

## 6. Current, verified state of the target host (orchestrator-host) and relevant code

Re-confirmed live over SSH during planning (not stale — checked after the
initial scout pass, including after unrelated commits landed on master):

```
Linux orchestrator-host 6.8.0-1064-raspi #68-Ubuntu SMP PREEMPT_DYNAMIC aarch64
Ubuntu 24.04.5 LTS (noble),  systemd 255 (255.4-1ubuntu8.17), unified cgroup hierarchy
/sys/fs/cgroup: cgroup2fs          <- cgroups v2, good
nproc 4 | Mem 7.6Gi (924Mi used, 6.7Gi available) | Swap 0B | / 229G, 189G free
dpkg --print-architecture: arm64
root filesystem: ext4 (/dev/sda2) -> machinectl clone is a full recursive copy, NOT a CoW snapshot
```

| Check | Result |
|---|---|
| `systemd-nspawn`, `machinectl`, `importctl` | **all MISSING** (expected — `importctl` was split out of `machinectl` in systemd v257; orchestrator-host runs 255, where `machinectl pull-tar`/`pull-raw` is the correct/only tool, not a gap) |
| `apt-get install -s systemd-container` | ✅ resolves clean: `systemd-container 255.4-1ubuntu8.17 [arm64]` + `libnss-mymachines`, 2 new packages, no other side effects simulated |
| `debootstrap` / `mmdebstrap` | both MISSING — **not needed**, see assumption 1 (using `machinectl pull-tar` instead) |
| `/var/lib/machines` | does not exist yet |
| `systemd-run` | present |
| `machine.slice` | `Delegate=no` (harmless — nspawn creates its own scope regardless) |
| kernel modules | `veth`, `bridge`, `overlay` all loaded |
| userns | `kernel.unprivileged_userns_clone=1`, `max_user_namespaces=28899` (irrelevant once `--private-users=no` disables it per-container) |
| `px` binary | **already installed** at `/usr/local/bin/px`, but **stale (v0.1.0)** vs. repo's current version — redeploy per §7 Step 4 |
| `go`, `git` | both present |
| **`podman` 4.x** | **already installed** (a fact the very first scout pass missed) — doesn't change the nspawn recommendation; see §8 open item |
| Already running | `vaultwarden` (Docker, healthy, `127.0.0.1:8081`), `albyhub` (exited) — **do not disturb either** |
| Network | `eth0` (LAN+public IPv6), `tailscale0` (a tailnet /32 address), `docker0` (down), `br-...` (vaultwarden's Docker bridge, up), `net.ipv4.ip_forward=1` |
| Firewall | `ufw active`, only `22/tcp` allowed inbound; `iptables -P INPUT DROP / -P FORWARD DROP`; chains for Docker + `ufw-*` + Tailscale (`ts-forward`/`ts-input`) all co-own the ruleset; `nft` reports the table is `iptables-nft`-managed, "do not touch" |

Code facts, re-verified during the implementation-plan pass (after a
`fix(subagent): killed jobs were narrated as failures, not kills` commit
landed on master — confirmed **not** on this feature's path, `ChildEvent`/
`forwardChildEvents` untouched by it):

- `internal/sandbox/podman_driver.go:407` — `Kill` → `forceRemove` → `podman
  rm -f -t 0`. `:246-270` — `Start` re-runs `bootstrap` (apt-get path),
  unusable as a resume primitive. `:283-355` — exec drops to a non-root
  bootstrapped user via `--userns=keep-id`. `:428-431` — discovery via
  `podman ps --filter label=poisson.sandbox=1`.
- `cmd/px/main.go:289-415` — `runPrint` already resumes a session by id,
  appends one turn, exits. **Gaps found**: does not wire `SubApproval`/
  `SudoPasswordFn`/`CrossProviderApprovalFn` today (so a plain `-p` session
  gets no `subagent` tool and no sudo-password prompt — a fact to preserve
  or deliberately change, see §7 Step 6); **does** wire a Podman
  `SandboxManager` today, which is dead weight inside an nspawn instance
  (recommend dropping it in `--print-json` mode, see §7 Step 6); uses
  `a.Prompt` (no cancellation context) — needs `a.PromptWithContext` (§7 Step
  9).
- `cmd/px/main.go:1149-1205` — `forwardChildEvents`/`writeChildEvent` already
  exist, already extracted into reusable functions, already unit-tested
  (`cmd/px/child_events_test.go`), callable directly from `runPrint`.
- `cmd/px/child_approval.go` — `childApprovalBroker`: FIFO, serialized (one
  outstanding approval at a time), blocks indefinitely, stdin EOF auto-denies
  all waiters (`denyAllWaiters`, `:89`). Fully reusable as-is.
- `internal/store/session.go:238-248` — `UpdateSession` is one UPDATE
  covering title/cwd/provider/model/updated_at. `cmd/px/main.go:333-341` —
  `runPrint` already calls this exact path when `sess.Provider != provName ||
  sess.Model != model`. **This one branch already IS the entire `/model`
  persistence mechanism** — do not build a second one.
- `internal/config/config.go:151` — `TrustedProviders` shape; `:549` —
  `noModelTables` guard; `:555-560` — `ProvidersMutuallyTrusted`; `:572-760`
  — `mapToConfig`'s `lookup()`-based if-ladder, the idiom to copy for a new
  `[orchestrator]` section; `:703-716` — `subagent.trusted_providers`
  parsing, the closest existing analog for an allow-list; `:709` —
  `ResolveProviderMeta`, used to validate each allow-listed model at load
  time; `:493-503` — `ConfigDir()` is `$HOME/.poisson`, **no env override** —
  matters for where an instance's `auth.json` needs to be bind-mounted to
  (`/root/.poisson/auth.json` inside, since instances run as root).
- `cmd/px/main.go:925-927` — `runChildMode`'s existing `--` prompt-terminator
  parsing is the pattern to mirror for `-p --print-json`'s own parser
  (currently `-p` has a latent footgun here — see §7 Step 5).
- `internal/mcpclient/client.go:97-145` — the HTTP-client idiom to copy for
  the Telegram client (§4 reuse decision 8).

---

## 7. Implementation plan

State dir throughout: `/var/lib/px-orchestrate/` (on orchestrator-host). Instance
rootfs: `/var/lib/machines/<name>/`. Milestones are sequenced by
independent-value-and-testability: each milestone should be fully working
and verifiable on its own before the next begins.

### M0 — Prerequisites (no Go code)

#### Step 1. Telegram supergroup + bot setup (user-facing runbook, write into this doc's own §Setup once built)
**What.** Exact click-path: BotFather `/newbot` → get token. Create a Telegram
group → promote to supergroup → Group Settings → **Topics: on**. Add the bot
as admin with **Manage Topics**, **Post Messages**, **Delete Messages**
rights. **BotFather → Group Privacy: off** (critical — otherwise the bot only
sees `/commands`, never plain messages). Post one message, then call
`getUpdates` to read the numeric `chat_id` (negative for a supergroup) and
your own numeric Telegram user id.
**Why here.** Token, `chat_id`, `allowed_user_ids` are config inputs that
Step 23/M4 cannot be tested without, and migrating a group to a supergroup
with Topics is effectively irreversible — do this before anything depends on
it.
**Edge cases.** Privacy mode left on → the bot silently never receives plain
messages (the single most common failure mode for this kind of bot — it
looks like the bot is broken when it's actually just deaf). Group not
actually migrated to a supergroup → `message_thread_id` rejected by the API.
Bot lacking Manage Topics → `/new` fails specifically at the
`createForumTopic` call, not at instance creation — make sure error messages
distinguish this.
**Verify.** `curl "https://api.telegram.org/bot<TOKEN>/getUpdates"` returns
your test message with a negative `chat.id` and `chat.is_forum: true`.

#### Step 2. orchestrator-host host prep
**What.** `apt install systemd-container` (resolves to `255.4-1ubuntu8.17`,
already in the archive — confirmed via `apt-get install -s`, dry-run
simulation, no side effects beyond `libnss-mymachines`). Create
`/var/lib/machines` and `/var/lib/px-orchestrate/instances`. **Do not**
install `debootstrap` (see assumption 1 — using `machinectl pull-tar`
instead). **Do not** touch vaultwarden's Docker setup or Tailscale. **Add no
iptables/ufw/nft rules of any kind.**
**Why here.** Everything in M2 shells out to `systemd-nspawn`/`machinectl`,
neither of which exist on the box yet.
**Edge cases.** `systemd-container`'s install may touch `systemd-resolved`
interactions — run the dry-run (`apt-get install -s`) first and read its
plan before actually installing, specifically checking it won't restart
networking on a box reached only over SSH/Tailscale.
**Verify.** `systemd-nspawn --version` prints 255. `machinectl list` returns
"No machines." `ufw status numbered` and `iptables-save | wc -l` are
byte-identical before and after the install (diff them explicitly, don't
just eyeball).

#### Step 3. Golden rootfs
**What.** `machinectl pull-tar` the **Ubuntu 24.04 arm64** cloud rootfs
tarball into `/var/lib/machines/px-golden`. One-time customization inside
it: `apt install git ripgrep ca-certificates curl jq`; disable
`systemd-networkd`/`systemd-resolved` (host networking is used, see §3.1);
lock the root password; pre-create `/root/.poisson/` and `/work`. **Do not**
put a px binary or any secrets in the golden image — px is bind-mounted
(§4 reuse decision 7), secrets are generated per-instance (§7 Step 15).
**Why here.** Step 12 (`Create`) clones this image for every new instance;
its contents decide what every instance can do out of the box.
**Edge cases.** orchestrator-host's root is **ext4**, so `machinectl clone` is always a
full recursive copy (~1.0-1.5GB per instance, not a CoW snapshot) — this is
a real sizing input for how many instances fit in 189GB free, factor it into
the resource-limit decision at Step 12/assumption 4. `pull-tar` verifies
signatures by default — a `--verify=no` fallback must be a conscious,
documented choice if signature verification ever fails, not a silent
workaround. Must be the **arm64** tarball (orchestrator-host is aarch64), not amd64.
The image must contain a real systemd (PID 1) since instances run
`--boot`.
**Verify.** `systemd-nspawn -D /var/lib/machines/px-golden --private-users=no
/bin/bash -c 'rg --version && git --version && ls -d /work'` succeeds.
`du -sh /var/lib/machines/px-golden` — record this number as the
per-instance disk-cost baseline.

#### Step 4. Deploy a current px to orchestrator-host
**What.** `CGO_ENABLED=0 GOARCH=arm64 go build -o px ./cmd/px`, scp to
`/usr/local/bin/px` on orchestrator-host (currently v0.1.0 there — stale vs. the
repo's current version).
**Why here.** M1 adds flags the in-container px must have. Since px is
bind-mounted (not baked into the image), the host copy IS every instance's
copy — this is the entire upgrade mechanism for the whole feature going
forward.
**Edge cases.** `file px` must report "statically linked" (a cgo-enabled
build would silently break inside a differently-versioned rootfs — confirm
`modernc.org/sqlite` really has no cgo path by checking `ldd` on the output,
not just trusting `CGO_ENABLED=0`). Replacing the binary while an instance is
mid-turn: write to `px.new` then atomically `mv` it into place — an atomic
rename doesn't disturb a process that already has the old inode open.
**Verify.** `ssh root@orchestrator-host 'px -v'` prints the repo's current version.
`ldd /usr/local/bin/px` says "not a dynamic executable".

---

### M1 — Headless JSON turn mode (`px -p --print-json`)

**Deliberate reordering vs. an earlier draft of this plan**: approvals move
from a later "polish" milestone into M1. `childApprovalBroker` already
exists and is fully tested — wiring it now costs ~20 lines during the same
`runPrint` edit and avoids a second editing pass over the same function
later.

This entire milestone is testable standalone via plain CLI invocations — no
nspawn, no Telegram, no orchestrator code at all yet.

#### Step 5. `--` prompt terminator and `--print-json` flag
**What.** In `cmd/px/main.go`: add a `printOpts.jsonOut` field. Extend
`parseArgs` to accept `--print-json`, and add a `--` terminator so everything
after it joins verbatim into `opts.prompt` — mirroring `runChildMode`'s own
parser (`main.go:925-927`). Update `helpText()` to document both.
**Why here.** Every downstream invocation looks like `px -p --print-json
--session <id> -- <telegram message>`. Without the `--` terminator, a
message that happens to start with `-` gets swallowed by `-p`'s existing
`!strings.HasPrefix(args[i+1], "-")` guard (`main.go:204`), and a multi-word
message risks being rejoined incorrectly. This is a real, currently-latent
bug in `-p`'s argument handling, not a hypothetical one — worth fixing even
independent of this feature.
**Edge cases.** A literal `--` appearing inside the message body itself (only
the *first* `--` may terminate flag parsing). An empty prompt after `--`
should exit 2 with the existing "no prompt given" message, not silently
proceed. `--print-json` given without `-p` is a user error, not silently
ignored. `--print-json` combined with `--yolo` is legal (approvals
auto-approve, the broker from Step 8 is simply never invoked).
**Verify.** Add cases to `cmd/px/main_test.go` alongside the existing
`TestParseArgsModelAndSessionDoNotSwallowNextFlag`. Update
`TestHelpDocumentsTopLevelOptions` (it asserts on help text content and will
fail once help text changes — this is expected, not a regression). Manually:
`px -p --print-json -- "-rm test"` should emit a JSON `text` event about that
literal string, not a flag-parsing error.

#### Step 6. Extract `runPrint`'s wiring into a shared builder
**What.** Pull the config/store/session-resolve, approval-closure,
`tools.BuildRegistry`, `agent.NewAgent`, skills, and
`ReloadConfigDependentTools` block (`main.go:290-390`) out into one
unexported function, e.g. `buildPrintAgent(opts printOpts) (*agent.Agent,
*store.Store, chan agent.OutputEvent, func() /*cleanup*/, error)`, in a new
file `cmd/px/print_session.go`. This is a **rework of existing code**, not
new logic — text-mode and JSON-mode `runPrint` then differ only in their
output pump and their approval closure, both built on top of this one shared
builder.
**Why here.** Both Step 7 (JSON output) and Step 8 (approval broker) need
this same wiring; duplicating it across two code paths is exactly the kind
of drift `docs/server-mode-plan.md` already warned about for a similar
situation. **Explicit scope limit**: do NOT attempt that doc's full
`internal/session.New` extraction here — that would drag `runREPL` and the
whole TUI into this feature's blast radius for zero benefit to the
orchestrator. Keep the rework scoped to exactly what `runPrint` itself needs.
**Edge cases.** Must preserve today's exact error/exit semantics (exit 2 on
an invalid model, exit 1 on a store/session failure) — do not let refactor
alone change externally-visible CLI behavior. Must keep the
`sess.Provider != provName || sess.Model != model` single-UPDATE branch
(`main.go:333-341`) intact — that one `if` **is** the `/model` command's
entire persistence mechanism, downstream. **Decision needed and must be
documented in code**: whether `SandboxManager` (Podman) stays wired when
`--print-json` is set. Recommendation: **drop it** in JSON mode — podman
isn't in the golden image, so offering `create_sandbox` to an in-instance
agent just produces confusing tool failures for no benefit.
**Verify.** `go test ./cmd/px/... -count=1` green with no test changes beyond
Step 5's additions. `px -p "hi"` (plain text mode, unchanged) produces
byte-identical output to before the refactor, run against a fake/test
provider session.

#### Step 7. JSON event output path
**What.** Inside `runPrint`, when `jsonOut` is set: replace the current
stdout/stderr text pump (`main.go:393-405`) with a call to
`forwardChildEvents(outputChan, a, writeChildEvent)` (already exists, already
tested), and emit the same terminal `done` event `runChildMode` already
emits at the end of a turn (`main.go:1129-1138`). Add the same
`recoverChildPanic` deferred-recovery pattern used elsewhere
(`main.go:904-909`, `:1100-1104`) so a panic mid-turn becomes a diagnosable
`error` event instead of a silently-closed pipe.
**Why here.** Depends on Step 6's extraction. Every consumer built in M3+
parses exactly this event stream — get its shape right here once.
**Edge cases.** stdout must carry **nothing but** JSON lines in this mode
(today's `[tool: X]` trace lines go to stderr — keep that split; stderr
becomes the orchestrator's separate diagnostics channel, not part of the
protocol). Each event must be exactly one line (`writeChildEvent` already
uses `json.Marshal` + `Println`, which is already safe for text containing
embedded newlines — verify this holds, don't just assume). A turn that
errors must still emit a final `done{success:false}` event before the
process exits — never let an error just terminate the process with no
terminal event, or the orchestrator can't distinguish "turn failed" from
"turn process crashed with no signal at all." Exit code should remain
non-zero (1) on error, giving the orchestrator a second, redundant signal
independent of the JSON stream.
**Verify.** `px -p --print-json --session s-test -- "say hi"` piped through
`jq -c .` parses every single line without error; the very last line is
`{"type":"done",...}`. Extend `cmd/px/child_events_test.go`'s existing table
of cases to include the print-mode caller specifically (not just child
mode).

#### Step 8. Approval round-trip over stdin
**What.** When `jsonOut && !yolo`: the `humanApproval` closure becomes
`approveViaChildBroker(ctx, &broker, event)`, building the same
`approval_request` payload `runChildMode` already builds
(`main.go:993-1014`), minus the `agent` field (which is child-mode-specific
metadata not relevant here). Start the broker before the turn begins.
`--yolo` preserves today's blanket-approve behavior unchanged, with no
broker involved at all.
**Why here.** Same file, same editing session as Step 7 — the broker
(`cmd/px/child_approval.go`) is already fully written and has 8 existing
test cases (`child_approval_test.go`); this is purely wiring, no new logic.
**Edge cases.** **Stdin conflict, the one genuinely tricky part**:
`runPrint` today slurps stdin for the prompt itself when none is given on
the command line (`main.go:116`). In `--print-json` mode, stdin instead
*becomes* the approval-response channel — so an explicit prompt (via the `--`
terminator from Step 5, or a plain `-p <arg>`) must be **required**, and the
process should exit 2 immediately if neither is given, rather than
ambiguously trying to read a prompt from the same stream approvals arrive
on. If the parent process (the orchestrator) dies, stdin sees EOF, which
already triggers `denyAllWaiters` (`child_approval.go:89`) — the turn ends
with a recorded denial in the transcript, not an indefinite hang; confirm
this path is actually exercised, don't just assume the existing code covers
it correctly for this new caller. Approval requests are already serialized
by `serialMu` inside the broker — at most one is ever outstanding at a time,
which the orchestrator (§ M3) can rely on as an invariant.
`fileApprovalFn`/`sandboxApprovalFn` already route through `humanApproval`,
so they get this behavior for free with no separate wiring.
**Verify.** Manual end-to-end: `printf
'{"type":"approval_response","approved":false,"reason":"nope"}\n' | px -p
--print-json --session s-test -- "run rm -rf /tmp/x"` — confirm an
`approval_request` event appears on stdout first, and the eventual tool
result in the transcript carries the denial reason.

#### Step 9. Signal-driven cancellation
**What.** Replace the current `a.Prompt(opts.prompt)` call with
`a.PromptWithContext(ctx, ...)`, where `ctx` is cancelled on SIGTERM/SIGINT.
On cancellation, emit a terminal `done{success:false,error:"terminated"}`
event and exit non-zero.
**Why here.** `/suspend` and `/kill` mid-turn, and a future `/cancel` on a
stuck approval, all reduce to "stop this specific turn process cleanly."
Without this, a bare SIGTERM kills mid-write and the orchestrator only ever
sees a truncated, unparseable stream with no terminal event at all.
**Edge cases.** A signal arriving **while the process is blocked inside the
approval broker** (Step 8) — cancelling the context alone won't release that
block, since the broker's wait is on a plain channel, not context-aware. The
signal handler must *also* close stdin / trigger `denyAllWaiters` directly,
not rely on context cancellation alone to unblock it. A second signal while
already shutting down should force an immediate hard exit rather than wait
indefinitely for graceful cleanup. This change must not regress the
existing non-JSON `-p` path — the same signal handler applies to both modes;
document explicitly that this is intentional and verify plain-text `-p`
still behaves sanely on Ctrl-C.
**Verify.** Start a deliberately long-running turn, `kill -TERM <pid>` from
another shell, confirm a final `done` line is emitted and the process exits
within roughly a second. Separately confirm the session's transcript in the
SQLite DB still contains the user's message even though the assistant's turn
was interrupted (i.e. nothing upstream of the cancellation point was lost).

---

### M2 — nspawn Runtime

#### Step 10. `Runtime` interface
**What.** New file `internal/orchestrator/runtime.go` defining the
`Runtime` interface exactly as sketched in §2.1: `Create`, `Start`, `Stop`
(clean poweroff, rootfs kept), `Terminate` (force), `Destroy` (rm -rf
rootfs), `List`, `Exec` (buffered, for `/status`-style one-shot commands),
`StartTurn` (streaming, returns a `*Turn` wrapping an
`subagent.AttachChild`-built `*ChildProcess`, see Step 11).
`InstanceInfo{Name, State, Since, MemoryCurrent}`.
**Why here.** M3's `Core` is written entirely against this interface (using
a fake implementation first), so the interface's exact shape must be settled
before either the real implementation (this milestone) or the fake
(M3) is written. Deliberately **not** `internal/sandbox.Driver` — see §2.2
for the full reasoning; `Stop` and `Destroy` are separate methods here
specifically because `Driver.Kill` fuses them and that fusion is wrong for
`/suspend`.
**Edge cases.** Every method takes a `context.Context` and must be
idempotent — `Stop` on an already-stopped instance, `Destroy` on a rootfs
that's already gone, `Start` on an already-running instance are all defined
as success, not error (matching `Driver.Start`'s own documented idempotence
today).
**Verify.** `go build ./...` compiles with the interface defined (even with
no implementation yet, via a stub). The interface's doc comments should
match the density/precision of `internal/sandbox/driver.go:142-162` — that
file is the bar to match for documentation quality here.

#### Step 11. `subagent.AttachChild` constructor
**What.** New exported function in `internal/subagent/spawn.go`: build a
`*ChildProcess` directly from a caller-supplied `io.WriteCloser` (for stdin)
and `io.Reader` (for stdout), instead of the existing `Spawn` function's
`exec.Command`-based construction. This is a small, additive change — no
existing behavior of `Spawn` itself changes.
**Why here.** Step 12's `StartTurn` needs exactly the `ReadEvent`/
`SendApprovalSafe`/`SendExpedite` machinery `ChildProcess` already provides,
but the actual process is a `systemd-run --pipe ...` invocation, not
something `Spawn`'s `exec.Command`-based path can produce directly. The
alternative — forking the JSON-lines protocol implementation into the
orchestrator package as a second copy — is exactly the kind of duplication
that causes the two copies to drift apart over time; a 5-line constructor
avoids that entirely.
**Edge cases.** `ChildProcess.Kill()` and `Reap()` (`spawn.go:351` onward)
currently assume `c.cmd != nil` — the attached-child variant must either
nil-guard both of these, or (safer) the orchestrator must simply never call
them on an attached child, treating process lifecycle as the orchestrator's
own responsibility rather than `ChildProcess`'s. Document this explicitly in
the new constructor's doc comment so it isn't rediscovered as a bug later.
**Verify.** New unit test in `internal/subagent/`: feed a canned in-memory
JSON-lines `io.Reader` through an attached `ChildProcess` and assert
`ReadEvent` correctly decodes each event type in sequence.
`go test ./internal/subagent/... -count=1`.

#### Step 12. nspawn implementation + argv builders
**What.** New file `internal/orchestrator/nspawn/runtime.go`, shelling out to
`systemctl`, `machinectl`, `systemd-run`, plus `cp`/`rm` for rootfs
management. Following `podman_driver.go`'s own internal shape: pure,
independently-testable argv-builder functions (analogous to
`sandbox.createArgs`, `podman_driver.go:83`) separated from the actual
`exec.Command` calls — e.g. `nspawnUnitArgs`, `turnRunArgs`.

One systemd **template unit**, shipped as a file this feature installs (not
hand-typed per instance): `/etc/systemd/system/px-instance@.service`,
roughly:
```
--private-users=no --capability=all --drop-capability=CAP_SYS_MODULE --boot
--machine=%i --directory=/var/lib/machines/%i
--bind-ro=/usr/local/bin/px
--bind-ro=<state>/instances/%i/secrets/auth.json:/root/.poisson/auth.json
--bind-ro=<state>/instances/%i/config.toml:/root/.poisson/config.toml
--bind=<state>/instances/%i/work:/work
--resolv-conf=<chosen mode, see edge cases>
```
with **no** `--private-network` (§3.1). A per-instance systemd drop-in
carries `MemoryMax=1200M CPUQuota=150% TasksMax=2048 MemoryAccounting=yes
Delegate=yes` (the `Delegate=yes` is required for nested podman, §3.2, and
costs nothing if unused).

Turn invocation (used by `StartTurn`): `systemd-run --machine=<name>
--unit=px-turn-<name> --pipe --collect --wait --setenv=HOME=/root
/usr/local/bin/px -p --print-json --session <sid> --model <p/m> -- <message>`.

**Why here.** Depends on Steps 10, 11, and M0's Steps 2-4. Note: an earlier
draft of this plan wrote the capability-subtraction as
`--capability=all,-CAP_SYS_MODULE` — that syntax is **not valid nspawn**;
the correct form is the two separate flags shown above (confirmed against
the `systemd-nspawn`/`systemd.nspawn` man pages).
**Edge cases (this is the highest-risk step in the whole plan — treat each
of these as a required behavior, not a nice-to-have):**
- `machinectl start` is deliberately **not used** anywhere — it routes
  through `systemd-nspawn@.service`, which forces `-U`, silently
  contradicting the "real root" requirement. The orchestrator owns its own
  unit file explicitly instead.
- **Container fails to boot**: `systemctl start` returns success *before*
  the boot actually completes. Poll `machinectl show <name> -p State`
  combined with an actual readiness probe (`systemd-run --machine=<name>
  --pipe --wait /bin/true` succeeding) against a ~30 second deadline. On
  timeout, capture `journalctl -u px-instance@<name> -n 50` into the
  returned error so a boot failure is diagnosable, not just "timed out."
- **Stale turn unit from a crashed orchestrator**: a leftover
  `px-turn-<name>` unit from a previous crash must be `systemctl stop`ped
  before starting a new one with the same name; `--collect` on the unit
  itself handles auto-garbage-collection of failed units generally, but
  don't rely on that alone for the specific "same name reused immediately"
  case.
- **Disk fills up mid-`Create`**: pre-check free space is at least 3x the
  golden image's size (via `statfs`) before starting a clone; on any
  failure during `Create`, `rm -rf` the partial clone rather than leaving a
  half-written rootfs behind (mirrors `podmanDriver.Create`'s own
  cleanup-on-failure contract, `podman_driver.go:179-182`).
- **Zero swap (orchestrator-host has none)**: a `MemoryMax` breach is an immediate OOM
  kill, not graceful swapping. Surface this as a distinct, user-legible
  failure — check `systemctl show -p Result` and journal `oom-kill` lines
  specifically, don't let it look like a generic crash.
- **`Destroy` is an `rm -rf` reachable from a Telegram command** — it must
  refuse to operate on any path not under `/var/lib/machines/`, and must
  reject any instance name that doesn't match the sanitized form from Step
  13 exactly. Treat this function as if it were parsing untrusted input,
  because transitively, it is.
**Verify.** Pure unit tests on the argv-builder functions alone (no real
binaries needed, following the `podman_pure_test.go` pattern exactly). Then,
gated behind an environment variable the same way `podman_integration_test.go`
gates its real-backend suite, run on orchestrator-host itself: create an instance,
confirm it appears in `machinectl list`; run `systemd-run --machine=<name>
--pipe /usr/bin/id -u` and confirm it prints `0`; run `capsh --print` inside
and confirm `CAP_SYS_MODULE` is absent while `CAP_SYS_ADMIN` is present;
`systemctl show px-instance@<name> -p MemoryMax` reports `1258291200`
(1200 * 1024 * 1024).

#### Step 13. Instance naming + per-instance filesystem layout
**What.** New file `internal/orchestrator/name.go`: `ResolveInstanceName(requested
string) (string, error)` — lowercase, `[a-z0-9-]` only (note: map `_` to
`-`, unlike the existing Podman sanitizer which *permits* underscores — see
§4 reuse decision 9 for why this must differ), collapse repeated `-` runs,
trim leading/trailing `-`, cap at 32 characters, prefix `px-`, fall back to a
random 4-hex-character name when the requested name is empty. Alongside
this, a small layout-creation helper builds
`<state>/instances/<name>/{instance.json, config.toml, secrets/auth.json,
work/}` with directories at mode `0700` and the secrets file at `0600`.
**Why here.** Both Step 12's argv builders and Step 14's metadata format key
off this name; Step 12's `Destroy` path-safety guard depends on names always
being in this sanitized form.
**Edge cases.** Table-test against genuinely adversarial input, not just
happy-path names: `../../etc`, `A_B`, a 200-character string, emoji, an
empty string, a string that's all leading/trailing dashes. Any collision
with an existing instance name, or a leftover `/var/lib/machines/<name>`
directory from a previous incomplete run, must be a hard error — never
silently reuse or overwrite. Remember the name ultimately comes from
untrusted Telegram message text (via `/new <name>`) — sanitize before it
ever reaches an `exec.Command` argv, treat it exactly as adversarial input
throughout.
**Verify.** A table-driven test asserting every output matches
`^px-[a-z0-9][a-z0-9-]{0,28}[a-z0-9]$` for every adversarial input case
listed above.

#### Step 14. Per-instance metadata + crash-safe state file
**What.** New file `internal/orchestrator/state.go`:
`InstanceMeta{Name, SessionID, Model, ChatID, TopicID, CreatedAt,
DesiredState (running|suspended), PendingDestroy bool, RepoURL}`, persisted
as `instance.json` inside that instance's state directory, written via the
standard tmp-file-then-atomic-rename pattern. Plus `Load()`/`SaveAll()`/
`Scan()` helpers that enumerate every instance directory under the state
root.
**Why here.** M3's restart-reconciliation logic (Step 21) reads this at
startup; Step 12's `Destroy` consumes the `PendingDestroy` flag specifically
to make a crash-mid-kill recoverable.
**Edge cases.** A corrupt or truncated `instance.json` (e.g. from a crash
during the write) must cause that one instance to be skipped with a loud log
message — never auto-destroy its rootfs just because metadata failed to
parse; that would turn a metadata bug into data loss. An instance found with
`PendingDestroy=true` already set at startup means a previous `/kill` was
interrupted mid-flight — finish the destroy rather than leaving it
half-done. Metadata present but the corresponding rootfs missing → treat as
a tombstone, report it in `/list` as gone, and let `/kill` on it just clean
up the leftover metadata. Rootfs present but metadata missing entirely →
treat as an orphan, list it as such, but do **not** auto-adopt it (there's
no Telegram topic to route its output to, so silently absorbing it would
create an unreachable instance).
**Verify.** Unit tests against a temp directory covering the round-trip and
the corruption case. Specifically test the crash-mid-write scenario (kill
the writer process between the tmp-write and the rename in a test harness)
and confirm the previous, still-valid file survives untouched.

#### Step 15. Per-instance secrets and config generation
**What.** As part of `Create`: generate `<state>/instances/<name>/secrets/
auth.json` (mode 0600) containing credentials scoped to only the allowed
providers for this instance, and a generated `config.toml` pinning
`provider.default` plus the instance's currently-selected model. Both are
bind-mounted read-only into the instance by Step 12's unit config. **Never**
bind-mount the host's own `~/.poisson/auth.json`, and **never** bake any
credential into the golden image.
**Why here.** Depends on Step 13's layout existing first; must happen before
an instance's first boot, since the bind mounts are specified at container
start time.
**Edge cases.** 🚨 An instance has full root and can trivially read its own
bind-mounted `auth.json` — treat that credential as compromised the moment
you'd trust the instance's agent less than you trust yourself (which, given
§5's threat model, you should). Prefer a dedicated per-instance API key
where the provider actually supports issuing one, and document plainly that
reusing an OAuth/subscription token across instances means one leaked
instance leaks that entire subscription's access, not just one instance's
scope. `Destroy` must shred (not just delete) the secrets directory. A
`Create` that fails partway through must not leave a secrets file behind
for a name that never actually got a running instance. Credential rotation
is explicitly **out of scope for v1** — note it as a known future need (a
`/rotate`-style admin path that rewrites every instance's `auth.json`), not
something this plan builds now.
**Verify.** `systemd-run --machine=<name> --pipe /bin/cat
/root/.poisson/auth.json` shows exactly the per-instance file's content
(not the host's). `mount | grep poisson` inside the instance shows the bind
as `ro`. A write attempt to that path from inside the instance fails.

---

### M3 — Core + registry, tested entirely against fakes

#### Step 16. Core types: `ChannelKey`, `Command`, `Event`, `Instance`
**What.** New file `internal/orchestrator/types.go` with the types sketched
in §2.1: `ChannelKey` as a plain comparable struct (usable directly as a map
key), `Command`, `Event` (embedding `subagent.ChildEvent` directly, per §4
reuse decision 1 — **no translation layer**), and `Instance` (holding
`Meta InstanceMeta`, its `ChannelKey`, an atomic status matching the
four-state enum from §4 reuse decision 3, its own bounded mailbox channel,
and any currently-pending approval).
**Why here.** Both the eventual Telegram frontend (M4) and the nspawn
runtime (M2, already built by this point) are consumed through these types
— define them before the `Core`'s actual body so the core logic never
leaks a transport-specific or runtime-specific type by accident.
**Edge cases.** `ChildEvent`'s `Approved` field carries `omitempty` on a
plain bool, which is harmless on the read side, but the orchestrator must
never construct an approval *response* by hand-marshaling a `ChildEvent` —
always go through `SendApprovalSafe` (`spawn.go:291`), which builds its own
correct map and doesn't have this footgun.
**Verify.** `go vet` clean. A compile-time assertion (e.g. a throwaway
`map[ChannelKey]int` declaration in a test) that `ChannelKey` is usable as a
map key without issue.

#### Step 17. `Frontend` interface + `FakeFrontend`
**What.** New file `internal/orchestrator/frontend.go` with the `Frontend`
interface from §2.1 (`Run`, `CreateChannel`, `CloseChannel`, `Send`, `Edit`),
where `Message` is plain text plus an optional kind tag — no Telegram-shaped
fields anywhere in this type. Alongside it, `FakeFrontend`: an in-memory
double that records every `Send`/`Edit` call and lets a test inject
`Command`s directly onto the channel — following the exact pattern
`internal/sandbox/fake_driver.go` already establishes for `Driver`.
**Why here.** Step 19's `Core` tests need this fake; the real Telegram
implementation (M4) just has to satisfy this same interface later, with zero
changes to anything built in this milestone.
**Edge cases.** `Edit` may not be supported by every future frontend — define
it as explicitly best-effort, with callers expected to fall back to a plain
`Send` if `Edit` returns an error or isn't wired.
**Verify.** A `var _ Frontend = (*FakeFrontend)(nil)` compile-time assertion.

#### Step 18. `FakeRuntime`
**What.** An in-memory `Runtime` double supporting scripted event streams
per turn and injectable failure modes: boot failure, disk full, an OOM kill,
a hung turn, a mid-turn approval request — enough to drive every edge case
listed in Steps 19-21 without ever touching a real nspawn container.
**Why here.** Every edge case below is either impractical or far too slow to
exercise against real containers in a normal test run.
**Verify.** `var _ Runtime = (*FakeRuntime)(nil)`. Used by every test in
Steps 19 through 22.

#### Step 19. Core registry + per-instance actor loop
**What.** New file `internal/orchestrator/core.go`:
```
Core{
  mu sync.Mutex
  byKey  map[ChannelKey]*Instance
  byName map[string]*Instance
  rt     Runtime
  fe     Frontend
  cfg    *OrchestratorConfig
  turnSem chan struct{} // global concurrent-turn cap
}
```
One goroutine per instance, each owning its own bounded mailbox channel; the
`Core` itself only ever routes incoming `Command`s to the right instance's
mailbox and never runs a turn directly. A global `turnSem` semaphore caps
how many turns run concurrently across *all* instances (separate from how
many instances merely exist, per §4 reuse decision 3's "two independent
caps"); a turn that can't immediately acquire a slot posts an explicit
"queued" notice to its topic rather than silently blocking with no user
feedback.
**Why here.** The concurrency model has to exist before any command is
actually wired to it in Step 22.
**Edge cases.**
- **Two Telegram messages arrive for the same topic before the first turn
  finishes**: the second simply queues in that instance's own mailbox;
  there's no cross-instance contention to worry about since each instance
  has its own goroutine and its own queue.
- **Mailbox genuinely fills up** (pick a bound, e.g. 8): reply explicitly
  "queue full, dropping this message" rather than silently dropping it —
  this is exactly the class of bug `7dcad36`'s predecessor commit
  (`19723c6`, "drop queued messages on session switch instead of leaking
  them into the next one") already had to fix once in the TUI; don't
  reintroduce a silent-drop bug in a new place.
- **Message arrives for an unknown/already-killed topic**: reply once with
  an explanation, never silently ignore, never auto-create a new instance
  for it.
- **`/new` requested while already at the configured `max_instances`
  ceiling**: refuse explicitly and name what's currently running, don't just
  fail generically.
- **An instance's goroutine panics**: recover, mark that instance `dead`,
  post the panic (or a redacted summary of it) to its topic — the same
  `recoverChildPanic` precedent used elsewhere in this codebase.
- **Orchestrator shutdown**: cancel every in-flight turn's context, force-
  deny every pending approval (don't leave them hanging forever across a
  planned restart), wait briefly for goroutines to exit, then terminate —
  matching `docs/server-mode-plan.md`'s own shutdown design.
**Verify.** Unit tests entirely against `FakeRuntime`+`FakeFrontend`: two
rapid-fire messages to the same instance produce two turns that run
strictly in order, never overlapping. `go test
./internal/orchestrator/... -race -count=1` must be clean — this package is
exactly the kind of concurrent code where a data race would otherwise hide
until production.

#### Step 20. Turn execution + event fan-out
**What.** Inside each instance's goroutine: call `rt.StartTurn`, then loop
`ReadEvent` on the resulting `*Turn`, mapping each `ChildEvent` onto
whatever the frontend needs to render (assistant text batched together
rather than sent per-token, a tool call rendered as a single one-line
trace, `error`/`done` treated as terminal states that end the loop). Batch
text output on roughly a 3-second timer or a ~3500-character size
threshold, whichever comes first, before actually calling `fe.Send`.
**Why here.** Depends on Steps 18 and 19 both existing already; M4 later
only has to swap out *which* frontend receives these calls, none of this
loop's logic changes.
**Edge cases.**
- **Turn process exits without ever emitting a `done` event** (it was
  killed, OOM'd, or panicked with no chance to clean up) — synthesize a
  terminal event from the process's exit status in this case, and
  explicitly distinguish "we killed it on purpose" from "it crashed" in
  that synthesized event — this is precisely the class of distinction
  `7dcad36` had to retrofit onto the subagent system after the fact; build
  it in correctly from day one here instead of repeating that mistake.
- **An unparseable line appears on stdout** (`ReadEvent` returns a parse
  error): log it and skip that one line, never let a single bad line abort
  the whole turn.
- **A tool dumps an enormous amount of output**: cap how much of any single
  event's text actually reaches the frontend, regardless of how much the
  tool itself produced.
- **`done{success:false}`**: post the error text to the topic, then leave the
  instance in a normal `idle` state afterward — a failed turn is not itself
  a reason to consider the instance broken.
**Verify.** A fake-driven test asserting: a scripted sequence of 12 text
deltas plus 2 tool-call events plus a final `done` event collapses into at
most 3 actual frontend `Send` calls, ends in `idle` status, and preserves
the original ordering of tool calls relative to text.

#### Step 21. Restart reconciliation
**What.** On orchestrator startup: scan the state directory (Step 14), call
`rt.List()` for the ground truth, and reconcile the two — following the
`sandbox.Manager.EnableDiscovery` pattern exactly (§4 reuse decision 4): the
runtime's own `List` is truth, the state directory is a cache on top of it,
never the reverse. Concretely: finish any instance still marked
`PendingDestroy`; `systemctl stop` any orphaned `px-turn-<name>` unit left
over from a previous crash; `Start` any instance whose `DesiredState` is
`running` but which the runtime reports as stopped; post an explicit
"orchestrator restarted, any previous turn was interrupted" notice to every
affected topic.
**Why here.** Depends on Steps 14 and 19 both existing. This same code path
is also the direct answer to "what happens when orchestrator-host reboots" — with a
`Restart=always` host-level unit (Step 27), this reconciliation logic runs
automatically after every reboot with no separate reboot-specific code
needed.
**Edge cases.** A turn unit is a transient **host**-systemd unit, not
something the orchestrator process itself owns in memory — it survives the
orchestrator's own death but can never be meaningfully "re-attached" to a
new orchestrator process; the correct handling is simply to stop it, not to
attempt any kind of reconnection. Because the user's message that started
that turn is already durably persisted in the instance's own session DB
(this was true the moment the turn started, regardless of whether it
finished), the *next* message to that instance still continues the same
conversation coherently — the reconciliation notice should say exactly
that, so the user isn't confused about whether their earlier message was
lost. Metadata says an instance should exist but the actual machine is
missing entirely (e.g. host rebooted before an instance had ever actually
been started) → start it fresh. Metadata is missing but a machine is
found running → report it as an orphan, do not silently adopt it (there is
no topic to route its output to). Reconciliation itself must be safely
re-runnable across repeated restarts without posting duplicate notices —
track something like a `last_notified_boot_id` per instance to dedupe this.
**Verify.** `systemctl restart px-orchestrate` with an instance currently
running: confirm `/list` still shows it as `idle` afterward, a fresh message
to it still works, and no duplicate topics or duplicate notices appear.
Separately: `kill -9` the orchestrator process mid-`/kill` on some instance,
restart it, and confirm the interrupted destroy actually completes on the
next startup.

#### Step 22. Command dispatch
**What.** New file `internal/orchestrator/commands.go` implementing:
`/new [name] [repo-url]`, `/list`, `/status`, `/model <provider/model>`,
`/suspend`, `/resume`, `/kill`, `/approve <id>`, `/deny <id> [reason]`,
`/cancel`, `/help`. Topic-scoped commands resolve their target instance from
the incoming `Command`'s `ChannelKey`; `/new` and `/list` are the two
commands that make sense from the General topic (no specific instance
context).
**Why here.** Depends on everything in Steps 19-21 already existing; still
fully testable against fakes, with zero Telegram involvement yet.
**Edge cases.**
- `/model` given a value not on the configured allow-list → refuse
  explicitly, echo back the actual allowed list, change nothing about the
  instance's current model.
- `/model` issued mid-turn → accepted, but explicitly only takes effect on
  the **next** turn (it's written into that instance's metadata and passed
  as `--model` the next time `px -p` is invoked, which lands directly in
  `runPrint`'s existing single-UPDATE path from §6) — the command's reply
  must say this out loud, not just silently queue the change.
- `/kill` mid-turn → set `PendingDestroy` first, stop the turn unit, attempt
  `machinectl poweroff` (falling back to `terminate` after a short deadline
  if poweroff doesn't complete), `rm -rf` the rootfs, close the Telegram
  topic, and only then drop the instance's registry entry — in exactly
  that order, so a crash at any point in this sequence leaves something
  Step 21 can finish cleanly on the next startup.
- `/suspend` mid-turn → the same "stop the turn first" step as `/kill`, then
  `machinectl poweroff` + `systemctl stop` (rootfs kept, unlike `/kill`),
  `DesiredState` set to `suspended`.
- `/resume` on an instance that's actually been `/kill`ed already → a clear,
  specific error, not a generic failure.
- An unauthorized Telegram user id sends any command → ignore it entirely,
  silently — do not even acknowledge that the bot exists to them, since an
  error reply is itself a free "yes, there's a bot here" oracle to a
  potential attacker.
- `/new` given a repo URL → the clone happens as a first exec step *inside*
  the already-created instance, not as part of instance creation itself; a
  clone failure should leave a live, working instance with an empty `/work`
  plus an explanatory message — never a half-destroyed instance because the
  clone step failed.
**Verify.** Table-driven dispatch tests against `FakeRuntime`/`FakeFrontend`
covering every command crossed with every possible instance state,
including the combinations that are supposed to be illegal (e.g. `/resume`
on a never-suspended instance) — assert the *specific* error message in
each illegal case, not just "returns an error."

#### Step 23. `[orchestrator]` config section
**What.** In `internal/config/config.go`: a new
`OrchestratorConfig{TelegramToken, ChatID, AllowedUserIDs []string,
AllowedModels []string, DefaultModel, MaxInstances, StateDir, Image}`
attached to `Config`. Parse it inside `mapToConfig` using the exact same
`lookup()`-based idiom already used for `subagent.trusted_providers`
(`:703-716`). Validate every entry in `AllowedModels` at load time via
`strings.Cut` + `ResolveProviderMeta`, using the same error-message shape
that path already produces for an unknown provider. Add `"orchestrator"` to
the existing `noModelTables` guard (`:549`). Add a commented-out example
block to the default config template. Add a nil-safe helper method,
`(c *Config) OrchestratorModelAllowed(s string) bool`, mirroring
`ProvidersMutuallyTrusted`'s own nil-safety shape (`:555`).
**Why here.** Step 22's `/model` command needs the allow-list to already
exist; Step 24 needs the token/chat-id fields. Placed here, after dispatch
is already written, so the exact shape this config needs to support is
already known rather than guessed at.
**Edge cases.** The Telegram token should be read primarily from a
`POISSON_TELEGRAM_TOKEN` environment variable, with the config-file field as
a documented fallback (the config file is already mode-0600-protected on
load, per `:520`, but env-var-first is still the safer default) — never log
the token anywhere, never echo it back verbatim in any error message.
`chat_id` for a supergroup is a large negative number — parse it as
`int64`, not `int`, or it will silently misbehave on some platforms. An
empty `allowed_models` list should mean `/model` refuses *everything*
(explicit denial), and the instance should default to just
`default_model` alone rather than ever silently falling back to "allow
anything." An unknown provider name anywhere in the list is a hard
configuration error at load time, exactly matching how
`subagent.trusted_providers` already behaves for the same mistake today.
**Verify.** New test cases in `internal/config/config_test.go`: a valid
`[orchestrator]` section parses correctly; an unknown provider name in
`allowed_models` produces a load-time error; a `model = "x"` key directly
under `[orchestrator]` (rather than inside a model sub-table) triggers the
existing `noModelTables` diagnostic correctly. `go test
./internal/config/... -count=1`.

---

### M4 — Telegram frontend

#### Step 24. Bot API client
**What.** New file `internal/orchestrator/telegram/client.go`, stdlib
`net/http` only, modeled directly on `internal/mcpclient/client.go:97-145`'s
idiom: `getUpdates(offset, timeout)`, `sendMessage`, `editMessageText`,
`createForumTopic`, `closeForumTopic`, `deleteForumTopic`, `getMe`. Response
structs decode only the specific fields this feature actually reads —
resist the urge to model the entire Bot API surface.
**Why here.** Depends on Step 23 for where the credentials come from; this
client is pure HTTP and fully independently testable with no Telegram
interaction required for its own tests.
**Edge cases.** The HTTP client's own timeout must exceed the long-poll
timeout passed to `getUpdates` (e.g. a 50-second poll needs at least a
70-second client timeout), or every single poll cycle will spuriously time
out. A `429` response with a `parameters.retry_after` field must be obeyed
by sleeping exactly that long before retrying — not an arbitrary fixed
backoff. A `409 Conflict` response means a *second* process is also calling
`getUpdates` with the same token — this is fatal and unrecoverable (typically
a second orchestrator instance accidentally left running); exit with a
clear, specific message rather than looping and fighting the other process
for updates. Telegram's hard 4096-character message limit means any longer
message must be chunked on line boundaries, never mid-word. **Do not use any
`parse_mode` in v1** — arbitrary LLM-generated output correctly escaped for
MarkdownV2/HTML is its own entire class of bugs, and plain text sidesteps
all of it; document this as a deliberate simplicity choice, not an
oversight. All response bodies should be read through `io.LimitReader` to
bound worst-case memory use from a misbehaving or malicious response.
**Verify.** `httptest.Server`-backed tests for every method listed above,
including one that specifically exercises a `429`-then-`200` retry sequence,
and one that sends a message over 4096 characters and confirms it produces
multiple actual HTTP requests. As a live smoke test: call `getMe` against
the real configured token and confirm it returns the bot's own identity.

#### Step 25. Telegram `Frontend` implementation
**What.** New file `internal/orchestrator/telegram/frontend.go` implementing
Step 17's `Frontend` interface: a long-poll loop that calls `getUpdates`,
filters incoming messages by the configured `chat_id` and
`allowed_user_ids`, parses each into an `orchestrator.Command`, and emits it
on the channel `Frontend.Run` returns. `ChannelKey{Frontend:"telegram",
Chat:<chat_id>, Topic:<message_thread_id>}`. `CreateChannel`/`CloseChannel`
map directly onto the forum-topic API calls from Step 24. The `getUpdates`
offset is itself persisted in the state directory (not just kept in
memory).
**Why here.** Depends on Step 24 existing, and on the `Core` (M3) already
being proven correct against fakes — so any failure discovered here is
unambiguously a transport-layer bug, not a routing/lifecycle bug hiding
behind a real network dependency.
**Edge cases.**
- **Long-poll connection drops mid-request** (a Tailscale flap, a carrier
  NAT timeout, anything network-layer): treat this as an entirely normal,
  expected event — re-poll immediately with a capped exponential backoff,
  never treat a single dropped poll as fatal.
- **Offset persistence must happen *after* a command is durably enqueued**,
  not before — so that a crash between receiving an update and actually
  processing it causes that update to be replayed on restart rather than
  silently lost. This in turn requires deduplicating replayed updates by
  `update_id <= last_seen` on the receiving side.
- Messages arriving in the group's **General** topic (no
  `message_thread_id` present at all) route to the global/instance-less
  commands (`/new`, `/list`) rather than to any specific instance.
- **Edited messages** are ignored entirely — do not re-process a message
  that was already handled once just because the user edited it afterward.
- **Non-text messages** (a photo, sticker, voice note) get a single
  one-line "text only, please" reply rather than being silently dropped or
  crashing the parser.
- **The bot gets removed from the group, or a specific topic gets deleted
  by a human directly in the Telegram UI** (outside this system's control):
  `Send`/`CreateChannel` calls against that topic will start failing with a
  `400`/`403` — mark the corresponding instance as orphaned rather than
  letting this single failure crash the whole poll loop.
- A **very long outage** (Telegram itself drops updates older than roughly
  24 hours) is accepted as a known, documented limitation, not something
  this feature attempts to work around.
**Verify.** Against `httptest`: an injected update produces exactly the
expected `Command` on the output channel; a simulated dropped connection
mid-poll demonstrably does *not* advance the persisted offset. Live smoke
test: post a real message in a real topic and confirm it reaches a minimal
stub `Core` unchanged.

#### Step 26. `px orchestrate` subcommand
**What.** New file `cmd/px/orchestrate.go` containing `runOrchestrate(args
[]string)`; add a `case "orchestrate":` branch to the existing subcommand
switch in `main.go` (`:131-154`), and a corresponding line in
`helpText()`. Flags: `--config-check` (validate the resolved config and
exit without starting anything), `--dry-run` (start with `FakeRuntime`
instead of the real nspawn one, for a smoke test with no containers
involved at all). This function wires together config loading → building
`nspawn.Runtime` + `telegram.Frontend` → `Core.Run`, with a signal handler
for graceful shutdown (reusing the same shutdown behavior designed in Step
19).
**Why here.** This is the final wiring step; every piece it assembles has
already been built and independently tested by this point.
**Edge cases.** Refuse to start at all, with a clear message, if not
actually running as root (nspawn genuinely needs it). Refuse to start if
`systemd-nspawn` itself isn't present on the host. Enforce a single running
instance of the orchestrator via a PID lockfile in the state directory
(mirroring the `serve.lock` concept already sketched in
`docs/server-mode-plan.md:88`) — this directly prevents the Telegram `409
Conflict` failure mode from Step 24 by construction, rather than merely
detecting it after the fact. `TestHelpDocumentsTopLevelOptions` will need
updating once help text changes here — expected, not a regression.
**Verify.** `px orchestrate --config-check` on orchestrator-host prints the fully
resolved configuration with the Telegram token redacted, and exits 0 with
no side effects. `px orchestrate` (for real) starts, and `/list` sent in
Telegram replies "no instances" on a freshly-initialized state directory.

#### Step 27. `px-orchestrate.service` (host systemd unit)
**What.** A host-level systemd unit: `Restart=always`, `RestartSec=5`,
`After=network-online.target`, credentials supplied via an
`EnvironmentFile=` at mode `0600` (containing `POISSON_TELEGRAM_TOKEN=...`),
`ExecStart=/usr/local/bin/px orchestrate`.
**Why here.** Depends on Step 26 existing; this is specifically what makes
Step 21's restart-reconciliation logic actually reachable after something
like a real host reboot, rather than only after a manually-triggered
restart during development.
**Edge cases.** A crash-loop caused by a bad configuration would otherwise
spam the Telegram chat with a startup/reconciliation notice on every single
restart attempt — use `StartLimitBurst`/`StartLimitIntervalSec` to bound
this, and rate-limit the reconciliation notice itself by tracking a boot id
(as already specified in Step 21) so it fires at most once per actual boot,
not once per crash-loop iteration. The unit must not attempt to start
before Tailscale is up, if orchestrator-host's only route to Telegram's API happens to
be via the tailnet rather than a direct route — verify which is actually
true for this host and order `After=` accordingly.
**Verify.** `systemctl restart px-orchestrate` followed by a full `reboot`
of orchestrator-host; after the reboot completes, confirm `/list` responds correctly
in Telegram and any instances that were running before the reboot are
running again afterward.

---

### M5 — Approvals over Telegram, polish, documentation

#### Step 28. Approval bridge
**What.** On the `Core` side: when an `approval_request` event arrives from
a running turn, mint a short numeric id scoped to that instance, register
the pending approval so it can be resolved **exactly once**
(`sync.Once`-style), set that instance's status to `awaiting_approval`, and
post the command text, its description, its assessed risk level, and the id
to that instance's topic — then block. `/approve <id>` and `/deny <id>
[reason]` resolve the pending approval via `ChildProcess.SendApprovalSafe`
on that instance's currently-running turn.
**Why here.** M1 already both emits and correctly consumes this exact
protocol on the process side; only the human-facing Telegram half is new
work at this point.
**Edge cases.**
- **Never answered at all** → by explicit design, this blocks indefinitely
  (per §4 reuse decision 3's carried-over rule): no auto-approve (which
  would mean unattended arbitrary shell execution) and no auto-deny (which
  would mean the model silently takes some other, possibly worse, path
  instead without the user ever knowing a decision was even needed).
  Visibility is handled instead via an explicit `awaiting_approval` badge
  shown in both `/list` and `/status`, plus `/cancel` as the deliberate,
  explicit escape hatch — which works by closing stdin, hitting the
  already-tested EOF-triggers-`denyAllWaiters` path from M1 Step 8.
- **`/approve`/`/deny` issued twice for the same id** → reply with whatever
  decision was already recorded the first time, do not attempt to resolve
  it a second time (this is the exact "resolve exactly once, replay the
  decision" rule carried over from `docs/server-mode-plan.md`).
- **`/approve <id>` referencing an id from a *different* instance/topic** →
  refuse explicitly; ids are never valid across instance boundaries.
- **The turn itself dies while an approval is still pending** → resolve
  that pending approval as denied and retire its id, rather than leaving it
  dangling forever in memory.
- **The whole orchestrator restarts while an approval was pending** → per
  Step 21, the underlying turn unit gets stopped during reconciliation
  regardless, so that specific approval is unrecoverable by definition —
  say so plainly in the reconciliation notice rather than leaving the user
  wondering why their earlier `/approve` seemingly did nothing.
**Verify.** A genuine end-to-end test on orchestrator-host: ask a real instance to run
something that trips the approval gate, confirm the prompt actually arrives
in Telegram with the right command/description/risk, `/deny 1 "not now"`,
and confirm the model's own tool result in the transcript contains that
denial reason. Repeat the same scenario ending in `/approve` instead, and
confirm the command actually executed.

#### Step 29. `/status` enrichment via existing commands
**What.** `/status` runs `px cost <session-id>` and `px sessions` inside
the target instance via `Runtime.Exec` (not a streaming turn), and adds
`systemctl show px-instance@<name> -p MemoryCurrent,Result`, instance
uptime, current model, current queue depth, and any pending approval to the
reply.
**Why here.** This is pure reuse of already-existing, already-correct
cost/session-reporting code (`cmd/px/main.go:628-677`, `:590-608`) — no new
cost-tracking or session-summarizing logic is written anywhere for this
feature.
**Edge cases.** Running `Exec` against a currently-**suspended** instance
should report "suspended" directly rather than attempting the exec at all
and producing a confusing failure. A slow or hanging exec should time out
and still return a partial status rather than making the entire `/status`
command appear to hang. Combined output can still exceed Telegram's
4096-character limit — truncate deliberately before sending, don't just let
the send fail.
**Verify.** `/status` on a genuinely live instance reports a cost figure
that matches running `px cost` manually inside that same instance by hand.

#### Step 30. Documentation + final verification sweep
**What.** Update this document itself with any deltas discovered during
actual implementation (architecture decisions, the two core interfaces as
actually shipped, the full setup runbook consolidated from Step 1 through
Step 4, every assumption below, and the security posture from §5, all in
one place). Add a one-line mention of `px orchestrate` to the top-level
`README.md`. Add an entry to `docs/TODO.md` for everything explicitly
deferred out of v1: multiple simultaneous frontends actually shipped (not
just the interface supporting it), a per-instance cost budget/cutoff,
a deeper instance-to-instance isolation review, and a credential-rotation
mechanism.
**Why here.** Last step, by definition — it documents what was actually
built, not just what was planned.
**Edge cases.** Explicitly document every accepted loss, not just the wins:
instance conversations are invisible to the host's own `px sessions`/
`recall` (assumption 6); instances share the host's network namespace and
can reach everything orchestrator-host itself can reach, including vaultwarden's port
and the entire tailnet (this is the single largest residual risk from §5
and deserves its own clearly-labeled section, not a footnote); a
per-instance `auth.json` is fully readable by that instance's own root user
the moment the instance exists.
**Verify.** The repo's existing `./test.sh` passes. `go build ./...`
succeeds cleanly. A reader with no prior context on this feature can follow
the consolidated §Setup end-to-end on a genuinely clean box and end up with
a working system.

---

## 8. Open item

**One thing worth a final call before Step 2 actually runs** (already
answered once during planning — recorded here so it isn't re-litigated):

- **(Chosen) Proceed with nspawn as planned.** Genuine root, a real PID 1,
  clean `machinectl`-based lifecycle, and `/suspend`/`/resume` semantics that
  actually match the requirement.
- Podman was reconsidered once already: **podman 4.x is in fact already
  installed on orchestrator-host** (a fact the very first feasibility pass missed).
  Rootful `podman run --privileged --systemd=always` would need zero new
  packages and no golden-image pipeline at all — but it still fails the
  `/suspend` requirement in exactly the same way `sandbox.Driver` already
  does (no stop-without-remove primitive), unless `Driver` is bypassed
  anyway — meaning the only actual saving would be M0's Steps 2-3, not the
  `Runtime` interface work itself. **Not recommended**, decision stands as
  nspawn.

---

## 9. Assumptions made (all overridable, listed so they're visible at a glance)

1. **Base image** — `machinectl pull-tar` of a golden Ubuntu 24.04 arm64
   cloud rootfs at `/var/lib/machines/px-golden`, full-copy cloned per
   instance (confirmed: orchestrator-host's root is ext4, so this is always a full
   copy, never a CoW snapshot — factor into disk sizing).
   Override: `debootstrap` instead (needs one extra package, slower per
   `/new`, more controllable exact contents).
2. **`/model` allow-list** — placeholder set
   `["anthropic/claude-sonnet-5", "anthropic/claude-opus-5",
   "ollama/glm-5.2:cloud"]`, default `anthropic/claude-sonnet-5`. Fully
   reconfigurable via `[orchestrator] allowed_models` — this is just the
   starting value.
3. **Secrets** — a generated `auth.json` + `config.toml` per instance under
   `<state>/instances/<name>/`, bind-mounted read-only. Never a shared host
   file, never baked into the golden image.
4. **Resource limits** — `MemoryMax=1200M`, `CPUQuota=150%`,
   `TasksMax=2048` per instance; a ceiling of **4** concurrently *running*
   instances given confirmed-live 7.6GB RAM and 0B swap on orchestrator-host.
5. **Approvals** — forwarded to Telegram (`/approve <id>`/`/deny <id>`),
   blocking indefinitely by design, never auto-resolved either direction.
   `--yolo` exists as an option but is not the default.
6. **Session DB location** — per-instance, inside the container at
   `/root/.poisson/poisson.db`. The host's own `px sessions`/`recall` will
   **not** see any instance's conversations. Accepted as a tradeoff, not
   treated as a bug to fix.
7. **Workspace** — `/work` starts empty inside every new instance;
   `/new <name> <repo-url>` optionally triggers a clone as a first step
   inside the already-running instance.
8. **Telegram setup** — assumed **not yet done**; Step 1 and this
   document's runbook carry the full manual setup as user-facing
   instructions, not code.
9. **Capabilities** — `--capability=all --drop-capability=CAP_SYS_MODULE`
   (note: an earlier draft's `--capability=all,-CAP_SYS_MODULE` syntax is
   invalid nspawn — corrected in this document). This is very slightly less
   than literal "full root" — override back to plain `--capability=all` if
   in-container kernel-module loading is ever genuinely needed.
10. **One process per turn**, not a resident agent daemon living inside
    each container — `px -p --print-json` starts, runs exactly one turn, and
    exits every time, matching `runPrint`'s existing lifecycle exactly.
    Overriding this to a long-lived in-container daemon would require
    designing a whole new persistent protocol for no benefit this plan's
    requirements actually need.
11. **Approvals implemented in M1, not saved for last** — a deliberate
    reordering from an earlier draft, purely because `childApprovalBroker`
    already exists fully-built and fully-tested, so wiring it during the
    same `runPrint` editing pass as the JSON output work is strictly
    cheaper than doing it as a separate later pass.

---

## 10. Implementation status (as actually built) + Setup runbook

All 30 steps (M0-M5) are implemented, tested (`go test ./...` and
`go test -race ./internal/orchestrator/...` both clean), and every
milestone's own verify criteria were exercised — either automated or, for
the pieces that need a real host/bot, live-verified against the actual
`LePoissonBot` and actual orchestrator-host. Not yet deployed as a running production
service (`px-orchestrate.service` is committed but not installed/enabled on
orchestrator-host) — see "Deployment status" below.

**Addendum**: `docs/orchestrator-host-mode-plan.md` (H1-H8, also complete)
adds box-instance yolo mode + `set_title` hiding, and a second instance
kind — host-direct (`/new-host`), running unconfined directly on the
orchestrator host with real approvals, alongside today's box kind (nspawn,
`/new`/`/new-box`). Read that doc for the full box/host distinction; its own
§7 records what changed from the original design during implementation
(notably `CompositeRuntime.RehydrateKindOf`, a real restart-time gap the
plan itself hadn't anticipated).

### Setup runbook (Steps 1-4, consolidated)

1. **Telegram**: BotFather `/newbot` → token. Bot Settings → Group Privacy →
   off. Create a group → enable Topics (auto-promotes to a supergroup) →
   add the bot as admin (Manage Topics, Post Messages, Delete Messages).
   Post one message, `getUpdates`, read `chat.id` (negative) and your own
   user id from `from.id`.
2. **orchestrator-host host prep**: `apt install systemd-container`. Create
   `/var/lib/machines` and `/var/lib/px-orchestrate/instances`. Verified
   live: zero `ufw`/`iptables` diff before/after.
3. **Golden rootfs**: `machinectl pull-tar` the Ubuntu 24.04 arm64 cloud
   image as `px-golden`. Inside it (via `systemd-nspawn` with
   `--bind-ro=/etc/resolv.conf` for DNS during setup only): `apt install git
   ripgrep ca-certificates curl jq`, `systemctl disable
   systemd-networkd systemd-resolved systemd-networkd.socket`, `passwd -l
   root`, `mkdir -p /root/.poisson /work`. Resulting image: ~1.4GB.
4. **Deploy `px`**: `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o px
   ./cmd/px`, scp to `/usr/local/bin/px.new` on the host, `mv` into place
   atomically. Confirmed statically linked (`ldd` reports "not a dynamic
   executable") — `modernc.org/sqlite` being pure Go is what makes this
   possible with no cgo cross-compilation at all.

Then, to actually run it: install `deploy/px-orchestrate.service` to
`/etc/systemd/system/`, create `/etc/px-orchestrate.env` (mode 0600,
`POISSON_TELEGRAM_TOKEN=...`), write `[orchestrator]` into
`/root/.poisson/config.toml` (`chat_id`, `allowed_user_ids`,
`allowed_models`, `default_model`; `max_instances`/`state_dir`/`image` have
sane defaults), `systemctl daemon-reload && systemctl enable --now
px-orchestrate`.

### Interface deltas vs. the original §2.1 sketch

- **`Runtime` gained two methods** not in the original sketch:
  `StopTurn(ctx, unitName)` and `StopOrphanedTurn(ctx, name)`. Discovered
  while implementing `/kill`/`/suspend` (M3) and restart reconciliation
  (also M3): both need to stop *just* the in-flight turn without touching
  the rest of the instance, and `Turn.UnitName` (already in the original
  sketch) had no corresponding interface method to actually use it with.
- **`Frontend.Send` returns `(msgID string, err error)`**, not just
  `error`. Added when a tool-call status message needed to be edited in
  place (`Frontend.Edit`) instead of spamming one message per tool call —
  a user-requested design change mid-M5 build, not in the original plan.
- Everything else (`Command`/`Event`/`ChannelKey`/`Instance` shapes,
  `CoreConfig`, `InstanceSpec`/`TurnSpec`/`InstanceInfo`) shipped
  essentially as sketched.

### Deviations from the original plan text

- **Tool-call rendering**: the plan's Step 20 as originally written sends
  one new message per tool call. Changed (user request, mid-M5) to edit one
  running status message in place per turn, falling back to a fresh `Send`
  only if `Edit` fails — avoids flooding a topic on a turn with many tool
  calls, at no cost (reuses `Frontend.Edit`, already defined and otherwise
  unused).
- **`/approve`/`/deny` have no numeric id**, unlike Step 28's original
  design. Telegram topics already scope every command to exactly one
  instance, and only one turn (hence at most one pending approval) ever
  runs per instance at a time — an id would disambiguate a case that
  structurally cannot occur here. `sync.Once`-style exactly-once resolution
  is achieved by clearing `Instance.pending` immediately on resolution
  (checked at the top of `handleApproval`), not by a separate id registry.
- **`processAlive`'s liveness probe** (Step 26) needed two corrections past
  a naive `Signal(0) == nil` check, both caught by tests, not by review: an
  `EPERM` result (process exists, owned by another user) must count as
  alive, and Go's `os.ErrProcessDone` sentinel (not always a wrapped
  `syscall.ESRCH`, confirmed empirically) must count as not-alive.

### Deployment status

Built, tested, and live-verified in `--dry-run` mode against the real bot
(`--config-check` output, `/list`, `/new test` creating a real forum topic
+ registered instance). **Not yet running as the production
`px-orchestrate.service`** on orchestrator-host — that requires real provider
credentials to exist there first (`root@orchestrator-host` currently has no
`~/.poisson/auth.json` at all), which is a separate, deliberate step given
what full-root-instance credential exposure means (see §5) — not something
to do incidentally while finishing the plan's own steps.

### Accepted losses, restated plainly (not just in §5)

- **Instance conversations are invisible to the host's own `px
  sessions`/`recall`** — each instance's `poisson.db` lives only inside
  that instance's own rootfs.
- **Instances share the host's network namespace** — real root inside an
  instance can reach everything orchestrator-host itself can reach: `vaultwarden`'s
  port, the entire tailnet, any other host service bound to a non-loopback
  address. This is the single largest residual risk from §5 and is
  deliberate (see §3.1's reasoning), not an oversight.
- **A per-instance `auth.json` is fully readable by that instance's own
  root user** the moment the instance exists — treat every credential
  copied into an instance as compromised the moment you'd trust that
  instance's agent less than yourself (§15's own doc comment says this
  too; repeating it here since it's the single most important operational
  fact about this feature).
