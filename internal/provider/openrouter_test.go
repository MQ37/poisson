package provider

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/auth"
	"github.com/mq37/poisson/internal/config"
)

func TestOpenRouterModels(t *testing.T) {
	p := NewOpenRouterProvider(auth.AuthStore{}, config.DefaultConfig())
	models, err := p.Models()
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	found := false
	for _, m := range models {
		if m.ID == "deepseek/deepseek-v4-flash-0731" {
			found = true
		}
	}
	if !found {
		t.Error("deepseek/deepseek-v4-flash-0731 not found")
	}
}

func TestOpenRouterBuildRequest(t *testing.T) {
	p := NewOpenRouterProvider(auth.AuthStore{}, config.DefaultConfig())
	req := &Request{
		Model:  "deepseek/deepseek-v4-flash-0731",
		System: []SystemBlock{{Text: "You are helpful."}},
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hello"}}},
		},
		Tools: []ToolDef{
			{Name: "bash", Description: "Run bash", Schema: json.RawMessage(`{"type":"object"}`)},
		},
		MaxTokens: 100,
		Effort:    "high",
	}
	or := p.buildRequest(req)
	if or.Model != "deepseek/deepseek-v4-flash-0731" {
		t.Errorf("model = %q", or.Model)
	}
	if !or.Stream {
		t.Error("should be streaming")
	}
	if or.StreamOptions == nil || !or.StreamOptions.IncludeUsage {
		t.Error("stream_options.include_usage must be enabled for token accounting")
	}
	if or.ReasoningEffort != "high" {
		t.Errorf("reasoning_effort = %q, want high", or.ReasoningEffort)
	}
	if len(or.Messages) != 2 || or.Messages[0].Role != "system" || or.Messages[1].Role != "user" {
		t.Fatalf("messages = %+v, want [system, user]", or.Messages)
	}
	if len(or.Tools) != 1 || or.Tools[0].Function.Name != "bash" {
		t.Fatalf("tools = %+v, want [bash]", or.Tools)
	}
}

// TestOpenRouterBuildRequestToolResultFoldedIntoUserTurn mirrors
// TestXAIBuildRequestToolResultFoldedIntoUserTurn: a placeholder tool_result
// folded into the same user turn (see quickanswer.go's
// pendingToolResultBlocks) must still surface as its own role:"tool"
// message, not get silently dropped, on this flat role-tagged format too.
func TestOpenRouterBuildRequestToolResultFoldedIntoUserTurn(t *testing.T) {
	p := NewOpenRouterProvider(auth.AuthStore{}, config.DefaultConfig())
	req := &Request{
		Model: "deepseek/deepseek-v4-flash-0731",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{
				{Type: "tool_result", ToolCallID: "call_1", ToolResult: "still running"},
				{Type: "text", Text: "what's it doing?"},
			}},
		},
	}
	or := p.buildRequest(req)
	if len(or.Messages) != 2 {
		t.Fatalf("messages = %+v, want 2 (tool + user)", or.Messages)
	}
	if or.Messages[0].Role != "tool" || or.Messages[0].ToolCallID != "call_1" {
		t.Errorf("message 0 = %+v, want tool result for call_1", or.Messages[0])
	}
	if or.Messages[1].Role != "user" {
		t.Errorf("message 1 = %+v, want user", or.Messages[1])
	}
}

func TestOpenRouterStreamText(t *testing.T) {
	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"hi from deepseek\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":15,\"completion_tokens\":5}}\n\n" +
		"data: [DONE]\n\n"

	server := newFakeSSEServer(sse)
	defer server.Close()

	cfg := config.DefaultConfig()
	cfg.OpenRouter.BaseURL = server.URL
	cfg.OpenRouter.APIKey = "sk-or-test"
	p := NewOpenRouterProvider(auth.AuthStore{}, cfg)

	ch, err := p.Stream(context.Background(), &Request{
		Model:    "deepseek/deepseek-v4-flash-0731",
		Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
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
	if text != "hi from deepseek" {
		t.Errorf("text = %q", text)
	}
	if done == nil || done.InputTokens != 15 || done.OutputTokens != 5 {
		t.Errorf("usage = %+v, want {15, 5}", done)
	}
	if server.lastRequest.Header.Get("Authorization") != "Bearer sk-or-test" {
		t.Errorf("Authorization header = %q", server.lastRequest.Header.Get("Authorization"))
	}
}

func TestOpenRouterNoCredentials(t *testing.T) {
	p := NewOpenRouterProvider(auth.AuthStore{}, config.DefaultConfig())
	_, err := p.Stream(context.Background(), &Request{Model: "deepseek/deepseek-v4-flash-0731"})
	if err == nil {
		t.Fatal("expected error with no credentials")
	}
	if !strings.Contains(err.Error(), "credentials") {
		t.Errorf("error should mention credentials, got: %v", err)
	}
}

// TestOpenRouterAPIKeyPrecedence checks auth.json wins over config.toml's
// openrouter.api_key — same precedence Anthropic's non-OAuth path documents.
func TestOpenRouterAPIKeyPrecedence(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.OpenRouter.APIKey = "sk-or-config"
	authStore := auth.AuthStore{"openrouter": {Type: "api_key", Key: "sk-or-authjson"}}
	p := NewOpenRouterProvider(authStore, cfg)
	if got := p.apiKey(); got != "sk-or-authjson" {
		t.Errorf("apiKey() = %q, want auth.json entry to win", got)
	}
}

func TestConvertOpenRouterUsageSplitsCachedTokens(t *testing.T) {
	u := &openaiSSEUsage{PromptTokens: 1000, CompletionTokens: 50, TotalTokens: 1050}
	u.PromptTokensDetails.CachedTokens = 900
	got := convertOpenRouterUsage(u)
	if got.InputTokens != 100 || got.CacheReadTokens != 900 || got.OutputTokens != 50 {
		t.Errorf("usage = %+v, want input=100 cacheRead=900 output=50", got)
	}
}
