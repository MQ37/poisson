# poisson — Agent Instructions

## 🚫 MANDATORY — Never Introduce a New Dependency Without Asking First

This is the single most important rule in this repo. poisson is deliberately
dependency-tiny (see README.md § Dependencies — currently 3 direct deps, the
rest transitive). Before reaching for a library — for *anything*, no matter
how small or well-established the package is — check whether the standard
library can do it. It usually can. This repo already has hand-rolled:
a TUI (no framework), a TOML parser, an HTTP client layer over `net/http`,
JSON via `encoding/json`, and (as of this writing) its own Unicode
display-width table (`internal/tui/runewidth.go`) instead of a width library.

- ❌ Never run `go get`/add a `require` line to `go.mod` to solve a task
  unless the user has explicitly approved that specific dependency first.
- ❌ "It's small," "it's just for one function," "it's widely used" are not
  exceptions — a hand-rolled from-scratch implementation is the default
  answer, even if it takes longer to write.
- ✅ If you genuinely believe stdlib can't do the job, stop and ask the user
  before adding anything — name the exact package, what it's for, and why
  stdlib falls short. Let them decide.
- If a dependency is approved, pin it to a release **≥ 14 days old** (check
  via `go list -m -versions`/`go list -m -json <pkg>@<version>` for the
  `Time` field) to dodge fresh-release bugs and supply-chain surprises —
  same rule the global AGENTS.md states for JS/pnpm, applied here to Go.

This rule exists because it was violated once already in this repo's history
(a CJK terminal-width fix pulled in `github.com/mattn/go-runewidth` plus a
transitive dependency without asking) — caught and reverted in favor of a
~90-line hand-rolled Unicode range table. Don't repeat that.
