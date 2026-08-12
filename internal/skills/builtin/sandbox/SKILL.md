---
name: sandbox
description: Set up an isolated podman sandbox for a work session — mount the right directory (workdir, or a fresh git worktree when parallel branches could conflict) from the start, do all bash/build/test work through the container, edit files with the normal host-path tools, inject one-off secrets without baking them into the container, and commit only from host. Use when the user says to work in a sandbox, work in a worktree, or wants an isolated/parallel environment for a task.
---

# Sandbox Workflow

One job: get into an isolated container **with the right files already live in
it**, so nothing has to be tarred, copied, or re-synced by hand mid-task — do
that setup wrong and every edit/build/test after it fights the plumbing
instead of the task.

## 1. Decide what to mount, before creating anything

| Request | Mount |
|---|---|
| "work in a sandbox" (a project/workdir is implied by context or stated) | that workdir, as-is |
| "work in a worktree" (this task, or explicitly parallel-safe work) | a **fresh git worktree**, created first (see step 2) |
| No file/project context at all (pure scratch, e.g. "test this snippet") | nothing — isolated container, no `hostPath` |

**Default to a worktree, not the live workdir, whenever the user could plausibly be
working on something else in the same repo at the same time** (multiple
features/branches in parallel, a long-running task, anything the user didn't
explicitly say is fine to touch directly). Mounting the live workdir risks a
concurrent `git checkout`/edit outside the sandbox colliding with what's
happening inside it. When in doubt, worktree.

## 2. If a worktree is needed, create it first — on host, before the sandbox

Per the global convention: worktrees live under `/tmp/`. On **host** (not yet
sandboxed — no container exists to route this through anyway):

```bash
git fetch origin
git worktree add /tmp/<descriptive-name> -b <feature-branch> origin/<default-branch>
```

Only after the worktree exists does `create_sandbox` get a real path to mount.

## 3. Create the sandbox, mounting that path from the start

```
create_sandbox({"hostPath": "<workdir-or-worktree-path>"})
```

Do this **once**, up front — not "create an isolated sandbox, then figure out
how to get files into it." `hostPath` is agent-supplied and optional (see
`docs/sandbox-plan.md`'s "Amendment" section); there is no default `/workspace`
anymore, so naming it explicitly here is what makes everything downstream
just work.

If the sandbox additionally needs a second directory (credentials, another
project), add it via `mounts` in the same call — that still needs human
approval, same as `hostPath` itself; batch both into one `create_sandbox`
call rather than improvising extra mounts later.

## 4. Work — bash through the container, files through the normal tools

- **Shell/build/test/install**: `bash(sandboxId=..., ...)` — no approval gate,
  the container is the safety boundary.
- **File edits**: plain `read`/`write`/`edit`/`grep`/`glob` against the
  **host** path you mounted (the worktree or workdir path from step 1/2) —
  these tools have no sandbox concept and never need one. Because the path is
  bind-mounted, the container sees every edit instantly; there is nothing to
  sync.
- Don't fall back to tarring the directory into the sandbox and copying a
  snapshot back out — that's exactly the workaround this skill exists to
  avoid. If you find yourself reaching for `sandbox_cp` to move the project
  itself in or out, stop: it means step 3 was skipped or the wrong path was
  mounted.

## 5. One-off secrets, without baking them into the container

`create_sandbox`'s `env` field bakes a value into the container permanently
(set once at `podman create`, visible via `podman inspect` and to anything
running in that container for its whole life) — wrong tool for a secret only
one command needs. Instead, inject it for that single call only, via the
plain host `bash` tool (not `bash(sandboxId=...)` — poisson's own sandboxed-
exec path has no room for extra flags):

```bash
SECRET_NAME=value_only_in_your_shell podman exec -e SECRET_NAME <container> <command>
```

`-e SECRET_NAME` (no `=value`) tells podman to pass through whatever that
variable currently holds in the invoking shell — the secret lives only in
that one exec's environment, never in the container's persistent config, and
disappears the moment the command exits. `<container>` is the sandboxId from
`create_sandbox` — only ever one this session actually owns, same discipline
as everywhere else.

## 6. Commit only from host — never inside the sandbox

`git commit` (and anything else identity-sensitive: push, tag, sign) always
runs via plain host `bash`, never `bash(sandboxId=...)`. The container has no
access to the host's `~/.gitconfig`/SSH agent/credentials unless deliberately
mounted, and per the global git-identity rule, identity must resolve from the
environment poisson is already running in — never forced or worked around.
Since the mounted path is the same real directory on both sides, a host-side
`git commit` in the worktree/workdir sees exactly what the sandbox just built
and tested.

## 7. Cleanup

- `sandbox_destroy` only kills the container. It **never** deletes the
  mounted directory — that's the agent's own workdir or worktree, not the
  sandbox's to dispose of.
- If a worktree was created for this task and the user wants it cleaned up
  too, that's a separate, explicit host step — never implied by
  `sandbox_destroy`:
  ```bash
  git worktree remove /tmp/<name>
  ```
  Ask first if it's not obvious the user's done with it.

## 8. Sandboxing subagents, not just yourself

Spawning several subagents that will each run bash (parallel PR reviews,
per-branch audits, `council`/`check-work` workloads) needs the same setup
done *before* the spawn — a subagent cannot call `create_sandbox` itself, it
can only use a sandbox this session already created and explicitly shares
via `sandboxIds`. Skipping this means every gated bash command each child
runs queues its own approval prompt, multiplied by however many are running
in parallel — the exact failure mode this skill exists to prevent, just one
level removed.

- **Create one sandbox per subagent** (or per worktree, if several subagents
  share a repo but work on different branches) — same `hostPath`-first
  discipline as step 3, done N times up front, not improvised mid-fan-out.
- **Stage everything the child will need before spawning it**: worktree
  already checked out, diff already computed and written to a file (`git
  diff ... > /tmp/<name>.diff`), dependencies already installed if the task
  is going to build/test — a subagent has no way to ask you for anything, so
  incomplete staging just becomes wasted turns re-deriving what the parent
  already knew.
- **Pass `sandboxIds: ["<id>", ...]`** in the `subagent` call, and tell the
  child explicitly in its task text to use `bash(sandboxId="<id>", ...)` for
  every command — it defaults to plain host bash otherwise, which puts every
  gated command it runs right back on the human approval queue.
- Issue the `create_sandbox` calls and the `subagent` calls that use them in
  the same batch/round where possible; there's no dependency between sibling
  sandboxes, only between a sandbox and the one subagent call that names it.

---

## ✅ Checklist

- [ ] Mount decided (workdir / fresh worktree / none) before calling `create_sandbox` — never created blind then patched.
- [ ] Worktree (if needed) created on host, under `/tmp/`, before the sandbox.
- [ ] `create_sandbox` called once with `hostPath` (and any extra `mounts`) set from the start.
- [ ] All shell/build/test work went through `bash(sandboxId=...)`.
- [ ] All file edits went through the normal host-path tools against the mounted path — no tar/sync workaround.
- [ ] Any one-off secret used `podman exec -e` via plain host `bash`, never baked into `create_sandbox`'s `env`.
- [ ] Every commit ran via plain host `bash`, never with `sandboxId` set.
- [ ] `sandbox_destroy` (if used) only killed the container — mounted directory and any worktree left untouched unless separately, explicitly removed.
- [ ] If spawning multiple subagents that run bash: one sandbox per subagent created and staged *before* the spawn, `sandboxIds` passed to each, task text tells the child to use `bash(sandboxId=...)`.
