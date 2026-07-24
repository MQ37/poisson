<p align="center">
  <img src="assets/logo.jpg" alt="poisson — a coding agent that lives in your terminal" width="600">
</p>

<p align="center">
  <i>Embrace the entropy, probabilities favor the bold.</i>
</p>

<p align="center">
  <img alt="Go 1.25" src="https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white">
  <img alt="pure Go" src="https://img.shields.io/badge/CGO-disabled-2f81f7">
  <img alt="single binary" src="https://img.shields.io/badge/build-single%20static%20binary-238636">
  <img alt="dependencies" src="https://img.shields.io/badge/direct%20deps-3-3fb950">
  <img alt="storage" src="https://img.shields.io/badge/storage-SQLite%20%2B%20FTS5-orange">
</p>

---

**poisson** is a small, fast coding agent you run in your terminal. It streams a
real conversation, calls tools (bash, file read/write/edit, subagents),
tracks every token and dollar, and keeps your whole history in a local SQLite
database you own. No cloud account for the app itself, no telemetry, no
Electron — just one static binary and your terminal.

It talks to **Anthropic** (Claude, via your Pro/Max subscription), **OpenAI**
(GPT-5.5, via your ChatGPT Plus/Pro subscription), **xAI** (Grok, via SuperGrok)
and **Ollama** (local + cloud models). You can paste
images, search past sessions full-text, compact context, and approve risky
shell commands from a popup.

```bash
go install github.com/mq37/poisson/cmd/px@latest   # needs Go 1.25+; installs to $(go env GOPATH)/bin
px login anthropic                                  # or: px login openai | xai — skip for Ollama, no auth needed
px                                                   # launch the TUI
```

---

## ✨ Features

- **Streaming REPL TUI** — hand-rolled ANSI, no TUI framework. Live thinking
  blocks (Ctrl+T to fold), tool cards you can expand (Ctrl+E) — including
  colored diffs for `edit`/`write` — a command palette (Ctrl+P), mouse scroll,
  and a status bar with live context % and exact cost.
- **Real tools** — file work is first-class, not a bash afterthought:
  `read` (line-numbered, offset/limit, images), `write`, `edit`
  (exact replace, multi-hunk `edits[]`, `replaceAll`, CRLF-safe, capped
  success snippet + miss hints), `grep` (ripgrep wrapper with caps; skips
  `.git`/`node_modules`/… even without a `.gitignore`), `glob` (e.g.
  `**/*_test.go`), and `batch` (up to 20 independent tool calls in one step —
  polyfill for models that only emit one `tool_use` per turn; no dataflow
  between steps; denies `bash`/`subagent`/`batch`). Also `bash` (still the
  full escape hatch; plain `cat`/`head`/`tail`/`sed -n` are refused in favor
  of `read`), `web_search` (plain DuckDuckGo link list, no account), `web_ask`
  (AI-synthesized answer — xAI Grok via OAuth when logged in, exa.ai keyless
  fallback otherwise), `fetch` (works on every provider — Ollama's own
  web_fetch API when available, otherwise a built-in HTML→Markdown
  converter), `recall` (full-text search across past sessions), plus
  **subagents** (parallel child agents) and **skills** (8 built in, see
  [below](#-built-in-skills); also user-defined via `~/.poisson/skills/`).
  Prefer the dedicated file tools over packing the same job into `bash`;
  multi-tool turns or `batch` beat shell pipelines for independent steps.
  Within one session, `bash` keeps **sticky cwd + environment in RAM**
  (`cd` / `export` carry to the next bash call); optional `workdir` overrides
  for one call. State is per agent (subagents isolated), never written to
  SQLite — restart/resume starts clean at the session cwd.
- **Bash safety guard, two speeds** — every shell command is checked before it
  runs. **Fast mode** (default): a deterministic guard auto-approves
  read-only, side-effect-free commands (`ls`, `cat`, `grep`/`rg`, `find`,
  `git status`/`diff`/`log`, ...) with zero LLM calls and no prompt at all —
  it also follows symlinks, so a sensitive file can't hide behind an
  innocent-looking name. Anything else is risk-classified by the LLM; low
  risk runs automatically, medium/high/unknown pops an approval prompt (you
  decide — it never auto-allows installs, destructive, or `npx`/`dlx`
  commands). **Paranoid mode** (**Shift+Tab** to toggle, shown bottom-right
  of the input): both the guard and the LLM classifier are skipped —
  literally every command asks you first.
- **Sessions in SQLite** — every message, tool call, and API call is persisted.
  **Full-text search** (FTS5) across your history, resume any session, and
  auto-compaction that summarizes old turns when context fills up.
- **Exact cost & tokens** — per-API-call token counts and USD cost, live in the
  status bar and via `/cost`.
- **Live usage-limit tracking** — Anthropic OAuth sessions show 5-hour and
  7-day (weekly) rolling usage % plus any pay-as-you-go balance right in the
  header; OpenAI/Codex sessions show weekly usage % and how many free
  "reset this window early" credits your account has (spend one with
  `/openai-reset-usage`). Refreshed at most every 5 minutes, straight from
  each provider's own usage endpoint — no scraping, no guessing from token
  counts.
- **Image input** — paste an image with **Ctrl+V** or attach `@screenshot.png`.
  Images are downscaled to 1024px and sent to vision-capable models. ([details](docs/images.md))
- **Multi-provider** — Anthropic (subscription OAuth), OpenAI (ChatGPT
  subscription OAuth), xAI (OAuth), Ollama (local daemon or Ollama cloud).
  Switch model/effort live.
- **Message queueing** — type while the agent is working (or while `/compact`
  is running); your message is spliced into the model's very next request —
  after the current tool round, or right before the turn would otherwise end —
  instead of waiting for the whole turn to finish.

---

## 🧰 Built-in skills

Eight skills ship baked into the `px` binary — no setup, no config directory
needed. The `skill` tool loads one by name and works the same for subagents
as it does in the main session.

| Skill | What |
|---|---|
| `code-quality` | Suckless-style simplicity/clarity principles — bloat, over-abstraction, needless defensiveness. Use before writing or reviewing code. |
| `code-review` | Full multi-lens review of a diff (correctness, security, API design, tests) with subagent-verified findings and apply/escalate/skip fixes. |
| `review-pr` | Gathers a PR/branch diff (local, GitHub, or fresh checkout) for review; hands off to `code-review` + `stacked-diff-review`. |
| `stacked-diff-review` | Risk-tiered (🔴/🟡/🟢) write-up format for presenting a review. |
| `check-work` | Spawns a fresh-context subagent to independently verify finished work actually satisfies the original request — PASS/FAIL verdict. |
| `council` | Convenes parallel subagent personas (Torvalds, Hotz, Davis, ...) to critique code/architecture and synthesizes a ranked verdict. |
| `create-issue` | Drafts a tight, single-focus issue or bug report. |
| `create-pr` | Picks a conventional-commit title, writes a What/Why/Testing description, self-reviews the diff before pushing. |

Add your own under `~/.poisson/skills/<name>/SKILL.md` — a user skill with the
same name as a built-in one overrides it, so you can customize any of the
eight without touching the binary. `/reload` rediscovers user skills without
restarting.

---

## 🚀 Install & run

Requires **Go 1.25+**. Everything else is vendored by the module graph.

```bash
go install github.com/mq37/poisson/cmd/px@latest   # -> $(go env GOPATH)/bin/px
```

Make sure `$(go env GOPATH)/bin` is on your `PATH`. Building from a local
clone instead (e.g. to hack on it) still works: `./build.sh` (compiles
`CGO_ENABLED=0` -> `./px`).

Authenticate a provider (stored in `~/.poisson/auth.json`, mode `0600`):

```bash
px login anthropic  # Claude Pro/Max — browser OAuth (subscription billing)
px login openai     # ChatGPT Plus/Pro — browser OAuth (Codex subscription)
px login xai        # SuperGrok — browser OAuth
px login ollama     # local Ollama at http://localhost:11434 (no auth needed)
```

Then just:

```bash
px                  # interactive TUI
px sessions         # list past sessions
px cost             # total spend
px version
```

---

## 🤖 Providers & models

| Provider | Model | Auth | Vision |
|---|---|---|---|
| `anthropic` | `claude-opus-5` | OAuth (Pro/Max, stealth) or `api_key` | ✅ |
| `openai` | `gpt-5.5` | OAuth (ChatGPT Plus/Pro, Codex) | ✅ |
| `openai` | `gpt-5.6-sol` | OAuth (ChatGPT Plus/Pro, Codex) | ✅ |
| `openai` | `gpt-5.6-terra` | OAuth (ChatGPT Plus/Pro, Codex) | ✅ |
| `openai` | `gpt-5.6-luna` | OAuth (ChatGPT Plus/Pro, Codex) | ✅ |
| `xai` | `grok-build` | OAuth (SuperGrok) | ✅ |
| `xai` | `grok-4.5` | OAuth (SuperGrok) | ✅ |
| `ollama` | `glm-5.2:cloud` *(default)* | local daemon / Ollama cloud | ❌ |
| `ollama` | `minimax-m3:cloud` | local daemon / Ollama cloud | ✅ |
| `ollama` | `kimi-k2.7-code:cloud` | local daemon / Ollama cloud | ✅ |

Provider notes: OpenAI/Codex ([details](docs/openai.md)) and Ollama caching /
`keep_alive` ([details](docs/ollama.md)).

Switch anytime: `/model`, `/effort`, `/providers` (or the Ctrl+P palette).
Reasoning effort levels: `low · medium · high · xhigh · max` (default `medium`;
each model advertises which it supports).

---

## ⚙️ Configuration

Everything lives in **`~/.poisson/`** (created on first run, mode `0700`):

| File | What |
|---|---|
| `~/.poisson/config.toml` | all settings (created with commented defaults on first run) |
| `~/.poisson/auth.json` | OAuth tokens / API keys (mode `0600`) |
| `~/.poisson/poisson.db` | SQLite: sessions, messages, tool/API calls, FTS index |

`config.toml` is written for you with every option commented out — uncomment to
override. The knobs (each `[<provider>].model` sets that provider's default
model — the one used when it's the active provider, or when you switch to it
via `/providers` without naming a model):

```toml
# Default model as "<provider>/<model>" — sets the provider and its model in one
# line (a bare "<model>" applies to the default provider). Overrides the
# per-provider settings below.
# model = "anthropic/claude-sonnet-5"

# Reasoning effort for every request (low | medium | high | xhigh | max)
# effort = "medium"

[provider]
# default = "ollama"                 # anthropic | ollama | xai | openai

[anthropic]
# model = "claude-opus-5"          # or claude-sonnet-5 (both adaptive-reasoning)
# api_key = "sk-ant-..."             # optional; OAuth (auth.json) preferred

[openai]
# model = "gpt-5.5"                 # via ChatGPT Codex subscription (px login openai)

[xai]
# model = "grok-build"          # via SuperGrok subscription (px login xai)

[ollama]
# base_url = "http://localhost:11434"
# model = "glm-5.2:cloud"

[compaction]
# threshold = 0.85                   # compact when context passes this fraction
# reserve_tokens = 16384             # ...or this much headroom is left, whichever is hit first
# model = ""                         # summarizer model or provider/model (default: session model)

[tui]
# theme = "dark"                     # dark | light
# show_tokens = true                 # context % in the status bar
# show_cost = true                   # $ in the status bar

# Pricing per 1M tokens (USD). Subscription/OAuth providers default to 0.
# [pricing.anthropic.claude-opus-5]
# input = 5.0
# output = 25.0
# cache_read = 0.5
# cache_write = 10.0

# Model metadata: context window, effort levels, vision, adaptive thinking.
# Teaches Poisson about a model it doesn't ship a built-in entry for (still
# works without this — just a generic fallback context window and no
# effort/vision support), or overrides one that's built in. Every field
# optional; omitted ones keep the built-in default. Shows up in /model too.
# [models.anthropic."claude-opus-4-9"]
# context_window = 1000000
# effort_levels = ["low", "medium", "high", "xhigh", "max"]
# vision = true
# adaptive_thinking = true
```

### Adding a custom / unlisted model

Any `model = "<provider>/<model-name>"` works right away, even if poisson has
no built-in entry for it — you just get a generic fallback context window and
no effort levels/vision/adaptive-thinking. Two optional `config.toml` blocks
fill that in:

```toml
# 1. Teach poisson the model's real capabilities (shows up in /model, and
#    gates which reasoning-effort levels the picker offers).
[models.ollama."glm-5.2:cloud"]
context_window = 200000
effort_levels = ["low", "medium", "high"]
vision = true

# 2. Teach it what the model costs, so /cost and the status bar aren't
#    silently $0. Ollama already defaults every model to $0 (built-in
#    wildcard, since most are local); Anthropic/OpenAI/xAI have no such
#    wildcard, so an unlisted model on those three shows $0 cost until you
#    add its rates here.
[pricing.ollama."glm-5.2:cloud"]
input = 0.5
output = 2.0
```

Both blocks key on the exact model name (quoted, since it usually contains
`.`/`:`), under `[models.<provider>."<model>"]` / `[pricing.<provider>."<model>"]`.
Pricing (only) also matches by prefix if the key ends in `*` — e.g.
`[pricing.ollama."*"]` is the built-in fallback that prices every unlisted
Ollama model at $0; model metadata overrides always need the exact name.

---

## ⌨️ Keys & commands

Bottom-bar keys (input focus):

```
Enter send · Ctrl+V image · Ctrl+F find · Ctrl+P palette
Ctrl+L effort · Ctrl+T fold thinking · Ctrl+E expand tool
Shift+Tab fast/paranoid approval mode
Esc cancel running turn · Ctrl+C clear input (twice to exit)
```

Click-drag over the conversation selects text — no Shift key needed — and
auto-scrolls when you drag past the top/bottom edge. **Ctrl+Y** copies the
selection to the system clipboard (via OSC 52, works over SSH). Plain
Ctrl+<letter> is used instead of Ctrl+Shift+C because most terminals
(including kitty's default `kitty_mod+c`) already bind Ctrl+Shift+C to their
own native copy action and never forward it to the app.

Slash commands: `/help` `/status` `/model` `/effort` `/providers` `/sessions`
`/resume` `/search` `/new` `/clear` `/name` `/compact` `/cost` `/reload`
`/btw` `/openai-reset-usage` `/quit`. Type `@` to fuzzy-attach a file (or
`@image.png` for an image).

---

## 📦 Dependencies — deliberately tiny

poisson has **3 direct dependencies**. Everything else you see below is a
*transitive* dependency pulled in **only** by those three — never imported by
our code. The rest of the agent is the Go standard library: hand-rolled ANSI
TUI, `net/http` for providers, `encoding/json`, and a hand-written TOML parser.

### Direct (3)

| Dependency | Why it's here |
|---|---|
| **`modernc.org/sqlite`** | Pure-Go SQLite — durable local storage for sessions, messages, the cost ledger, and **FTS5** full-text search. Being pure Go is the whole point: it keeps the build **CGo-free**, so `go build` produces a single static binary with no C toolchain and no external database. |
| **`golang.org/x/term`** | Correct raw-mode terminal handling for the REPL/TUI across platforms. The standard library has no portable equivalent. |
| **`golang.org/x/image`** | Quality image downscaling (CatmullRom resampling) + WebP decoding for pasted/attached images. Stdlib decodes PNG/JPEG/GIF but ships no resampler and no WebP decoder. |

### Transitive (pulled in by the three above, not by us)

| Dependency | Comes from |
|---|---|
| `modernc.org/libc`, `modernc.org/memory`, `modernc.org/mathutil` | `modernc.org/sqlite` (its pure-Go C runtime) |
| `github.com/remyoudompheng/bigfft`, `github.com/ncruces/go-strftime` | `modernc.org/sqlite` |
| `github.com/google/uuid`, `github.com/mattn/go-isatty`, `github.com/dustin/go-humanize` | `modernc.org/sqlite` |
| `golang.org/x/sys` | `golang.org/x/term` |

**What we deliberately *don't* depend on:** no TUI framework, no web framework,
no HTTP client library, no JSON library, no CLI/flags library, no TOML library,
no ORM. Fewer moving parts, faster builds, a smaller attack surface, and a
codebase one person can hold in their head.

> New dependencies are only added when the standard library genuinely can't do
> the job, and are pinned to a release **≥ 14 days old** to dodge fresh-release
> bugs and supply-chain surprises.

---

## 🧭 Design

- **Single static binary** — `CGO_ENABLED=0`, one file, copy it anywhere.
- **Local-first & private** — your data lives in `~/.poisson/poisson.db`. No
  telemetry, no analytics, no phone-home.
- **Suckless-ish** — simplicity over features, delete-before-add, readable code.
- **Tested without the network** — the suite (750+ tests) mocks every provider;
  it never makes a real API call, and runs clean under `-race`.

---

<p align="center"><sub>poisson · run <code>px</code> · <code>/help</code> for the tour</sub></p>
� <code>/help</code> for the tour</sub></p>
