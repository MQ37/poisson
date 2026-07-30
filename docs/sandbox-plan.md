# Sandbox Plan

Podman-backed sandbox execution for `bash`, replacing the dead `sandbox
bool` scaffolding (`BuildOptions.Sandbox`, per-tool `sandbox bool` param,
and the already-dead `POISSON_SANDBOX`/`IS_SANDBOX` env-var history) with
real per-call container routing. No cruc dependency — native
`internal/sandbox` package, podman CLI only.

File tools (`read/write/edit/grep/glob`) need **no changes at all** — see
"File tools" below. Only `bash` and the new `create_sandbox`/`sandbox`
management tools are new surface.

Sticky bash (cwd/env persistence across calls) is removed entirely, for
both host and sandboxed bash — every call is stateless. This is a
deliberate, real behavior change for existing host-mode users (`cd`/`export`
currently persist; after this they won't), accepted to keep the sandbox
implementation clean. It also collapses most of the cross-process/
cross-sandbox state questions that a keyed-sticky design would otherwise
raise.

## Goals

- `create_sandbox` tool: podman container, agent sets it up itself via
  `bash(sandboxId=...)` (install deps, whatever the project needs) — no
  built-in provisioning logic in poisson beyond spinning up the container
  and its one bootstrap user (see "Root access" below).
- `bash` takes an optional `sandboxId` and skips guard/risk-classifier/
  approval entirely when set — sandbox isolation *is* the safety boundary
  for command risk, not the approval gate.
- `read/write/edit/grep/glob` are untouched. `create_sandbox` hands the
  agent a plain host path (`hostPath`); the agent uses the existing tools
  against absolute paths under it, exactly like any other host path.
  Sensitive-path approval and the symlink guard keep running unconditionally
  — sandboxing never bypasses file-identity checks, only command-risk ones.
- Only `create_sandbox` needs human approval, and only when it's given a
  host mount beyond its own scratch workspace, or an injected secret.

## Non-goals (for v1)

- No in-container daemon/warm-process for low-latency exec — ship naive
  `podman exec`-per-call first, optimize only if latency is a real
  complaint.
- No helper-binary/JSON-RPC scheme inside the container — dropped in favor
  of the bind mount (below), which needs neither `rg` nor any poisson code
  installed in the image.
- No sticky cwd/env, for host or sandboxed bash — removed, not deferred.
- No `sandboxId` param on file tools — dropped in favor of a plain host
  path handed back by `create_sandbox`.
- No fully-correct timeout-kill for a hung sandboxed command —
  `podmanDriver.Exec` kills the local `podman exec` client process on
  timeout (stops poisson from hanging forever), but that doesn't guarantee
  the process running inside the container's own pid namespace actually
  dies, since it isn't a child of the local client the way a host bash
  subprocess is. A full fix needs more machinery (tracking the remote pid,
  a second targeted exec to kill it) — accepted as a known v1 gap, same
  "ship the naive version first" principle as the line above.

## Architecture

### `internal/sandbox` package (new)

```go
type Driver interface {
    Create(ctx context.Context, opts CreateOpts) (id string, err error)
    Exec(ctx context.Context, id, cmd, workdir string, timeout time.Duration) (stdout, stderr string, exitCode int, err error)
    Inspect(ctx context.Context, id string) (alive bool, err error)
    Kill(ctx context.Context, id string) error
    List(ctx context.Context) ([]Info, error)
}
```

`List` was added in the crash-recovery step (see below): every live sandbox
this Driver's backend knows about, regardless of which process created it.
`CreateOpts` gained `Name` (agent-chosen, resolved through
`ResolveSandboxName` — sanitized and `px-sandbox-`-prefixed, or a random
fallback when empty) and `SessionID` (stamped by `Manager.Create` from its
own `sessionID`, purely informational). `Create`'s returned `id` is now
always that resolved name, not an opaque hash — the container's name *is*
its id, so "know the exact name" is the whole access model.

- `podmanDriver` — **implemented**, real implementation, shells out to the
  `podman` CLI (same style as `bash.go`/`grep.go` today — no REST API, no
  new dependency). Takes an optional `globalArgs []string` prefix (e.g.
  `--root`/`--runroot`) and `extraEnv []string` (e.g. the confining
  `XDG_*`/`TMPDIR` overrides), both nil in production (real `create_sandbox`
  uses whatever storage the host user has configured) — only the
  integration suite below sets them, and always per-`exec.Cmd`, never via
  `os.Setenv`/ambient export (see the disk-wear guard's env-var-hygiene
  note below for why that distinction matters). **Now wired into
  `cmd/px/main.go`** (see "Production wiring" below) — `create_sandbox` is a
  real, usable feature as of that step, not inert scaffolding.
- `fakeDriver` — in-memory Go maps, no subprocess, same idiom as
  `provider.FakeProvider` (`internal/provider/fake.go`) and the `rgBin`
  override in `grep.go`. Used for the bulk of tests; no podman needed, runs
  in any CI.
- A small number of real-podman integration tests, gated behind an env
  check (`PODMAN_INTEGRATION=1`) or build tag, skipped by default.

  **Disk-wear guard for this suite**: rootless podman's graphroot (`--root`,
  default `$XDG_DATA_HOME/containers/storage`) is where every image layer
  and container overlay diff actually lands — real per-test write churn on
  the real disk otherwise. Full confinement, **empirically verified** on the
  dev box (real `podman pull`+`run`, before/after directory-state diff of
  `~/.local/share/containers`, `~/.config/containers`,
  `/run/user/$UID/{containers,libpod}` — identical, zero touch):

  ```
  tmpDir := mktemp -d /tmp/poisson-podman-test.XXXXXX   # confirmed tmpfs via findmnt
  env: XDG_DATA_HOME=tmpDir/data XDG_CONFIG_HOME=tmpDir/config
       XDG_RUNTIME_DIR=tmpDir/runtime (chmod 0700) TMPDIR=tmpDir/tmp
  podman --root tmpDir/root --runroot tmpDir/run ...
  ```

  `--root`/`--runroot` alone cover images, container layers, volumes, and
  CNI/netavark network configs (all under `graphroot`) — the per-test churn.
  The `XDG_CONFIG_HOME`/`XDG_RUNTIME_DIR` overrides additionally catch
  `containers.conf`/`storage.conf`/`registries.conf`/`policy.json` and
  `auth.json`, which `--root` doesn't relocate — belt-and-suspenders beyond
  what's strictly needed for the disk-wear concern (those are already
  `/run/user/$UID` tmpfs and near-static even unconfined), but confirmed to
  work cleanly with no downside. This is also podman's own upstream pattern:
  the BATS system-test suite (`test/system/helpers.bash`) uses a
  `mktemp`'d `PODMAN_TMPDIR` plus `--root`/`--runroot` overrides
  (`_PODMAN_TEST_OPTS`) as its standard hermetic-test mechanism — not a
  workaround, the maintainers' own approach.

  A harness running somewhere other than this dev box should check
  `findmnt -n -o FSTYPE /tmp` itself and skip loudly rather than silently
  write to a disk-backed `/tmp`. Set `driver=overlay` explicitly for the
  test root and fail loudly if it falls back to `vfs` (heavier, defeats the
  point) instead of silently degrading.

  **Cleanup gotcha, hit and confirmed live — twice, with a correction.**
  Tearing down `tmpDir` after the suite runs must **not** use a plain
  `os.RemoveAll`/`rm -rf` immediately — rootless podman's user-namespace uid
  mapping leaves files under the container overlay diff owned by
  subordinate uids a plain unprivileged delete can't touch (`Permission
  denied`). First fix tried, `podman unshare rm -rf tmpDir`, worked in
  isolation but turned out unreliable in practice: it can partially delete
  the tree (including `tmpDir`'s own `runtime`/`tmp` scaffolding) before
  hitting a still-busy overlay/netns mount and aborting, which then breaks
  `podman unshare` itself on any retry (it needs that scaffolding to
  initialize) — a self-inflicted, unrecoverable failure mode, not a race
  that a retry loop can close. **What actually works, used by
  `podman_integration_test.go`'s `confinedDriver` cleanup**: `podman --root
  X --runroot Y system reset --force` (with the exact same `--root`/
  `--runroot` used for everything else against that confined root) *before*
  ever touching the directory tree directly — it waits out the container's
  own async overlay/netns teardown properly, and a plain `rm -rf` afterward
  (no `podman unshare` needed at all) then succeeds cleanly. This is
  specific to the test harness blowing away a whole confined storage root
  directly; it does **not** affect normal `sandbox_destroy`/idle-reap
  teardown of a real container, which goes through `podman rm -f -t 0 <id>`
  (`-t 0`: skip the SIGTERM grace period — `sleep infinity` doesn't handle
  SIGTERM, so without this every teardown pays a needless ~10s wait before
  podman escalates to SIGKILL, confirmed empirically) and lets podman handle
  its own uid-mapped layer cleanup internally.

  **Env-var hygiene, also caught empirically**: `XDG_DATA_HOME`/
  `XDG_CONFIG_HOME`/`XDG_RUNTIME_DIR`/`TMPDIR` must be set *per invocation*
  (`Cmd.Env` on each individual `exec.Command`, as `podmanDriver` actually
  does), never `export`ed/ambient across a whole shell session or process —
  an exported override that leaks into one unrelated later `podman`
  invocation (even just `podman unshare`, whose own setup can lazily touch
  storage at whatever XDG path is ambient) was enough to create a small
  stray `containers/storage` directory outside the intended confined root
  during manual verification. `podmanDriver` avoids this by construction
  (each `exec.Cmd` gets its own explicit `Env`, never `os.Setenv`), but it's
  worth naming as a way to accidentally defeat the whole confinement
  guarantee if this is ever reimplemented differently.

`Manager` wraps a `Driver` plus session-scoped bookkeeping:

```go
type Manager struct { /* driver + session-scoped registry of owned sandboxes */ }

func (m *Manager) Create(ctx context.Context, opts CreateOpts) (Sandbox, error)
func (m *Manager) Authorize(sb Sandbox) // subagent allow-list — see "Subagents"
func (m *Manager) Get(id string) (Sandbox, bool)
func (m *Manager) Exec(ctx context.Context, id, cmd, workdir string, timeout time.Duration) (stdout, stderr string, exitCode int, err error)
func (m *Manager) Alive(ctx context.Context, id string) (bool, error) // Owns-checked, then delegates to Driver.Inspect
func (m *Manager) Kill(ctx context.Context, id string) error
func (m *Manager) Owns(id string) bool // session-scoped id check — see "Ownership/validation"

type Sandbox struct {
    ID        string // driver-assigned container id
    HostPath  string // /tmp/poisson-sandbox-<id>/workspace — handed to the agent verbatim
    CreatedAt time.Time
    LastUsed  time.Time
}
```

`Create`/`Authorize`/`Get` all deal in `Sandbox` values, never a pointer into
the Manager's own tracked map — `Exec` mutates a sandbox's `LastUsed` under
the Manager's mutex on every call, so a caller holding a raw pointer from an
earlier `Get` would race the mutation the moment more than one goroutine
touches the same sandboxId (real once concurrent `batch` calls route through
the same sandboxId). Caught by `-race` during step 3 implementation, fixed
before it could bite the later concurrency-heavy steps.

File tools never call `Driver` or `Manager` at all — see below.

### Image

Pinned tag, not `latest`: `ubuntu:26.04` (or whatever the current LTS is at
ship time — never a floating/mutable tag). Official Ubuntu images publish
amd64/arm64/etc., so cross-arch hosts (Mac podman machine, arm64 servers)
aren't a real concern.

### The bind mount

`create_sandbox` bind-mounts a host-side scratch directory into the
container as its workspace:

```
podman create -v /tmp/poisson-sandbox-<id>/workspace:/workspace:Z \
  ubuntu:26.04 sleep infinity
```

- `bash(sandboxId=...)` execs inside the container, operates on
  `/workspace` (container-side path) — gets process/network/filesystem
  isolation for everything *outside* that one directory.
- Only `/workspace` is shared. Everything else the container does (apt
  installs, files outside `/workspace`) stays isolated in the container's
  own overlay layer — not visible to host tools. Host tools only ever see
  the project tree, never installed-package internals.

### File tools: no `sandboxId`, just a path

`read/write/edit/grep/glob` already accept arbitrary absolute host paths
today — no cwd-jail, no new path-shape support needed. `create_sandbox`'s
result tells the agent the plain host path (`hostPath:
"/tmp/poisson-sandbox-<id>/workspace"`) and says: use the existing tools
against absolute paths under it.

This means:
- Zero new tool params, zero `Manager` lookups from file-tool code, zero
  new plumbing.
- `checkSensitivePath` and `internal/guard/symlink.go` already run on every
  call unconditionally — the earlier "symlink escape" and "sensitive-path
  bypass" concerns are handled by code that was never sandbox-specific to
  begin with. **Do not** add a skip for these under any sandbox condition:
  a secret's sensitivity doesn't change because it's reached through a
  mirrored directory instead of the original project path, and command-risk
  containment (what the sandbox actually buys you) has nothing to do with
  file identity.
- Trade-off, accepted: the agent must pass full absolute paths every call
  (no relative-to-sandbox-root shorthand). Real but minor footgun: if it
  forgets and passes a relative path, `grep`/`glob` silently default to the
  *session* root instead of erroring — mitigate with an explicit reminder
  in `create_sandbox`'s result text, not code.

### Root access: matching-uid user + passwordless sudo

**Implemented in `podmanDriver` (step 7), empirically verified against real
podman — including a correction to this section's own earlier claim.**

Tension: `apt-get install` needs root inside the container's own rootfs;
the bind mount needs the container's process to write as the *same uid* as
the host user, or host-side `write`/`edit` can't touch what it creates.
One flat "run as root" or one flat "run as host-uid" can't satisfy both at
once.

**Correction**: an earlier version of this section claimed
`--userns=keep-id` was unnecessary — "explicit matching uid is easier to
reason about." That was wrong, caught only by actually running it: without
`keep-id`, "uid 1000 inside the container" and "uid 1000 on the host" are
different real identities (rootless podman's default namespace remapping),
so bootstrapping a user at the same *number* does not give it the same
real *identity* as the host user — a bootstrapped user wrote to
`/workspace` and got `Permission denied`. `Create` now always passes
`--userns=keep-id`, which is what actually makes container-side and
host-side uids the same identity. Second-order effect, also caught
empirically: `--userns=keep-id` changes `podman exec`'s own *default* user
too — a plain `podman exec` with no `--user` flag then defaults to the
keep-id-mapped identity, not root, so bootstrap's own `useradd`/
`apt-get install sudo` need an explicit `--user root` or they fail with
permission errors (confirmed: `apt-get update` failed with
`/var/lib/apt/lists/partial ... Permission denied` before this fix).

Bootstrap, run once at `Create` time via `podman exec --user root`, before
returning the container id:

1. **Reuse an existing user at the host's uid/gid if one exists, don't
   assume a fresh user gets created.** `ubuntu:26.04` (and most other
   cloud-oriented base images) ships a pre-existing `ubuntu:x:1000:1000`
   user, and uid 1000 is the near-universal default for the first regular
   user on Linux — a naive `useradd` collides with it (`UID 1000 is not
   unique`) far more often than not. The script checks
   `getent passwd $HOST_UID` first; only runs `groupadd`/`useradd` (with a
   `poisson` username) when nothing already occupies that uid. Either way,
   the *resolved* username (`ubuntu`, or `poisson` if freshly created) is
   printed as the script's only stdout output and recorded per-container —
   never hardcoded as `poisson`.
2. Install `sudo` if not already present (`command -v sudo || apt-get
   install -y sudo`), grant it passwordless for the resolved user:
   `echo "$RESOLVED_USER ALL=(ALL) NOPASSWD:ALL" >/etc/sudoers.d/poisson-sandbox`.
3. Every ordinary `Exec` call after that uses the recorded resolved user
   (`podman exec --user $RESOLVED_USER ...`) — never root by default.

Ordinary commands (build, test, script) land owned by the host uid
automatically, so host `read/write/edit/grep/glob` work on them with zero
special-casing. Anything needing real root — `sudo apt-get install foo` —
the agent just prefixes `sudo`.

**Edge cases:**
- This is plumbing, not a security boundary. NOPASSWD sudo means the
  matching user can become real root-in-container trivially any time — no
  actual privilege separation happens inside the container. That's
  expected: the split exists purely for bind-mount ownership bookkeeping.
  The real security boundary (no host root, no host network/process access
  beyond the one mounted dir) is unaffected either way.
- Files/directories the container creates *via* `sudo` (e.g.
  `sudo mkdir /workspace/build`) come back root-owned inside the container's
  namespace, not host-uid — same ownership problem one level down.
  Mitigate with guidance in `create_sandbox`'s description: use `sudo` only
  for package management; do regular build/file work as the default user.
  Not fully enforceable, but the common case holds.
- Bootstrap cost: a few seconds + a package-manager network hit per
  `create_sandbox` (`useradd` + `apt-get install sudo`). Fine for v1;
  optimize later with a custom thin image (`ubuntu:26.04` + `sudo`
  preinstalled) poisson builds once and reuses.
- Read-only tools (`read/grep/glob`) were never affected by any of this —
  default root umask (022) leaves files world-readable regardless of
  writer uid. This entire section only exists for `write`/`edit` needing to
  overwrite files in place.

### Mount safety (bind mount ≠ host exposure)

Mounting only exposes that one directory, and rootless podman means even a
fully compromised process inside never gets more than the invoking host
user's own privileges over it — no real host root, no network exposure via
the mount (filesystem-only).

Real, scoped risk: sandboxed code (buggy or malicious dependency) can write
*anything* into the shared directory — a malicious binary, a booby-trapped
build artifact, a symlink to a sensitive host path. That risk only
crystallizes if something later executes/opens those files **outside** the
sandbox (e.g. running a built binary directly on host "for convenience").
Same category of risk as building an untrusted repo locally — not novel to
the mount, but real.

Mitigations:
- Mount flags `:Z,nosuid,nodev` (SELinux relabel + defense-in-depth against
  a planted setuid binary or device node mattering if ever run on host).
- Host-side scratch dir created `0700`, owned by the invoking user, before
  `podman create` — on a shared/multi-user host, a world-writable
  `/tmp/poisson-sandbox-*` would let another local user race or tamper
  with it while the sandbox runs.
- Policy: never bind-mount credentials or other host-sensitive directories
  directly into `/workspace` — those go through the approval-gated
  extra-volume path on `create_sandbox`, kept separate from the
  always-auto-approved base workspace mount.
- The symlink guard (already unconditional, see "File tools") is the
  backstop for "container plants a symlink to `/etc/passwd`, host tool
  later follows it" — no new code needed, just confirm it isn't bypassed.

### Ownership / validation

`sandboxId` is per-call model input to `bash` only (file tools don't take
it at all — see above), so it must be checked against `Manager.Owns(id)` —
a session-scoped set of container ids this session's own `create_sandbox`
calls actually produced, or that were explicitly authorized for a subagent
(see "Subagents") — before any podman command runs. A hallucinated or
guessed id must be rejected the same way a bad `workdir` is today.

### Approval

- `create_sandbox`: auto-approved when it requests no host mount beyond
  its own scratch workspace and no secret/env injection. Requires human
  approval — showing the exact extra host paths/mode — when the agent
  asks for an additional bind mount (e.g. `~/.ssh`, a second project dir)
  or an injected secret/token.
- `bash` with `sandboxId` set: no approval, no risk classification.
- File tools: always the existing sensitive-path/symlink checks, sandboxed
  target or not — see "File tools."

### Subagents: allow-list only, no creation

Subagents are separate OS processes (`internal/subagent/spawn.go`), each
with their own `BashTool`/`Manager` in their own address space — no shared
memory with the parent.

- A subagent **cannot call `create_sandbox`** — it can only use sandboxIds
  the parent explicitly authorizes. Keeps ownership single-owner (the
  parent session's store row), avoids cross-process/cross-DB cleanup
  bookkeeping (a subagent runs against its own ephemeral
  `POISSON_SUBAGENT_DB`). Same shape as the existing rule that a subagent
  never gets the `subagent` tool itself.
- The parent's `subagent` tool call itself carries a `sandboxIds` array in
  its own schema — the calling model explicitly names which sandboxes
  (already created via its own `create_sandbox` calls) to share with a
  given child, resolved against this session's own `Manager` before
  spawning anything (a foreign/hallucinated id fails the whole spawn
  loudly, same discipline as `bash`'s own sandboxId handling — not silently
  dropped).
- Env var, same pattern as every other `POISSON_SUBAGENT_*` var in
  `buildSpawnEnv`: `POISSON_SUBAGENT_SANDBOXES`, **omitted entirely when
  empty** (mirrors `TestBuildSpawnEnvOmitsUnsetFields`) — **implemented as
  a JSON array of `{id, hostPath}` objects, not the originally-sketched
  comma-separated id list**: `HostPath` is needed too (a subagent's own
  `sandbox_cp` would otherwise resolve paths against an empty root), and a
  filesystem path can in principle contain a comma — JSON via the stdlib is
  simpler and more robust than a hand-rolled delimited format with escaping
  edge cases, for what's already process-internal control-plane data (same
  trust level `POISSON_SUBAGENT_DB` already carrying a filesystem path has).
- The child must still verify liveness before use — `Manager.Alive`
  (`Owns`-checked, then `Driver.Inspect`) — not blindly trust the env
  string: the parent (or the idle reaper) may have killed it between spawn
  and use. An exec against a dead container is an ordinary tool-error, not
  a crash — already proven at the `Manager`/`BashTool` layer
  (`TestManager_AuthorizedSandboxCanGoDeadUnderneathAChild`,
  `TestBashTool_SandboxedExecFailurePropagates`, both from earlier steps);
  nothing new to test here specifically for the subagent case, since the
  mechanism is identical once a child's `Manager` has the record.
- No sticky state to propagate (removed entirely, see top) — the allow-list
  is a pure permission check, nothing else crosses the process boundary.

**Implemented**: `internal/subagent.SpawnInput.AuthorizedSandboxes
[]SandboxAuth` (`{ID, HostPath}` — deliberately not `sandbox.Sandbox`
itself, to avoid coupling a generic process-spawning package to the full
sandbox-package shape for fields it doesn't need); `buildSpawnEnv` JSON-
encodes it into `POISSON_SUBAGENT_SANDBOXES`; `ParseAuthorizedSandboxes`
decodes it back (round-trip tested). `SubagentTool` gained
`sandboxMgr`/`SetSandboxManager` (same `Set*` idiom as `BashTool`'s own),
a `sandboxIds` schema field, and pre-spawn validation (`Manager.Get` per
id, fail the whole call on any miss) — wired onto the tool by
`BuildRegistry` whenever `opts.SandboxManager != nil`. Proven end-to-end
with a real spawned process (fake shell "child" script, same pattern as
`TestSubagentToolRelaysRetryingStatusEndToEnd`): an owned sandboxId
resolves, survives the env-var round trip, and the child process's real
environment shows exactly the authorized `{id, hostPath}`.

**Not yet implemented**: `runChildMode` (`cmd/px/main.go`) does not parse
`POISSON_SUBAGENT_SANDBOXES` or construct a `Manager` for the child's own
registry yet — doing so needs a real `Driver` to back it (there's nothing
for `Manager.Exec`/`Kill`/`Inspect` to route through otherwise), so this
lands together with the `podmanDriver` step, not before. Same deferral
reasoning as the bootstrap script above: guessing at wiring before the
real consumer exists would be designing against nothing.

### Crash recovery / cross-session discovery

**Implemented.** The problem: a podman container isn't tied to poisson's
process lifetime — it keeps running (CPU/memory/its overlay layer) after a
crash, a closed terminal, or an abandoned session, exactly like the `/tmp`
mirror directory sitting on disk. A `Manager`'s in-memory ownership map was
the *only* record of what a sandbox even was — a fresh process (after a
crash, or just a different concurrent session) had no way to find or reuse
one, only to leak it forever.

**Design: podman itself is the source of truth, not a new database table.**
No `sandboxes` session-store table was added — every container this
package creates already carries a `poisson.sandbox=1` label (and, when a
`sessionID` is set, `poisson.session=<id>`), so `Driver.List` (`podman ps -a
--filter label=poisson.sandbox=1` + one batched `podman inspect`) *is* the
persistent record, for free, across any number of crashed/restarted
processes. This is a deliberate simplification over this section's earlier
sketch (session-store table + launch-time sweep reading it) — podman
already durably remembers what exists; a second, potentially-stale copy of
that fact in SQLite would just be another thing to keep in sync.

**Naming: agent-chosen, and the name *is* the id.** `create_sandbox` takes
an optional `name` (e.g. `"api-testing-2"`); `ResolveSandboxName` sanitizes
it and prefixes `px-sandbox-` (or generates a random `px-sandbox-<hex>` name
when omitted). The resolved name is passed as the container's real `--name`
*and* returned as `sandboxId` — there's no separate opaque handle. A
colliding name fails `podman create` (and `FakeDriver.Create`) loudly and
immediately; the agent picks another name or checks `list_sandboxes` first.
Prefixing serves two purposes: keeps `podman ps` filtering unambiguous, and
keeps a sandbox from ever colliding with an unrelated container already on
the host.

**Access model: open across sessions, gated only by knowing the exact
name.** `Manager` gained `EnableDiscovery()`: when set, a local-map miss on
`Get`/`Owns` falls back to `Driver.List`, and adopts a match by exact `id`
that is still `Running` (a stopped/crashed match is refused, not silently
adopted — same reasoning `Alive` already applies elsewhere). This is an
explicit, deliberate loosening from the original "only this process's own
Manager, or an explicitly Authorize'd one" model — the user's call: any
session/instance can use any live sandbox it can name, nudged (not
technically forced) by a prompt guideline to prefer reattaching to a
recognized one over creating a duplicate, and to leave sandboxes that look
like someone else's in-progress work alone. The residual risk (an agent
guessing/hallucinating another session's exact live name) is accepted as
low-probability and fail-loud (a wrong name either matches nothing, or
matches something the agent would have had to already know the name of) —
not fail-open onto a random container.

**The subagent gate is unaffected, on purpose.** `EnableDiscovery` is called
*only* by `cmd/px/main.go`'s `newSandboxManager` (a top-level session's own
Manager) — `resolveChildSandboxManager` (a subagent's Manager) builds its
`Manager` directly and never calls it, so a subagent's access remains
exactly what its parent explicitly `Authorize`'d, nothing discoverable on
its own. Locked in by `TestManager_DiscoverySubagentManagerNeverEnabled`.

**`list_sandboxes` tool**: browses every live sandbox `Driver.List` reports
— across every session, not just this one — showing `sandboxId`, `hostPath`,
the creating session's id, `createdAt`, and `running`. Read-only: seeing an
entry never grants access on its own, `Owns`/`Get` still gate
`bash`/`sandbox_cp`/`sandbox_destroy` independently either way. Registered
alongside the other sandbox tools whenever `SandboxManager != nil`,
including for a child registry (browsing a subagent can't act on beyond
what it was authorized for is harmless).

**Still open, not started** (unaffected by the above — these are about
*resource exhaustion*, not *discoverability*, which is now solved):

- No idle-reap sweep yet: a forgotten, no-longer-wanted sandbox runs
  forever until explicitly `sandbox_destroy`'d or manually `podman rm -f`'d.
  A `sweepStaleSandboxes()` pass (same shape as `sweepStaleSpillFiles()`,
  `sync.Once`, TTL-based) could now be built directly against `Driver.List`
  + each `Info.CreatedAt`/a liveness heartbeat, with no session-store table
  needed — but the TTL default (and whether it's `config.toml`-configurable)
  is still an open question below.
- No "N sandboxes still running — kill them?" prompt on clean session end.

**`/sandbox ls` / `/sandbox kill <id>` — implemented.** `internal/tui/commands.go`'s
`cmdSandbox` looks up the actual `list_sandboxes`/`sandbox_destroy` tool
instances via a new `Agent.Tools()` accessor (`internal/agent/agent.go`,
same pattern `ExpediteSubagents` already uses for `"subagent"`) and calls
`Execute` directly — reusing their exact tested logic (cross-session
listing, `Owns`-gated destroy, clean errors on unknown/foreign ids) rather
than duplicating it against `Manager` directly. `/sandbox ls` pretty-prints
the tool's JSON content (id, running, session, created); `/sandbox kill
<id>` forwards `sandbox_destroy`'s own result text as-is. Registered in
`liveSafeCommands` (safe mid-turn — podman ops don't touch turn/session
state) and the usual completion/palette/help surfaces
(`complete.go`/`overlay_palette.go`/`keys_help.go`). No manager configured
(no `podman` / session without sandbox support) reports "sandboxing is not
available in this session" the same way the agent-facing tools do.

## Tool schema changes

Implemented, as actually shipped (two refinements from the original sketch:
`env` not `secrets` — plain KEY=VALUE injection, no separate secrets-vault
concept exists in this codebase; `workspacePath` not `containerPath` on
`sandbox_cp` — it's always relative to the sandbox's own workspace root,
never an arbitrary container path, matching the "file tools get a plain
host path, not a sandboxId" decision):

```
bash{command, description, workdir, timeout, sandboxId?}
create_sandbox{image?, name?, mounts?, env?} -> {sandboxId, hostPath}
sandbox_cp{sandboxId, direction: in|out, hostPath, workspacePath}
sandbox_destroy{sandboxId} -> ok/error content
list_sandboxes{} -> [{sandboxId, hostPath, sessionId, createdAt, running}]
subagent{task, name?, sandboxIds?} -> unchanged otherwise
```

`sandbox_destroy` is the agent-driven complement to the (not yet built)
idle-reap sweep and `/sandbox kill` slash command (see "Lifecycle /
cleanup"): the agent calls it itself once it's done with a sandbox, instead
of relying solely on a TTL sweep to eventually notice. Always auto-approved
— destroying a sandbox only discards disposable container/scratch-dir
state, never host data, so there's no risk to gate. Kills the container via
`Manager.Kill` and removes the whole `<base>` scratch-workspace tree (not
just `hostPath` itself — `hostPath` is `<base>/workspace`); there's no
`sandboxes` store row to delete yet, since that table doesn't exist until
the lifecycle/persistence follow-up lands. Double-destroy or destroying a
foreign/unowned id goes through `Manager.Owns` (via `Manager.Get`) the same
way `bash` does — a clean error, not a crash; tested.

`read/write/edit/grep/glob` schemas are **unchanged**.

**Implemented** (`bash`'s `sandboxId` half — step 4): `BashTool` gets a
`sandboxMgr *sandbox.Manager` field wired post-construction via
`SetSandboxManager`, not a constructor param — same `Bind*`/`Set*` idiom
`SubagentTool` already uses (`SetRuntime`, `SetProgressFn`, ...), chosen
specifically to avoid re-touching the ~130 existing `NewBashTool(...)` call
sites for a dependency most callers don't need. `BuildOptions.SandboxManager
*sandbox.Manager` (nil by default) is the wiring point; `BuildRegistry`
calls `SetSandboxManager` only when non-nil. `Execute` checks `in.SandboxID
!= ""` right after the unconditional yolo-block check (so yolo can't be
smuggled through a sandboxed call either) and branches to
`executeSandboxed`, which: errors clearly if no Manager is configured;
checks `Manager.Owns` before any Manager/Driver call; routes the command
through `Manager.Exec` instead of a local `exec.Cmd`; skips `approvalFn`
entirely; still attaches the same `cdWorkdirHint`/`dedicatedToolHint`
advisory hints as the host path (no stale-dir self-heal, though — `workdir`
there is a container-side path this process can't `os.Stat`).

**Implemented** (`create_sandbox`/`sandbox_cp`/`sandbox_destroy` — step 5):
`CreateSandboxTool` asks `SandboxApprovalFn` only when `mounts`/`env` are
non-empty (env values are redacted to just their key in the approval
prompt — secrets shouldn't be echoed back even to a human approval dialog),
makes a fresh `os.MkdirTemp`-based scratch `<base>/workspace` tree (cleaned
up if `Manager.Create` then fails), and calls `Manager.Create`. No
bootstrap script runs yet — see "Root access" above for why that's
deliberately deferred to the `podmanDriver` step rather than guessed at now.
`SandboxCpTool` needs no `Driver`/`Manager.Exec` at all: a sandbox's
workspace is already a plain host directory (`Manager.Get`'s `HostPath`),
so moving a file between it and an arbitrary host path is an ordinary
host-to-host copy, gated by the same `checkSensitivePath` read/write use.
Its `workspacePath` is resolved via a new `resolveInWorkspace` helper that
treats an absolute-looking path as workspace-relative (never a literal host
path — the exact "`/etc/passwd` in a sandboxed call must never mean the
real host `/etc/passwd`" concern from earlier) and re-resolves symlinks on
both sides (`guard.ResolveSymlinkTarget`) so a symlink planted inside the
workspace can't point a copy outside it — regression-tested directly.
`SandboxDestroyTool` takes no `approvalFn` param at all (always allowed);
kills via `Manager.Kill` then removes the whole scratch `<base>` tree via
`Manager.Get`'s `HostPath`. All three are registered by `BuildRegistry`
only when `opts.SandboxManager != nil`, so a session without sandbox
support doesn't even see them as available tools; `create_sandbox`
additionally excludes `Child` registries (a subagent may only use sandboxes
its parent explicitly authorizes, never mint its own — the allow-list
mechanism itself is still the next step, not built yet). Whether a subagent
should also be excluded from `sandbox_cp`/`sandbox_destroy` is left an open
question for that step, not decided here.

Removed: `BashTool.sticky` field and `StickyCwd()` accessor,
`internal/tools/bash_sticky.go` and `bash_sticky_test.go` in full
(`bashSticky`, `wrapBashForSticky`, `readStickyDump`, `parseEnvNull`,
`envForCmd`, `bashSingleQuote`; keep only the plain path-join half of
`stickyStartDir`, renamed, with "sticky" gone from the name), the
`os.MkdirTemp` state-dir block and stale-dir self-heal branch in
`Execute`, `sandbox bool`/`BuildOptions.Sandbox` and the matching
constructor param on all five tools, and every `POISSON_SANDBOX`/
`IS_SANDBOX` reference: `guard_test.go:471` (`TestClassify_SandboxEnvDoesNotAutoApprove`),
`spawn_args_test.go:140-145,155` assertions, the comment in `main.go:689`,
the comment in `spawn.go:140-141`. Tool description/schema text claiming
cwd/env "sticks across calls" gets rewritten; `cdWorkdirHint`'s trailing
"...or rely on sticky cwd after a plain cd" clause is dropped.

## Testing

Written in this order, per the removal being staged before the new
mechanism lands:

1. ✅ **Characterization first**: current sticky isolation between the main
   agent's `BashTool` and a subagent's `BashTool` — regression lock proving
   today's process-boundary isolation, before any code changes. (Made moot
   once sticky is removed entirely, but locks in the process-isolation
   property the rest of this design still depends on.)
2. ✅ Dead-code removal lands clean: `sandbox bool`/`BuildOptions.Sandbox` and
   all sticky code gone, existing test suite green with stateless bash.
3. ✅ `internal/sandbox` with `FakeDriver` before any real podman code —
   `Manager`/`bash` logic tested against the fake exclusively; gated
   integration suite (`PODMAN_INTEGRATION=1`) against real podman, run for
   real: full create→bootstrap→exec-as-resolved-user→sudo→kill lifecycle,
   plus an automated before/after snapshot proving the confined root
   touches nothing outside `/tmp` — both passing against the real `podman`
   CLI on this dev box, not simulated.
4. ✅ Per-tool: sandboxed vs host `bash` behavior parity (approval skip,
   hints) — plus `create_sandbox`/`sandbox_cp`/`sandbox_destroy`'s own
   parity/error-path tests (no-manager-configured, foreign id, driver
   failure, cleanup-on-failure).
5. ✅ `batch` mixing a host `bash` call and one or more `sandboxId` calls in
   one round — real path an agent will hit.
6. ✅ Ownership rejection: foreign/guessed `sandboxId` denied — for `bash`,
   `sandbox_cp`, and `sandbox_destroy` alike.
7. ✅ Subagent: `POISSON_SUBAGENT_SANDBOXES` omitted when the parent has none
   (`TestBuildSpawnEnvOmitsUnsetFields`) and round-trips correctly when
   present (`TestBuildSpawnEnvPropagatesAuthorizedSandboxes`); `subagent`
   tool rejects a foreign/hallucinated sandboxId and errors clearly with no
   `Manager` configured (`TestSubagentTool_SandboxIdsRejectsForeignID`,
   `TestSubagentTool_SandboxIdsRequireManagerConfigured`); a real spawned
   process actually receives the authorized `{id, hostPath}` in its own
   environment (`TestSubagentTool_SandboxIdsPropagateToChildEnvironment`);
   `create_sandbox` absent from a child registry
   (`TestBuildRegistry_WithSandboxManager_ChildOmitsCreateSandbox`, from the
   prior step). Liveness-check-on-a-now-dead-sandboxId itself was already
   proven at the `Manager`/`BashTool` layer in earlier steps — the
   mechanism is identical once a child's `Manager` holds the record, so
   nothing new to test there. `runChildMode` actually wiring a `Manager`
   for the child registry remains for the `podmanDriver` step (see
   "Subagents" above).
8. ✅ Symlink-escape regression: planted symlink inside a sandbox's workspace
   pointing outside it still rejected by `sandbox_cp` (`TestSandboxCpTool_
   SymlinkEscapeRejected`) — the file-tools half (read/write/edit/grep/glob
   never needing their own check, since they were never sandbox-specific)
   was already true by construction, nothing new to test there.
9. ⬜ Idle-reap sweep: stale container + tmp dir actually removed on next
   launch — needs a heartbeat mechanism (see "Open questions"), not started.
10. ✅ Crash recovery / cross-session discovery: a foreign live sandbox is
    invisible without `EnableDiscovery` (`TestManager_ForeignSandboxNotConfusedWithAnothersOwn`,
    unchanged) but adopted with it (`TestManager_DiscoveryAttachesForeignLiveSandbox`);
    a non-running match is refused, not adopted
    (`TestManager_DiscoveryRefusesDeadSandbox`); a subagent's own Manager
    never gets discovery even when the underlying container is right there
    in the shared driver (`TestManager_DiscoverySubagentManagerNeverEnabled`);
    `Manager.List` is a plain pass-through, browsing grants no access
    (`TestManager_ListProxiesDriver`, `TestListSandboxesTool_BrowsingGrantsNoAccess`);
    naming policy (`TestResolveSandboxName`), a duplicate name error, both at
    the `Manager` and the `create_sandbox` tool layer
    (`TestManager_CreateNameCollisionErrorsClearly`,
    `TestCreateSandboxTool_NameCollisionErrorsClearly`); and cross-session
    visibility end-to-end through the actual tool
    (`TestListSandboxesTool_CrossSessionVisibility`). A real `-race`-caught
    regression from this step: `Manager.Get` briefly dereferenced its
    tracked `*Sandbox` after releasing `m.mu` — fixed by copying under the
    lock before returning, same discipline the original `Get`/`Create`
    value-copy design already established.

## Production wiring

**Implemented.** The feature is live, not just built-and-tested: `cmd/px/main.go`'s
three `BuildRegistry` call sites now each get a real `sandbox.Manager`.

- `runPrint` (headless `-p`) and `runREPL` (interactive TUI): each
  constructs `newSandboxManager(sessionID)` — `sandbox.NewManager(sandbox.NewPodmanDriver(nil, nil))`,
  no storage confinement (confinement is test-only, see the disk-wear
  guard), `SetSessionID(sessionID)` and `EnableDiscovery()` (see "Crash
  recovery" above — a top-level session is exactly the case that should be
  able to find/reattach to any live sandbox by name) — and passes it as
  `BuildOptions.SandboxManager`. A new `sandboxApprovalFn`, mirroring
  `fileApprovalFn`'s exact shape (fixed `BashRiskHigh`, asks the human
  directly — no LLM classification, since "does this request carry extra
  mounts/env" is exactly as deterministic a question as "is this path
  sensitive"), is passed as `BuildOptions.SandboxApprovalFn`.
- `runChildMode` (subagent child): a new `resolveChildSandboxManager(envValue string) *sandbox.Manager`
  parses `POISSON_SUBAGENT_SANDBOXES` (`subagent.ParseAuthorizedSandboxes`)
  and `Authorize`s each entry onto a fresh Manager — built directly
  (`sandbox.NewManager(sandbox.NewPodmanDriver(nil, nil))`), never via
  `newSandboxManager`, specifically so `EnableDiscovery` can't be
  accidentally inherited by a subagent's Manager — returning `nil` (not an
  empty-but-non-nil Manager) when there's nothing authorized or the value
  is malformed, so a plain subagent (the common case: most spawns authorize
  nothing) doesn't even get `sandbox_cp`/`sandbox_destroy` registered at
  all, same "don't offer a dead-end tool" reasoning used everywhere else in
  this design. Pulled out of `runChildMode` as its own function for the
  same testability reason `subagent.buildSpawnArgs`/`buildSpawnEnv` were
  pulled out of `Spawn` — `runChildMode` itself can't be unit-tested
  (`os.Exit` paths, real stdin/stdout), but the parsing+authorization logic
  now can be, independent of it.
- **Why this "just works" across the parent/child process boundary with no
  extra plumbing**: real podman state isn't process-local. A child process
  constructing its own fresh `podmanDriver` (no shared Go state with the
  parent at all) and calling `Exec`/`Kill`/`Inspect` against a real
  container id the *parent* created reaches that same real container,
  because the shared state is podman's own on-disk/kernel state, not
  anything this package holds in memory — `Manager.Authorize` only needs to
  supply the `{id, hostPath}` record so the child's own ownership check
  (`Owns`) passes; the routing itself needs nothing further.
- `cmd/cost-eval/main.go` (a benchmarking harness, not a real session —
  already deny-all for every approval by design) is deliberately left
  unwired: giving it sandbox support would be inconsistent with its
  bounded-cost-measurement purpose and would spin up real containers
  during automated cost runs.
- `internal/project/prompt.go`'s system prompt gained a guideline nudging
  the model to prefer `create_sandbox` + `bash(sandboxId=...)` over a host
  command that would need human approval, whenever `create_sandbox` is
  actually available (it simply won't be in the tool list otherwise, so
  the guideline is safe to state unconditionally). Extended in the crash-
  recovery step: name the sandbox descriptively (that name is its
  `sandboxId`, reusable by any session later), check `list_sandboxes` before
  creating a duplicate, and prefer reattaching to a recognized sandbox over
  poking one that looks like someone else's in-progress work — the
  technical access model is open (see "Crash recovery" above), so this
  guideline is the only thing enforcing good manners between sessions.

## Open questions

1. `create_sandbox` concurrency limit per session — cap how many a single
   session can have open at once?
2. Idle-timeout duration default, and whether it's configurable in
   `config.toml`.
3. Does `sandbox_cp` also need a "copy between two sandboxes" mode, or is
   host-mediated (out of one, in to another) good enough for v1?
4. Should `/workspace` start empty (agent `git clone`s or `sandbox_cp`s the
   project in itself), or does `create_sandbox` seed it from the current
   session cwd automatically? No longer a security question (sensitive-path
   approval applies either way, per "File tools") — now purely a UX/token-
   cost question.
5. Idle-reap sweep, now that "Crash recovery" above has settled discovery:
   `Driver.List`'s `Info.CreatedAt` alone isn't "last used" — a heartbeat
   (e.g. mtime of a sentinel file under `hostPath`, touched on every
   `Manager.Exec`) is the likely mechanism, still to be designed and built.
6. Should a generic/very common agent-chosen name (e.g. "test", "scratch")
   get an automatic disambiguating suffix, or is a clear collision error
   (agent retries, or checks `list_sandboxes` first) good enough? No
   evidence yet that collisions are a real friction point in practice.
