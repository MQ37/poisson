package provider

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"poisson/internal/auth"
	"poisson/internal/config"
)

// --- Prompt-cache tests (no real API calls) ---

func TestAnthropicPromptCacheBreakpoints(t *testing.T) {
	p := NewAnthropicProvider(auth.AuthStore{"anthropic": {Type: "oauth", Access: "t"}}, &config.Config{Stealth: config.DefaultStealthConfig()})
	req := &Request{
		Model: "claude-x",
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

	// Exactly one breakpoint each on: last tool, last system block, last message.
	if ar.Tools[0].CacheControl != nil || ar.Tools[1].CacheControl == nil {
		t.Errorf("cache breakpoint must be on the LAST tool only")
	}
	if ar.System[0].CacheControl != nil || ar.System[1].CacheControl == nil {
		t.Errorf("cache breakpoint must be on the LAST system block only")
	}
	last := ar.Messages[len(ar.Messages)-1]
	if last.Content[len(last.Content)-1].CacheControl == nil {
		t.Errorf("cache breakpoint must be on the last message's last block")
	}
	if ar.Messages[0].Content[0].CacheControl != nil {
		t.Errorf("earlier messages must not carry a cache breakpoint")
	}

	// Wire format must be an object {"type":"ephemeral"}, never a bare string.
	blob, _ := json.Marshal(ar)
	if !strings.Contains(string(blob), `"cache_control":{"type":"ephemeral"}`) {
		t.Fatalf("cache_control must serialize as an object, got: %s", blob)
	}
	if strings.Contains(string(blob), `"cache_control":"`) {
		t.Fatalf("cache_control serialized as a bare string (invalid): %s", blob)
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

func TestAnthropicEffortMapsToThinking(t *testing.T) {
	p := NewAnthropicProvider(auth.AuthStore{"anthropic": {Type: "api_key", Key: "k"}}, &config.Config{Stealth: config.DefaultStealthConfig()})

	// No effort: no thinking block, no top-level effort field.
	plain := p.buildAnthropicRequest(&Request{Model: "claude-opus-4-8", MaxTokens: 100}, false)
	if plain.Thinking != nil {
		t.Fatalf("expected no thinking block when effort empty")
	}
	body, _ := json.Marshal(plain)
	if strings.Contains(string(body), "\"effort\"") {
		t.Fatalf("request must not contain top-level effort field: %s", body)
	}

	// xhigh effort: thinking enabled, max_tokens raised above budget.
	hi := p.buildAnthropicRequest(&Request{Model: "claude-opus-4-8", MaxTokens: 100, Effort: "xhigh"}, false)
	if hi.Thinking == nil || hi.Thinking.Type != "enabled" {
		t.Fatalf("expected thinking enabled for xhigh")
	}
	if hi.Thinking.BudgetTokens <= 0 || hi.MaxTokens <= hi.Thinking.BudgetTokens {
		t.Fatalf("max_tokens (%d) must exceed thinking budget (%d)", hi.MaxTokens, hi.Thinking.BudgetTokens)
	}
}

func TestAnthropicToolResultsBecomeUserMessage(t *testing.T) {
	p := NewAnthropicProvider(auth.AuthStore{"anthropic": {Type: "api_key", Key: "k"}}, &config.Config{Stealth: config.DefaultStealthConfig()})

	req := &Request{
		Model: "claude-opus-4-8",
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
	go p.pumpSSE(context.Background(), &stringReadCloser{strings.NewReader(sse)}, ch)

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
	go p.pumpSSE(context.Background(), &stringReadCloser{strings.NewReader(sse)}, ch)

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
		Model:  "claude-opus-4-8",
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
		Model: "claude-opus-4-8", // no effort → thinking disabled
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

func TestAnthropicUnsignedThinkingDegradesToText(t *testing.T) {
	p := NewAnthropicProvider(auth.AuthStore{"anthropic": {Type: "api_key", Key: "k"}}, &config.Config{Stealth: config.DefaultStealthConfig()})
	req := &Request{
		Model:  "claude-opus-4-8",
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
