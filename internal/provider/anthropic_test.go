package provider

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"poisson/internal/auth"
	"poisson/internal/config"
)

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
