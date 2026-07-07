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
