# TODO

Deferred cleanups and follow-ups. Keep entries terse and actionable.

## Security

### Residual trust in LLM bash-risk "low" auto-approve

`WrapRiskGatedApproval` still auto-runs commands the deterministic guard does
not clear when the LLM risk classifier returns a single-word `low`
(`internal/agent/approval_gate.go`, `risk.go`). Hard escalations (rm, npx/dlx,
package installs, dangerous git) are deterministic; everything else is one
model shot at temp 0.

- Expand deterministic medium/high lists (network binaries, `scp`/`rsync`,
  `docker`, `sqlite3` against sensitive paths, …) so fewer commands ever reach
  the LLM.
- Optional: default to paranoid mode, or never auto-approve on LLM-only low
  without a second confirming signal.
- Prompt-injection in the agent-supplied `description` is already called out
  in the risk system prompt; models still mis-rate.

## Tech debt

### Remove the duplicate byte-level input parser (`editor.feed` / `editor.handleEscape`)

`internal/tui/input.go` has a byte-level input parser — `editor.feed([]byte)` and
`editor.handleEscape` (CSI/SS3/OSC parsing) — that shadows what the production
`keyDecoder` (`keyDec.Push`) already does. Production input flows
`keyDec.Push(data)` → `feedKey(Key)` → `editor.applyKey(Key)`; `editor.feed` is
**never called in production** (the only non-test caller is itself, recursively,
in the paste-continuation path).

It survives only because ~9 tests in `internal/tui/tui_test.go` drive the editor
via `e.feed([]byte{...})` to assert byte-sequence → behavior (kitty Enter
encodings, Ctrl+J newline, paste, arrows). Those assertions really belong at the
`keyDec` / `applyKey` level.

- **Keep** `TUI.feed` — it is *not* a duplicate; it is `keyDec.Push` + `feedKey`
  (the real decode path) and is the correct test entry point used by 6 test files.
- **Do**: migrate the `e.feed(...)` cases in `tui_test.go` to `keyDec`+`applyKey`
  (or `tui.feed`), then delete `editor.feed` + `editor.handleEscape` and their
  byte-parsing helpers.
- Verify afterwards with `deadcode ./...`.

### Long sessions: expensive compaction + repeated re-reads

Analysis of real host conversations (`~/.poisson/poisson.db`) found one marathon
session compacted 4× (400–520K raw tokens per cycle, $3.3–5.5/cycle). After each
compaction the agent re-read the same files repeatedly (a README re-read 11
times, an evals file 11 times) because the compaction summary doesn't retain
"already read file X, here's its content/hash" state — the agent can't tell
whether a stale read is still trustworthy, so it re-fetches defensively.

- Possible directions: earlier/cheaper incremental compaction thresholds, or a
  manifest of recently-read file paths (+ mtime/hash) carried in the compaction
  summary so the agent trusts recent reads instead of re-fetching.
- Not scoped yet — flagging only.

## Environment / infra

### Bash tool glitch: `fork/exec /usr/bin/bash: no such file or directory`

Observed once in a live session, alongside a subagent auth failure at the same
time. Both infra-side (host environment), not a code issue in this repo —
`/usr/bin/bash` momentarily missing/unresolvable from the process's exec path,
and a separate subagent auth outage in the same window. Not reproduced or
root-caused yet; investigate if it recurs (check whether it's a transient
mount/PATH issue, container restart, or something else host-side).

## Notes (current behavior)

### Edit/write tool cards: how the colored diff is built

The TUI does **not** store a unified diff, and the DB does **not** store one
either. Diff rows are reconstructed at paint time from the tool call's own
input JSON (already on the assistant `tool_use` message):

| Source | Role |
|---|---|
| `tool_input` (`path`, `oldText`/`newText`, or `content` for write) | The only durable content — red = `oldText` lines, green = `newText` / write body |
| `BlockMeta.DiffBase` (RAM only) | Pre-edit file bytes snapshotted at live `ToolStart` (`appendToolCall`), used to place absolute line numbers |
| Live disk read of `path` | Fallback when `DiffBase` is empty: locate `newText` in the current file |

What **is** in SQLite (`messages.content`): the normal agent transcript —
`tool_use` with `tool_input`, and a short `tool_result` string like
`edited main.go (1 edit(s) applied)`. No `DiffBase`, no computed lines, no
tok/s/duration.

What that means on resume / after the file moves on:

- **Historical red/green text** always comes back correctly (it's the stored
  `oldText`/`newText`).
- **Absolute line numbers are best-effort**, not durable:
  - Live session: good — `DiffBase` still holds the pre-edit image.
  - Resume, file unchanged since that edit: usually OK via `newText` lookup
    on disk.
  - Resume, file edited further / moved / deleted: numbers often fall back
    to hunk-local `1`. The text is still the historical edit; the gutter
    may no longer match the current file.
- Write cards number `1..N` of the written blob, not “line N of whatever is
  on disk now.”
- `appendToolCallReplay` (hydrate) never sets `DiffBase`.
- Path resolution for the disk fallback uses process `Getwd()`, not session
  cwd — relative paths can miss if those diverge.

Possible later upgrades (not scoped): persist a small
`{path, startLine, endLine}` (or a hash + start line) next to the tool_use;
or reverse-apply `oldText`/`newText` against a post-image to recover the
pre-edit start without storing the whole file. Until then the current
behavior is intentional and acceptable.
