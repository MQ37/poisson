package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/mq37/poisson/internal/auth"
	"github.com/mq37/poisson/internal/config"
)

// AnthropicProvider implements the Provider interface for Anthropic's
// Messages API. It supports both API key and OAuth authentication.
// When OAuth is active, stealth transformations are applied (see
// anthropic_stealth.go).
type AnthropicProvider struct {
	baseURL string
	auth    auth.AuthStore
	config  *config.Config
	client  *http.Client

	usageMu    sync.Mutex
	usageCache *AnthropicUsageLimits // see anthropic_usage.go
}

// NewAnthropicProvider creates an Anthropic provider with the given auth
// store and config. The baseURL defaults to https://api.anthropic.com.
func NewAnthropicProvider(a auth.AuthStore, cfg *config.Config) *AnthropicProvider {
	baseURL := "https://api.anthropic.com"
	if cfg != nil && cfg.Anthropic.APIKey != "" {
		// Could have a custom baseURL in the future
	}
	return &AnthropicProvider{
		baseURL: baseURL,
		auth:    a,
		config:  cfg,
		client:  &http.Client{},
	}
}

// ID returns "anthropic".
func (p *AnthropicProvider) ID() string { return "anthropic" }

// stealthUserAgent returns the "claude-cli/<version> (external, <entrypoint>)"
// user-agent spoofed on stealth OAuth requests — the parenthesized suffix is
// real (cc-sniff capture: "claude-cli/2.1.201 (external, sdk-cli)"), not
// decorative; a bare "claude-cli/<version>" is a different, wrong shape.
// Shared by anthropic.go and anthropic_usage.go so the fallback lives in one
// place, not two copy-pasted literals.
func stealthUserAgent(cfg *config.Config) string {
	sc := config.DefaultStealthConfig()
	if cfg != nil {
		sc = cfg.Stealth
	}
	return "claude-cli/" + sc.CCVersion + " (external, " + sc.CCEntrypoint + ")"
}

// Models returns the curated Anthropic models (see KnownModels in models.go —
// the single source of truth for IDs/context windows, so this list can't
// drift out of sync with it).
func (p *AnthropicProvider) Models() ([]Model, error) {
	if m := CuratedModels("anthropic"); len(m) > 0 {
		return m, nil
	}
	return []Model{{ID: "claude-opus-5", Name: "Claude Opus 5", ContextWindow: 1000000}}, nil
}

// Stream sends a request to the Anthropic Messages API and returns a channel
// of StreamEvents. It handles both API key and OAuth authentication, and
// applies stealth transformations when OAuth is active.
func (p *AnthropicProvider) Stream(ctx context.Context, req *Request) (<-chan StreamEvent, error) {
	return p.streamWithRetry(ctx, req, 0)
}

// refreshOAuthIfNeeded reports whether auth is OAuth, refreshing a token
// that expires within 5 minutes first. All auth-map access is guarded by
// auth.StoreMu — a single process-wide lock, not a private-to-this-struct
// mutex — because the same underlying AuthStore map is also written by
// XAIProvider/WebAskTool's grok backend (see auth.StoreMu's doc comment for
// why a per-provider-instance mutex isn't enough). The refresh itself goes
// through auth.RefreshIfExpired, which is also cross-process safe (a
// subagent child racing this same refresh against its parent, or two
// independent px instances) — see its doc comment.
func (p *AnthropicProvider) refreshOAuthIfNeeded() bool {
	auth.StoreMu.Lock()
	defer auth.StoreMu.Unlock()
	isOAuth := auth.IsOAuth(p.auth, "anthropic")
	if isOAuth {
		if _, err := auth.RefreshIfExpired(p.auth, "anthropic", 5*60*1000, auth.RefreshAnthropicToken); err != nil {
			log.Printf("warning: refresh anthropic auth: %v", err)
		}
	}
	return isOAuth
}

// forceRefreshOAuth refreshes the OAuth token unconditionally — used after a
// 401, where the stored token is known bad regardless of its expiry field.
// Goes through auth.ForceRefresh (cross-process safe, see its doc): if
// another process already refreshed this same entry in the meantime, this
// adopts that result instead of racing its own (possibly already-rotated
// and therefore failing) refresh_token.
func (p *AnthropicProvider) forceRefreshOAuth() error {
	auth.StoreMu.Lock()
	defer auth.StoreMu.Unlock()
	_, err := auth.ForceRefresh(p.auth, "anthropic", auth.RefreshAnthropicToken)
	return err
}

func (p *AnthropicProvider) streamWithRetry(ctx context.Context, req *Request, retry int) (<-chan StreamEvent, error) {
	isOAuth := p.refreshOAuthIfNeeded()

	streamReq := req
	if isOAuth {
		streamReq = cloneRequest(req)
		p.applyStealth(streamReq)
	}

	anthropicReq := p.buildAnthropicRequest(streamReq, isOAuth)
	reqBody, err := json.Marshal(anthropicReq)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	adaptive := anthropicReq.Thinking != nil && anthropicReq.Thinking.Type == "adaptive"

	// requestID is stable across retries of this call (a real client reuses
	// its x-client-request-id and only bumps X-Stainless-Retry-Count), attemptN
	// is not.
	requestID := newUUIDv4()
	attemptN := 0

	// DoWithRetry retries connection failures and transient server errors
	// (429/5xx, plus Anthropic's 529 "overloaded_error") with exponential
	// backoff, indefinitely, before this function ever returns — the 401
	// refresh-and-retry-once logic right below is unaffected, since it only
	// ever sees the final response DoWithRetry settles on. A fresh request is
	// built on every attempt (reqBody is only marshaled once above, and
	// bytes.NewReader over it is safe to create repeatedly) so a body already
	// consumed by a failed attempt is never resent short or empty.
	resp, err := DoWithRetry(ctx, AnthropicRetryableStatus, func(attemptCtx context.Context) (*http.Response, error) {
		// ?beta=true matches every real Claude Code /v1/messages capture
		// (cc-sniff 0011-0015) — omitting it is itself a fingerprintable
		// difference from genuine traffic.
		httpReq, err := http.NewRequestWithContext(attemptCtx, "POST",
			p.baseURL+"/v1/messages?beta=true", bytes.NewReader(reqBody))
		if err != nil {
			return nil, err
		}
		p.setHeaders(httpReq, isOAuth, adaptive, attemptN, requestID)
		attemptN++
		return p.client.Do(httpReq)
	})
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == 401 && isOAuth && retry == 0 {
		resp.Body.Close()
		if err := p.forceRefreshOAuth(); err != nil {
			return nil, fmt.Errorf("token expired, refresh failed: %w", err)
		}
		return p.streamWithRetry(ctx, req, 1)
	}

	if resp.StatusCode != 200 {
		body, _ := readCappedBody(resp)
		resp.Body.Close()
		return nil, fmt.Errorf("anthropic API error (status %d): %s", resp.StatusCode, string(body))
	}

	ch := make(chan StreamEvent, 64)
	go p.pumpSSE(ctx, resp.Body, ch, isOAuth)
	return ch, nil
}

func cloneRequest(req *Request) *Request {
	if req == nil {
		return nil
	}
	cp := *req
	if len(req.System) > 0 {
		cp.System = append([]SystemBlock(nil), req.System...)
	}
	if len(req.Messages) > 0 {
		cp.Messages = append([]Message(nil), req.Messages...)
	}
	if len(req.Tools) > 0 {
		cp.Tools = append([]ToolDef(nil), req.Tools...)
	}
	return &cp
}

// anthropicRequest is the JSON body sent to the Messages API.
type anthropicRequest struct {
	Model        string                 `json:"model"`
	Messages     []anthropicMessage     `json:"messages"`
	System       []anthropicSystem      `json:"system,omitempty"`
	Tools        []anthropicTool        `json:"tools,omitempty"`
	MaxTokens    int                    `json:"max_tokens"`
	Stream       bool                   `json:"stream"`
	Temperature  *float64               `json:"temperature,omitempty"`
	Thinking     *anthropicThinking     `json:"thinking,omitempty"`
	OutputConfig *anthropicOutputConfig `json:"output_config,omitempty"`
}

type anthropicThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
	Display      string `json:"display,omitempty"`
}

type anthropicOutputConfig struct {
	Effort string `json:"effort"`
}

type anthropicMessage struct {
	Role    string                  `json:"role"`
	Content []anthropicContentBlock `json:"content"`
}

type anthropicContentBlock struct {
	Type      string                `json:"type"`
	Text      string                `json:"text,omitempty"`
	ID        string                `json:"id,omitempty"`
	Name      string                `json:"name,omitempty"`
	Input     json.RawMessage       `json:"input,omitempty"`
	ToolUseID string                `json:"tool_use_id,omitempty"`
	Content   json.RawMessage       `json:"content,omitempty"`   // for tool_result
	IsError   bool                  `json:"is_error,omitempty"`  // for tool_result
	Thinking  string                `json:"thinking,omitempty"`  // for thinking
	Signature string                `json:"signature,omitempty"` // for thinking
	Data      string                `json:"data,omitempty"`      // for redacted_thinking
	Source    *anthropicImageSource `json:"source,omitempty"`    // for image

	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

// anthropicCacheControl marks a prompt-cache breakpoint. Anthropic requires an
// object (`{"type":"ephemeral"}`), not a bare string.
type anthropicCacheControl struct {
	Type string `json:"type"`          // "ephemeral"
	TTL  string `json:"ttl,omitempty"` // "" = 5m default, or "1h" (needs beta header)
}

type anthropicImageSource struct {
	Type      string `json:"type"`       // "base64"
	MediaType string `json:"media_type"` // "image/png"
	Data      string `json:"data"`       // base64-encoded bytes
}

type anthropicSystem struct {
	Type         string                 `json:"type"`
	Text         string                 `json:"text"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

type anthropicTool struct {
	Name         string                 `json:"name"`
	Description  string                 `json:"description"`
	InputSchema  json.RawMessage        `json:"input_schema"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

// anthropicMaxOutputTokens is the output-token ceiling px requests. max_tokens
// is required by the API and *includes* thinking tokens, so it must dwarf the
// thinking budget to leave room for the answer. The old budget+1024 cap let the
// model spend the whole allowance on thinking and return nothing
// (stop_reason=max_tokens, no content). 64000 matches what the real Claude Code
// client requests for this model. Billing is by actual output, so a high cap
// costs nothing extra on short replies.
const anthropicMaxOutputTokens = 64000

// anthropicAdaptiveEffort maps a Poisson effort level to an Anthropic
// output_config.effort value. "max" clamps to "xhigh" — the highest value the
// real Claude Code client is observed to send. "" (unknown/off) disables it.
func anthropicAdaptiveEffort(effort string) string {
	switch effort {
	case "low", "medium", "high", "xhigh":
		return effort
	case "max":
		return "xhigh"
	default:
		return ""
	}
}

func anthropicThinkingBudget(effort string) int {
	switch effort {
	case "low":
		return 1024
	case "medium":
		return 2048
	case "high":
		return 4096
	case "xhigh":
		return 8192
	case "max":
		return 16384
	default:
		return 0
	}
}

// buildAnthropicRequest converts a Poisson Request to the Anthropic API format.
func (p *AnthropicProvider) buildAnthropicRequest(req *Request, isOAuth bool) anthropicRequest {
	ar := anthropicRequest{
		Model:       req.Model,
		MaxTokens:   req.MaxTokens,
		Stream:      true,
		Temperature: req.Temperature,
	}
	if ar.MaxTokens == 0 {
		ar.MaxTokens = anthropicMaxOutputTokens
	}
	settings, _ := MergedModelSettings(p.config, "anthropic", req.Model)
	if effort := anthropicAdaptiveEffort(req.Effort); settings.AdaptiveThinking && effort != "" {
		// Adaptive reasoning: the model decides how much to think, steered by
		// output_config.effort — exactly what the real Claude Code client sends.
		ar.Thinking = &anthropicThinking{Type: "adaptive", Display: "summarized"}
		ar.OutputConfig = &anthropicOutputConfig{Effort: effort}
		ar.Temperature = nil
		if ar.MaxTokens < anthropicMaxOutputTokens {
			ar.MaxTokens = anthropicMaxOutputTokens
		}
	} else if budget := anthropicThinkingBudget(req.Effort); budget > 0 {
		ar.Thinking = &anthropicThinking{Type: "enabled", BudgetTokens: budget}
		ar.Temperature = nil
		// max_tokens must exceed the thinking budget with ample room for the
		// answer; jump to the full ceiling if a caller passed a tiny max_tokens.
		if ar.MaxTokens <= budget {
			ar.MaxTokens = anthropicMaxOutputTokens
		}
	}

	// System blocks.
	for _, sb := range req.System {
		ar.System = append(ar.System, anthropicSystem{Type: "text", Text: sb.Text})
	}

	// Messages. The Anthropic API only accepts "user" and "assistant" roles;
	// tool results are sent as tool_result content blocks inside a user message.
	// Consecutive tool results are coalesced into one user message so roles
	// keep alternating (required when the model made parallel tool calls).
	for i := 0; i < len(req.Messages); i++ {
		msg := req.Messages[i]

		if msg.Role == "tool" {
			var blocks []anthropicContentBlock
			for j := i; j < len(req.Messages) && req.Messages[j].Role == "tool"; j++ {
				// A tool that loaded an image (currently only `read` on an
				// image file) carries it as a sibling "image" content block
				// right after its "tool_result" block in the SAME store
				// message (see agent.go) — never inlined as base64 text.
				// Anthropic's own documented shape for a tool_result that
				// includes an image nests it INSIDE that tool_result's own
				// "content" array (a well-known pattern from Anthropic's
				// computer-use tool), so pair each image with the
				// tool_result immediately preceding it here, rather than
				// appending it as an unrelated top-level block.
				var pending *anthropicContentBlock
				var pendingText string
				flush := func() {
					if pending == nil {
						return
					}
					blocks = append(blocks, *pending)
					pending = nil
				}
				for _, cb := range req.Messages[j].Content {
					switch cb.Type {
					case "tool_result":
						flush()
						tr := anthropicContentBlock{Type: "tool_result", ToolUseID: cb.ToolCallID, IsError: cb.ToolIsError}
						raw, _ := json.Marshal(cb.ToolResult)
						tr.Content = raw
						pendingText = cb.ToolResult
						pending = &tr
					case "image":
						if pending == nil {
							continue
						}
						data, mt, ok := imageBlockBase64(cb)
						if !ok {
							continue
						}
						nested, _ := json.Marshal([]anthropicContentBlock{
							{Type: "text", Text: pendingText},
							{Type: "image", Source: &anthropicImageSource{Type: "base64", MediaType: mt, Data: data}},
						})
						pending.Content = nested
					}
				}
				flush()
				i = j
			}
			// blocks is nil if every grouped tool message's content failed the
			// tool_result filter above. Content has no `omitempty`, so a nil
			// slice marshals as `"content":null`, which Anthropic rejects
			// ("messages.N.content: Input should be a valid array") — and an
			// empty (non-nil) array is rejected too, since Anthropic requires
			// at least one content block per message. Fall back to a
			// placeholder so the turn stays real.
			if len(blocks) == 0 {
				blocks = []anthropicContentBlock{{Type: "text", Text: "[no tool output]"}}
			}
			ar.Messages = append(ar.Messages, anthropicMessage{Role: "user", Content: blocks})
			continue
		}

		// Thinking blocks may only be replayed when thinking is enabled for this
		// request; sending them otherwise is rejected, and dropping them while
		// thinking is enabled is also rejected for assistant turns with tool_use.
		thinkingEnabled := ar.Thinking != nil

		am := anthropicMessage{Role: msg.Role}
		for _, cb := range msg.Content {
			switch cb.Type {
			case "thinking":
				if !thinkingEnabled {
					continue
				}
				if cb.Redacted {
					am.Content = append(am.Content, anthropicContentBlock{
						Type: "redacted_thinking", Data: cb.ThinkingSignature,
					})
					continue
				}
				if cb.ThinkingSignature == "" {
					// No signature (e.g. aborted stream) — Anthropic rejects an
					// unsigned thinking block, so degrade it to plain text.
					if cb.Thinking != "" {
						am.Content = append(am.Content, anthropicContentBlock{Type: "text", Text: cb.Thinking})
					}
					continue
				}
				am.Content = append(am.Content, anthropicContentBlock{
					Type: "thinking", Thinking: cb.Thinking, Signature: cb.ThinkingSignature,
				})
			case "text":
				am.Content = append(am.Content, anthropicContentBlock{
					Type: "text", Text: cb.Text,
				})
			case "image":
				if data, mt, ok := imageBlockBase64(cb); ok {
					am.Content = append(am.Content, anthropicContentBlock{
						Type:   "image",
						Source: &anthropicImageSource{Type: "base64", MediaType: mt, Data: data},
					})
				}
			case "tool_use":
				name := cb.ToolName
				if isOAuth {
					name = prefixToolName(name)
				}
				am.Content = append(am.Content, anthropicContentBlock{
					Type: "tool_use", ID: cb.ToolCallID, Name: name, Input: cb.ToolInput,
				})
			case "tool_result":
				resultContent, _ := json.Marshal(cb.ToolResult)
				am.Content = append(am.Content, anthropicContentBlock{
					Type: "tool_result", ToolUseID: cb.ToolCallID, Content: resultContent,
				})
			}
		}
		// am.Content can end up empty when every block was filtered — most
		// commonly a message that was ONLY a thinking block, replayed into a
		// request where thinking is disabled (e.g. compaction's summarization
		// call). nil marshals as `"content":null`, not `[]`, which the API
		// rejects with an opaque "Input should be a valid array" 400 instead of
		// a clear empty-content error. Fall back to a placeholder so the turn
		// stays present (preserving role alternation) and the array is real.
		if len(am.Content) == 0 {
			am.Content = []anthropicContentBlock{{Type: "text", Text: "[empty turn]"}}
		}
		ar.Messages = append(ar.Messages, am)
	}

	// Tools. Under OAuth, names are camouflaged as Claude Code's own
	// MCP-tool convention (see prefixToolName) — real bare lowercase names
	// like "bash"/"read" are what got other third-party clients flagged.
	for _, td := range req.Tools {
		name := td.Name
		if isOAuth {
			name = prefixToolName(name)
		}
		ar.Tools = append(ar.Tools, anthropicTool{
			Name: name, Description: td.Description, InputSchema: td.Schema,
		})
	}

	applyPromptCache(&ar)
	return ar
}

// applyPromptCache places ephemeral cache_control breakpoints so Anthropic
// caches the stable prefix (tools + system) and the conversation across turns.
// Caching is prefix-based: each breakpoint caches everything up to and
// including it (max 4 allowed; we use up to 4: 1 tools + up to 2 system + 1
// messages). Cache reads bill at ~0.1x input price, so in an agentic loop —
// which resends the full system, tools, and growing history every turn —
// this slashes input token cost. The stealth billing block (system[0]) is
// derived from the first user message, so it is stable per session and safe
// inside the cached prefix.
//
// TTL is "1h" (matches Claude Code): the default 5-minute cache expires
// between interactive turns whenever the user pauses >5 min, turning every
// resumed turn into a full-price cache WRITE instead of a 0.1x read. The 1h
// pool is GA — no anthropic-beta header required. 1h writes bill at 2x input
// (vs 1.25x for 5m), but reads stay 0.1x, so a single reused turn past the
// 5-minute mark already pays back the write premium.
func applyPromptCache(ar *anthropicRequest) {
	cc := &anthropicCacheControl{Type: "ephemeral", TTL: "1h"}
	if n := len(ar.Tools); n > 0 {
		ar.Tools[n-1].CacheControl = cc // caches all tool definitions
	}
	// System gets up to two breakpoints: one on the second-to-last block and
	// one on the last. Compaction appends the summary as the FINAL system
	// block, after the billing/identity/system-prompt blocks that never
	// change for the life of the session. A single breakpoint at the end
	// would bundle that stable prefix and the summary into one cached unit —
	// every compaction changes the summary bytes, so every compaction would
	// force a full-price cache WRITE of the whole bundle, stable prefix
	// included, instead of a cheap read. Marking the second-to-last block too
	// keeps the stable prefix (up to and including the system prompt) its
	// own cache hit, confining the write to just the summary block. Before
	// the first compaction (no summary block yet) this just double-marks the
	// stable prefix, which is harmless. Total breakpoints used: tools(1) +
	// system(<=2) + messages(1) <= 4, Anthropic's hard cap.
	if n := len(ar.System); n > 0 {
		if n >= 2 {
			ar.System[n-2].CacheControl = cc
		}
		ar.System[n-1].CacheControl = cc
	}
	// Rolling breakpoint: cache the conversation prefix. The final request
	// message is always a user turn (a prompt or coalesced tool results).
	if n := len(ar.Messages); n > 0 {
		last := &ar.Messages[n-1]
		if last.Role == "user" && len(last.Content) > 0 {
			last.Content[len(last.Content)-1].CacheControl = cc
		}
	}
}

// setHeaders configures the HTTP request headers based on auth type.
// retryCount is this attempt's 0-indexed position within DoWithRetry's loop
// (X-Stainless-Retry-Count); requestID is stable across those retries
// (x-client-request-id) — see streamWithRetry.
func (p *AnthropicProvider) setHeaders(req *http.Request, isOAuth bool, adaptiveEffort bool, retryCount int, requestID string) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("anthropic-version", "2023-06-01")

	auth.StoreMu.Lock()
	access := p.auth["anthropic"].Access
	apiKey := auth.GetAPIKey(p.auth, "anthropic")
	auth.StoreMu.Unlock()

	if isOAuth {
		req.Header.Set("Authorization", "Bearer "+access)
		// effort-2025-11-24 only appears on requests that actually use adaptive
		// thinking — verified against real Claude Code captures (cc-sniff):
		// present when thinking.type is "adaptive", absent on a plain
		// thinking:disabled request. Sending it unconditionally would diverge
		// from the real client's header shape, which this stealth path exists
		// to match exactly.
		req.Header.Set("anthropic-beta", stealthBetaHeader(adaptiveEffort))
		req.Header.Set("user-agent", stealthUserAgent(p.config))
		req.Header.Set("x-app", "cli")
		// The rest of these mirror every /v1/messages header real Claude Code
		// sends (cc-sniff captures 0011-0015) that was previously missing
		// outright — their absence is itself a fingerprintable gap, not a
		// neutral omission.
		req.Header.Set("anthropic-dangerous-direct-browser-access", "true")
		req.Header.Set("X-Claude-Code-Session-Id", stealthSessionID())
		req.Header.Set("x-client-request-id", requestID)
		req.Header.Set("X-Stainless-Arch", stealthArch())
		req.Header.Set("X-Stainless-Lang", "js")
		req.Header.Set("X-Stainless-OS", stealthOS())
		req.Header.Set("X-Stainless-Package-Version", "0.94.0")
		req.Header.Set("X-Stainless-Retry-Count", strconv.Itoa(retryCount))
		req.Header.Set("X-Stainless-Runtime", "node")
		req.Header.Set("X-Stainless-Runtime-Version", "v26.3.0")
		req.Header.Set("X-Stainless-Timeout", "600")
	} else {
		if apiKey == "" && p.config != nil {
			apiKey = p.config.Anthropic.APIKey
		}
		req.Header.Set("x-api-key", apiKey)
	}
}

// pumpSSE reads the SSE stream from the response body and converts it to
// StreamEvents on the channel. isOAuth mirrors buildAnthropicRequest's flag:
// tool names arriving from a stealth request carry the mcp_ camouflage
// prefix (see prefixToolName) and must be unwrapped before dispatch.
func (p *AnthropicProvider) pumpSSE(ctx context.Context, body io.ReadCloser, ch chan<- StreamEvent, isOAuth bool) {
	defer close(ch)
	defer body.Close()

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 16*1024*1024), 16*1024*1024)

	toolCalls := make(map[int]*ToolCall)
	toolInputBuffers := make(map[int]*bytes.Buffer)

	// Anthropic reports input/cache tokens only in message_start; the final
	// message_delta carries output_tokens. Capture the former here and combine
	// at EventDone, otherwise input/cache tokens are recorded as 0 (breaking
	// cost, context %%, and auto-compaction).
	var startInput, startCacheRead, startCacheWrite int
	doneSent := false

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "event: ") {
			continue
		}

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		var event struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		switch event.Type {
		case "message_start":
			var msg struct {
				Message struct {
					Usage struct {
						InputTokens      int `json:"input_tokens"`
						OutputTokens     int `json:"output_tokens"`
						CacheReadTokens  int `json:"cache_read_input_tokens"`
						CacheWriteTokens int `json:"cache_creation_input_tokens"`
					} `json:"usage"`
				} `json:"message"`
			}
			json.Unmarshal([]byte(data), &msg)
			startInput = msg.Message.Usage.InputTokens
			startCacheRead = msg.Message.Usage.CacheReadTokens
			startCacheWrite = msg.Message.Usage.CacheWriteTokens

		case "content_block_start":
			var block struct {
				Index        int `json:"index"`
				ContentBlock struct {
					Type string `json:"type"`
					ID   string `json:"id"`
					Name string `json:"name"`
					Data string `json:"data"`
				} `json:"content_block"`
			}
			json.Unmarshal([]byte(data), &block)
			switch block.ContentBlock.Type {
			case "tool_use":
				name := block.ContentBlock.Name
				if isOAuth {
					name = unprefixToolName(name)
				}
				call := &ToolCall{
					ID:   block.ContentBlock.ID,
					Name: name,
				}
				toolCalls[block.Index] = call
				toolInputBuffers[block.Index] = &bytes.Buffer{}
				select {
				case <-ctx.Done():
					return
				case ch <- StreamEvent{Type: EventToolUseStart, ToolCall: call}:
				}
			case "redacted_thinking":
				select {
				case <-ctx.Done():
					return
				case ch <- StreamEvent{Type: EventThinkingRedacted, Text: block.ContentBlock.Data}:
				}
			}

		case "content_block_delta":
			var delta struct {
				Index int `json:"index"`
				Delta struct {
					Type        string `json:"type"`
					Text        string `json:"text"`
					PartialJSON string `json:"partial_json"`
				} `json:"delta"`
			}
			var sigDelta struct {
				Delta struct {
					Thinking  string `json:"thinking"`
					Signature string `json:"signature"`
				} `json:"delta"`
			}
			json.Unmarshal([]byte(data), &delta)
			switch delta.Delta.Type {
			case "text_delta":
				select {
				case <-ctx.Done():
					return
				case ch <- StreamEvent{Type: EventTextDelta, Text: delta.Delta.Text}:
				}
			case "thinking_delta":
				json.Unmarshal([]byte(data), &sigDelta)
				select {
				case <-ctx.Done():
					return
				case ch <- StreamEvent{Type: EventThinkingDelta, Text: sigDelta.Delta.Thinking}:
				}
			case "signature_delta":
				json.Unmarshal([]byte(data), &sigDelta)
				select {
				case <-ctx.Done():
					return
				case ch <- StreamEvent{Type: EventThinkingSignature, Text: sigDelta.Delta.Signature}:
				}
			case "input_json_delta":
				if buf, ok := toolInputBuffers[delta.Index]; ok {
					buf.WriteString(delta.Delta.PartialJSON)
				}
			}

		case "content_block_stop":
			var stop struct {
				Index int `json:"index"`
			}
			json.Unmarshal([]byte(data), &stop)
			call := toolCalls[stop.Index]
			if call != nil {
				if buf, ok := toolInputBuffers[stop.Index]; ok {
					call.Input = json.RawMessage(buf.Bytes())
					delete(toolInputBuffers, stop.Index)
				}
				delete(toolCalls, stop.Index)
				select {
				case <-ctx.Done():
					return
				case ch <- StreamEvent{Type: EventToolUseStop, ToolCall: call}:
				}
			}

		case "message_delta":
			var msgDelta struct {
				Delta struct {
					StopReason string `json:"stop_reason"`
				} `json:"delta"`
				Usage struct {
					InputTokens      int `json:"input_tokens"`
					OutputTokens     int `json:"output_tokens"`
					CacheReadTokens  int `json:"cache_read_input_tokens"`
					CacheWriteTokens int `json:"cache_creation_input_tokens"`
				} `json:"usage"`
			}
			json.Unmarshal([]byte(data), &msgDelta)
			// Combine message_start input/cache tokens with message_delta output.
			inTok := msgDelta.Usage.InputTokens
			if inTok == 0 {
				inTok = startInput
			}
			cacheRead := msgDelta.Usage.CacheReadTokens
			if cacheRead == 0 {
				cacheRead = startCacheRead
			}
			cacheWrite := msgDelta.Usage.CacheWriteTokens
			if cacheWrite == 0 {
				cacheWrite = startCacheWrite
			}
			usage := &Usage{
				InputTokens:      inTok,
				OutputTokens:     msgDelta.Usage.OutputTokens,
				CacheReadTokens:  cacheRead,
				CacheWriteTokens: cacheWrite,
			}
			select {
			case <-ctx.Done():
				return
			case ch <- StreamEvent{Type: EventDone, Usage: usage, StopReason: msgDelta.Delta.StopReason}:
				doneSent = true
			}

		case "message_stop":
			return

		case "error":
			var errEvent struct {
				Error struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				} `json:"error"`
			}
			json.Unmarshal([]byte(data), &errEvent)
			select {
			case <-ctx.Done():
				return
			case ch <- StreamEvent{
				Type:      EventError,
				Error:     fmt.Errorf("anthropic: %s: %s", errEvent.Error.Type, errEvent.Error.Message),
				Retryable: IsRetryableStreamErrorType(errEvent.Error.Type),
			}:
				// EventError is terminal, same as every other case above (e.g.
				// message_stop) — the consumer is entitled to stop ranging the
				// channel the instant it sees one. Without this return, the loop
				// kept scanning, hit clean EOF, and the "!doneSent" fallback below
				// did a second terminal send to a reader that already left,
				// blocking forever whenever ctx has no deadline.
				return
			}
		}
	}

	if err := scanner.Err(); err != nil {
		if ctx.Err() == nil {
			select {
			case ch <- StreamEvent{Type: EventError, Error: fmt.Errorf("sse read: %w", err)}:
			case <-ctx.Done():
			}
		}
		return
	}
	// Clean EOF before message_delta/message_stop means the turn was truncated;
	// surface an error so it isn't recorded as a successful, zero-cost turn.
	if !doneSent && ctx.Err() == nil {
		select {
		case ch <- StreamEvent{Type: EventError, Error: fmt.Errorf("anthropic: stream ended before completion")}:
		case <-ctx.Done():
		}
	}
}
