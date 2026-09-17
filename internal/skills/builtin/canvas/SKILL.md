---
name: canvas
description: Build a self-contained HTML report/dashboard/prototype instead of a markdown wall of text, when the content is genuinely denser as something visual — comparisons, annotated diffs, diagrams, dashboards synthesizing several sources, long plans/specs, or a throwaway interactive editor. Ends the turn with a `file://` link so the human can click it open instead of finding the file by hand. Use whenever asked for an "HTML report", "canvas", "dashboard", "mockup", or when a comparison/diagram/long plan would read better rendered than as text.
---

# Canvas — HTML output instead of a markdown wall

Markdown is a report; HTML is a surface the human can actually look at, scan,
and click through. Not every reply needs one — most don't — but for the
right content it's the difference between a file that gets skimmed once and
one that gets read.

## 1. When to reach for this

- A comparison or matrix (options side by side, before/after, a decision grid).
- A code review or diff walkthrough: render the actual diff with inline
  margin annotations, color-coded by severity — much easier to follow than
  the same information as fenced code blocks and bullet points.
- A diagram: dependency graph, data flow, architecture, a sequence of
  events — SVG draws these; ASCII art in markdown is the thing this
  replaces.
- A dashboard/report synthesizing multiple sources (logs, git history, a
  handful of files) into one page.
- A plan/spec long enough that a markdown version would run past ~100
  lines — past that length people stop reading it top to bottom; a page
  with real structure (sections, a table of contents, collapsible detail)
  gets read instead.
- A throwaway interactive editor for one specific piece of data (reorder
  some items into buckets, tune a few parameters, annotate a transcript) —
  see §6 for the one extra rule this shape needs.

## 2. When not to

- A short answer, a single fact, a one-paragraph explanation. HTML for
  three sentences is worse than the three sentences.
- Anything meant to stay a durable, diffable source artifact — a real spec
  committed to the repo, a doc future edits will `git diff` against. Those
  stay markdown; this skill is for the disposable read-once-or-twice
  report, not project source.

## 3. One self-contained file, nothing fetched over the network

Anthropic's own artifact-design guidance (Claude/ChatGPT web artifacts)
allows a Google Fonts `<link>` and a pinned cdnjs `<script>`, because those
render inside a browser sandbox with real network access. A poisson canvas
doesn't get that: it's opened cold via `file://`, possibly offline, with no
guaranteed network and no CSP to lean on. So the one hard rule here is
stricter than theirs:

- **Everything inline** — `<style>` and `<script>` in the document itself,
  no external `<link>`/`<script src>` to any host, no `@import url(...)`.
- **System font stack only** — `-apple-system, BlinkMacSystemFont,
  "Segoe UI", Roboto, Helvetica, Arial, sans-serif` (and the equivalent
  monospace stack for code/data). No Google Fonts, no CDN font.
- Data the page needs (numbers, file contents, diagram edges) gets inlined
  as JS literals or embedded SVG at generation time — never a `fetch()` the
  page makes after opening.

This is the same "no new dependency, hand-roll it" principle this repo
already applies to its own Go code, extended to what it hands the user.

## 4. Theme — three states, not two

The viewer's OS is in one of three states: explicit light, explicit dark,
or "system" (which shows the page un-stamped — most viewers land here).
Structure tokens for all three, or the un-stamped default silently renders
one theme's text on the other theme's background:

```css
:root {
  /* bare :root defines the COMPLETE light palette — every token, always */
  --bg: #ffffff;
  --fg: #1a1a1a;
  --accent: #2563eb;
}
@media (prefers-color-scheme: dark) {
  :root:not([data-theme="light"]) {
    /* redefines only the tokens — guarded so an explicit light choice wins */
    --bg: #16181d;
    --fg: #e6e6e6;
    --accent: #6ea8fe;
  }
}
:root[data-theme="dark"] {
  /* same redefinition again, so an in-page toggle also wins over the OS */
  --bg: #16181d;
  --fg: #e6e6e6;
  --accent: #6ea8fe;
}
body { background: var(--bg); color: var(--fg); }
```

Rules that keep this actually working:

- Declare every token in the bare `:root` block first — a color that only
  exists inside a `@media`/`[data-theme]` block is invisible in the
  un-stamped default state. That's the classic unreadable-page bug.
- `body` sets an explicit `background` from a token. A transparent body
  silently composites over whatever the browser chrome paints behind
  `file://`, which isn't guaranteed to match either theme.
- Every element that sets a color takes it from the token set, never a
  literal — a hardcoded hex that "looked fine" in one theme breaks in the
  other.
- A single-theme design (a deliberately dark-only terminal-style report) may
  skip the media query and `[data-theme]` blocks entirely — but still paint
  `background` and every color explicitly in the bare `:root`, so the page
  holds regardless of the viewer's OS setting.

## 5. Layout and density

- flex/grid + `gap` for spacing siblings, not per-element margins that
  silently collapse or double.
- A side gutter of at least 16px at every width, set once (`padding` on
  `body` or one outer wrapper) — its vertical padding as `padding-block`,
  never a `padding` shorthand that zeroes the sides.
- Stack to one column at phone width (~400px).
- A wide table, code block, or diagram gets its own
  `overflow-x: auto` container — the page body itself never scrolls
  sideways.
- `font-variant-numeric: tabular-nums` wherever digits line up in columns.
- Diagrams: inline SVG with an explicit `viewBox` sized to include room for
  the outermost labels, and every shape an explicit `fill` from a theme
  token — a diagram that only reads in one theme is the same bug as §4.

## 6. Anti-"AI slop" checklist

Skip the look every model defaults to when nothing is specified:

- No purple-to-blue gradient hero.
- No Inter or Space Grotesk as the "safe" font — a system stack has its own
  character, use it.
- No `rounded-lg` stamped on every single block — spend border/radius/shadow
  by role (the one thing that needs to read as "separate" gets it, not
  everything).
- No emoji as section markers.
- No numbered badges (01 / 02 / 03) unless the content is an actual
  sequence where order carries information.
- Not everything centered — most reports read better left-aligned with a
  real type scale, not a stack of centered cards.

## 7. The interactive-editor shape needs one more thing: an export

A throwaway editor built for one specific piece of data (drag tickets
between buckets, tune a few sliders, annotate a transcript) is only useful
if what the human did in it comes back out. End it with an explicit
"copy as JSON" / "copy as markdown" button using
`navigator.clipboard.writeText(...)` — but clipboard permissions on a
`file://` origin vary by browser, so back it with a visible, selectable
`<textarea readonly>` holding the same content as a fallback the human can
select-all and copy by hand if the button silently fails.

## 8. Save it, then hand back a `file://` link

```bash
mkdir -p /tmp/poisson-canvas
out="/tmp/poisson-canvas/<slug>-$(date +%Y%m%d-%H%M%S).html"
```

Write the report to `$out` (a descriptive `<slug>`, not a generic name —
several reports in the same session shouldn't collide or overwrite each
other). Then **always end the reply with the file's `file://` link** —
`file://$out` (absolute path) as a bare URL or as markdown
`[label](file://...)`. The TUI turns that into a real clickable terminal
hyperlink (OSC 8 — supporting terminals open it on click); a bare path
without the `file://` scheme is just text the human has to go open by
hand, which defeats the point of building this instead of a markdown
answer.

## 9. Build cleanly

Close every non-void tag, double-quote every attribute, give every form
control a stable `id`, give keyboard focus a visible state, respect
`prefers-reduced-motion` for anything that animates. Everything meant to be
read is visible once the page loads, without scrolling to trigger it — no
content parked at `opacity: 0` waiting on a scroll observer.

---

## ✅ Checklist

- [ ] Content actually denser as HTML than markdown (§1) — not reached for
      out of habit.
- [ ] One file, everything inline — no external `<link>`/`<script src>`,
      system font stack only (§3).
- [ ] Three-state theme tokens declared in bare `:root` first, `body`
      background explicit, every color from a token (§4).
- [ ] Layout uses flex/grid + gap, 16px+ gutter, stacks at ~400px, no
      sideways page scroll (§5).
- [ ] No gradient hero / Inter font / uniform rounded corners / emoji
      markers / fake numbered sequence / everything-centered (§6).
- [ ] If it's an interactive editor: a copy/export control plus a
      `<textarea readonly>` fallback (§7).
- [ ] Saved under `/tmp/poisson-canvas/<slug>-<timestamp>.html`, reply ends
      with the `file://` link — never a bare path (§8).
