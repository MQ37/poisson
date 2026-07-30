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

## Architecture

### `internal/sandbox` package (new)

```go
type Driver interface {
    Create(ctx context.Context, opts CreateOpts) (id string, err error)
    Exec(ctx context.Context, id, cmd, workdir string, timeout time.Duration) (stdout, stderr string, exitCode int, err error)
    Inspect(ctx context.Context, id string) (alive bool, err error)
    Kill(ctx context.Context, id string) error
}
```

- `podmanDriver` — real implementation, shells out to the `podman` CLI
  (same style as `bash.go`/`grep.go` today — no REST API, no new
  dependency). Takes an optional `globalArgs []string` prefix, nil in
  production (real `create_sandbox` uses whatever storage the host user has
  configured) — only the integration suite below sets it.
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

  **Cleanup gotcha, hit and confirmed live**: tearing down `tmpDir` after
  the suite runs must **not** use a plain `os.RemoveAll`/`rm -rf` — rootless
  podman's user-namespace uid mapping leaves files under the container
  overlay diff owned by subordinate uids a plain unprivileged delete can't
  touch (`Permission denied` on every file). Use `podman unshare rm -rf
  tmpDir` instead (enters the same uid-mapped namespace podman itself
  uses) — confirmed working. This is specific to the test harness blowing
  away a whole confined storage root directly; it does **not** affect
  normal `sandbox_destroy`/idle-reap teardown of a real container, which
  goes through `podman rm -f <id>` and lets podman handle its own
  uid-mapped layer cleanup internally.

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

Tension: `apt-get install` needs root inside the container's own rootfs;
the bind mount needs the container's process to write as the *same uid* as
the host user, or host-side `write`/`edit` can't touch what it creates
(container's default root user, unmapped, writes as `uid 0`, or under
`--userns=keep-id` root still maps to an unrelated subordinate uid — either
way, not the host user). One flat "run as root" or one flat "run as
host-uid" can't satisfy both at once.

Resolution — bootstrap once at `create_sandbox` time, before returning
`sandboxId`:

1. `groupadd -g $HOST_GID poisson; useradd -u $HOST_UID -g $HOST_GID -m poisson`
   — a container user whose numeric uid/gid literally match the host
   invoking user (poisson knows its own `os.Getuid()`/`os.Getgid()`).
2. Install `sudo`, grant it passwordless:
   `echo "poisson ALL=(ALL) NOPASSWD:ALL" >/etc/sudoers.d/poisson`.
3. Every `bash(sandboxId=...)` execs as that user:
   `podman exec --user poisson <id> bash -c ...` — never root by default.

Ordinary commands (build, test, script) land owned by the host uid
automatically, so host `read/write/edit/grep/glob` work on them with zero
special-casing. Anything needing real root — `sudo apt-get install foo` —
the agent just prefixes `sudo`.

No `--userns=keep-id` needed: it was solving the same problem implicitly
and less legibly. Explicit matching uid is easier to reason about and
debug.

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
- New env var, same pattern as every other `POISSON_SUBAGENT_*` var in
  `buildSpawnEnv` (`spawn.go:145-163`): `POISSON_SUBAGENT_SANDBOXES`,
  comma-separated container-id list, **omitted entirely when empty**
  (mirrors `TestBuildSpawnEnvOmitsUnsetFields`).
- The child must still verify liveness before use — `Manager.Inspect` /
  `podman inspect` against the id — not blindly trust the env string: the
  parent (or the idle reaper) may have killed it between spawn and use.
  An exec against a dead container is an ordinary tool-error, not a crash;
  needs a test.
- No sticky state to propagate (removed entirely, see top) — the allow-list
  is a pure permission check, nothing else crosses the process boundary.

### Lifecycle / cleanup

A podman container isn't tied to poisson's process lifetime — it keeps
running (CPU/memory/its overlay layer) after a crash, a closed terminal, or
an abandoned session, exactly like the `/tmp` mirror directory sitting on
disk. Left unmanaged, every forgotten session leaves one more container and
one more `/tmp` dir behind, forever — `podman ps` clutters, disk fills,
eventually real resource exhaustion.

- Session store gains a `sandboxes` table: `id, session_id, container_id,
  host_path, created_at, last_used_at`.
- On every `px` launch: a `sweepStaleSandboxes()` pass, same shape as the
  existing `sweepStaleSpillFiles()` (`sync.Once`, TTL-based) — checks each
  recorded container against `podman ps`, kills + removes the tmp dir for
  anything past an idle TTL or whose session no longer exists.
- Explicit `/sandbox ls` / `/sandbox kill <id>` surfaced to the user.
- On clean session end: if any sandboxes are still alive, ask
  "N sandboxes still running — kill them? (Recommended) / leave running".

## Tool schema changes

```
bash{command, description, workdir, timeout, sandboxId?}
create_sandbox{image?, mounts?, secrets?} -> {sandboxId, hostPath}
sandbox_cp{sandboxId, direction: in|out, hostPath, containerPath}
sandbox_destroy{sandboxId} -> {ok}
```

`sandbox_destroy` is the agent-driven complement to the idle-reap sweep and
the `/sandbox kill` slash command (see "Lifecycle / cleanup"): the agent
calls it itself once it's done with a sandbox, instead of relying solely on
the TTL sweep to eventually notice. Always auto-approved — destroying a
sandbox only discards disposable container/scratch-dir state, never host
data, so there's no risk to gate. Kills the container, removes its
`/tmp/poisson-sandbox-<id>` tree, and deletes its `sandboxes` row.
Double-destroy or destroying a foreign/unowned id must still go through the
same `Manager.Owns(id)` check as `bash` — a no-op error, not a crash.

`read/write/edit/grep/glob` schemas are **unchanged**.

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

1. **Characterization first**: current sticky isolation between the main
   agent's `BashTool` and a subagent's `BashTool` — regression lock proving
   today's process-boundary isolation, before any code changes. (Made moot
   once sticky is removed entirely, but locks in the process-isolation
   property the rest of this design still depends on.)
2. Dead-code removal lands clean: `sandbox bool`/`BuildOptions.Sandbox` and
   all sticky code gone, existing test suite green with stateless bash.
3. `internal/sandbox` with `fakeDriver` before any real podman code —
   `Manager`/`bash` logic tested against the fake exclusively; a small
   gated integration suite against real podman, opt-in only.
4. Per-tool: sandboxed vs host `bash` behavior parity (approval skip,
   hints).
5. `batch` mixing a host `bash` call and one or more `sandboxId` calls in
   one round — real path an agent will hit.
6. Ownership rejection: foreign/guessed `sandboxId` denied.
7. Subagent: liveness-check on an authorized-but-now-dead sandboxId returns
   a tool error, not a crash; `POISSON_SUBAGENT_SANDBOXES` omitted when the
   parent has none (mirrors `TestBuildSpawnEnvOmitsUnsetFields`); a
   subagent calling `create_sandbox` is rejected/absent from its registry.
8. Symlink-escape regression: planted symlink inside `/workspace` pointing
   at a sensitive host path still caught by host-side file tools.
9. Idle-reap sweep: stale container + tmp dir actually removed on next
   launch.

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
