# TODO

Deferred cleanups and follow-ups. Keep entries terse and actionable.

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
