package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/auth"
	"github.com/mq37/poisson/internal/config"
)

// flakyThenOKServer fails the first failFor requests with the given status
// (or, if status == 0, by hijacking and closing the connection to simulate a
// network-level failure) then serves a normal SSE response.
func flakyThenOKServer(t *testing.T, failFor int, failStatus int, sseResponse string) *httptest.Server {
	t.Helper()
	var calls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&calls, 1)
		if int(n) <= failFor {
			if failStatus == 0 {
				hj, ok := w.(http.Hijacker)
				if !ok {
					t.Fatal("ResponseWriter does not support hijacking")
				}
				conn, _, err := hj.Hijack()
				if err != nil {
					t.Fatalf("hijack: %v", err)
				}
				conn.Close() // abrupt close: simulates a network-level failure
				return
			}
			w.WriteHeader(failStatus)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		io.Copy(w, strings.NewReader(sseResponse))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// shrinkAnthropicRetryTiming makes the retry backoff schedule fast for tests
// and restores it after — the same pattern as the package-level retry tests.
func shrinkAnthropicRetryTiming(t *testing.T) {
	t.Helper()
	oldBase, oldCap, oldAttempt := retryBackoffBase, retryBackoffCap, retryAttemptTimeout
	retryBackoffBase = time.Millisecond
	retryBackoffCap = 5 * time.Millisecond
	retryAttemptTimeout = 2 * time.Second
	t.Cleanup(func() {
		retryBackoffBase, retryBackoffCap, retryAttemptTimeout = oldBase, oldCap, oldAttempt
	})
}

// --- Prompt-cache tests (no real API calls) ---

func TestAnthropicPromptCacheBreakpoints(t *testing.T) {
	p := NewAnthropicProvider(auth.AuthStore{"anthropic": {Type: "oauth", Access: "t"}}, &config.Config{Stealth: config.DefaultStealthConfig()})
	req := &Request{
		Model:  "claude-x",
		System: []SystemBlock{{Text: "sys A"}, {Text: "sys B"}},
		Tools: []ToolDef{
			{Name: "read", Description: "r", Schema: json.RawMessage(`{"type":"object"}`)},
			{Name: "write", Description: "w", Schema: json.RawMessage(`{"type":"object"}`)},
		},
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "first"}}},
			{Role: "assistant", Content: []ContentBlock{{Type: "text", Text: "reply"}}},
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "second"}}},
		},
	}
	ar := p.buildAnthropicRequest(req, false)

	// One breakpoint on the last tool; two on system (second-to-last + last,
	// since there are >=2 system blocks) — see applyPromptCache; one on the
	// last message.
	if ar.Tools[0].CacheControl != nil || ar.Tools[1].CacheControl == nil {
		t.Errorf("cache breakpoint must be on the LAST tool only")
	}
	if ar.System[0].CacheControl == nil || ar.System[1].CacheControl == nil {
		t.Errorf("cache breakpoints must be on the last TWO system blocks")
	}
	last := ar.Messages[len(ar.Messages)-1]
	if last.Content[len(last.Content)-1].CacheControl == nil {
		t.Errorf("cache breakpoint must be on the last message's last block")
	}
	if ar.Messages[0].Content[0].CacheControl != nil {
		t.Errorf("earlier messages must not carry a cache breakpoint")
	}

	// Wire format must be an object with a 1h TTL, never a bare string.
	blob, _ := json.Marshal(ar)
	if !strings.Contains(string(blob), `"cache_control":{"type":"ephemeral","ttl":"1h"}`) {
		t.Fatalf("cache_control must serialize as an object with ttl 1h, got: %s", blob)
	}
	if strings.Contains(string(blob), `"cache_control":"`) {
		t.Fatalf("cache_control serialized as a bare string (invalid): %s", blob)
	}
}

// TestAnthropicPromptCacheSplitsSummaryFromStableSystemPrefix reproduces the
// pre/post-compaction shape: 3 stable system blocks (billing, identity,
// system prompt) with no summary yet, then the same 3 blocks plus a 4th
// (compaction summary) appended. The breakpoint over the stable prefix must
// land on the SAME block (the system prompt, index 2) in both cases, so that
// block's cached prefix survives a compaction instead of being invalidated
// along with the summary that changes underneath it.
func TestAnthropicPromptCacheSplitsSummaryFromStableSystemPrefix(t *testing.T) {
	p := NewAnthropicProvider(auth.AuthStore{"anthropic": {Type: "oauth", Access: "t"}}, &config.Config{Stealth: config.DefaultStealthConfig()})
	base := func(withSummary bool) *Request {
		sys := []SystemBlock{{Text: "billing"}, {Text: "identity"}, {Text: "system prompt"}}
		if withSummary {
			sys = append(sys, SystemBlock{Text: "compaction summary"})
		}
		return &Request{
			Model:    "claude-x",
			System:   sys,
			Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
		}
	}

	before := p.buildAnthropicRequest(base(false), false)
	after := p.buildAnthropicRequest(base(true), false)

	// Pre-compaction: breakpoints on identity (n-2) and system prompt (n-1).
	if before.System[0].CacheControl != nil {
		t.Errorf("billing block must not carry a breakpoint pre-compaction")
	}
	if before.System[1].CacheControl == nil || before.System[2].CacheControl == nil {
		t.Errorf("pre-compaction breakpoints must be on identity and system prompt")
	}

	// Post-compaction: the system-prompt breakpoint (index 2) must still be
	// there — same block, same position — so its cached prefix isn't
	// disturbed by the new summary block appended after it.
	if after.System[2].CacheControl == nil {
		t.Errorf("system prompt breakpoint must survive compaction (stable prefix)")
	}
	if after.System[2].Text != "system prompt" {
		t.Fatalf("expected breakpoint on system prompt block, got block with text %q", after.System[2].Text)
	}
	// The new summary block (index 3) gets its own breakpoint; the now-
	// interior identity block (index 1) does not need one anymore.
	if after.System[3].CacheControl == nil {
		t.Errorf("summary block must carry its own breakpoint")
	}
	if after.System[0].CacheControl != nil {
		t.Errorf("billing block must not carry a breakpoint post-compaction")
	}
}

// --- Stealth tests (no real API calls) ---

func TestComputeCCH(t *testing.T) {
	// cch = first 5 hex chars of SHA-256(text)
	got := computeCCH("hello world")
	// SHA-256("hello world") = b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9
	// First 5 hex chars: b94d2
	if len(got) != 5 {
		t.Errorf("cch length = %d, want 5", len(got))
	}
}

func TestComputeVersionSuffix(t *testing.T) {
	cfg := config.DefaultStealthConfig()
	// Positions [4, 7, 20] of "hello world this is a test message"
	text := "hello world this is a test message"
	chars := string([]byte{text[4], text[7], '0'}) // 'o', 'o', '0' (pos 20 is 'e')
	if chars != "ooe" {
		// pos 4 = 'o', pos 7 = 'o', pos 20 = 'e'
	}
	suffix := computeVersionSuffix(text, cfg)
	if len(suffix) != 3 {
		t.Errorf("suffix length = %d, want 3", len(suffix))
	}
}

func TestBuildBillingHeaderValue(t *testing.T) {
	cfg := config.DefaultStealthConfig()
	val := buildBillingHeaderValue("hello world", cfg)
	if !strings.HasPrefix(val, "x-anthropic-billing-header: ") {
		t.Errorf("expected billing header prefix, got %q", val)
	}
	if !strings.Contains(val, "cc_version=2.1.156.") {
		t.Errorf("expected cc_version, got %q", val)
	}
	if !strings.Contains(val, "cc_entrypoint=sdk-cli") {
		t.Errorf("expected cc_entrypoint, got %q", val)
	}
	if !strings.Contains(val, "cch=") {
		t.Errorf("expected cch=, got %q", val)
	}
}

func TestSanitizeSystemText(t *testing.T) {
	cfg := config.DefaultStealthConfig()

	// Paragraph with pi-mono fingerprint should be removed.
	text := "You are helpful.\n\nSee github.com/badlogic/pi-mono for docs.\n\nBe concise."
	result := sanitizeSystemText(text, cfg)
	if strings.Contains(result, "pi-mono") {
		t.Errorf("pi-mono paragraph should be removed, got %q", result)
	}
	if !strings.Contains(result, "You are helpful.") {
		t.Errorf("first paragraph should remain, got %q", result)
	}
	if !strings.Contains(result, "Be concise.") {
		t.Errorf("last paragraph should remain, got %q", result)
	}
}

func TestSanitizeSystemTextInlineReplacements(t *testing.T) {
	cfg := config.DefaultStealthConfig()

	text := "if pi honestly thinks this is a good idea, do it."
	result := sanitizeSystemText(text, cfg)
	if strings.Contains(result, "if pi honestly") {
		t.Errorf("should replace 'if pi honestly', got %q", result)
	}
	if !strings.Contains(result, "if the assistant honestly") {
		t.Errorf("expected replacement, got %q", result)
	}
}

func TestSanitizeSystemTextEnvironmentPhrase(t *testing.T) {
	cfg := config.DefaultStealthConfig()

	text := "Here is some useful information about the environment you are running in:\n/home/user"
	result := sanitizeSystemText(text, cfg)
	if strings.Contains(result, "Here is some useful information") {
		t.Errorf("should replace environment phrase, got %q", result)
	}
	if !strings.Contains(result, "Environment context you are running in:") {
		t.Errorf("expected replacement, got %q", result)
	}
}

func TestApplyStealth(t *testing.T) {
	cfg := &config.Config{Stealth: config.DefaultStealthConfig()}
	authStore := auth.AuthStore{
		"anthropic": {Type: "oauth", Access: "token123"},
	}
	p := NewAnthropicProvider(authStore, cfg)

	req := &Request{
		Model: "claude-sonnet-4-20250514",
		System: []SystemBlock{
			{Text: "You are Poisson, a coding assistant.\n\nSee Poisson documentation for details."},
			{Text: "Be concise."},
		},
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hello world"}}},
		},
	}

	p.applyStealth(req)

	// Final order: [billing_header, cc_identity, ...sanitized]
	if len(req.System) < 3 {
		t.Fatalf("expected at least 3 system blocks, got %d", len(req.System))
	}

	// system[0] should be billing header.
	if !strings.HasPrefix(req.System[0].Text, "x-anthropic-billing-header:") {
		t.Errorf("system[0] should be billing header, got %q", req.System[0].Text)
	}

	// system[1] should be CC identity.
	if req.System[1].Text != claudeCodeIdentity {
		t.Errorf("system[1] should be CC identity, got %q", req.System[1].Text)
	}

	// The "Poisson documentation" paragraph should have been removed.
	for i := 2; i < len(req.System); i++ {
		if strings.Contains(req.System[i].Text, "Poisson documentation") {
			t.Errorf("system[%d] should not contain Poisson documentation fingerprint: %q", i, req.System[i].Text)
		}
	}
}

func TestApplyStealthRemovesPiIdentity(t *testing.T) {
	cfg := &config.Config{Stealth: config.DefaultStealthConfig()}
	authStore := auth.AuthStore{
		"anthropic": {Type: "oauth", Access: "token123"},
	}
	p := NewAnthropicProvider(authStore, cfg)

	req := &Request{
		Model: "claude-sonnet-4-20250514",
		System: []SystemBlock{
			{Text: "You are Claude Code, Anthropic's official CLI for Claude."},
			{Text: "Be concise."},
		},
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "test"}}},
		},
	}

	p.applyStealth(req)

	// The pi-style identity should be skipped, replaced with CC identity.
	if req.System[1].Text != claudeCodeIdentity {
		t.Errorf("system[1] should be CC identity, got %q", req.System[1].Text)
	}

	for _, sb := range req.System {
		if strings.Contains(sb.Text, "official CLI for Claude") {
			t.Errorf("pi identity should be removed, found in: %q", sb.Text)
		}
	}
}

// --- Anthropic provider tests with fake server ---

func TestAnthropicStreamText(t *testing.T) {
	// Simulate Anthropic SSE response with a fake HTTP server.
	sseResponse := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello from anthropic\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"input_tokens\":10,\"output_tokens\":5}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

	server := newFakeSSEServer(sseResponse)
	defer server.Close()

	cfg := &config.Config{Stealth: config.DefaultStealthConfig()}
	authStore := auth.AuthStore{
		"anthropic": {Type: "api_key", Key: "test-key"},
	}
	p := NewAnthropicProvider(authStore, cfg)
	p.baseURL = server.URL

	ch, err := p.Stream(context.Background(), &Request{
		Model:     "claude-sonnet-4-20250514",
		Messages:  []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
		MaxTokens: 100,
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var text string
	var done *Usage
	for ev := range ch {
		switch ev.Type {
		case EventTextDelta:
			text += ev.Text
		case EventDone:
			done = ev.Usage
		case EventError:
			t.Fatalf("error: %v", ev.Error)
		}
	}
	if text != "hello from anthropic" {
		t.Errorf("text = %q, want %q", text, "hello from anthropic")
	}
	if done == nil {
		t.Fatal("no done event")
	}
	if done.InputTokens != 10 || done.OutputTokens != 5 {
		t.Errorf("usage = %+v, want {10, 5}", done)
	}
}

func TestAnthropicStreamMergesStartAndDeltaUsage(t *testing.T) {
	// Anthropic reports input/cache tokens in message_start, while
	// message_delta often only contains output_tokens. Dropping message_start
	// makes cost/context/compaction accounting read as zero input tokens.
	sseResponse := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":123,\"output_tokens\":0,\"cache_read_input_tokens\":7,\"cache_creation_input_tokens\":11}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":9}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

	server := newFakeSSEServer(sseResponse)
	defer server.Close()

	cfg := &config.Config{Stealth: config.DefaultStealthConfig()}
	authStore := auth.AuthStore{
		"anthropic": {Type: "api_key", Key: "test-key"},
	}
	p := NewAnthropicProvider(authStore, cfg)
	p.baseURL = server.URL

	ch, err := p.Stream(context.Background(), &Request{
		Model:     "claude-sonnet-4-20250514",
		Messages:  []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
		MaxTokens: 100,
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var done *Usage
	for ev := range ch {
		switch ev.Type {
		case EventDone:
			done = ev.Usage
		case EventError:
			t.Fatalf("error: %v", ev.Error)
		}
	}
	if done == nil {
		t.Fatal("no done event")
	}
	if done.InputTokens != 123 || done.OutputTokens != 9 || done.CacheReadTokens != 7 || done.CacheWriteTokens != 11 {
		t.Fatalf("usage = %+v, want input=123 output=9 cache_read=7 cache_write=11", done)
	}
}

func TestAnthropicStreamToolCall(t *testing.T) {
	sseResponse := "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"read\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"path\\\":\\\"main.go\\\"}\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"input_tokens\":20,\"output_tokens\":10}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

	server := newFakeSSEServer(sseResponse)
	defer server.Close()

	cfg := &config.Config{Stealth: config.DefaultStealthConfig()}
	authStore := auth.AuthStore{
		"anthropic": {Type: "api_key", Key: "test-key"},
	}
	p := NewAnthropicProvider(authStore, cfg)
	p.baseURL = server.URL

	ch, err := p.Stream(context.Background(), &Request{
		Model:     "claude-sonnet-4-20250514",
		Messages:  []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "read main.go"}}}},
		MaxTokens: 100,
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var toolCalls []*ToolCall
	var done *Usage
	for ev := range ch {
		switch ev.Type {
		case EventToolUseStart:
			toolCalls = append(toolCalls, ev.ToolCall)
		case EventToolUseStop:
			if ev.ToolCall != nil {
				var input map[string]string
				json.Unmarshal(ev.ToolCall.Input, &input)
				if input["path"] != "main.go" {
					t.Errorf("tool input path = %q, want %q", input["path"], "main.go")
				}
			}
		case EventDone:
			done = ev.Usage
		case EventError:
			t.Fatalf("error: %v", ev.Error)
		}
	}
	if len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(toolCalls))
	}
	if toolCalls[0].Name != "read" {
		t.Errorf("tool name = %q, want %q", toolCalls[0].Name, "read")
	}
	if done == nil || done.InputTokens != 20 || done.OutputTokens != 10 {
		t.Errorf("usage mismatch: %+v", done)
	}
}

func TestAnthropicStreamInterleavedToolCalls(t *testing.T) {
	sseResponse := `event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"read"}}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_2","name":"write"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"a"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"b"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":".txt\"}"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":".txt\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","usage":{"input_tokens":20,"output_tokens":10}}

event: message_stop
data: {"type":"message_stop"}

`

	server := newFakeSSEServer(sseResponse)
	defer server.Close()

	cfg := &config.Config{Stealth: config.DefaultStealthConfig()}
	authStore := auth.AuthStore{"anthropic": {Type: "api_key", Key: "test-key"}}
	p := NewAnthropicProvider(authStore, cfg)
	p.baseURL = server.URL

	ch, err := p.Stream(context.Background(), &Request{
		Model:     "claude-sonnet-4-20250514",
		Messages:  []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "do tools"}}}},
		MaxTokens: 100,
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	got := map[string]string{}
	for ev := range ch {
		switch ev.Type {
		case EventToolUseStop:
			var input map[string]string
			if err := json.Unmarshal(ev.ToolCall.Input, &input); err != nil {
				t.Fatalf("tool input %s: %v (%s)", ev.ToolCall.ID, err, ev.ToolCall.Input)
			}
			got[ev.ToolCall.ID] = input["path"]
		case EventError:
			t.Fatalf("error: %v", ev.Error)
		}
	}
	if got["toolu_1"] != "a.txt" || got["toolu_2"] != "b.txt" {
		t.Fatalf("tool inputs = %#v, want toolu_1=a.txt toolu_2=b.txt", got)
	}
}

// TestAnthropicStealthHeadersAdaptiveEffort verifies the effort-2025-11-24
// beta flag appears only on a request that actually uses adaptive thinking
// (thinking.type=adaptive) — verified against real Claude Code captures
// (cc-sniff): present on adaptive requests, absent on a plain
// thinking:disabled one. Companion to TestAnthropicStealthHeaders, which
// checks the negative case.
func TestAnthropicStealthHeadersAdaptiveEffort(t *testing.T) {
	server := newFakeSSEServer("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	defer server.Close()

	cfg := &config.Config{Stealth: config.DefaultStealthConfig()}
	authStore := auth.AuthStore{
		"anthropic": {Type: "oauth", Access: "oauth-token-123", Refresh: "refresh-456", Expires: 9999999999999},
	}
	p := NewAnthropicProvider(authStore, cfg)
	p.baseURL = server.URL

	_, err := p.Stream(context.Background(), &Request{
		Model:     "claude-opus-5", // adaptive-capable, per TestAnthropicEffortMapsToThinking
		Messages:  []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "test"}}}},
		MaxTokens: 1,
		Effort:    "xhigh",
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	req := server.lastRequest
	if req == nil {
		t.Fatal("no request captured")
	}
	if !strings.Contains(req.Header.Get("anthropic-beta"), "effort-2025-11-24") {
		t.Errorf("anthropic-beta = %q, want effort-2025-11-24 for an adaptive-thinking request", req.Header.Get("anthropic-beta"))
	}
}

// TestAnthropicStealthToolNameObfuscation verifies tool names are camouflaged
// as Claude Code's MCP-tool convention (bash -> mcp_Bash) only under OAuth,
// on both the outgoing tool/tool_use wire shape and the incoming
// tool_use.name from the SSE stream, which must unwrap back to the original
// name the rest of poisson (dispatch, UI) expects.
func TestAnthropicStealthToolNameObfuscation(t *testing.T) {
	sseResponse := "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"mcp_Bash\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

	server := newFakeSSEServer(sseResponse)
	defer server.Close()

	cfg := &config.Config{Stealth: config.DefaultStealthConfig()}
	authStore := auth.AuthStore{
		"anthropic": {Type: "oauth", Access: "oauth-token-123", Refresh: "refresh-456", Expires: 9999999999999},
	}
	p := NewAnthropicProvider(authStore, cfg)
	p.baseURL = server.URL

	ch, err := p.Stream(context.Background(), &Request{
		Model:     "claude-sonnet-4-20250514",
		Messages:  []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "do a tool"}}}},
		MaxTokens: 100,
		Tools:     []ToolDef{{Name: "bash", Description: "run a shell command"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var sawStart bool
	for ev := range ch {
		switch ev.Type {
		case EventToolUseStart:
			sawStart = true
			if ev.ToolCall.Name != "bash" {
				t.Errorf("ToolCall.Name = %q, want unwrapped %q", ev.ToolCall.Name, "bash")
			}
		case EventError:
			t.Fatalf("error: %v", ev.Error)
		}
	}
	if !sawStart {
		t.Fatal("never saw EventToolUseStart")
	}

	var body struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(server.lastBody, &body); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	if len(body.Tools) != 1 || body.Tools[0].Name != "mcp_Bash" {
		t.Fatalf("request tools = %#v, want a single mcp_Bash", body.Tools)
	}
}

func TestAnthropicStealthHeaders(t *testing.T) {
	server := newFakeSSEServer("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	defer server.Close()

	cfg := &config.Config{Stealth: config.DefaultStealthConfig()}
	authStore := auth.AuthStore{
		"anthropic": {Type: "oauth", Access: "oauth-token-123", Refresh: "refresh-456", Expires: 9999999999999},
	}
	p := NewAnthropicProvider(authStore, cfg)
	p.baseURL = server.URL

	_, err := p.Stream(context.Background(), &Request{
		Model:     "claude-sonnet-4-20250514",
		Messages:  []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "test"}}}},
		MaxTokens: 1,
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	// Verify the request had stealth headers.
	req := server.lastRequest
	if req == nil {
		t.Fatal("no request captured")
	}
	if req.Header.Get("Authorization") != "Bearer oauth-token-123" {
		t.Errorf("Authorization = %q, want Bearer token", req.Header.Get("Authorization"))
	}
	if !strings.Contains(req.Header.Get("anthropic-beta"), "claude-code-20250219") {
		t.Errorf("anthropic-beta = %q, missing claude-code", req.Header.Get("anthropic-beta"))
	}
	if !strings.Contains(req.Header.Get("anthropic-beta"), "oauth-2025-04-20") {
		t.Errorf("anthropic-beta = %q, missing oauth", req.Header.Get("anthropic-beta"))
	}
	if !strings.HasPrefix(req.Header.Get("user-agent"), "claude-cli/") {
		t.Errorf("user-agent = %q, want claude-cli/...", req.Header.Get("user-agent"))
	}
	if !strings.Contains(req.Header.Get("user-agent"), "(external, sdk-cli)") {
		t.Errorf("user-agent = %q, want '(external, sdk-cli)' suffix (cc-sniff: claude-cli/2.1.201 (external, sdk-cli))", req.Header.Get("user-agent"))
	}
	if !strings.HasSuffix(req.URL.String(), "?beta=true") {
		t.Errorf("request URL = %q, want ?beta=true suffix (every real /v1/messages capture has it)", req.URL.String())
	}
	if req.Header.Get("anthropic-dangerous-direct-browser-access") != "true" {
		t.Errorf("anthropic-dangerous-direct-browser-access = %q, want %q", req.Header.Get("anthropic-dangerous-direct-browser-access"), "true")
	}
	if req.Header.Get("X-Claude-Code-Session-Id") == "" {
		t.Error("X-Claude-Code-Session-Id missing")
	}
	if req.Header.Get("x-client-request-id") == "" {
		t.Error("x-client-request-id missing")
	}
	for _, h := range []string{"X-Stainless-Arch", "X-Stainless-Lang", "X-Stainless-OS", "X-Stainless-Package-Version", "X-Stainless-Retry-Count", "X-Stainless-Runtime", "X-Stainless-Runtime-Version", "X-Stainless-Timeout"} {
		if req.Header.Get(h) == "" {
			t.Errorf("%s missing", h)
		}
	}
	// claude-sonnet-4-20250514 with no Effort set doesn't use adaptive thinking
	// (see TestAnthropicEffortMapsToThinking) — the effort beta flag must be
	// absent, matching real Claude Code captures where it only appears on
	// requests that actually send thinking.type=adaptive.
	if strings.Contains(req.Header.Get("anthropic-beta"), "effort-2025-11-24") {
		t.Errorf("anthropic-beta = %q, must not carry effort-2025-11-24 for a non-adaptive request", req.Header.Get("anthropic-beta"))
	}
	if req.Header.Get("x-app") != "cli" {
		t.Errorf("x-app = %q, want 'cli'", req.Header.Get("x-app"))
	}
}

func TestAnthropicAPIKeyHeaders(t *testing.T) {
	server := newFakeSSEServer("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	defer server.Close()

	cfg := &config.Config{Stealth: config.DefaultStealthConfig(), Anthropic: config.AnthropicConfig{APIKey: "sk-test"}}
	authStore := auth.AuthStore{}
	p := NewAnthropicProvider(authStore, cfg)
	p.baseURL = server.URL

	_, err := p.Stream(context.Background(), &Request{
		Model:     "claude-sonnet-4-20250514",
		Messages:  []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "test"}}}},
		MaxTokens: 1,
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	req := server.lastRequest
	if req == nil {
		t.Fatal("no request captured")
	}
	if req.Header.Get("x-api-key") != "sk-test" {
		t.Errorf("x-api-key = %q, want sk-test", req.Header.Get("x-api-key"))
	}
	if req.Header.Get("Authorization") != "" {
		t.Errorf("Authorization should be empty for API key auth, got %q", req.Header.Get("Authorization"))
	}
}

func TestAnthropicMaxTokensGenerousAndDecoupled(t *testing.T) {
	p := NewAnthropicProvider(auth.AuthStore{"anthropic": {Type: "api_key", Key: "k"}}, &config.Config{Stealth: config.DefaultStealthConfig()})
	// No explicit max_tokens => generous ceiling, not the old tiny 4096.
	r := p.buildAnthropicRequest(&Request{Model: "claude-opus-5"}, false)
	if r.MaxTokens != anthropicMaxOutputTokens {
		t.Fatalf("default max_tokens = %d, want %d", r.MaxTokens, anthropicMaxOutputTokens)
	}
	// Even at max effort, max_tokens must leave ample room over the thinking
	// budget (the old budget+1024 cap is what starved the answer).
	hi := p.buildAnthropicRequest(&Request{Model: "claude-opus-5", Effort: "max"}, false)
	if hi.Thinking == nil {
		t.Fatal("expected thinking enabled at max effort")
	}
	if hi.MaxTokens-hi.Thinking.BudgetTokens < 40000 {
		t.Fatalf("max_tokens (%d) leaves too little over budget (%d)", hi.MaxTokens, hi.Thinking.BudgetTokens)
	}
}

func TestAnthropicEffortMapsToThinking(t *testing.T) {
	p := NewAnthropicProvider(auth.AuthStore{"anthropic": {Type: "api_key", Key: "k"}}, &config.Config{Stealth: config.DefaultStealthConfig()})

	// No effort: no thinking block, no top-level effort field.
	plain := p.buildAnthropicRequest(&Request{Model: "claude-opus-5", MaxTokens: 100}, false)
	if plain.Thinking != nil {
		t.Fatalf("expected no thinking block when effort empty")
	}
	body, _ := json.Marshal(plain)
	if strings.Contains(string(body), "\"effort\"") {
		t.Fatalf("request must not contain top-level effort field: %s", body)
	}

	// Adaptive model (opus) + xhigh: adaptive thinking + output_config.effort,
	// no budget_tokens, generous max_tokens.
	hi := p.buildAnthropicRequest(&Request{Model: "claude-opus-5", MaxTokens: 100, Effort: "xhigh"}, false)
	if hi.Thinking == nil || hi.Thinking.Type != "adaptive" {
		t.Fatalf("expected adaptive thinking for xhigh, got %+v", hi.Thinking)
	}
	if hi.OutputConfig == nil || hi.OutputConfig.Effort != "xhigh" {
		t.Fatalf("expected output_config.effort=xhigh, got %+v", hi.OutputConfig)
	}
	if hi.MaxTokens != anthropicMaxOutputTokens {
		t.Fatalf("max_tokens = %d, want %d", hi.MaxTokens, anthropicMaxOutputTokens)
	}
	if body, _ := json.Marshal(hi); strings.Contains(string(body), "budget_tokens") {
		t.Fatalf("adaptive request must omit budget_tokens: %s", body)
	}

	// "max" clamps to xhigh (the top value the real client sends).
	mx := p.buildAnthropicRequest(&Request{Model: "claude-opus-5", Effort: "max"}, false)
	if mx.OutputConfig == nil || mx.OutputConfig.Effort != "xhigh" {
		t.Fatalf("max should clamp to xhigh, got %+v", mx.OutputConfig)
	}

	// A non-adaptive model falls back to budget-based thinking.
	leg := p.buildAnthropicRequest(&Request{Model: "claude-legacy-x", MaxTokens: 100, Effort: "xhigh"}, false)
	if leg.Thinking == nil || leg.Thinking.Type != "enabled" {
		t.Fatalf("expected budget thinking for non-adaptive model, got %+v", leg.Thinking)
	}
	if leg.Thinking.BudgetTokens <= 0 || leg.MaxTokens <= leg.Thinking.BudgetTokens {
		t.Fatalf("max_tokens (%d) must exceed budget (%d)", leg.MaxTokens, leg.Thinking.BudgetTokens)
	}
	if leg.OutputConfig != nil {
		t.Fatalf("budget mode must not set output_config")
	}
}

func TestAnthropicToolResultsBecomeUserMessage(t *testing.T) {
	p := NewAnthropicProvider(auth.AuthStore{"anthropic": {Type: "api_key", Key: "k"}}, &config.Config{Stealth: config.DefaultStealthConfig()})

	req := &Request{
		Model: "claude-opus-5",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "do it"}}},
			{Role: "assistant", Content: []ContentBlock{
				{Type: "tool_use", ToolCallID: "t1", ToolName: "read", ToolInput: json.RawMessage(`{"path":"a"}`)},
				{Type: "tool_use", ToolCallID: "t2", ToolName: "read", ToolInput: json.RawMessage(`{"path":"b"}`)},
			}},
			// Two parallel tool results stored as separate "tool" messages.
			{Role: "tool", Content: []ContentBlock{{Type: "tool_result", ToolCallID: "t1", ToolResult: "A"}}},
			{Role: "tool", Content: []ContentBlock{{Type: "tool_result", ToolCallID: "t2", ToolResult: "B"}}},
		},
	}

	ar := p.buildAnthropicRequest(req, false)
	for _, m := range ar.Messages {
		if m.Role != "user" && m.Role != "assistant" {
			t.Fatalf("invalid role %q (Anthropic allows only user/assistant)", m.Role)
		}
	}
	// Expect: user, assistant, user(coalesced tool_results).
	if len(ar.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(ar.Messages))
	}
	last := ar.Messages[2]
	if last.Role != "user" {
		t.Fatalf("tool results must be in a user message, got %q", last.Role)
	}
	if len(last.Content) != 2 {
		t.Fatalf("expected 2 coalesced tool_result blocks, got %d", len(last.Content))
	}
	for _, b := range last.Content {
		if b.Type != "tool_result" || b.ToolUseID == "" {
			t.Fatalf("bad tool_result block: %+v", b)
		}
	}
}

func TestAnthropicSSEParsesThinking(t *testing.T) {
	sse := "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"let me reason\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"SIG123\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"input_tokens\":10,\"output_tokens\":5}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

	p := NewAnthropicProvider(auth.AuthStore{"anthropic": {Type: "api_key", Key: "k"}}, &config.Config{Stealth: config.DefaultStealthConfig()})
	ch := make(chan StreamEvent, 64)
	go p.pumpSSE(context.Background(), &stringReadCloser{strings.NewReader(sse)}, ch, false)

	var thinking, sig string
	for ev := range ch {
		switch ev.Type {
		case EventThinkingDelta:
			thinking += ev.Text
		case EventThinkingSignature:
			sig += ev.Text
		}
	}
	if thinking != "let me reason" {
		t.Fatalf("thinking = %q", thinking)
	}
	if sig != "SIG123" {
		t.Fatalf("signature = %q", sig)
	}
}

func TestAnthropicSSEParsesRedactedThinking(t *testing.T) {
	sse := "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"redacted_thinking\",\"data\":\"ENCRYPTED\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

	p := NewAnthropicProvider(auth.AuthStore{"anthropic": {Type: "api_key", Key: "k"}}, &config.Config{Stealth: config.DefaultStealthConfig()})
	ch := make(chan StreamEvent, 8)
	go p.pumpSSE(context.Background(), &stringReadCloser{strings.NewReader(sse)}, ch, false)

	var redacted string
	for ev := range ch {
		if ev.Type == EventThinkingRedacted {
			redacted = ev.Text
		}
	}
	if redacted != "ENCRYPTED" {
		t.Fatalf("redacted payload = %q", redacted)
	}
}

func TestAnthropicReplaysThinkingFirstWhenEnabled(t *testing.T) {
	p := NewAnthropicProvider(auth.AuthStore{"anthropic": {Type: "api_key", Key: "k"}}, &config.Config{Stealth: config.DefaultStealthConfig()})
	req := &Request{
		Model:  "claude-opus-5",
		Effort: "high",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "go"}}},
			{Role: "assistant", Content: []ContentBlock{
				{Type: "thinking", Thinking: "reason", ThinkingSignature: "SIG"},
				{Type: "tool_use", ToolCallID: "t1", ToolName: "read", ToolInput: json.RawMessage(`{}`)},
			}},
			{Role: "tool", Content: []ContentBlock{{Type: "tool_result", ToolCallID: "t1", ToolResult: "ok"}}},
		},
	}
	ar := p.buildAnthropicRequest(req, false)
	asst := ar.Messages[1]
	if asst.Content[0].Type != "thinking" || asst.Content[0].Signature != "SIG" {
		t.Fatalf("first assistant block must be signed thinking, got %+v", asst.Content[0])
	}
	if asst.Content[1].Type != "tool_use" {
		t.Fatalf("tool_use must follow thinking, got %q", asst.Content[1].Type)
	}
}

func TestAnthropicOmitsThinkingWhenDisabled(t *testing.T) {
	p := NewAnthropicProvider(auth.AuthStore{"anthropic": {Type: "api_key", Key: "k"}}, &config.Config{Stealth: config.DefaultStealthConfig()})
	req := &Request{
		Model: "claude-opus-5", // no effort → thinking disabled
		Messages: []Message{
			{Role: "assistant", Content: []ContentBlock{
				{Type: "thinking", Thinking: "reason", ThinkingSignature: "SIG"},
				{Type: "text", Text: "answer"},
			}},
		},
	}
	ar := p.buildAnthropicRequest(req, false)
	for _, b := range ar.Messages[0].Content {
		if b.Type == "thinking" {
			t.Fatalf("thinking block must be omitted when thinking disabled")
		}
	}
}

// TestAnthropicThinkingOnlyMessageGetsPlaceholderWhenDisabled guards against a
// regression where an assistant turn consisting solely of a thinking block
// (e.g. the model was cancelled mid-thought before producing any text or
// tool call) sent through a request with thinking disabled (as compaction's
// summarization call always does) left that message's Content nil. Content
// has no `omitempty`, so it marshalled as `"content":null` and Anthropic
// rejected the whole request with an opaque "messages.N.content: Input
// should be a valid array" 400 instead of a usable error.
func TestAnthropicThinkingOnlyMessageGetsPlaceholderWhenDisabled(t *testing.T) {
	p := NewAnthropicProvider(auth.AuthStore{"anthropic": {Type: "api_key", Key: "k"}}, &config.Config{Stealth: config.DefaultStealthConfig()})
	req := &Request{
		Model: "claude-opus-5", // no effort → thinking disabled
		Messages: []Message{
			{Role: "assistant", Content: []ContentBlock{
				{Type: "thinking", Thinking: "reason", ThinkingSignature: "SIG"},
			}},
		},
	}
	ar := p.buildAnthropicRequest(req, false)
	if ar.Messages[0].Content == nil {
		t.Fatal("Content must never be nil (marshals as null, Anthropic rejects it)")
	}
	if len(ar.Messages[0].Content) == 0 {
		t.Fatal("Content must never be empty (Anthropic requires at least one block)")
	}
}

func TestAnthropicUnsignedThinkingDegradesToText(t *testing.T) {
	p := NewAnthropicProvider(auth.AuthStore{"anthropic": {Type: "api_key", Key: "k"}}, &config.Config{Stealth: config.DefaultStealthConfig()})
	req := &Request{
		Model:  "claude-opus-5",
		Effort: "high",
		Messages: []Message{
			{Role: "assistant", Content: []ContentBlock{
				{Type: "thinking", Thinking: "partial reasoning", ThinkingSignature: ""},
				{Type: "text", Text: "answer"},
			}},
		},
	}
	ar := p.buildAnthropicRequest(req, false)
	b := ar.Messages[0].Content[0]
	if b.Type != "text" || b.Text != "partial reasoning" {
		t.Fatalf("unsigned thinking should degrade to text, got %+v", b)
	}
}

// --- Network-resilience tests (retry with exponential backoff) ---

const anthropicMinimalSSE = "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

func TestAnthropicStreamRetriesNetworkFailureThenSucceeds(t *testing.T) {
	shrinkAnthropicRetryTiming(t)
	srv := flakyThenOKServer(t, 2, 0, anthropicMinimalSSE) // 2 connection-level failures, then OK

	cfg := &config.Config{Stealth: config.DefaultStealthConfig()}
	authStore := auth.AuthStore{"anthropic": {Type: "api_key", Key: "test-key"}}
	p := NewAnthropicProvider(authStore, cfg)
	p.baseURL = srv.URL

	var retries []int
	trace := &RetryTrace{OnRetry: func(attempt int, delay time.Duration, reason string) {
		retries = append(retries, attempt)
	}}
	ctx := WithRetryTrace(context.Background(), trace)

	ch, err := p.Stream(ctx, &Request{
		Model:    "claude-opus-5",
		Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range ch {
		// drain — a minimal message_stop-only SSE body produces no events
		// worth asserting on beyond "the channel closes cleanly".
	}
	if len(retries) != 2 || retries[0] != 1 || retries[1] != 2 {
		t.Errorf("retries = %v, want [1 2]", retries)
	}
}

func TestAnthropicStreamRetries503ThenSucceeds(t *testing.T) {
	shrinkAnthropicRetryTiming(t)
	srv := flakyThenOKServer(t, 2, 503, anthropicMinimalSSE)

	cfg := &config.Config{Stealth: config.DefaultStealthConfig()}
	authStore := auth.AuthStore{"anthropic": {Type: "api_key", Key: "test-key"}}
	p := NewAnthropicProvider(authStore, cfg)
	p.baseURL = srv.URL

	ch, err := p.Stream(context.Background(), &Request{
		Model:    "claude-opus-5",
		Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range ch {
	}
}

func TestAnthropicStreamRetries529ThenSucceeds(t *testing.T) {
	// 529 (overloaded_error) is Anthropic-specific — not in the generic
	// default retryable-status set, confirmed retried here via
	// AnthropicRetryableStatus.
	shrinkAnthropicRetryTiming(t)
	srv := flakyThenOKServer(t, 1, 529, anthropicMinimalSSE)

	cfg := &config.Config{Stealth: config.DefaultStealthConfig()}
	authStore := auth.AuthStore{"anthropic": {Type: "api_key", Key: "test-key"}}
	p := NewAnthropicProvider(authStore, cfg)
	p.baseURL = srv.URL

	ch, err := p.Stream(context.Background(), &Request{
		Model:    "claude-opus-5",
		Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range ch {
	}
}

func TestAnthropicStreamDoesNotRetry400(t *testing.T) {
	shrinkAnthropicRetryTiming(t)
	var calls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&calls, 1)
		w.WriteHeader(400)
		w.Write([]byte(`{"error":"bad request"}`))
	}))
	defer srv.Close()

	cfg := &config.Config{Stealth: config.DefaultStealthConfig()}
	authStore := auth.AuthStore{"anthropic": {Type: "api_key", Key: "test-key"}}
	p := NewAnthropicProvider(authStore, cfg)
	p.baseURL = srv.URL

	_, err := p.Stream(context.Background(), &Request{
		Model:    "claude-opus-5",
		Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
	})
	if err == nil {
		t.Fatal("expected an error for a 400 response")
	}
	if n := atomic.LoadInt64(&calls); n != 1 {
		t.Errorf("calls = %d, want 1 (400 must not be retried)", n)
	}
}

func TestAnthropicStreamCancelledDuringRetryReturnsSilently(t *testing.T) {
	oldBase, oldCap, oldAttempt := retryBackoffBase, retryBackoffCap, retryAttemptTimeout
	retryBackoffBase = 2 * time.Second
	retryBackoffCap = 2 * time.Second
	retryAttemptTimeout = 5 * time.Second
	defer func() { retryBackoffBase, retryBackoffCap, retryAttemptTimeout = oldBase, oldCap, oldAttempt }()

	// Always fails — the test proves cancellation during backoff aborts
	// promptly with ctx.Err(), not some wrapped/decorated error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
	}))
	defer srv.Close()

	cfg := &config.Config{Stealth: config.DefaultStealthConfig()}
	authStore := auth.AuthStore{"anthropic": {Type: "api_key", Key: "test-key"}}
	p := NewAnthropicProvider(authStore, cfg)
	p.baseURL = srv.URL

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := p.Stream(ctx, &Request{
			Model:    "claude-opus-5",
			Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
		})
		done <- err
	}()

	time.Sleep(30 * time.Millisecond) // let the first attempt fail and enter backoff
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error (context cancelled) when cancelled during retry backoff")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stream did not return promptly after cancellation during backoff")
	}
}
