<p align="center">
  <img src="assets/logo.jpg" alt="poisson — a coding agent that lives in your terminal" width="294">
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
**xAI** (Grok, SuperGrok), **OpenRouter** (400+ models, API key), **Ollama**
(local + cloud), and **llama.cpp** (local `llama-server`) — paste images,
search past sessions full-text, compact context, approve risky shell
commands from a popup.

```bash
go install github.com/mq37/poisson/cmd/px@latest   # needs Go 1.25+; installs to $(go env GOPATH)/bin
px login anthropic                                  # or: px login openai | xai — skip for Ollama, no auth needed
px                                                   # launch the TUI
```

> ⚠️ **Experimental.** poisson is under active, fast-moving development — commands, flags, and behavior can change without notice between versions.

---

## ✨ Features

- **Streaming REPL TUI** — hand-rolled ANSI, no framework. Thinking blocks,
  tool cards with diffs, command palette (Ctrl+P), status bar with context %
  and cost.
- **Real tools, not just bash** — `read`/`write`/`edit`/`grep`/`glob`,
  `batch`, `web_search`/`web_ask`/`fetch` ([details](docs/web-tools.md)),
  `recall`, `set_title`, subagents, and 13 built-in skills
  ([below](#-built-in-skills), user-extendable). `bash` is stateless — pass
  `workdir` explicitly.
- **Recovers leaked `<invoke>` tool calls** — a weak/local model sometimes
  echoes other harnesses' `<invoke>`/`<parameter>` XML as plain text instead
  of a real call (never poisson's own format). Parsed back into a real,
  dispatchable call when it resolves cleanly; left as-is otherwise.
- **Bash safety guard, three speeds** — Fast mode (default): deterministic
  auto-approve for read-only commands, LLM risk-classifies the rest (low
  auto-approves, medium/high/unknown asks you). The classifier also
  auto-denies — no human asked — a command whose own output would leak a
  secret to stdout (`echo $AWS_SECRET_KEY`, an unredirected `doppler
  secrets download`, ...); switch to Paranoid mode to review and approve
  one yourself if that's a false positive. Paranoid mode asks for
  everything; installs/destructive/`npx`-style always ask. Shift+Tab cycles
  Fast → Paranoid → **Yolo** — every command runs immediately, no approval
  of any kind, any risk. Opt-in only, never the default; use on a box/
  sandbox you're fine handing full unattended shell access to.
- **Secret redaction** — tool output is scanned for secret-shaped text
  (vendor tokens, PEM keys, JWTs, credential `KEY=VALUE` pairs) and masked
  with `[REDACTED]` before reaching the model, TUI, or session store.
  Best-effort, not a guarantee.
- **Podman sandboxes** — `create_sandbox` gives an isolated container;
  `bash` calls passing its `sandboxId` skip the approval gate entirely — the
  container is the boundary. Managed from any session via `sandbox_cp`/
  `sandbox_destroy`/`list_sandboxes` or `/sandbox ls`/`kill`. Requires
  `podman`. ([details](docs/sandbox-plan.md))
- **Sessions in SQLite** — every message/tool/API call persisted, FTS5
  full-text search, resume any session, auto-compaction when context fills up.
- **Exact cost & tokens**, live in the status bar and `/cost`, plus live
  usage-limit tracking for Anthropic/OpenAI subscription accounts.
- **Image input** — paste (Ctrl+V) or `@screenshot.png`. ([details](docs/images.md))
- **`<render>` file citations** — cites a snippet (`<render file="path"
  from="10" to="50"/>`) instead of retyping it, zero output-token cost;
  `ref="<commit-or-branch>"` cites a git ref instead of the working tree.
  A citation that fails to resolve (bad path/ref) gets a couple of automatic,
  visibly-marked retries in the same turn before the answer is considered done.
- **Message queueing** — type while the agent works; sent at the next turn
  boundary instead of waiting for the whole turn to finish.

---

## 🧰 Built-in skills

Thirteen skills ship baked into the `px` binary — no setup, no config directory
needed. The `skill` tool loads one by name (each is a `SKILL.md`) and works the
same for subagents as it does in the main session: `code-quality`,
`code-review`, `tdd`, `feature-impact`, `review-pr`, `stacked-diff-review`,
`check-work`, `council`, `grilling`, `create-issue`, `create-pr`, `sandbox`,
`create-skill` — covering code review, TDD discipline, blast-radius impact
analysis, independent self-verification, multi-persona critique, and issue/PR
drafting.

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
px login anthropic   # Claude Pro/Max — browser OAuth (subscription billing)
px login openai      # ChatGPT Plus/Pro — browser OAuth (Codex subscription)
px login xai         # SuperGrok — browser OAuth
px login openrouter  # plain API key from https://openrouter.ai/keys
px login ollama      # local Ollama at http://localhost:11434 (no auth needed)
# llamacpp: local llama-server at http://localhost:11212, no auth needed either

# anthropic/openai on a headless/SSH host: add --manual to paste the auth
# code instead of waiting on a local browser callback that can't be reached.
px login anthropic --manual
```

Then just:

```bash
px                  # interactive TUI
px sessions         # list past sessions
px resume <id>      # open the TUI resumed straight into a past session
px cost             # total spend
px version

# Headless one-shot (no TUI). Risky bash is denied unless --yolo (for *you*
# in a real shell only — the agent cannot nest it via the bash tool).
px -p "summarize this repo"
px -p --yolo "run the test suite and fix failures"
```

---

## 🤖 Providers & models

**Anthropic** (Claude, OAuth or `api_key`), **OpenAI** (ChatGPT/Codex
subscription, OAuth), **xAI** (Grok, SuperGrok OAuth), **OpenRouter** (400+
models, API key), **Ollama** (local daemon or cloud, no auth), and
**llama.cpp** (local `llama-server`, no auth). Each ships a curated default
and tracks that provider's latest model lineup — any `<provider>/<model-name>`
works even before poisson has a built-in entry for it (see
[Adding a custom / unlisted model](#adding-a-custom--unlisted-model)).
`llamacpp`/Ollama share the same wire format; [**alpaca**](https://github.com/MQ37/alpaca)
discovers cached GGUF models and launches `llama-server` with sane defaults.

**Custom provider instances**: `[custom_providers.<name>]` defines a second
(third, ...) Ollama-compatible endpoint under a name you pick — e.g. a
daemon on a remote host, alongside your local one:

```toml
[custom_providers.bastion]              # a second Ollama instance, any name
type = "ollama"                         # only "ollama" supported today
base_url = "http://bastion-host:11434"
model = "laguna-s-2.1:q4_K_M"           # optional — omit to discover live via /api/tags
```

Works everywhere a built-in provider does: `/providers`, `/model`,
`px -p bastion/<model>`, subagent provider pinning. No login needed (same as
`ollama`/`llamacpp`). Curate a model with the same `[models.<name>."<model>"]`
table a built-in provider uses (context window, effort levels, vision).

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
# default = "ollama"                 # anthropic | ollama | xai | openai | openrouter | llamacpp

[anthropic]
# model = "claude-opus-5"          # or claude-sonnet-5 / claude-fable-5 (all adaptive-reasoning)
# classifier = "claude-sonnet-5"    # bash-risk classifier for this provider
# api_key = "sk-ant-..."             # optional; OAuth (auth.json) preferred

[openai]
# model = "gpt-5.6-terra"           # via ChatGPT Codex subscription (px login openai)
                                    # or gpt-5.6-sol / gpt-5.6-luna / gpt-5.5

[xai]
# model = "grok-build"          # via SuperGrok subscription (px login xai)

[openrouter]
# model = "deepseek/deepseek-v4-flash-0731"  # via API key (px login openrouter)
# api_key = "sk-or-..."                       # optional; prompted by px login openrouter
# base_url = "https://openrouter.ai/api/v1"

[ollama]
# base_url = "http://localhost:11434"
# model = "glm-5.2:cloud"

[llamacpp]
# base_url = "http://localhost:11212"     # local llama-server instance
# model = "unsloth/Laguna-S-2.1-GGUF"

# [custom_providers.bastion]              # a second Ollama instance, any name
# type = "ollama"                         # only "ollama" supported
# base_url = "http://bastion-host:11434"
# model = "laguna-s-2.1:q4_K_M"           # optional — omit to discover live via /api/tags
#
# [models.bastion."laguna-s-2.1:q4_K_M"]  # same [models.*] schema as any built-in provider
# context_window = 262144

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

# Pricing (per 1M tokens) and model metadata (context window, effort levels,
# vision) overrides — see "Adding a custom / unlisted model" below for the
# full [pricing.*] / [models.*] example.
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
Enter send · Tab switch input/conversation focus · Ctrl+V image
Ctrl+F find · Ctrl+P palette · Ctrl+L effort · Ctrl+T fold thinking
Ctrl+E expand tool · Ctrl+M model picker · Ctrl+S session picker
Ctrl+B /btw prompt · Ctrl+R/Ctrl+N step input history
Ctrl+G finish subagents now · Shift+Tab cycle fast/paranoid/yolo approval mode
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
approval gate — usually worth pointing at something small and fast, since the
answer is one word and an expensive session model otherwise pays its own rate
per gated command. Mid-turn only unless made permanent via that provider's own
`classifier = "..."` in `config.toml` (see the `[anthropic]` block above).
Resolution order: `/classifier-model` pin → `[<provider>] classifier` →
`[classifier] model` → the session's own model.

---

## 📦 Dependencies — deliberately tiny

poisson has **3 direct dependencies** (`modernc.org/sqlite`, `golang.org/x/term`,
`golang.org/x/image`); everything else is stdlib.

---

## 🧭 Design

- **Single static binary** — `CGO_ENABLED=0`, one file, copy it anywhere.
- **Local-first & private** — your data lives in `~/.poisson/poisson.db`. No
  telemetry, no analytics, no phone-home.
- **Suckless-ish** — simplicity over features, delete-before-add, readable code.
- **Tested without the network** — the suite mocks every provider; it never
  makes a real API call.

---

<p align="center"><sub>poisson · run <code>px</code> · <code>/help</code> for the tour</sub></p>
