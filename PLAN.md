# Poisson — Implementation Plan

Step-by-step build plan derived from docs/SPEC.md. Each phase has concrete file
deliverables, function signatures, and a testing checkpoint. Phases are
ordered bottom-up: each layer is testable on the one below.

---

## Phase 1: Project Scaffold

**Goal:** Go module, package dirs, main.go that compiles and runs.

### Files
- `go.mod` — module `poisson`, go 1.25, require `modernc.org/sqlite` + `golang.org/x/term`
- `main.go` — package main, prints "Poisson v0.1.0" for now
- `internal/config/` `internal/auth/` `internal/provider/` `internal/guard/`
  `internal/store/` `internal/agent/` `internal/tools/` `internal/subagent/`
  `internal/skills/` `internal/project/` `internal/tui/` — empty dirs with
  placeholder `.go` files (package declaration only)

### Steps
1. `go mod init poisson`
2. `go get modernc.org/sqlite`
3. `go get golang.org/x/term`
4. Create all internal package dirs
5. Write `main.go` with version print
6. `go build ./...` passes

### Checkpoint
```
go build ./... && ./px
# Output: Poisson v0.1.0
```

---

## Phase 2: Config (TOML parser + loader)

**Goal:** Parse `~/.poisson/config.toml` into a typed config struct.

### Files
- `internal/config/toml.go` — minimal TOML parser
- `internal/config/config.go` — Config struct, Load(), defaults, `~/.poisson/` dir creation

### `internal/config/toml.go`
- `func Parse(data string) (map[string]interface{}, error)`
- Support: `#` comments, `[section]` and `[section.subsection]` headers,
  `key = value` with strings (`"..."`), integers, booleans, arrays (`[a, b, c]`)
- No inline tables, no multiline strings, no datetimes
- Return nested `map[string]interface{}` (sections → keys → values)

### `internal/config/config.go`
```go
type Config struct {
    Provider   ProviderConfig
    Anthropic  AnthropicConfig
    XAI        XAIConfig
    Ollama     OllamaConfig
    Compaction CompactionConfig
    Stealth    StealthConfig
    Guard      GuardConfig
    TUI        TUIConfig
    Pricing    map[string]map[string]Pricing // [provider][model]Pricing
}

type Pricing struct {
    InputPerMTok       float64
    OutputPerMTok      float64
    CacheReadPerMTok   float64
    CacheWritePerMTok  float64
}
```
- `func Load() (*Config, error)` — read `~/.poisson/config.toml`, parse, apply defaults
- `func ConfigDir() string` — returns `~/.poisson/`, creates if missing
- `func ConfigPath() string` — `~/.poisson/config.toml`
- Default values: provider=anthropic, compaction.threshold=0.8,
  stealth constants from SPEC §3.3, pricing defaults from SPEC §3.4
- If config.toml doesn't exist, create it with commented-out defaults

### Steps
1. Write TOML parser, test with sample configs (nested tables, arrays, comments)
2. Write Config struct + Load() + defaults
3. Write ConfigDir() with mkdir -p
4. Test: create temp config, load, verify fields

### Checkpoint
```go
cfg, err := config.Load()
// cfg.Compaction.Threshold == 0.8
// cfg.Stealth.CCVersion == "2.1.156"
// cfg.Stealth.CCHPositions == []int{4, 7, 20}
```

---

## Phase 3: Store (SQLite schema + CRUD)

**Goal:** SQLite database with all tables, CRUD operations for sessions,
messages, api_calls, FTS5 search, pricing.

### Files
- `internal/store/store.go` — open, pragmas, schema, migrations
- `internal/store/session.go` — session CRUD
- `internal/store/message.go` — message CRUD, FTS5 indexing
- `internal/store/api_calls.go` — api_call CRUD
- `internal/store/search.go` — FTS5 full-text search
- `internal/store/pricing.go` — model_pricing CRUD + cost computation

### `internal/store/store.go`
```go
type Store struct {
    db *sql.DB
}

func Open(path string) (*Store, error)
```
- `PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;`
- Create all tables from SPEC §8.1 (sessions, messages, messages_fts,
  api_calls, compactions, model_pricing)
- Run idempotent migrations (CREATE TABLE IF NOT EXISTS)

### `internal/store/session.go`
```go
type Session struct {
    ID                string
    ParentID          *string
    ForkPoint         *string
    IsSubagent        bool
    Title             *string
    CompactionSummary *string
    CreatedAt         int64
    UpdatedAt         int64
    Cwd               string
    Provider          string
    Model             string
}

func (s *Store) CreateSession(sess *Session) error
func (s *Store) GetSession(id string) (*Session, error)
func (s *Store) ListSessions(limit, offset int) ([]Session, error)
func (s *Store) UpdateSession(sess *Session) error
func (s *Store) SetCompactionSummary(id, summary string) error
func (s *Store) ClearCompactionSummary(id string) error
```

### `internal/store/message.go`
```go
type Message struct {
    ID         string
    SessionID  string
    Seq        int
    Role       string  // user | assistant | tool
    Content    string  // JSON array of content blocks
    DeletedAt  *int64
    Compacted  bool
    APICallID  *string
    CreatedAt  int64
}

func (s *Store) AppendMessage(msg *Message) error
func (s *Store) GetMessages(sessionID string) ([]Message, error)
// Returns only active: deleted_at IS NULL AND compacted = 0
func (s *Store) SoftDeleteMessages(sessionID string, fromSeq int) error
func (s *Store) MarkCompacted(sessionID string, upToSeq int) error
func (s *Store) CloneMessages(srcSessionID string, upToSeq int, dstSessionID string) error
// For fork — copies active messages, inserts new IDs, inserts FTS5 rows
```
- FTS5: on AppendMessage, extract text from content JSON, insert into
  messages_fts. On SoftDeleteMessages, leave FTS5 rows (filtered at query time).
  On CloneMessages, insert FTS5 rows for cloned messages.

### `internal/store/api_calls.go`
```go
type APICall struct {
    ID              string
    SessionID       string
    Seq             int
    Model           string
    InputTokens     int
    OutputTokens    int
    CacheReadTokens int
    CacheWriteTokens int
    Cost            float64
    CreatedAt       int64
}

func (s *Store) RecordAPICall(call *APICall) error
func (s *Store) GetLastAPICall(sessionID string) (*APICall, error)
func (s *Store) GetSessionCost(sessionID string) (float64, error)
func (s *Store) GetTotalCost() (float64, error)
func (s *Store) GetSessionTokenBreakdown(sessionID string) (TokenBreakdown, error)
```

### `internal/store/search.go`
```go
type SearchResult struct {
    SessionID string
    MessageID string
    Role      string
    Snippet   string
    Rank      float64
}

func (s *Store) Search(query string, limit int) ([]SearchResult, error)
// FTS5 MATCH, JOIN messages, filter deleted_at IS NULL
```

### `internal/store/pricing.go`
```go
func (s *Store) SeedPricing(cfg *config.Config) error
func (s *Store) GetPricing(provider, model string) (Pricing, error)
func (s *Store) ComputeCost(provider, model string, input, output, cacheRead, cacheWrite int) float64
```

### Steps
1. Write store.go: Open, pragmas, schema creation
2. Write session.go: CRUD, test create/get/list
3. Write message.go: CRUD, FTS5 insert, test append/get/soft-delete/clone
4. Write api_calls.go: CRUD, cost aggregation, test record/get-cost
5. Write search.go: FTS5 query, test search returns correct results
6. Write pricing.go: seed from config, compute cost, test cost formula

### Checkpoint
```
go test ./internal/store/ -v
# All CRUD tests pass, FTS5 search works, cost computation correct
```

---

## Phase 4: Provider Interface + Ollama

**Goal:** Define the provider/tool types and implement the Ollama provider
with streaming.

### Files
- `internal/provider/provider.go` — Provider interface, Request, Response,
  StreamEvent, Usage, Tool interface, ToolDef, ContentBlock, ToolCall
- `internal/provider/ollama.go` — Ollama provider

### `internal/provider/provider.go`
```go
type Provider interface {
    ID() string
    Stream(ctx context.Context, req *Request) (<-chan StreamEvent, error)
    Models() ([]Model, error)
}

type Request struct {
    Model       string
    System      []SystemBlock
    Messages    []Message
    Tools       []ToolDef
    MaxTokens   int
    Temperature *float64
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
    Usage    *Usage
}

type Usage struct {
    InputTokens  int
    OutputTokens int
}

type AnthropicUsage struct {
    Usage
    CacheReadTokens  int
    CacheWriteTokens int
}

type Tool interface {
    Name() string
    Description() string
    Schema() json.RawMessage
    Execute(ctx context.Context, input json.RawMessage) (ToolResult, error)
}

type ToolResult struct {
    Content string
    Error   string
}

type SystemBlock struct {
    Text     string
    CacheCtl string
}

type Message struct {
    Role    string
    Content []ContentBlock
}

type ContentBlock struct {
    Type       string  // text | tool_use | tool_result
    Text       string
    ToolCallID string
    ToolName   string
    ToolInput  json.RawMessage
    ToolResult string
}

type ToolDef struct {
    Name        string
    Description string
    Schema      json.RawMessage
}

type ToolCall struct {
    ID    string
    Name  string
    Input json.RawMessage
}

type Model struct {
    ID           string
    Name         string
    ContextWindow int
}
```
- Channel lifecycle: producer goroutine uses `defer close(ch)`.
  `EventDone` or `EventError` is always last. On `ctx.Cancel()`, close
  immediately without done/error.

### `internal/provider/ollama.go`
```go
type OllamaProvider struct {
    baseURL string
    model   string
}

func NewOllamaProvider(baseURL, model string) *OllamaProvider
func (p *OllamaProvider) ID() string  // "ollama"
func (p *OllamaProvider) Stream(ctx, req) (<-chan StreamEvent, error)
func (p *OllamaProvider) Models() ([]Model, error)
```
- `POST {baseURL}/api/chat` with `stream: true`
- Map Request → Ollama format (messages, tools, options)
- Parse streaming NDJSON: each line is a JSON chunk
- `message.content` → EventTextDelta
- `message.tool_calls` → EventToolUseStart/Delta/Stop
- Final chunk (`done: true`): `prompt_eval_count` → Usage.InputTokens,
  `eval_count` → Usage.OutputTokens → EventDone
- `GET {baseURL}/api/tags` → Models()

### Steps
1. Write provider.go: all types, interfaces, constants
2. Write ollama.go: Stream implementation with NDJSON parsing
3. Test: stream a simple prompt to Ollama, verify text deltas + usage

### Checkpoint
```go
p := provider.NewOllamaProvider("http://localhost:11434", "gemma4:12b")
ch, _ := p.Stream(ctx, &provider.Request{
    Model: "gemma4:12b",
    Messages: []provider.Message{
        {Role: "user", Content: []provider.ContentBlock{{Type: "text", Text: "Say hi"}}},
    },
})
for ev := range ch {
    if ev.Type == provider.EventTextDelta { fmt.Print(ev.Text) }
    if ev.Type == provider.EventDone { fmt.Println(ev.Usage) }
}
// Output: Hi there! ... &{Input:14 Output:97}
```

---

## Phase 5: Tools (bash + guard, read, write, edit, search, ls, glob)

**Goal:** Implement all core tools with the Tool interface, including the
bash guard classifier.

### Files
- `internal/tools/registry.go` — ToolRegistry, registration, lookup
- `internal/guard/guard.go` — isAllSafe classifier
- `internal/guard/safe_list.go` — SAFE, DANGEROUS_TOKENS, SENSITIVE paths
- `internal/guard/detectors.go` — per-command danger detectors
- `internal/guard/segments.go` — command segment splitter
- `internal/tools/bash.go` — guarded bash execution
- `internal/tools/read.go` — read file (text + images)
- `internal/tools/write.go` — write file
- `internal/tools/edit.go` — exact text replacement
- `internal/tools/search.go` — ripgrep wrapper
- `internal/tools/ls.go` — directory listing
- `internal/tools/glob.go` — file pattern matching

### `internal/tools/registry.go`
```go
type Registry struct {
    tools map[string]provider.Tool
}

func NewRegistry() *Registry
func (r *Registry) Register(tool provider.Tool)
func (r *Registry) Get(name string) (provider.Tool, bool)
func (r *Registry) Definitions() []provider.ToolDef
func (r *Registry) Execute(ctx, name string, input json.RawMessage) (provider.ToolResult, error)
// Execute dispatches to a single tool
func (r *Registry) ExecuteParallel(ctx, calls []provider.ToolCall) ([]provider.ToolResult, error)
// ExecuteParallel dispatches all calls concurrently with sync.WaitGroup
```

### `internal/guard/` (port of bash-guard)
- `segments.go`: split on `;`, `|`, `&&`, `||`, `|()`
- `safe_list.go`: SAFE array, DANGEROUS_TOKENS set, DESTRUCTIVE_COMMANDS set,
  SENSITIVE_EXACT_BASENAMES, SENSITIVE_DIR_PATTERNS, SSH_PRIV_KEY_RE
- `detectors.go`: hasDangerousPatterns, containsAnsiEscape, touchesDotEnv,
  touchesEnv, touchesSensitivePath, findHasDangerousFlag, ghApiIsMutating,
  gitSubIsMutating, gitHasOutputFlag, rgHasDangerousFlag, sedHasDangerousFlag,
  sedScriptIsDangerous, treeHasDangerousFlag, yqHasDangerousFlag,
  tailHasFollowFlag
- `guard.go`:
  ```go
  func IsAllSafe(command string) bool
  func Classify(command string) (safe bool, reason string)
  ```

### `internal/tools/bash.go`
```go
type BashTool struct {
    cwd       string
    sandbox   bool  // POISSON_SANDBOX=1
    allowList []string  // session-level always-allow prefixes
}

func (t *BashTool) Name() string  // "bash"
func (t *BashTool) Execute(ctx, input) (ToolResult, error)
```
- Parse input: command, description, workdir, timeout
- If sandbox → skip guard
- If !IsAllSafe(command) → prompt for approval (callback function)
- Execute via `os/exec.CommandContext("bash", "-c", command)`
- Stream stdout/stderr
- Return `{stdout, stderr, exitCode}`

### `internal/tools/read.go`
- Read file, support offset/limit
- Truncate to 2000 lines or 50KB
- Return content as text

### `internal/tools/write.go`
- Create parent dirs with `os.MkdirAll`
- Write content, return success

### `internal/tools/edit.go`
- Read file, apply edits (exact text replacement)
- Verify each oldText is unique and exists
- Fail on non-unique or missing matches
- Write modified content

### `internal/tools/search.go`
- Wrap `rg --json` for structured output
- Parse JSON output, return matching lines with file paths + line numbers

### `internal/tools/ls.go`
- `os.ReadDir`, format with file type + size
- Support `all` (hidden files) and `recursive`

### `internal/tools/glob.go`
- Use `filepath.Glob` or doublestar pattern matching
- Return matching file paths

### Steps
1. Write guard/ (segments, safe_list, detectors, guard) — port from bash-guard
2. Test guard: IsAllSafe("git status") == true, IsAllSafe("rm -rf /") == false
3. Write registry.go with ExecuteParallel
4. Write bash.go with guard integration
5. Write read, write, edit, search, ls, glob
6. Test each tool independently

### Checkpoint
```
go test ./internal/guard/ -v
go test ./internal/tools/ -v
# Guard classifies correctly, all tools work
```

---

## Phase 6: Agent Loop

**Goal:** Turn cycle: ingest → build context → stream → dispatch tools →
compact if needed → commit. Parallel tool dispatch. Exact token/cost
recording.

### Files
- `internal/agent/agent.go` — Agent struct, turn loop
- `internal/agent/tokens.go` — context window tracking, % display
- `internal/agent/compaction.go` — auto-compaction (stub for now, full impl in Phase 11)

### `internal/agent/agent.go`
```go
type Agent struct {
    store       *store.Store
    provider    provider.Provider
    tools       *tools.Registry
    config      *config.Config
    sessionID   string
    outputChan  chan OutputEvent  // serialized terminal output
}

type OutputEvent struct {
    Type    string  // text | tool_start | tool_result | status | approval | error
    Text    string
    ToolName string
    ToolInput json.RawMessage
    ...
}

func NewAgent(...) *Agent
func (a *Agent) Prompt(userInput string) error
// The main turn loop
func (a *Agent) buildRequest() (*provider.Request, error)
// Load AGENTS.md (Phase 13), skills (Phase 12), system prompt,
// assemble messages from store (filter deleted_at IS NULL, compacted = 0,
// prepend compaction_summary to System if set)
func (a *Agent) runTurn() error
// Stream → collect tool calls → dispatch parallel → append results →
// check compaction → re-stream → commit
```

### Turn loop pseudocode (in `runTurn`):
```go
for {
    req := a.buildRequest()
    ch, err := a.provider.Stream(ctx, req)
    // read events:
    //   EventTextDelta → outputChan
    //   EventToolUseStart/Delta/Stop → collect tool calls
    //   EventDone → record api_call, append assistant message
    //   EventError → handle error
    
    if no tool calls { break }
    
    // dispatch all tool calls concurrently
    results := a.tools.ExecuteParallel(ctx, toolCalls)
    
    // append all tool_result messages to store
    for _, result := range results {
        a.store.AppendMessage(...)
    }
    
    // check compaction
    if a.shouldCompact() {
        a.compact()  // Phase 11 stub for now
    }
    
    // loop: re-stream with updated context
}
```

### `internal/agent/tokens.go`
```go
func (a *Agent) ContextPercent() float64
// last_api_call.input_tokens / context_window * 100
func (a *Agent) ContextTokens() (used, total int)
func (a *Agent) ShouldCompact() bool
// last_api_call.input_tokens + estimated_new_tokens > threshold * context_window
func (a *Agent) EstimateTokens(text string) int
// rough: len(text) / 4 — only for compaction trigger, never stored
```

### Steps
1. Write agent.go: Agent struct, Prompt(), buildRequest(), runTurn()
2. Write tokens.go: context tracking from last api_call
3. Wire up: Prompt → AppendMessage → buildRequest → Stream → dispatch → commit
4. Test with Ollama: send a prompt, verify turn loop works end-to-end
5. Test with tool calls: prompt that triggers bash/read, verify parallel dispatch

### Checkpoint
```go
agent := agent.NewAgent(store, ollamaProvider, toolRegistry, config, sessionID)
agent.Prompt("Read main.go and tell me what it does")
// Output: streams text, dispatches read tool, streams response
// Store: has user msg, assistant msg, tool msg, api_call row with exact tokens
```

---

## Phase 7: TUI (readline REPL)

**Goal:** Streaming readline REPL with status bar, slash commands,
@file expansion, single serialized output channel.

### Files
- `internal/tui/tui.go` — REPL loop, raw mode, line editor
- `internal/tui/render.go` — markdown-ish rendering, tool call display, status bar

### `internal/tui/tui.go`
```go
type TUI struct {
    outputChan  chan agent.OutputEvent
    inputReader *LineReader
    status      StatusBar
}

func NewTUI() *TUI
func (t *TUI) Run(agent *agent.Agent) error
// Main loop: read input → agent.Prompt → drain outputChan → render
func (t *TUI) handleSlashCommand(cmd string) error
// /new, /resume, /sessions, /search, /fork, /undo, /compact,
// /model, /reload, /clear, /help, /cost, /quit
```

### LineReader
- Raw mode via `golang.org/x/term.MakeRaw`
- Handle: Enter (submit), ↑/↓ (history), Tab (slash command completion),
  Ctrl+J (newline in multi-line), `\` at end of line (continuation)
- `@path` expansion: detect `@` followed by file path, read file, inline as
  code block before sending

### `internal/tui/render.go`
- Stream text deltas token-by-token
- Tool calls rendered inline: `[tool] path`, spinner, `✓` or `✗`
- Status bar: `[session] ctx: % (%/% tok) | $cost | provider/model`
- `⚠` warning at >75% context
- "compacting..." during compaction

### Output coordination
- Single `outputChan` for all terminal writes
- Agent writes to outputChan, TUI goroutine reads and renders
- Approval prompts go through the same channel — no concurrent terminal writes

### Steps
1. Write LineReader with raw mode, history, basic editing
2. Write render.go: text streaming, tool display, status bar
3. Wire outputChan: agent → channel → TUI render
4. Implement slash commands (stub /fork, /search for now — full impl in Phase 8)
5. Test: interactive REPL with Ollama, verify streaming + status bar

### Checkpoint
```
go run . 
poisson> What is 2+2?
Poisson: 2+2 equals 4.
[abc123] ctx: 1.2% (350/30,400) | $0.00 | ollama/gemma4:12b
poisson> /quit
```

---

## Phase 8: Session Commands

**Goal:** All slash commands working: /new, /resume, /sessions, /search,
/undo, /fork, /reload, /cost, /model, /clear, /help.

### Files
- Update `internal/tui/tui.go` — implement all slash command handlers
- Update `internal/agent/agent.go` — session switching support

### Commands to implement
- `/new` — create new session in store, switch agent to it
- `/resume <id>` — load session, switch agent, load messages
- `/sessions` — list sessions (paginated), show ID, title, date, message count
- `/search <query>` — FTS5 search, display results with session/message context
- `/fork [seq]` — show message list or fork from seq/latest, clone messages +
  FTS5 rows, switch to new session
- `/undo` — soft delete last user turn + subsequent messages, clear
  compaction_summary if needed, refuse to cross compaction boundary
- `/reload` — re-read config.toml, re-walk AGENTS.md, re-scan skills, rebuild
  system prompt on agent
- `/cost` — show session token breakdown (from api_calls) + total cost
- `/model <name>` — switch provider/model on agent + session
- `/clear` — clear terminal screen
- `/help` — list all commands
- `/quit` — exit

### Steps
1. Implement /new, /resume, /sessions (straightforward CRUD)
2. Implement /search (FTS5 query, format results)
3. Implement /undo (soft delete via store.SoftDeleteMessages)
4. Implement /fork (clone messages + FTS5, switch session)
5. Implement /cost (aggregate api_calls)
6. Implement /model, /clear, /help, /quit
7. Implement /reload (re-load config, re-discover resources)
8. Test each command interactively

### Checkpoint
```
poisson> /sessions
#  abc123  2024-01-15  14 msgs  claude-sonnet-4
#  def456  2024-01-14   3 msgs  gemma4:12b
poisson> /resume abc123
poisson> /undo
# Undid last turn (3 messages soft-deleted)
poisson> /cost
# Input: 12,847 tokens | Output: 3,213 tokens | Cost: $0.0892
```

---

## Phase 9: Anthropic Provider (API key + OAuth + stealth)

**Goal:** Anthropic Messages API with API key auth, then OAuth auth with
full stealth (billing header, system prompt sanitization, CC identity).

### Files
- `internal/provider/anthropic.go` — Anthropic provider, SSE streaming
- `internal/provider/anthropic_stealth.go` — cch hash, billing header,
  system prompt sanitizer, CC identity swap
- `internal/auth/auth.go` — token store (auth.json read/write)
- `internal/auth/anthropic_oauth.go` — PKCE, callback server, token exchange,
  refresh

### `internal/auth/auth.go`
```go
type AuthEntry struct {
    Type    string  // "oauth" | "api_key" | "none"
    Access  string
    Refresh string
    Expires int64
    Key     string
}

type AuthStore map[string]AuthEntry  // keyed by provider name

func LoadAuth() (AuthStore, error)       // read ~/.poisson/auth.json
func SaveAuth(store AuthStore) error     // write ~/.poisson/auth.json, chmod 0600
func IsOAuth(store AuthStore, provider string) bool
func GetAccessToken(store AuthStore, provider string) string
```

### `internal/auth/anthropic_oauth.go`
```go
func LoginAnthropic() (*AuthEntry, error)
// 1. Generate PKCE (crypto/rand → base64url, SHA-256 → base64url challenge)
// 2. Start callback server on 127.0.0.1:0 (OS-assigned port)
// 3. Build authorize URL, open browser
// 4. Wait for callback (channel), validate state == verifier
// 5. Exchange code for tokens (POST platform.claude.com/v1/oauth/token)
// 6. Return AuthEntry with access/refresh/expires

func RefreshAnthropicToken(refreshToken string) (*AuthEntry, error)
// POST token URL with grant_type=refresh_token
```
- Client ID: `9d1c250a-e61b-44d9-88ed-5944d1962f5e`
- Authorize URL: `https://claude.ai/oauth/authorize`
- Token URL: `https://platform.claude.com/v1/oauth/token`
- Scopes from SPEC §4.2
- Expiry: `now + expires_in*1000 - 5*60*1000`
- Browser open: `xdg-open` (Linux), `open` (macOS), `start` (Windows)
- Headless fallback: print URL, prompt for redirect URL paste

### `internal/provider/anthropic.go`
```go
type AnthropicProvider struct {
    baseURL string
    auth    auth.AuthStore
    config  *config.Config
}

func NewAnthropicProvider(auth auth.AuthStore, cfg *config.Config) *AnthropicProvider
func (p *AnthropicProvider) ID() string  // "anthropic"
func (p *AnthropicProvider) Stream(ctx, req) (<-chan StreamEvent, error)
func (p *AnthropicProvider) Models() ([]Model, error)
```
- `POST {baseURL}/v1/messages` with `stream: true`
- **API key auth**: `x-api-key`, `anthropic-version: 2023-06-01`
- **OAuth auth**: `Authorization: Bearer`, `anthropic-beta`,
  `user-agent: claude-cli/{version}`, `x-app: cli` + stealth transform
- Parse SSE: `event: content_block_delta` → text deltas,
  `event: message_start`/`message_delta` → usage,
  `event: message_stop` → EventDone
- Token refresh: if `expires` within 5 min, refresh before request
- Map Request → Anthropic format (system blocks, messages, tools)
- Map Anthropic tool_use → Poisson ToolCall
- Usage: `input_tokens`, `output_tokens`, `cache_read_input_tokens`,
  `cache_creation_input_tokens` → AnthropicUsage

### `internal/provider/anthropic_stealth.go`
```go
func (p *AnthropicProvider) applyStealth(req *provider.Request)
// Only if auth is OAuth
// 1. Sanitize system blocks (remove fingerprint paragraphs, inline replacements)
// 2. Insert CC identity as system[0]
// 3. Compute billing header, prepend as system[0]
// Final: [billing_header, cc_identity, actual_system_prompt]

func computeCCH(firstUserMessageText string, cfg *config.StealthConfig) string
// SHA-256(text)[:5]

func computeVersionSuffix(text string, cfg *config.StealthConfig) string
// SHA-256(salt + sampledChars + version)[:3]
// sampledChars = chars at cch_positions, "0" if out of bounds

func buildBillingHeaderValue(messages []provider.Message, cfg *config.StealthConfig) string
// "x-anthropic-billing-header: cc_version={ver}.{suffix}; cc_entrypoint={ep}; cch={cch};"

func sanitizeSystemText(text string) string
// Split on \n\n+, drop paragraphs with fingerprint anchors
// Apply inline text replacements

func stealthHealthCheck(p *AnthropicProvider) error
// Send 1-token probe, verify 200 OK
// If fail: return error, caller falls back to API key mode
```

### Steps
1. Write auth.go: auth.json load/save, token check helpers
2. Write anthropic_oauth.go: PKCE, callback server, token exchange, refresh
3. Test: `px login anthropic` flow (manual test, browser)
4. Write anthropic.go: API key auth first, SSE parsing, streaming
5. Test: stream a prompt with API key, verify text + usage
6. Write anthropic_stealth.go: billing header, sanitizer, CC identity
7. Wire stealth into anthropic.go Stream() for OAuth path
8. Test: stream a prompt with OAuth, verify stealth headers in request
9. Implement stealth health check on startup

### Checkpoint
```
px login anthropic
# Browser opens → login → "Logged in to Anthropic (Claude Pro/Max)"
px --provider anthropic -p "What is 2+2?"
# 2+2 equals 4.
# (stealth active, subscription billing used)
```

---

## Phase 10: Anthropic OAuth Login CLI

**Goal:** `px login anthropic` and `px logout anthropic` commands.

### Files
- Update `main.go` — handle `login`/`logout` subcommands

### Steps
1. In main.go arg parsing: if args[0] == "login" → call auth.LoginAnthropic()
2. If args[0] == "logout" → clear auth entry, save auth.json
3. Print success/failure messages
4. Handle headless fallback (print URL, read paste from stdin)

### Checkpoint
```
px login anthropic   # browser flow
px logout anthropic  # "Logged out"
```

---

## Phase 11: Compaction

**Goal:** Mid-turn auto-compaction with configurable model and overflow
handling.

### Files
- `internal/agent/compaction.go` — full implementation

### `internal/agent/compaction.go`
```go
func (a *Agent) shouldCompact() bool
// last_api_call.input_tokens + estimate(new tool results) > threshold * window

func (a *Agent) compact() error
// 1. Collect active messages
// 2. If conversation > 50% of window: summarize oldest half only
// 3. Build summarization request (handoff prompt from SPEC §13.3)
// 4. Stream summary from compaction model (config.Compaction.Model or session model)
// 5. Mark old messages compacted = 1
// 6. Store summary on sessions.compaction_summary
// 7. Record api_call for summarization (exact tokens + cost)
// 8. Record compaction row
// 9. Update status bar
// 10. Return — caller re-streams with compacted context
```

### Overflow handling
- If active messages' estimated tokens > 50% of context window:
  - Summarize only the oldest half of messages
  - Keep recent half verbatim (not marked compacted)
  - Store summary on session
- This prevents the summarization request itself from exceeding the window

### Steps
1. Write shouldCompact() with estimation logic
2. Write compact() with summarization request
3. Implement overflow handling (oldest half strategy)
4. Wire into agent loop (step 4d in SPEC §17.1)
5. Test: fill context to 80% with repeated tool calls, verify compaction fires
6. Test: verify compacted messages are excluded from context but visible to /fork
7. Test: verify compaction summary is prepended to Request.System

### Checkpoint
```
poisson> Read every file in this directory and summarize each one
# (agent reads 20 files, context fills to 80%)
# Status bar: "compacting... (summarizing 35 messages)"
# (compaction completes, agent continues with compacted context)
# Status bar: "ctx: 15.2% (4,620/30,400 tok)"
```

---

## Phase 12: Skills

**Goal:** Discover SKILL.md files, inject into system prompt, skill tool.

### Files
- `internal/skills/skills.go` — discovery, frontmatter parsing, system prompt listing
- `internal/tools/skill.go` — skill tool

### `internal/skills/skills.go`
```go
type Skill struct {
    Name        string
    Description string
    ArgumentHint string
    FilePath    string
    BaseDir     string
    Body        string  // frontmatter stripped
}

func Discover() ([]Skill, error)
// Scan ~/.poisson/skills/*/SKILL.md
// Parse YAML frontmatter (name, description, argument-hint)
// Strip frontmatter, store body

func FormatSkillsForPrompt(skills []Skill) string
// "Available skills:\n- name: description\n- ..."
```

### `internal/tools/skill.go`
```go
type SkillTool struct {
    skills map[string]*skills.Skill
}

func (t *SkillTool) Name() string  // "skill"
func (t *SkillTool) Execute(ctx, input) (ToolResult, error)
// 1. Parse input: name, args
// 2. Look up skill by name
// 3. Return skill.Body + args as tool result
```

### Steps
1. Write skills.go: Discover(), frontmatter parser, FormatSkillsForPrompt()
2. Write skill.go: SkillTool
3. Wire into agent.buildRequest(): append skills list to system prompt
4. Register skill tool in registry
5. Test: create a test skill, verify it appears in system prompt, verify
   agent can invoke it

### Checkpoint
```
mkdir -p ~/.poisson/skills/test-skill
echo '---
description: "Test skill"
---
Do the thing.' > ~/.poisson/skills/test-skill/SKILL.md

poisson> /reload
poisson> Use the test skill
# Agent calls skill tool, gets "Do the thing.", follows it
```

---

## Phase 13: AGENTS.md Discovery

**Goal:** Walk cwd→root, collect AGENTS.md files, inject into system prompt.

### Files
- `internal/project/discover.go` — AGENTS.md discovery
- `internal/project/prompt.go` — system prompt assembly

### `internal/project/discover.go`
```go
type ContextFile struct {
    Path    string
    Content string
}

func LoadProjectContextFiles(cwd, agentDir string) ([]ContextFile, error)
// 1. Check agentDir (~/.poisson/) for AGENTS.md, CLAUDE.md
// 2. Walk cwd → /, collect AGENTS.md, CLAUDE.md (dedup, root-first)
// 3. Return: [global, ...ancestors_root_to_cwd]
```

### `internal/project/prompt.go`
```go
func BuildSystemPrompt(opts BuildSystemPromptOptions) string
// Assemble: base prompt + tools list + guidelines +
//   <project_context> with context files +
//   skills list (from Phase 12) +
//   date + cwd
```

### Steps
1. Write discover.go: walk logic, candidate filenames, dedup
2. Write prompt.go: assemble full system prompt with context files
3. Wire into agent.buildRequest(): call LoadProjectContextFiles + BuildSystemPrompt
4. Test: create AGENTS.md in cwd, verify it appears in system prompt
5. Test: create AGENTS.md in parent dir, verify both are loaded (parent first)

### Checkpoint
```
echo '# Project Rules
Always use tabs.' > AGENTS.md

poisson> /reload
poisson> What are the project rules?
# Agent responds with: "Always use tabs."
```

---

## Phase 14: Subagents

**Goal:** Spawn child px processes, JSON-line protocol, approval forwarding
via stdin/stdout pipes.

### Files
- `internal/subagent/spawn.go` — spawn child, read stdout, write stdin
- `internal/tools/subagent.go` — subagent tool

### `internal/subagent/spawn.go`
```go
type ChildProcess struct {
    cmd    *exec.Cmd
    stdin  io.WriteCloser
    stdout io.Reader
}

type ChildEvent struct {
    Type        string  // progress | tool | approval_request | text | done
    Text        string
    Tool        string
    ToolInput   json.RawMessage
    Command     string
    Description string
    Cwd         string
    Agent       string
    Success     bool
    ToolCount   int
    Turns       int
    ContextTokens int
}

func Spawn(input SpawnInput) (*ChildProcess, error)
// exec.Command("Poisson", "--json", "--tools", "read,write,edit,bash,search,ls,glob",
//   "--no-skills", "--session", childSessionID, task)
// env: POISSON_SUBAGENT_CHILD=1, POISSON_SUBAGENT_NAME=name
// if POISSON_SANDBOX=1 → set on child env too

func (c *ChildProcess) ReadEvent() (*ChildEvent, error)
// Read one JSON line from stdout, parse

func (c *ChildProcess) SendApproval(approved bool) error
// Write {"type":"approval_response","approved":true} to stdin

func (c *ChildProcess) Wait() error
// Wait for process exit
```

### `internal/tools/subagent.go`
```go
type SubagentTool struct {
    cwd        string
    store      *store.Store
    outputChan chan agent.OutputEvent
    approvalFn func(command, description, cwd, agent string) bool
}

func (t *SubagentTool) Name() string  // "subagent"
func (t *SubagentTool) Execute(ctx, input) (ToolResult, error)
// 1. Parse input: task, name
// 2. Create child session in store (is_subagent = 1)
// 3. Spawn child process
// 4. Read events loop:
//    - progress/text → outputChan (stream to TUI)
//    - tool → outputChan (show tool call)
//    - approval_request → call approvalFn, SendApproval()
//    - done → break
// 5. Return child's final output as tool result
```

### Child mode handling in main.go
- If `POISSON_SUBAGENT_CHILD=1`:
  - Read task from args
  - Run agent in JSON output mode (write events to stdout, read approval
    responses from stdin)
  - No TUI, no interactive REPL
  - Bash guard: if not sandbox, send approval_request to stdout, block on
    stdin for response (30s timeout → auto-deny)

### Steps
1. Write spawn.go: Spawn, ReadEvent, SendApproval, Wait
2. Write subagent.go: SubagentTool with event loop + approval forwarding
3. Add child mode to main.go: JSON output, stdin approval reading
4. Register subagent tool in registry (only in parent mode, not child mode)
5. Test: spawn subagent with simple task, verify output
6. Test: subagent hits unsafe bash → approval request → parent prompts →
   response forwarded → child continues or denies
7. Test: 30s approval timeout → auto-deny
8. Test: parent dies → child auto-denies (stdin closed)

### Checkpoint
```
poisson> Use a subagent to read main.go and summarize it
  [subagent: Torvalds] starting...
  [subagent: Torvalds] [read] main.go
  [subagent: Torvalds] main.go contains the entry point...
  Subagent finished. 1 tool calls, 1 turns.
Poisson: The subagent reports that main.go is the entry point...
```

---

## Phase 15: xAI Provider (OAuth + Grok models)

**Goal:** xAI Grok provider with browser + device-code OAuth.

### Files
- `internal/provider/xai.go` — xAI provider
- `internal/auth/xai_oauth.go` — xAI OAuth (browser + device-code)

### `internal/auth/xai_oauth.go`
```go
func LoginXAI() (*AuthEntry, error)
// Prompt: browser vs device-code
// Browser: PKCE, callback server on 127.0.0.1:0, exchange code
// Device-code: POST device/code, poll token URL

func RefreshXAIToken(refreshToken string) (*AuthEntry, error)
```
- Client ID: `b1a00492-073a-47ea-816f-4c329264a828`
- Authorize URL: `https://auth.x.ai/oauth2/authorize`
- Token URL: `https://auth.x.ai/oauth2/token`
- Device code URL: `https://auth.x.ai/oauth2/device/code`
- Scope: `openid profile email offline_access grok-cli:access api:access`
- Auto-refresh on 401 using refresh token

### `internal/provider/xai.go`
```go
type XAIProvider struct {
    auth   auth.AuthStore
    config *config.Config
}

func NewXAIProvider(auth auth.AuthStore, cfg *config.Config) *XAIProvider
func (p *XAIProvider) ID() string  // "xai"
func (p *XAIProvider) Stream(ctx, req) (<-chan StreamEvent, error)
func (p *XAIProvider) Models() ([]Model, error)
```
- OpenAI-compatible endpoint: `POST https://api.x.ai/v1/chat/completions`
- OAuth auth: `Authorization: Bearer {access_token}`
- Auto-refresh on 401: refresh token, retry once
- Parse SSE streaming (OpenAI format)
- Map OpenAI tool_calls → Poisson ToolCall
- Usage: `usage.prompt_tokens` → InputTokens, `usage.completion_tokens` → OutputTokens

### Steps
1. Write xai_oauth.go: browser flow + device-code flow
2. Test: `px login xai` (both flows)
3. Write xai.go: streaming, tool mapping, usage parsing
4. Test: stream a prompt with OAuth, verify text + usage
5. Test: token refresh on expired token

### Checkpoint
```
px login xai
# Browser: "Authorization Successful"
px --provider xai -p "What is 2+2?"
# 2+2 equals 4.
```

---

## Phase 16: Network Tools (fetch + exa_search)

**Goal:** fetch (Ollama web_fetch) and exa_search (exa.ai) tools.

### Files
- `internal/tools/fetch.go` — Ollama web_fetch wrapper
- `internal/tools/exa_search.go` — exa.ai search with JWT

### `internal/tools/fetch.go`
```go
type FetchTool struct {
    ollamaBaseURL string
}

func (t *FetchTool) Name() string  // "fetch"
func (t *FetchTool) Execute(ctx, input) (ToolResult, error)
// POST {ollamaBaseURL}/api/fetch with {"url": "..."}
// Return extracted text
```
- **Only register** when Ollama is configured or detected as running

### `internal/tools/exa_search.go`
```go
type ExaSearchTool struct{}

func (t *ExaSearchTool) Name() string  // "exa_search"
func (t *ExaSearchTool) Execute(ctx, input) (ToolResult, error)
// 1. Get JWT: load from ~/.poisson/exa-token.json cache, or POST /api/token/issue
// 2. POST /api/search with Authorization: Bearer {jwt}
// 3. On 401: re-issue JWT, retry once
// 4. On 429: return "exa_search rate limited" error
// 5. Return results (titles, URLs, excerpts, AI summary)
```
- Token URL: `https://exa.ai/api/token/issue`
- Search URL: `https://exa.ai/api/search`
- Cache JWT in `~/.poisson/exa-token.json` with 10s safety margin
- Headers: `User-Agent: Mozilla/5.0 ...`, `Origin: https://exa.ai`, `Referer: https://exa.ai/`

### Steps
1. Write fetch.go, test with Ollama running
2. Write exa_search.go: token issue + cache + search + retry
3. Test exa_search: verify results returned
4. Test JWT retry: expire token, verify re-issue + retry
5. Test rate limit: verify graceful error on 429
6. Wire conditional registration: fetch only when Ollama available

### Checkpoint
```
poisson> fetch https://example.com
# Returns page text content

poisson> exa_search "what is the TOON format"
# Returns search results with titles, URLs, excerpts
```

---

## Phase 17: Fork

**Goal:** /fork command with message selection, FTS5 row cloning.

### Files
- Update `internal/tui/tui.go` — fork command with message picker
- Update `internal/store/message.go` — CloneMessages (may already exist from Phase 3)

### Steps
1. `/fork` with no arg: fork from latest active message
2. `/fork <seq>`: fork from specific seq
3. `/fork` with no arg + interactive: show numbered message list, user selects
4. CloneMessages: copy active messages up to fork point, new UUIDs, new session,
   insert FTS5 rows, copy compaction_summary if fork point is after compaction
5. Set parent_id + fork_point on new session
6. Switch agent to new session
7. Test: fork a session, verify messages are independent copies
8. Test: fork after compaction, verify compacted messages are accessible in fork
9. Test: verify FTS5 search finds both original and forked messages

### Checkpoint
```
poisson> /fork
  1. [user] Read main.go
  2. [assistant] main.go is the entry point...
  3. [user] Now fix the bug
  4. [assistant] I'll edit main.go...
Select message to fork from (1-4): 2
# Forked to new session from message 2
[def789] ctx: 0.8% (240/30,400) | $0.0000 | anthropic/claude-sonnet-4
poisson> 
```

---

## Phase 18: Final Integration + Polish

**Goal:** End-to-end testing, edge cases, error handling polish.

### Tasks
1. **stdin support**: if stdin is not a TTY, read and append to prompt
2. **`px cost` CLI**: aggregate cost across all sessions / per session
3. **`px sessions` CLI**: list sessions from CLI (non-interactive)
4. **`px search` CLI**: search from CLI
5. **OAuth callback port fallback**: try default port, fall back to port 0
6. **Stealth health check**: probe on startup, fallback to API key
7. **Error messages**: make all error messages user-friendly
8. **Config generation**: if no config.toml, create one with commented defaults
9. **Context window detection**: query model context window from provider
10. **Status bar**: verify context %, cost, model display work for all providers
11. **Compaction edge cases**: compact at exactly 80%, compact with 0 messages,
    compact with huge tool result
12. **Subagent edge cases**: child crashes, approval timeout, parent dies
13. **Fork edge cases**: fork from compacted message, fork after undo

### Final checkpoint
```
go build ./...                           # compiles
go test ./... -v                         # all tests pass
px login anthropic                      # OAuth works
px --provider ollama -p "hello"         # Ollama works
px --provider anthropic -p "hello"      # Anthropic + stealth works
px --provider xai -p "hello"            # xAI works
px                                       # interactive REPL
poisson> /sessions                           # session management
poisson> /search "hello"                     # FTS5 search
poisson> /fork                               # fork works
poisson> /undo                               # undo works
poisson> /cost                               # cost tracking
poisson> /reload                             # reload works
poisson> use a subagent to read main.go      # subagent works
poisson> fetch https://example.com           # fetch works (with Ollama)
poisson> exa_search "test query"             # exa search works
poisson> /quit                               # clean exit
```