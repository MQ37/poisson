<p align="center">
  <img src="assets/logo.jpg" alt="poisson — a coding agent that lives in your terminal" width="600">
</p>

<p align="center">
  <i>Embrace the entropy, probabilities favor the bold.</i>
</p>

<p align="center">
  <img alt="Go 1.25" src="https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white">
  <img alt="single binary" src="https://img.shields.io/badge/build-single%20static%20binary%20(no%20CGO)-238636">
  <img alt="dependencies" src="https://img.shields.io/badge/direct%20deps-3-3fb950">
  <img alt="storage" src="https://img.shields.io/badge/storage-SQLite%20%2B%20FTS5-orange">
</p>

---

**poisson** is a small, fast coding agent you run in your terminal. It streams
a real conversation, calls tools (bash, file read/write/edit, subagents),
tracks every token and dollar, and keeps your whole history in a local SQLite
database you own — just one static binary and your terminal.

It talks to **Anthropic** (Claude), **OpenAI** (ChatGPT subscription),
**xAI** (Grok, SuperGrok), **Ollama** (local + cloud), and **llama.cpp**
(local `llama-server`) — paste images, search past sessions full-text,
compact context, approve risky shell commands from a popup.

```bash
go install github.com/mq37/poisson/cmd/px@latest   # needs Go 1.25+; installs to $(go env GOPATH)/bin
px login anthropic                                  # or: px login openai | xai — skip for Ollama, no auth needed
px                                                   # launch the TUI
```

> ⚠️ **Experimental.** poisson is under active, fast-moving development — commands, flags, and behavior can change without notice between versions.

---

## ✨ Features

- **Streaming REPL TUI** — hand-rolled ANSI, no framework. Foldable thinking
  blocks, expandable tool cards with colored diffs, command palette
  (Ctrl+P), mouse scroll, status bar with live context % and cost.
- **Real tools, not just bash** — `read`/`write`/`edit`/`grep`/`glob` for file
  work, `batch` for independent calls in one step, plus `web_search`,
  `web_ask`, `fetch` (selectable backends, [details](docs/web-tools.md)),
  `recall` (cross-session full-text search), subagents,
  and 13 built-in skills ([below](#-built-in-skills)), user-extendable via
  `~/.poisson/skills/`. `bash` is stateless — pass `workdir` explicitly on
  any call that needs a directory other than session cwd.
- **Bash safety guard, two speeds** — Fast mode (default): a deterministic
  guard auto-approves read-only commands with zero LLM calls; anything else
  is LLM risk-classified — low auto-approves too, medium/high/unknown asks
  you. Paranoid mode (Shift+Tab) asks for every command. Installs,
  destructive ops, and `npx`/`dlx`-style commands are always high risk and
  never auto-approve.
- **Podman sandboxes** — `create_sandbox` gives an isolated, named container
  (passwordless sudo, matching-uid mount); `bash` calls that pass its
  `sandboxId` then run with **no approval gate at all** — the container is
  the safety boundary, not the prompt. `sandbox_cp`/`sandbox_destroy`/
  `list_sandboxes` manage it from any session, even after a crash; `/sandbox
  ls`/`kill <id>` are the TUI equivalent. Requires `podman` on `PATH`.
  ([details](docs/sandbox-plan.md))
- **Sessions in SQLite** — every message/tool/API call persisted, full-text
  search (FTS5), resume any session, auto-compaction when context fills up.
- **Exact cost & tokens**, live in the status bar and `/cost`, plus live
  usage-limit tracking for Anthropic/OpenAI subscription accounts.
- **Image input** — paste (Ctrl+V) or `@screenshot.png`. ([details](docs/images.md))
- **`<render>` file citations** — the agent cites `<render file="path"
  from="10" to="50"/>` instead of retyping a snippet, expanded into a
  full-width widget at zero output-token cost; `ref="<commit-or-branch>"`
  cites the file as of a git ref instead of the working tree.
- **Message queueing** — type while the agent works; sent at the next turn
  boundary instead of waiting for the whole turn to finish.

---

## 🧰 Built-in skills

Thirteen skills ship baked into the `px` binary — no setup, no config directory
needed. The `skill` tool loads one by name and works the same for subagents
as it does in the main session.

| Skill | What |
|---|---|
| `code-quality` | Suckless-style simplicity/clarity principles — bloat, over-abstraction, needless defensiveness. Use before writing or reviewing code. |
| `code-review` | Full multi-lens review of a diff (correctness, security, API design, tests) with subagent-verified findings and apply/escalate/skip fixes. |
| `tdd` | Red-green-refactor discipline — failing test before production code, minimum code to pass, refactor under green. |
| `feature-impact` | Blast-radius inventory for a change that adds a path beside an existing one — enumerates every mode, flag, and consumer on the shared seam and classifies each as handled, rejected, or unknown. |
| `review-pr` | Gathers a PR/branch diff (local, GitHub, or fresh checkout) for review; hands off to `code-review` + `stacked-diff-review`. |
| `stacked-diff-review` | Risk-tiered (🔴/🟡/🟢) write-up format for presenting a review. |
| `check-work` | Spawns a fresh-context subagent to independently verify finished work actually satisfies the original request — PASS/FAIL verdict. |
| `council` | Convenes parallel subagent personas (Torvalds, Hotz, Davis, ...) to critique code/architecture and synthesizes a ranked verdict. |
| `grilling` | Relentless one-question-at-a-time interview mapping a plan as a design tree, to stress-test it before acting. |
| `create-issue` | Drafts a tight, single-focus issue or bug report. |
| `create-pr` | Picks a conventional-commit title, writes a What/Why/Testing description, self-reviews the diff before pushing. |
| `sandbox` | Sets up an isolated podman sandbox for a work session — right directory mounted from the start, build/test through the container, commit only from host. |
| `create-skill` | Authors a new SKILL.md, builtin (Go, embedded) or user-scoped (`~/.poisson/skills/`). |

Add your own under `~/.poisson/skills/<name>/SKILL.md` — a user skill with the
same name as a built-in one overrides it, so you can customize any of the
thirteen without touching the binary. `/reload` rediscovers user skills without
restarting.

---

## 🚀 Install & run

Requires **Go 1.25+**. Everything else is vendored by the module graph. The
quickstart's `go install` above puts `px` at `$(go env GOPATH)/bin/px` — make
sure that's on your `PATH`. Building from a local clone instead (e.g. to hack
on it) still works: `./build.sh` (compiles `CGO_ENABLED=0` -> `./px`).

Authenticate a provider (stored in `~/.poisson/auth.json`, mode `0600`):

```bash
px login anthropic  # Claude Pro/Max — browser OAuth (subscription billing)
px login openai     # ChatGPT Plus/Pro — browser OAuth (Codex subscription)
px login xai        # SuperGrok — browser OAuth
px login ollama     # local Ollama at http://localhost:11434 (no auth needed)
# llamacpp: local llama-server at http://localhost:11212, no auth needed either
```

Then just:

```bash
px                  # interactive TUI
px sessions         # list past sessions
px resume <id>      # open the TUI resumed straight into a past session
px cost             # total spend
px version

# Headless one-shot (no TUI). Risky bash is denied unless --yolo.
# --yolo is for *you* in a real shell — the agent cannot nest it via the bash tool.
px -p "summarize this repo"
px -p --yolo "run the test suite and fix failures"
```

---

## 🤖 Providers & models

| Provider | Model | Auth | Vision |
|---|---|---|---|
| `anthropic` | `claude-opus-5` *(default)* | OAuth (Pro/Max, stealth) or `api_key` | ✅ |
| `anthropic` | `claude-sonnet-5` | OAuth (Pro/Max, stealth) or `api_key` | ✅ |
| `openai` | `gpt-5.6-terra` *(default)* — balanced | OAuth (ChatGPT Plus/Pro, Codex) | ✅ |
| `openai` | `gpt-5.6-sol` — frontier | OAuth (ChatGPT Plus/Pro, Codex) | ✅ |
| `openai` | `gpt-5.6-luna` — cost-optimized | OAuth (ChatGPT Plus/Pro, Codex) | ✅ |
| `openai` | `gpt-5.5` — previous generation | OAuth (ChatGPT Plus/Pro, Codex) | ✅ |
| `xai` | `grok-build` *(default)* | OAuth (SuperGrok) | ✅ |
| `xai` | `grok-4.5` | OAuth (SuperGrok) | ✅ |
| `ollama` | `glm-5.2:cloud` *(default)* | local daemon / Ollama cloud | ❌ |
| `ollama` | `minimax-m3:cloud` | local daemon / Ollama cloud | ✅ |
| `ollama` | `kimi-k2.7-code:cloud` | local daemon / Ollama cloud | ✅ |
| `llamacpp` | `unsloth/Laguna-S-2.1-GGUF` *(default)* | local `llama-server`, no auth | ❌ |
| `llamacpp` | `unsloth/Qwen3.6-27B-MTP-GGUF` | local `llama-server`, no auth | ✅ |
| `llamacpp` | `poolside/Laguna-XS-2.1-GGUF` | local `llama-server`, no auth | ❌ |

Provider notes: OpenAI/Codex ([details](docs/openai.md)) and Ollama caching /
`keep_alive` ([details](docs/ollama.md)). `llamacpp` talks to the same
OpenAI-compatible `/v1/chat/completions` endpoint as Ollama — point it at any
local `llama-server` instance (default port `11212`). To discover cached GGUF
models and launch `llama-server`/`llama-cli` with sane defaults (GPU offload,
context size, MTP speculative decoding), use
[**alpaca**](https://github.com/MQ37/alpaca) — a small standalone session
launcher built for this.

**Custom provider instances**: `[custom_providers.<name>]` defines a second
(third, ...) Ollama-compatible endpoint under a name you pick — e.g. a
daemon on a remote host, alongside your local one. Works everywhere a
built-in provider does: `/providers`, `/model`, `px -p <name>/<model>`,
subagent provider pinning. No login needed (same as `ollama`/`llamacpp`).
See the `[custom_providers.*]` block in the shipped `config.toml` template
for the full example.

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
# default = "ollama"                 # anthropic | ollama | xai | openai | llamacpp

[anthropic]
# model = "claude-opus-5"          # or claude-sonnet-5 (both adaptive-reasoning)
# classifier = "claude-sonnet-5"    # bash-risk classifier for this provider
# api_key = "sk-ant-..."             # optional; OAuth (auth.json) preferred

[openai]
# model = "gpt-5.6-terra"           # via ChatGPT Codex subscription (px login openai)
                                    # or gpt-5.6-sol / gpt-5.6-luna / gpt-5.5

[xai]
# model = "grok-build"          # via SuperGrok subscription (px login xai)

[ollama]
# base_url = "http://localhost:11434"
# model = "glm-5.2:cloud"

[llamacpp]
# base_url = "http://localhost:11212"     # local llama-server instance
# model = "unsloth/Laguna-S-2.1-GGUF"

# [custom_providers.bastion]              # a second Ollama instance, any name
# type = "ollama"                         # only "ollama" supported
# base_url = "http://bastion-host:11434"
# model = "laguna-s-2.1:q4_K_M"

[classifier]
# model = ""                         # fallback bash-risk classifier (bare = all providers, "provider/model" = one)

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
Ctrl+M model picker · Ctrl+S session picker · Ctrl+B /btw prompt
Ctrl+R/Ctrl+N step input history · Ctrl+G finish subagents now
Shift+Tab fast/paranoid approval mode
Esc cancel running turn · Ctrl+C clear input (twice to exit)
```

Click-drag selects text (auto-scrolls past the edge); **Ctrl+Y** copies to
the system clipboard via OSC 52 (works over SSH) — plain Ctrl+<letter>
because most terminals already claim Ctrl+Shift+C for their own copy action.

Slash commands: `/help` `/status` `/model` `/effort` `/classifier-model`
`/providers` `/sessions` `/resume` `/search` `/new` `/clear` `/name`
`/compact` `/cost` `/reload` `/sandbox` `/btw` `/openai-reset-usage` `/quit`.
Type `@` to fuzzy-attach a file (or `@image.png` for an image).

`/classifier-model` picks which model rates bash-command risk for the
approval gate, for the currently selected provider — usually worth pointing
at something small and fast, since the answer is one word, and an expensive
session model otherwise pays its own rate once per gated command. It never
touches the model running the conversation, works mid-turn, and lasts for the
session. To make it permanent, set it next to that provider's own model in
`config.toml`:

```toml
[anthropic]
model = "claude-opus-5"
classifier = "claude-sonnet-5"

[xai]
classifier = "grok-build-0.1"
```

`[classifier] model` remains the fallback for providers that declare no
classifier of their own (bare = all of them, `"provider/model"` = that one
only). Resolution order: `/classifier-model` pin → `[<provider>] classifier`
→ `[classifier] model` → the session's own model.

---

## 📦 Dependencies — deliberately tiny

poisson has **3 direct dependencies**; everything else below is *transitive*,
pulled in only by those three. The rest is stdlib: hand-rolled ANSI TUI,
`net/http` for providers, `encoding/json`, a hand-written TOML parser.

### Direct (3)

| Dependency | Why it's here |
|---|---|
| **`modernc.org/sqlite`** | Pure-Go SQLite — durable local storage for sessions, messages, the cost ledger, and **FTS5** full-text search. Being pure Go is the whole point: it keeps the build **CGo-free**, so `go build` produces a single static binary with no C toolchain and no external database. |
| **`golang.org/x/term`** | Correct raw-mode terminal handling for the REPL/TUI across platforms. The standard library has no portable equivalent. |
| **`golang.org/x/image`** | Quality image downscaling (CatmullRom resampling) + WebP decoding for pasted/attached images. Stdlib decodes PNG/JPEG/GIF but ships no resampler and no WebP decoder. |

### Transitive

Everything else in `go.sum` is pulled in by those three, not by us — mostly
`modernc.org/sqlite`'s pure-Go C runtime shims. See `go.mod` for the full list.

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
- **Tested without the network** — the suite (1,200+ tests) mocks every
  provider; it never makes a real API call.

---

<p align="center"><sub>poisson · run <code>px</code> · <code>/help</code> for the tour</sub></p>
