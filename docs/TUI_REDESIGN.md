# TUI Redesign Plan

## Target (from screenshot)

Three regions stacked vertically inside an alt-screen:

1. **Scrollback region** — top, fills leftover rows. Color-coded message
   bars: user (cyan), assistant (default), tool_call (blue), tool_result
   (default), thinking (dim), system (yellow), error (red), compacting (purple).
2. **Input region** — 3 rows. Multi-line editor. `Enter` = newline;
   `Ctrl+J` or `Esc+Enter` = submit. Hint line below.
3. **Status bar** — 2 rows. Line 1: cwd + branch (left), model · effort
   (right). Line 2: token counts (↑in ↓out R⌫read W✎write $cost · ctx%),
   tool call counters, "ctrl+c again to exit" indicator.

## Mechanism

Pure ANSI escapes. No ncurses, no bubbletea, no tview.

- `ESC[?1049h` / `ESC[?1049l` — alt-screen enter/exit
- `ESC[?25l` / `ESC[?25h` — hide/show cursor
- `ESC[H;Hf` — position cursor (1-indexed rows/cols)
- `ESC[2K` — clear line; `ESC[J` — clear below
- `ESC[?2004h` / `ESC[?2004l` — bracketed paste (already used)
- `SIGWINCH` handler — recompute row layout on resize

Three goroutines:
- **render goroutine** — ~30fps redraw from a `scrollback` ring buffer +
  current `input` text + current `status` snapshot. Driven by a `dirty`
  channel + 30ms fallback tick.
- **input goroutine** — reads raw stdin, UTF-8 decode, bracketed paste,
  key dispatch (arrows, Home/End, PgUp/PgDn, Ctrl-A/E/K/U/W, Backspace,
  Delete, Tab=indent, Up/Down=history).
- **agent goroutine** — unchanged; writes `OutputEvent`s into the
  scrollback buffer via `Append(ev)`.

Approval (existing `TUI.Approve`) flips back to blocking stdin, suspends
the render goroutine, renders the prompt into the scrollback, reads a
key, resumes render.

## File Layout

| File | LoC target | Purpose |
|---|---|---|
| `tui.go` | ~250 | alt-screen lifecycle, region layout, resize, signal handling, goroutine wiring |
| `input.go` | ~400 | multi-line editor (cursor, paste, history, submit) |
| `scrollback.go` | ~350 | ring buffer of styled lines, wrap, append, render |
| `status.go` | ~150 | 2-row status bar (split off from current `render.go`) |
| `commands.go` | unchanged | all `/commands` keep their existing semantics |
| `render.go` | ~80 | `OutputEvent → scrollback line` styling only |
| `palette.go` | ~40 | ANSI color helpers |

Total ~1300 LoC, replacing ~1100 LoC across `tui.go` (867) + `render.go` (245).

## Test Strategy

TTY mocking is brittle. Focus tests on:

- **Pure functions** — line wrap (`wrapLine`), event→line styling
  (`eventToLine`), status formatter, history navigation (state machine).
- **Scrollback** — ring buffer append/wrap/scroll, viewport math.
- **Input** — UTF-8 decode, paste accumulation, key dispatch table
  (table-driven test of every Ctrl- combo).

Skip: full TUI integration tests. Manual smoke test instead:
`script -c ./px /tmp/out.txt` to capture the alt-screen output.

## Out of Scope

- Kitty / iTerm graphics protocol images
- True color detection (assume 256-color; fall back to 16)
- Mouse support (clickable scrollback, text selection)
- Vertical split (editor on left, output on right)

## Risks

- **Tests**: ~50% of current `tui_test.go` (TTY-bound) must be rewritten
  or deleted.
- **Resilience**: alt-screen redraws are O(rows × cols). On a 200×200
  terminal that's 40k cells/redraw × 30fps = 1.2M cells/s. Cheap, but
  watching a long stream will repaint a lot. Mitigate by only repainting
  changed rows (dirty tracking).
- **Status flicker**: token counts update mid-stream. The status bar is
  its own region; we redraw just those 2 rows when the snapshot changes.
- **Approval prompt**: now an overlay in the scrollback region. Needs
  careful redraw so the prompt doesn't get clobbered by a streaming event
  arriving during the prompt. Serialize via `approvalMu`.

## Estimated Effort

~6-8 hours implementation + 2 hours test rewrite + 1 hour manual smoke.
Plan to land in 3 commits:

1. Add scrollback + alt-screen scaffold; render old events into it (no
   input change yet). Verify parity with readline mode.
2. Multi-line input + new keybindings; keep old single-line path as
   fallback if Enter=submit during migration.
3. Status bar split + live token counters.

Flag flipping: `POISSON_TUI=classic` to revert to readline for the
release window.
