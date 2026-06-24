# Poisson — Coding CLI Agent in Go

A minimal, single static binary coding-CLI agent written in Go with zero
dependencies except `modernc.org/sqlite` (pure-Go, cgo-free). It bakes in —
as first-class native features — the three mechanisms currently bolted onto
pi via extensions:

- **Anthropic stealth OAuth** (Pro/Max subscription billing, not pay-as-you-go)
- **Bash guard** (safe-command auto-allow, everything else prompts)
- **Subagents** (one-shot child `px` processes)

Plus: Ollama and xAI Grok providers, skills, sessions in SQLite with FTS5
search, fork/undo, auto-compaction with live context %, and a streaming
readline REPL.

---

## 1. Repository Layout

```
poisson/
  main.go                        # package main — go install works
  go.mod
  internal/
    config/        config.go      # ~/.poisson/config.toml loader (minimal TOML)
    auth/          auth.go       # token store + OAuth providers (anthropic, xai)
    provider/
      provider.go               # Provider interface
      anthropic.go              # Anthropic Messages API (API key or OAuth stealth)
      anthropic_stealth.go      # cch billing header + system prompt sanitizer + CC identity
      ollama.go                 # Ollama native /api/chat
      xai.go                    # xAI Grok (OAuth SuperGrok)
    guard/
      guard.go                  # bash safe-list classifier (port of bash-guard)
      safe_list.go              # SAFE, DANGEROUS_TOKENS, SENSITIVE paths
      detectors.go              # per-command danger detectors
      segments.go               # command segment splitter
    store/
      store.go                  # sqlite open, migrations, schema (WAL mode)
      session.go                # session CRUD, fork, undo, soft delete
      message.go                # message CRUD, FTS5 indexing
      api_calls.go              # per-API-call usage + cost storage
      search.go                 # full-text search (filters soft-deleted)
      pricing.go                # model_pricing CRUD + cost computation
    agent/
      agent.go                  # turn loop, streaming, tool dispatch
      compaction.go             # auto-compaction (mid-turn capable)
      tokens.go                 # context window tracking, % display
    tools/
      registry.go               # tool registry
      bash.go                   # guarded bash execution
      read.go                   # read file (text + images)
      write.go                  # write file
      edit.go                   # exact-text-replacement edit
      search.go                 # ripgrep wrapper
      ls.go                     # directory listing
      glob.go                   # file pattern matching
      fetch.go                  # Ollama web_fetch API wrapper
      exa_search.go             # exa.ai search (token issue + search)
      subagent.go               # spawn child px process
      skill.go                  # skill invocation tool
    subagent/
      spawn.go                  # spawn child px, pipe-based approval forwarding
    skills/
      skills.go                 # SKILL.md discovery + <skill> injection
    project/
      discover.go               # AGENTS.md discovery (walk cwd→root)
      prompt.go                 # system prompt assembly
    tui/
      tui.go                    # raw-mode line editor, status bar, slash commands
      render.go                 # markdown-ish rendering, tool call display
  docs/
    SPEC.md
  AGENTS.md
  README.md
```

`main.go` is in the repo root so `go install <repo-url>@latest` produces a
`px` binary in `$GOPATH/bin`.

---

## 2. Dependencies

| Dependency | Purpose | Notes |
|---|---|---|
| `modernc.org/sqlite` | SQLite with FTS5 | pure Go, cgo-free, static binary. |
| `golang.org/x/term` | Raw terminal mode for REPL | quasi-stdlib (Go team maintained), cross-platform |
| Go stdlib | everything else | `net/http`, `crypto/sha256`, `encoding/json`, `os/exec`, `path/filepath`, etc. |

No other third-party modules. No TUI framework — the REPL uses `golang.org/x/term`
for raw mode and hand-rolled line editing with `bufio`.

---

## 3. Config

### 3.1 Config directory: `~/.poisson/`

```
~/.poisson/
  config.toml        # user config
  auth.json          # OAuth tokens (anthropic, xai) + API keys
  poisson.db             # SQLite database (sessions, messages, FTS5)
  AGENTS.md          # optional global project instructions
  skills/            # skills directory
    {skill-name}/
      SKILL.md
```

### 3.2 Config format: minimal TOML

A hand-rolled minimal TOML parser (~200 LoC) supporting:
- `#` line comments
- `[section]` headers
- `key = value` pairs: strings (`"..."`), integers, booleans, arrays (`[a, b, c]`)
- Nested tables via `[section.subsection]`

No inline tables, no multiline strings, no datetimes. The config surface is
small enough that this subset suffices.

### 3.3 config.toml schema

```toml
# Default provider + model
[provider]
default = "anthropic"          # anthropic | ollama | xai

[anthropic]
model = "claude-sonnet-4-20250514"
# If auth.json has OAuth tokens for anthropic, stealth mode is active.
# Otherwise set an API key here or in auth.json.
# api_key = "sk-ant-..."

[xai]
model = "grok-build-0.1"

[ollama]
base_url = "http://localhost:11434"
model = "qwen3-coder:30b"

[compaction]
threshold = 0.8                # fraction of context window (0.0–1.0)
model = ""                     # model for summarization (default: session model)
# The summarization prompt is hardcoded but the threshold and model are configurable.

[stealth]
# Anthropic Claude Code stealth constants. All configurable so users can
# update them when Anthropic rotates values without a Poisson release.
cc_version = "2.1.156"
cc_entrypoint = "sdk-cli"
cch_salt = "59cf53e54c78"
cch_positions = [4, 7, 20]     # character positions sampled from first user msg

[guard]
# Commands in addition to the built-in safe-list that auto-allow.
# extra_safe = ["make", "cargo build"]

[tui]
theme = "dark"                  # dark | light
show_tokens = true              # show context % in status bar
show_cost = true               # show $ cost in status bar

# Pricing per 1M tokens (USD). OAuth/subscription providers default to 0.
# Values here override the built-in defaults. Use nested tables, not inline tables.
# [pricing.anthropic.claude-sonnet-4-20250514]
# input = 3.0
# output = 15.0
# cache_read = 0.3
# cache_write = 3.75
# [pricing.xai.grok-build-0.1]
# input = 0
# output = 0
# [pricing.ollama.qwen3-coder:30b]
# input = 0
# output = 0
```

### 3.4 Built-in pricing defaults

If a model is not explicitly priced in `config.toml`, built-in defaults are
used. These are seeded into the `model_pricing` table on first run and can
be overridden via config.

| Provider | Model | Input/MTok | Output/MTok | Cache Read/MTok | Cache Write/MTok |
|---|---|---|---|---|---|
| anthropic | claude-sonnet-4-20250514 | 3.0 | 15.0 | 0.3 | 3.75 |
| anthropic | claude-opus-4-* | 15.0 | 75.0 | 1.5 | 18.75 |
| anthropic | claude-haiku-3.5-* | 0.8 | 4.0 | 0.08 | 1.0 |
| anthropic | (OAuth) any model | 0 | 0 | 0 | 0 |
| xai | grok-3-* | 5.0 | 15.0 | 0 | 0 |
| xai | (OAuth) any model | 0 | 0 | 0 | 0 |
| ollama | (any) | 0 | 0 | 0 | 0 |

When using OAuth (Anthropic Pro/Max, xAI SuperGrok), pricing is always 0 —
the cost is the subscription, not per-token. The status bar shows `$0.00` and
`/cost` shows "subscription (no per-token cost)".

---

## 4. Auth

### 4.1 auth.json format

```json
{
  "anthropic": {
    "type": "oauth",
    "access": "eyJ...",
    "refresh": "eyJ...",
    "expires": 1718000000000
  },
  "xai": {
    "type": "oauth",
    "access": "...",
    "refresh": "...",
    "expires": 1718000000000
  },
  "ollama": {
    "type": "none"
  }
}
```

Alternative for API key auth:
```json
{
  "anthropic": { "type": "api_key", "key": "sk-ant-..." }
}
```

### 4.2 Anthropic OAuth (Claude Pro/Max)

Ported from pi-mono `packages/ai/src/utils/oauth/anthropic.ts`:

- **PKCE**: 32 random bytes → base64url verifier; SHA-256 → base64url challenge.
- **Loopback callback server**: `127.0.0.1` on an OS-assigned port (bind to port 0).
  The actual port is used in the `redirect_uri` parameter. Falls back to the
  default port 53692 if needed for compat, but prefers OS-assigned to avoid
  conflicts when multiple Poisson instances run.
- **Authorize URL**: `https://claude.ai/oauth/authorize`
  - Params: `code=true`, `client_id=9d1c250a-...`, `response_type=code`,
    `redirect_uri=http://localhost:{actual_port}/callback`,
    `scope=org:create_api_key user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload`,
    `code_challenge`, `code_challenge_method=S256`, `state=<verifier>`.
- **Token URL**: `https://platform.claude.com/v1/oauth/token`
  - Exchange: `grant_type=authorization_code`, `client_id`, `code`, `state`,
    `redirect_uri`, `code_verifier`.
  - Refresh: `grant_type=refresh_token`, `client_id`, `refresh_token`.
  - Expiry: `now + expires_in*1000 - 5min` (5-min skew).
- **Client ID** (base64-decoded): `9d1c250a-e61b-44d9-88ed-5944d1962f5e`.

**`px login anthropic`** opens browser to the authorize URL, starts the
callback server, exchanges the code, writes tokens to `~/.poisson/auth.json`.

**Token refresh**: before each request, if `expires` is within 5 minutes,
refresh automatically.

### 4.3 xAI OAuth (SuperGrok)

Ported from pi-mono `xai-grok-oauth/xai-oauth.ts`:

- **Client ID**: `b1a00492-073a-47ea-816f-4c329264a828`
- **Authorize URL**: `https://auth.x.ai/oauth2/authorize`
- **Token URL**: `https://auth.x.ai/oauth2/token`
- **Device code URL**: `https://auth.x.ai/oauth2/device/code`
- **Scope**: `openid profile email offline_access grok-cli:access api:access`
- **Loopback callback**: `127.0.0.1` on an OS-assigned port (bind to port 0).
  Falls back to default port 56121 if needed for compat.
- Supports both browser and device-code flows (device code for headless/VPS).
- **`px login xai`** — prompts browser vs device-code, writes tokens.

### 4.4 Ollama

No auth. Config provides `base_url` (default `http://localhost:11434`).

---

## 5. Providers

### 5.1 Provider interface

```go
type Provider interface {
    ID() string
    Stream(ctx context.Context, req *Request) (<-chan StreamEvent, error)
    Models() ([]Model, error)
}

type Request struct {
    Model       string
    System      []SystemBlock   // ordered system blocks
    Messages    []Message
    Tools       []ToolDef
    MaxTokens   int
    Temperature *float64
}

type SystemBlock struct {
    Text       string
    CacheCtl   string  // "ephemeral" or "" — Anthropic-only, ignored by other providers
}

type Message struct {
    Role    string          // user | assistant | tool
    Content []ContentBlock  // text, tool_use, tool_result
}

type StreamEventType int

const (
    EventTextDelta StreamEventType = iota
    EventToolUseStart
    EventToolUseDelta
    EventToolUseStop
    EventDone
    EventError
)

type StreamEvent struct {
    Type     StreamEventType
    Text     string
    ToolCall *ToolCall
    Error    error
    Usage    *Usage   // exact token counts from provider response (on EventDone)
}

type Usage struct {
    InputTokens  int
    OutputTokens int
}

// AnthropicUsage extends Usage with prompt-caching fields.
// Only returned by the Anthropic provider; other providers return *Usage.
type AnthropicUsage struct {
    Usage
    CacheReadTokens  int
    CacheWriteTokens int
}

// Tool is the interface every tool implements.
type Tool interface {
    Name() string
    Description() string
    Schema() json.RawMessage   // JSON Schema for input parameters
    Execute(ctx context.Context, input json.RawMessage) (ToolResult, error)
}

type ToolResult struct {
    Content string
    Error   string  // empty if success
}
```

**Channel lifecycle contract:**
- The channel returned by `Stream` is **always closed**. The producer goroutine
  uses `defer close(ch)`.
- `EventDone` or `EventError` is always the last event before close.
- On `ctx.Cancel()`, the producer closes the channel immediately without
  emitting `EventDone` or `EventError`.
- The caller must `range` over the channel and stop when it closes.

**`Models()`** returns available models for the provider. Used by `px --model`
  tab-completion and `/model` command. If the provider doesn't support model
  listing (e.g. Ollama with a custom model), it returns the configured model only.

### 5.2 Anthropic provider

- Endpoint: `POST {baseURL}/v1/messages` (default `https://api.anthropic.com`).
- **API key auth**: `x-api-key: <key>`, `anthropic-version: 2023-06-01`.
- **OAuth auth** (stealth): `Authorization: Bearer <token>`,
  `anthropic-beta: claude-code-20250219,oauth-2025-04-20`,
  `user-agent: claude-cli/{cc_version}`, `x-app: cli`.
- System prompt handling differs for OAuth (see §6, stealth within this provider).
- Streaming via SSE.
- Supports tool_use, extended thinking (if model supports).

### 5.3 Ollama provider

- Endpoint: `POST {base_url}/api/chat` with `stream: true`.
- Maps Poisson's tool schema to Ollama's `tools` format.
- Parses streaming NDJSON (each line is a JSON chunk).
- No auth headers.

### 5.4 xAI provider

- Endpoint: `POST https://api.x.ai/v1/chat/completions` (OpenAI-compatible) or
  `POST https://api.x.ai/v1/responses` (Responses API, for grok-build with
  built-in `web_search` tool).
- **OAuth auth**: `Authorization: Bearer <access_token>`.
- Auto-refresh on 401 using refresh token.
- Model list includes `grok-build-0.1` and standard Grok models.

---

## 6. Anthropic Stealth (within Anthropic provider)

The stealth mechanism is entirely Anthropic-specific — it only activates
when `auth.json` has `anthropic.type == "oauth"`. It has no relevance to the
Ollama or xAI providers. The code lives in `internal/provider/` alongside
`anthropic.go`, not as a separate package.

When OAuth is active, every Anthropic request is transformed to look
indistinguishable from a genuine Claude Code client.

### 6.1 Request headers (OAuth path)

```
Authorization: Bearer {access_token}
anthropic-version: 2023-06-01
anthropic-beta: claude-code-20250219,oauth-2025-04-20
user-agent: claude-cli/{cc_version}
x-app: cli
accept: application/json
anthropic-dangerous-direct-browser-access: true
```

### 6.2 System prompt manipulation

Before sending, the provider builds the system array as:

1. **system[0]** = `"You are a Claude agent, built on Anthropic's Claude Agent SDK."`
   (the real Claude Code identity — replaces the pi-style identity marker).
2. **system[1]** = the agent's actual system prompt (AGENTS.md, skills, tools, etc.)
   with stealth sanitization applied (see §6.3).
3. **system[0] (billing header)** — prepended before everything else:
   the `x-anthropic-billing-header` text block (see §6.4).

Final system array order: `[billing_header, claude_code_identity, actual_system_prompt]`.

### 6.3 System prompt sanitization

Applied to the actual system prompt (system[1]):

- **Paragraph removal**: split on `\n\n+`, drop any paragraph containing:
  - `"github.com/badlogic/pi-mono"` → replaced with Poisson equivalents:
    `"Poisson"`, `"~/.poisson"` (so Poisson-specific fingerprints are also stripped)
  - `"operating inside pi"` → `"operating inside Poisson"`
  - Pi-specific documentation references
  - Any third-party agent fingerprints
- **Inline text replacements**:
  - `"if pi honestly"` → `"if the assistant honestly"`
  - `"Here is some useful information about the environment you are running in:"`
    → `"Environment context you are running in:"`

### 6.4 Billing header (cch)

The `x-anthropic-billing-header` is computed from the first user message text
and injected as `system[0]` (a text block, no cache_control — it changes per
request):

```
x-anthropic-billing-header: cc_version={version}.{suffix}; cc_entrypoint={entrypoint}; cch={cch};
```

Where:
- **cch** = first 5 hex chars of `SHA-256(firstUserMessageText)`.
- **version** = from `[stealth] cc_version` in config (default `"2.1.156"`).
- **suffix** = first 3 hex chars of `SHA-256(salt + sampledChars + version)`.
  - `salt` = from `[stealth] cch_salt` in config (default `"59cf53e54c78"`).
  - `sampledChars` = chars at positions from `[stealth] cch_positions` in config
    (default `[4, 7, 20]`) of the first user message (0-indexed; `"0"` if out
    of bounds).
- **entrypoint** = from `[stealth] cc_entrypoint` in config (default `"sdk-cli"`).

All constants are configurable so users can update them when Anthropic rotates
values without waiting for a Poisson release.

**Health check:** on startup when stealth is active, Poisson sends a lightweight
probe request (e.g. a 1-token completion) to verify the billing header is
accepted. If the probe fails with a 400/401/403, Poisson falls back to API key
mode (if configured) and prints a warning: "Stealth billing header rejected.
Falling back to API key mode. Update [stealth] constants in config.toml."

**Prompt caching impact:** the billing header in `system[0]` changes every
request (it's derived from the first user message text). This destroys
Anthropic prompt caching for the system prompt — every request pays full
input token cost instead of cache-read pricing. This is inherent to the
stealth disguise. API key mode (non-OAuth) does not inject the billing
header and benefits from prompt caching normally.

**`px login anthropic`** activates this. All OAuth requests draw from the
Claude Pro/Max subscription quota instead of pay-as-you-go API billing.

> **TOS WARNING**: This masquerades Poisson's HTTP traffic as Claude Code. It comes
> with no guarantees. You might be banned for breaking Anthropic's TOS. Use at
> your own risk.

---

## 7. Bash Guard

Port of pi's `bash-guard` extension. Classifies bash commands as safe or
requiring approval.

### 7.1 Classification pipeline

```
command string
  → hasDangerousPatterns(raw)     # redirects, pipes into dangerous shells, ANSI escapes
  → containsAnsiEscape(raw)
  → segments(raw)                  # split on ; | && || |() etc.
  → for each segment:
      normalize tokens
      prefix match against SAFE list
      per-command danger detectors (find, gh, git, rg, sed, tree, yq, tail)
  → touchesDotEnv(allTokens)
  → touchesEnv(allTokens)
  → touchesSensitivePath(allTokens)
```

If all segments pass → **auto-allow**. Otherwise → **prompt**.

### 7.2 Safe list (built-in)

```
git status, git diff, git log, git show, git branch, git remote,
git stash list, git stash show, git rev-parse, git describe, git shortlog,
git blame, git ls-files, git ls-tree, git tag,
cat, head, tail, wc, file, stat,
md5sum, sha256sum, sha1sum, sha512sum, diff, cmp, od, xxd,
mkdir, touch,
grep, rg, find, which, whereis, locate, type,
ls, tree, pwd,
cd, pushd, popd, dirs, du, df, realpath, readlink,
dirname, basename,
npm list, npm view, npm outdated, npm explain,
pnpm list, pnpm view, pnpm outdated,
yarn list, yarn info, yarn outdated,
jq, yq, sed,
gh pr list, gh pr view, gh pr diff,
gh issue list, gh issue view, gh api, gh repo view,
uname, date, whoami, id, hostname, uptime,
echo
```

### 7.3 Dangerous tokens (red-flagged)

```
parallel, eval, exec, source,
bash, sh, zsh, dash, ksh, fish,
python, python2, python3, node, nodejs,
ruby, perl, php, lua,
curl, wget,
nc, netcat, ncat, socat,
openssl, su, doas,
chmod, chown, mv, cp, ln, umask,
unshare, nsenter, chroot,
base64, uuencode, uudecode
```

### 7.4 Destructive commands (always prompt)

```
rm, rmdir, dd, shred, mkfs, mke2fs,
fdisk, parted, sfdisk, cfdisk, wipefs, truncate, unlink
```

### 7.5 Sensitive paths

- Exact basenames: `.bash_history`, `.zsh_history`, `.npmrc`, `.yarnrc`,
  `.git-credentials`, `authorized_keys`, `known_hosts`, etc.
- Dir patterns: `/.ssh/`, `/.aws/`, `/.config/gcloud/`, `/.config/gh/`,
  `/etc/passwd`, `/etc/shadow`, etc.
- SSH private key regex: `_(rsa|ecdsa|ed25519|dsa)$`.

### 7.6 Approval UX

In the REPL, when a non-safe command is about to run:
```
[approval required] $ rm -rf node_modules
[a]llow [d]eny [a]lways-allow
```

`always-allow` adds the command prefix to the session's temporary allowlist
(stored in memory, not persisted).

### 7.7 Sandbox bypass

`POISSON_SANDBOX=1` (or `IS_SANDBOX=1` for pi compat) bypasses all gating —
for subagent children running in isolated environments.

### 7.8 Subagent bash approval forwarding

When a subagent child encounters a non-safe command, it sends an approval
request to the parent via the **JSON-line protocol over its stdout/stdin
pipes** (not HTTP). The child writes an approval request to stdout; the
parent reads it, prompts the user in the REPL, and writes the response to
the child's stdin. See §10.3.

---

## 8. Storage (SQLite)

### 8.1 Schema

```sql
PRAGMA journal_mode = WAL;       -- concurrent readers + one writer
PRAGMA busy_timeout = 5000;      -- 5s timeout on SQLITE_BUSY

-- Sessions
CREATE TABLE sessions (
    id                  TEXT PRIMARY KEY,      -- UUID
    parent_id           TEXT,                  -- fork parent session (nullable)
    fork_point          TEXT,                  -- message_id from which this forked (nullable)
    is_subagent         INTEGER DEFAULT 0,     -- 1 if created by a subagent child
    title               TEXT,
    compaction_summary  TEXT,                  -- set when compacted; prepended to Request.System
    created_at          INTEGER NOT NULL,
    updated_at          INTEGER NOT NULL,
    cwd                 TEXT NOT NULL,
    provider            TEXT NOT NULL,
    model               TEXT NOT NULL,
    -- Integrity: fork_point is null iff parent_id is null
    CHECK (
        (parent_id IS NULL AND fork_point IS NULL) OR
        (parent_id IS NOT NULL AND fork_point IS NOT NULL)
    )
);

-- Messages (append-only — never deleted, only soft-deleted via deleted_at)
CREATE TABLE messages (
    id          TEXT PRIMARY KEY,      -- UUID
    session_id  TEXT NOT NULL REFERENCES sessions(id),
    seq         INTEGER NOT NULL,      -- monotonic ordering within session
    role        TEXT NOT NULL,          -- user | assistant | tool
    content     TEXT NOT NULL,         -- JSON array of content blocks
    deleted_at  INTEGER,               -- soft delete timestamp (NULL = active)
    compacted   INTEGER DEFAULT 0,     -- 1 = superseded by compaction (excluded from context)
    api_call_id TEXT,                  -- set on assistant messages: links to the API call that generated them
    created_at  INTEGER NOT NULL
);

CREATE INDEX idx_messages_session ON messages(session_id, seq);
CREATE INDEX idx_sessions_parent ON sessions(parent_id);

-- FTS5 for full-text search over message text
-- Standalone table; search queries JOIN messages and filter deleted_at IS NULL
CREATE VIRTUAL TABLE messages_fts USING fts5(
    session_id UNINDEXED,
    message_id UNINDEXED,
    role UNINDEXED,
    content_text,           -- extracted text from content blocks (text blocks only)
    tokenize='unicode61'
);

-- API calls (one row per provider HTTP call — exact usage + cost)
CREATE TABLE api_calls (
    id                  TEXT PRIMARY KEY,
    session_id          TEXT NOT NULL REFERENCES sessions(id),
    seq                 INTEGER NOT NULL,   -- which turn within the session
    model               TEXT NOT NULL,
    input_tokens        INTEGER NOT NULL,   -- exact, from provider usage
    output_tokens       INTEGER NOT NULL,   -- exact, from provider usage
    cache_read_tokens   INTEGER DEFAULT 0,  -- Anthropic prompt caching
    cache_write_tokens  INTEGER DEFAULT 0,  -- Anthropic prompt caching
    cost                REAL NOT NULL,
    created_at          INTEGER NOT NULL
);

CREATE INDEX idx_api_calls_session ON api_calls(session_id, created_at);

-- Compaction records (historical metadata)
CREATE TABLE compactions (
    id            TEXT PRIMARY KEY,
    session_id    TEXT NOT NULL REFERENCES sessions(id),
    message_id    TEXT,                 -- nullable: may dangle after undo
    summary       TEXT NOT NULL,
    tokens_before INTEGER,
    tokens_after  INTEGER,
    cost          REAL DEFAULT 0.0,    -- cost of the compaction LLM call
    created_at    INTEGER NOT NULL
);

-- Model pricing (seeded from config, used for exact cost computation)
CREATE TABLE model_pricing (
    provider             TEXT NOT NULL,
    model                TEXT NOT NULL,
    input_per_mtok       REAL NOT NULL,
    output_per_mtok      REAL NOT NULL,
    cache_read_per_mtok  REAL DEFAULT 0,
    cache_write_per_mtok REAL DEFAULT 0,
    PRIMARY KEY (provider, model)
);
```

**SQLite concurrency:** WAL mode allows concurrent readers (e.g. `/search`)
while one writer is active. Subagent children that share the main DB get
their own session rows (`is_subagent = 1`). The `busy_timeout` prevents
`SQLITE_BUSY` errors under contention.

### 8.2 Token counting (exact, per API call)

Token counts are **exact**, sourced from the provider's `usage` response on
every API call. No estimation for accounting.

Each provider's streaming response includes a final usage event:
- **Ollama**: `prompt_eval_count` (input), `eval_count` (output) on the
  final `done: true` chunk.
- **Anthropic**: `input_tokens`, `output_tokens`, `cache_read_input_tokens`,
  `cache_creation_input_tokens` in the `message_delta` usage event.
- **xAI**: `usage.prompt_tokens`, `usage.completion_tokens` in the final
  chunk.

These are stored **per API call** in the `api_calls` table, not per message.
The provider returns aggregate usage for the entire request (system prompt +
all messages). This cannot be cleanly decomposed to per-message input tokens
— the stealth billing header changes every request, context files can change
between turns, and the system prompt shifts.

**Output tokens** are attributable to the assistant message that was
generated. `messages.api_call_id` on assistant rows links to the `api_calls`
row containing the exact output token count.

**User and tool messages** have `api_call_id = NULL` — they don't have their
own usage. Their token cost is embedded in the next API call's `input_tokens`.

If a provider doesn't return usage (rare), the `api_calls` row gets 0 tokens
and the context % display shows a warning.

### 8.3 Cost tracking (exact, per API call)

Cost is computed **exactly** from the stored token counts and model pricing.

The `model_pricing` table stores per-model rates (USD per 1M tokens). It is
seeded from `config.toml` and can be overridden. For OAuth providers
(Anthropic Pro/Max, xAI SuperGrok), pricing is set to 0 — subscription
billing, not per-token.

Cost per API call:
```
cost = (input_tokens / 1e6) * input_per_mtok
     + (output_tokens / 1e6) * output_per_mtok
     + (cache_read_tokens / 1e6) * cache_read_per_mtok
     + (cache_write_tokens / 1e6) * cache_write_per_mtok
```

This is stored in `api_calls.cost`. Per-session cost:
```sql
SELECT SUM(cost) FROM api_calls WHERE session_id = ?;
```

Per-message cost for display (assistant messages only):
```sql
SELECT api_calls.cost FROM api_calls
  JOIN messages ON messages.api_call_id = api_calls.id
  WHERE messages.id = ?;
```

`/cost` shows the current session's total cost, per-call breakdown, and
token totals. A global total across all sessions is available via `px cost`.

### 8.4 Context tracking

Context % is derived from the **last API call's** `input_tokens` — the
provider already told you the real number.

```
context_pct = last_api_call.input_tokens / context_window * 100
```

The status bar displays:

```
ctx: 42.3% (12,847 / 30,400 tok) | $0.0124 | model: claude-sonnet-4
```

Between tool results and the next provider call, the exact input token count
isn't known yet. A rough estimate (char count / 4) is used as a **trigger
threshold** for compaction (see §13). This estimate is never stored — it's
only used to decide whether to compact before the next call. The exact count
arrives on the next provider response and corrects the display.

---

## 9. Session Management

### 9.1 Slash commands

| Command | Description |
|---|---|
| `/sessions` | List all sessions (paginated, newest first) |
| `/search <query>` | Full-text search across all sessions via FTS5 |
| `/resume <id>` | Resume an existing session |
| `/new` | Start a new session (leaves current) |
| `/fork [msg]` | Fork: clone conversation to a chosen message (or latest), switch to clone |
| `/undo` | Undo the last user message + all agent messages after it (soft delete) |
| `/compact` | Manually trigger compaction |
| `/model <name>` | Switch model/provider mid-session |
| `/reload` | Reload config.toml + AGENTS.md + skills, rebuild system prompt |
| `/clear` | Clear the screen (not the session) |
| `/help` | Show available commands |
| `/cost` | Show session token breakdown and cost |
| `/quit` | Exit |

### 9.2 /fork

`/fork` with no argument forks from the latest message. `/fork <seq>` or
`/fork <message-id>` forks from a specific message.

1. If no argument: fork from the latest active (non-deleted, non-compacted)
   message. If argument given: show a numbered list of messages and let the
   user select, or fork directly if a valid seq/id is provided.
2. Clone all active messages up to and including the selected message into a
   new session (`parent_id = current`, `fork_point = selected_message_id`).
   Insert FTS5 rows for the cloned messages.
3. Copy `compaction_summary` if the fork point is after a compaction.
4. Switch the REPL to the new cloned session.
5. The new session is linked to its parent for navigation.

### 9.3 /undo

Soft delete — messages are never physically deleted.

1. Find the last active `user` message (`deleted_at IS NULL AND compacted = 0`).
2. Set `deleted_at = now()` on that message and all `assistant`/`tool` messages
   after it (by seq).
3. The conversation reverts to the state before that user turn.
4. Queries that load context filter `WHERE deleted_at IS NULL AND compacted = 0`.
5. FTS5 search filters out soft-deleted messages via JOIN on `messages`.
6. If a compaction happened after the last user message, the compaction record
   is preserved as historical metadata (`compactions.message_id` may dangle —
   it's nullable). If the undo removes the compaction summary, clear
   `sessions.compaction_summary`.
7. Undo cannot cross a compaction boundary into compacted messages — those
   are already marked `compacted = 1` and excluded from the active set. If the
   user tries, Poisson reports: "Cannot undo past compaction point. Use /fork
   before compacting to preserve a branch."

---

## 10. Subagents

### 10.1 Architecture

Mirrors pi's subagent extension: a child `Poisson` process is spawned in JSON
output mode with a restricted tool set. The child shares the main `~/.poisson/poisson.db`
with its own session row (`is_subagent = 1`).

```
parent Poisson process
  ├─ user prompts → agent calls subagent tool
  ├─ spawn child: px --json --tools read,write,edit,bash,search,ls,glob
  │                 --no-skills --session <child-session-id> "<task>"
  ├─ env: POISSON_SUBAGENT_CHILD=1, POISSON_SUBAGENT_NAME=<name>
  ├─ child stdout → JSON lines (progress, tool calls, approval requests, done)
  ├─ child stdin  ← JSON lines (approval responses)
  └─ return child's final output as tool result
```

No HTTP approval server. Approval requests and responses flow over the
child's existing stdin/stdout pipes via the JSON-line protocol.

### 10.2 Child tool set

Subagents get: `read`, `write`, `edit`, `bash`, `search`, `ls`, `glob`.
They do **not** get: `fetch`, `exa_search`, `subagent`, `skill` (no recursion,
no network tools — keeps subagents focused and sandboxable).

### 10.3 Bash approval via JSON-line protocol

When the child encounters a non-safe bash command, it writes an approval
request to stdout (a JSON line) and blocks reading stdin for the response.
The parent's stdout reader detects the approval request, prompts the user
in the REPL, and writes the response to the child's stdin.

Child → parent (stdout):
```json
{"type":"approval_request","command":"rm -rf build/","description":"Clean build artifacts","cwd":"/home/user/project","agent":"reviewer"}
```

Parent → child (stdin):
```json
{"type":"approval_response","approved":true}
```

**Terminal coordination:** all terminal writes go through a single serialized
output channel. The approval prompt is rendered through the same channel as
streaming text and tool output — no concurrent writes to the terminal.

**Failure handling:**
- If the parent doesn't respond within 30 seconds, the child auto-denies and
  returns the denial as a tool result.
- If the child's stdin is closed (parent died), the child auto-denies.
- `POISSON_SANDBOX=1` bypasses all gating — the child never sends approval
  requests.

### 10.4 Child output protocol (JSON lines)

Each line on stdout is a JSON event:
```json
{"type":"progress","text":"Reading file.go..."}
{"type":"tool","tool":"bash","input":{"command":"ls -la"}}
{"type":"approval_request","command":"rm -rf build/","description":"Clean build artifacts","cwd":"/home/user/project","agent":"reviewer"}
{"type":"text","text":"Here is my analysis..."}
{"type":"done","success":true,"toolCount":5,"turns":3,"contextTokens":4200}
```

The parent reads these lines, renders progress/tool/text to the TUI, and
handles `approval_request` by prompting the user and writing the response
to the child's stdin.

### 10.5 Subagent tool definition

```json
{
  "name": "subagent",
  "description": "Spawn a one-shot child Poisson agent to complete a specific task. The child has read, write, edit, bash, search, ls, and glob tools. Use when you need focused work isolated from the main session. The child returns its final output when done. It cannot ask questions — give it a complete, self-contained task.",
  "input_schema": {
    "type": "object",
    "properties": {
      "task": { "type": "string", "description": "Complete, self-contained task for the subagent. Include context, file paths, and expected output format." },
      "name": { "type": "string", "description": "Display name for the subagent. If omitted, a name is chosen automatically." }
    },
    "required": ["task"]
  }
}
```

---

## 11. Skills

### 11.1 Discovery

Skills live in `~/.poisson/skills/{skill-name}/SKILL.md`. Each SKILL.md has
optional YAML frontmatter and a markdown body:

```markdown
---
description: "Review code for quality issues"
argument-hint: "[file or directory]"
---

# Code Quality Review

Read the specified file(s) and check for:
- ...
- ...
```

### 11.2 Skill tool

The `skill` tool invokes a named skill by inserting its body into the
conversation as a user message (or system context, depending on the skill
type). The skill tool:

1. Takes `name` (skill name) and optional `args` (argument string).
2. Looks up `~/.poisson/skills/{name}/SKILL.md`.
3. Strips frontmatter, gets the body.
4. If `argument-hint` is set, appends the user-provided args.
5. Returns the skill body + args as the tool result for the agent to follow.

### 11.3 System prompt injection

All discovered skills are listed in the system prompt as available, with
their name and description:

```
Available skills:
- code-quality: Review code for quality issues
- create-pr: Create a pull request
- ...
```

The agent can then invoke the `skill` tool to load a skill's full instructions.

### 11.4 Skill tool definition

```json
{
  "name": "skill",
  "description": "Load and invoke a skill by name. The skill's instructions are returned as context for you to follow.",
  "input_schema": {
    "type": "object",
    "properties": {
      "name": { "type": "string", "description": "Skill name (directory under ~/.poisson/skills/)" },
      "args": { "type": "string", "description": "Optional arguments to pass to the skill" }
    },
    "required": ["name"]
  }
}
```

---

## 12. AGENTS.md / Context Files

### 12.1 Discovery

Like pi-mono, Poisson walks the directory tree from `cwd` up to `/`, collecting
`AGENTS.md` files. It also checks `~/.poisson/AGENTS.md` for global instructions.

```
loadProjectContextFiles(cwd, agentDir=~/.poisson):
  contextFiles = []
  seen = set()

  # Global
  global = loadFromDir(agentDir)   # checks AGENTS.md, CLAUDE.md
  if global: contextFiles.append(global)

  # Walk cwd → root (closest to cwd last)
  ancestors = []
  currentDir = cwd
  while currentDir != "/":
    file = loadFromDir(currentDir)
    if file and file.path not in seen:
      ancestors.unshift(file)   # root-first ordering
      seen.add(file.path)
    currentDir = parent(currentDir)

  contextFiles.extend(ancestors)
  return contextFiles
```

Candidate filenames per directory: `AGENTS.md`, `CLAUDE.md`.

### 12.2 System prompt injection

Context files are injected into the system prompt as:

```
<project_context>

Project-specific instructions and guidelines:

<project_instructions path="/home/user/project/AGENTS.md">
{content}
</project_instructions>

</project_context>
```

Global `~/.poisson/AGENTS.md` appears first, then ancestor files ordered
root → cwd (so the closest AGENTS.md to cwd is last/most specific).

### 12.3 Reload

Context files are loaded at session start and on `/new` or `/resume`. The
`/reload` command reloads `config.toml` + AGENTS.md files + skills and
rebuilds the system prompt without restarting the session. Use when you edit
config, AGENTS.md, or add/remove skills mid-session. The conversation
history is preserved; only the system prompt and config settings are
refreshed.

---

## 13. Compaction

### 13.1 Trigger

Compaction triggers **mid-turn** — after every tool result is appended, not
just between user turns. This ensures long-running tasks with many tool calls
and no user interaction compact when needed.

The check uses the **last API call's exact `input_tokens`** plus a rough
estimate of new tool result tokens (char count / 4) that haven't been counted
by a provider call yet. If the combined total exceeds `threshold *
context_window` (default 0.8 / 80%), compaction fires immediately.

The estimate is **only used for the trigger** — never for stored accounting.
The exact count arrives on the next provider response.

### 13.2 Process

1. **Pause** the current turn (the agent is between tool results and the next
   provider call).
2. Collect all active messages (`deleted_at IS NULL AND compacted = 0`).
3. Build a summarization request to the compaction model (from
   `[compaction] model` in config, or the session model if not set):
   - System prompt: the handoff summarization prompt (§13.3).
   - Messages: the active conversation (user + assistant + tool turns).
4. **Overflow handling:** if the conversation to summarize is itself near the
   context window, summarize only the oldest half of the messages, keeping
   recent messages verbatim. This prevents the summarization request from
   exceeding the context window.
5. Stream the summary from the provider.
6. **Mark old messages** as `compacted = 1` (they stay in the DB for fork/search
   but are excluded from the active context).
7. **Store the summary** on `sessions.compaction_summary`.
8. **Record** an `api_calls` row for the summarization call (exact tokens +
   cost) and a `compactions` record with before/after token counts.
9. **Resume** the turn — the next provider call uses the compaction summary
   (prepended to `Request.System`) + remaining active messages. The agent
   continues as if nothing changed, with the summary replacing the old history.

### 13.3 Handoff summarization prompt

```
You are a context summarization assistant. Your task is to read a
conversation between a user and an AI assistant, then produce a structured
summary following the exact format specified.

Do NOT continue the conversation. Do NOT respond to any questions in the
conversation. ONLY output the structured summary.

Produce a summary with these sections:

## Big Picture
What is the overall goal of this conversation? What is the user trying to
accomplish?

## Key Decisions
Important decisions made, approaches chosen, and why.

## Current State
What has been done so far. What files were created, modified, or examined.
What commands were run and their outcomes.

## User Instructions
Any specific instructions, constraints, or preferences the user has stated.
Preserve these verbatim if possible.

## Pending Tasks
What remains to be done. If the conversation was interrupted mid-task,
describe exactly where things left off so the next agent can continue
seamlessly.

## Important Details
Any small but critical details: file paths, error messages, environment
quirks, version numbers, or anything that would be hard to rediscover.
```

### 13.4 Context display

Status bar (always visible in the REPL):

```
[session: abc123] ctx: 42.3% (12,847 / 30,400 tok) | $0.0124 | anthropic/claude-sonnet-4
```

When compaction is approaching:
```
[session: abc123] ctx: 78.1% (23,742 / 30,400 tok) ⚠ compacting soon | $0.0231 | anthropic/claude-sonnet-4
```

During compaction:
```
[session: abc123] compacting... (summarizing 47 messages) | anthropic/claude-sonnet-4
```

---

## 14. Tools

The complete tool set. **No other tools** beyond these.

### 14.1 bash

```json
{
  "name": "bash",
  "description": "Execute a bash command. Safe commands run automatically; others require approval.",
  "input_schema": {
    "type": "object",
    "properties": {
      "command": { "type": "string" },
      "description": { "type": "string", "description": "Short description of what the command does" },
      "workdir": { "type": "string", "description": "Working directory (default: cwd)" },
      "timeout": { "type": "integer", "description": "Timeout in seconds (default: 120)" }
    },
    "required": ["command"]
  }
}
```

Guarded by §7 Bash Guard. Streams stdout/stderr. Returns
`{stdout, stderr, exitCode}`.

### 14.2 read

```json
{
  "name": "read",
  "description": "Read the contents of a file. Supports text files and images (jpg, png, gif, webp). Output is truncated to 2000 lines or 50KB.",
  "input_schema": {
    "type": "object",
    "properties": {
      "path": { "type": "string" },
      "offset": { "type": "integer", "description": "Line number to start reading from (1-indexed)" },
      "limit": { "type": "integer", "description": "Maximum number of lines to read" }
    },
    "required": ["path"]
  }
}
```

### 14.3 write

```json
{
  "name": "write",
  "description": "Write content to a file. Creates parent directories. Overwrites if exists.",
  "input_schema": {
    "type": "object",
    "properties": {
      "path": { "type": "string" },
      "content": { "type": "string" }
    },
    "required": ["path", "content"]
  }
}
```

### 14.4 edit

```json
{
  "name": "edit",
  "description": "Edit a file using exact text replacement. Each oldText must match a unique, non-overlapping region.",
  "input_schema": {
    "type": "object",
    "properties": {
      "path": { "type": "string" },
      "edits": {
        "type": "array",
        "items": {
          "type": "object",
          "properties": {
            "oldText": { "type": "string" },
            "newText": { "type": "string" }
          },
          "required": ["oldText", "newText"]
        }
      }
    },
    "required": ["path", "edits"]
  }
}
```

Verifies each `oldText` exists and is unique in the file. Fails on
non-unique or missing matches.

### 14.5 search (ripgrep)

```json
{
  "name": "search",
  "description": "Search file contents using ripgrep. Returns matching lines with file paths and line numbers.",
  "input_schema": {
    "type": "object",
    "properties": {
      "pattern": { "type": "string", "description": "Regex pattern" },
      "path": { "type": "string", "description": "Directory or file to search (default: cwd)" },
      "glob": { "type": "string", "description": "File glob filter (e.g. '*.go')" },
      "ignore_case": { "type": "boolean" },
      "max_results": { "type": "integer", "default": 100 }
    },
    "required": ["pattern"]
  }
}
```

Wraps `rg --json` for structured output.

### 14.6 ls

```json
{
  "name": "ls",
  "description": "List directory contents with file types and sizes.",
  "input_schema": {
    "type": "object",
    "properties": {
      "path": { "type": "string", "description": "Directory path (default: cwd)" },
      "all": { "type": "boolean", "description": "Show hidden files" },
      "recursive": { "type": "boolean", "description": "List recursively" }
    }
  }
}
```

### 14.7 glob

```json
{
  "name": "glob",
  "description": "Find files matching a glob pattern.",
  "input_schema": {
    "type": "object",
    "properties": {
      "pattern": { "type": "string", "description": "Glob pattern (e.g. '**/*.go')" },
      "path": { "type": "string", "description": "Base directory (default: cwd)" }
    },
    "required": ["pattern"]
  }
}
```

### 14.8 fetch (Ollama web_fetch)

```json
{
  "name": "fetch",
  "description": "Fetch and extract text content from a web page URL using the local Ollama instance's web_fetch API.",
  "input_schema": {
    "type": "object",
    "properties": {
      "url": { "type": "string", "description": "URL to fetch" }
    },
    "required": ["url"]
  }
}
```

Calls `POST {ollama_base_url}/api/fetch` (or the Ollama web_fetch endpoint)
with `{"url": "..."}` and returns the extracted text content. Requires Ollama
running locally with web fetch enabled.

**Registration:** this tool is only registered when Ollama is the configured
provider or when Ollama is detected as running. If Ollama is not available,
the tool is not offered to the agent, avoiding a hidden cross-provider
dependency.

### 14.9 exa_search

```json
{
  "name": "exa_search",
  "description": "Search the web via exa.ai. Returns results with titles, URLs, and excerpts. Also returns an AI-generated summary of the results.",
  "input_schema": {
    "type": "object",
    "properties": {
      "query": { "type": "string", "description": "Search query" },
      "num": { "type": "integer", "description": "Number of results (default: 10, max: 100)" },
      "type": { "type": "string", "description": "Search type: keyword | neural (default: keyword)" },
      "verbose": { "type": "boolean", "description": "Include full text excerpts (default: false)" }
    },
    "required": ["query"]
  }
}
```

Uses exa.ai's free landing-page API:
1. `POST https://exa.ai/api/token/issue` — get a short-lived JWT (~5 min).
2. `POST https://exa.ai/api/search` with `Authorization: Bearer {jwt}`.
3. Cache the JWT in `~/.poisson/exa-token.json` with 10s safety margin.

Headers mimic a browser: `User-Agent: Mozilla/5.0 ...`, `Origin: https://exa.ai`,
`Referer: https://exa.ai/`.

**JWT retry:** on 401 response from the search endpoint, re-issue the JWT and
retry the search once. On 429 (rate limited), return a clear error to the
agent: "exa_search rate limited. Try again later or use fetch + manual
parsing."

### 14.10 subagent

See §10.5.

### 14.11 skill

See §11.4.

---

## 15. TUI / REPL

### 15.1 Streaming readline REPL

A raw-mode terminal line editor (no full-screen TUI for v1):

```
poisson> █
```

- Multi-line input: `\\` at end of line continues on next line, or a special
  key (e.g. `Ctrl+J`) inserts a newline.
- `Enter` submits.
- `↑`/`↓` history navigation.
- `Tab` for slash-command completion.
- `@path` inline expansion: `@foo.go` is expanded to the file contents before
  sending (reads the file and inlines it as a code block).

### 15.2 Streaming output

Agent responses stream token-by-token. Tool calls are rendered inline:

```
> Fix the bug in main.go

Poisson: Let me look at the file first.
  [read] main.go
  ⠋ reading...

Poisson: I see the issue. The error handling is missing...
  [edit] main.go
    - if err != nil { return err }
    + if err != nil { log.Fatal(err); return err }
  ✓ edited

Poisson: The fix is applied. The error is now logged before returning.
```

### 15.3 Status bar

A persistent one-line status bar at the bottom:

```
[abc123] ctx: 42.3% (12,847/30,400) | $0.0124 | anthropic/claude-sonnet-4
```

Updates after every message. Shows a `⚠` warning at >75% context usage.
Cost shows `$0.00` for OAuth/subscription providers.

**Terminal output coordination:** all terminal writes go through a single
serialized output channel. Streaming text, tool call rendering, approval
prompts, and status bar updates all write through this channel — no
concurrent goroutines write to the terminal directly. This prevents
interleaved output during subagent approval or parallel tool dispatch.

### 15.4 Slash commands

See §9.1.

---

## 16. CLI

```
px                           # start interactive REPL (new session)
px -r                       # resume latest session
px -r <session-id>          # resume specific session
px -p "prompt"              # one-shot prompt (non-interactive)
px -p "prompt" --json       # one-shot, JSON output (for subagent use)
cat file.go | px -p "review"  # stdin appended to prompt (Unix pipe)
px --provider ollama        # override provider
px --model qwen3-coder:30b  # override model
px --tools a,b,c            # restrict tool set
px --no-skills              # disable skill loading
px --no-context-files       # disable AGENTS.md discovery
px --session <path>         # use a specific session DB file
px login anthropic          # OAuth login
px login xai                # OAuth login (browser or device-code)
px logout anthropic         # clear stored tokens
px sessions                 # list sessions
px search <query>           # search across all sessions
px cost                     # show total cost across all sessions
px cost <session-id>        # show cost for a specific session
```

**stdin support:** if stdin is not a TTY (e.g. piped input), Poisson reads stdin
and appends it to the prompt. This enables Unix composition:
`cat main.go | px -p "review this file"`.

Environment variables:
- `POISSON_SANDBOX=1` — bypass bash guard (subagent isolation)
- `POISSON_SUBAGENT_CHILD=1` — subagent child mode (internal)
- `POISSON_SUBAGENT_NAME=<name>` — subagent display name (internal)

---

## 17. Agent Loop

### 17.1 Turn cycle

```
1. INGEST: append user message to session (SQLite)
2. BUILD: load AGENTS.md (§12), skills (§11), build system prompt,
   apply stealth if Anthropic OAuth (§6), assemble messages from session
   history (filter deleted_at IS NULL AND compacted = 0, prepend
   session.compaction_summary to Request.System if set)
3. CALL: provider.Stream(ctx, req) — stream text deltas to TUI
4. TOOLS: collect all tool_use blocks from the stream
   a. Dispatch all tools concurrently (sync.WaitGroup)
   b. Guard bash commands (§7) — subagent approvals via JSON-line (§10.3)
   c. Append all tool_result messages
   d. CHECK COMPACTION: if last_api_call.input_tokens + estimated_new_tokens
      > threshold * context_window → compact (§13, mid-turn)
   e. Go to step 2 (rebuild with new messages + compaction summary if compacted)
5. COMMIT: append assistant message, record api_calls row (exact usage +
   cost), update status bar (context %, cost)
6. Return to REPL for next input
```

Steps 2–4 repeat for each tool-use cycle within a turn. Compaction can fire
at step 4d — the turn pauses, summarizes, marks old messages compacted, and
resumes with the compacted context.

### 17.2 Error handling

- Provider errors: show error, offer retry.
- Tool errors: return error text as tool_result, let the agent react.
- Bash command denied: return denial as tool_result.
- OAuth token expired: auto-refresh, retry once.
- Stealth billing header rejected (400/401/403): fall back to API key mode
  if configured, print warning. If no API key, show error and suggest
  updating `[stealth]` constants in config.toml.
- Subagent approval timeout (30s): auto-deny, return denial as tool_result.
- Subagent child dies: return error as tool_result.

---

## 18. Implementation Order

1. **Project scaffold**: `go.mod`, `main.go`, package structure.
2. **Config**: TOML parser + `~/.poisson/config.toml` loader (nested tables, arrays).
3. **Store**: SQLite schema (WAL mode, api_calls, soft delete, compacted),
   migrations, session/message/api_call CRUD, model_pricing seeding.
4. **Provider interface + Ollama**: simplest provider, good for testing.
   Define Provider, StreamEvent (typed), Tool interface, channel lifecycle.
5. **Tools**: bash (with guard), read, write, edit, search, ls, glob.
   Parallel dispatch via sync.WaitGroup.
6. **Agent loop**: turn cycle, streaming, tool dispatch, mid-turn compaction
   check, exact token/cost recording per API call.
7. **TUI**: readline REPL, status bar (context %, cost), streaming output,
   single serialized output channel.
8. **Session commands**: /new, /resume, /sessions, /search, /undo (soft delete),
   /fork (with /clone merged), /reload, /cost.
9. **Anthropic provider**: API key auth first, then OAuth + stealth (billing
   header, system prompt sanitization, CC identity — all within the
   anthropic package, constants from config).
10. **Anthropic OAuth login**: `px login anthropic`, stealth health check.
11. **Compaction**: mid-turn capable, configurable model, overflow handling
    (summarize oldest half).
12. **Skills**: discovery, system prompt injection, skill tool.
13. **AGENTS.md**: discovery (walk cwd→root), system prompt injection.
14. **Subagents**: child process spawn, JSON-line protocol including approval
    forwarding via stdin/stdout pipes.
15. **xAI provider**: OAuth login (browser + device-code), Grok models.
16. **fetch + exa_search tools**: network tools (fetch only when Ollama
    configured, exa_search with JWT retry).
17. **Fork**: /fork command with message selection, FTS5 row cloning.

---

## 19. Future

- **Full TUI**: v1 is a streaming REPL. A full-screen TUI with panes can come
  later if needed.
- **Session export**: export to Markdown/HTML — low priority, can add as a
  slash command.
- **Multi-model routing**: use different models for different tasks
  (e.g. cheap model for compaction, strong model for coding).
- **Checkpoint/restore**: git-stash-like working tree snapshots beyond /undo.
- **Auto-title sessions**: one cheap LLM call on first turn to name the session.

No MCP support will be added. Token counting and cost tracking are exact
and implemented in v1.