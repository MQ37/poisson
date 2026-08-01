package provider

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/auth"
	"github.com/mq37/poisson/internal/config"
)

func TestXAIModels(t *testing.T) {
	p := NewXAIProvider(auth.AuthStore{}, config.DefaultConfig())
	models, err := p.Models()
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) == 0 {
		t.Fatal("no models")
	}
	found := false
	for _, m := range models {
		if m.ID == "grok-build" {
			found = true
		}
	}
	if !found {
		t.Error("grok-build not found")
	}
}

// TestXAIBuildRequestToolResultFoldedIntoUserTurn covers /btw's shape (see
// quickanswer.go's pendingToolResultBlocks): a placeholder tool_result for a
// still-running tool call folded into the same Role:"user" message as the
// question, rather than a separate Role:"tool" message. xAI's flat
// role-tagged message list has no alternation constraint, so this must
// still surface as its own role:"tool" message — not get silently dropped.
func TestXAIBuildRequestToolResultFoldedIntoUserTurn(t *testing.T) {
	p := NewXAIProvider(auth.AuthStore{}, config.DefaultConfig())
	req := &Request{
		Model: "grok-build",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{
				{Type: "tool_result", ToolCallID: "call_1", ToolResult: "still running"},
				{Type: "text", Text: "what's it doing?"},
			}},
		},
	}
	xaiReq := p.buildRequest(req)

	if len(xaiReq.Messages) != 2 {
		t.Fatalf("messages = %+v, want 2 (tool + user)", xaiReq.Messages)
	}
	if xaiReq.Messages[0].Role != "tool" || xaiReq.Messages[0].ToolCallID != "call_1" {
		t.Errorf("message 0 = %+v, want tool result for call_1", xaiReq.Messages[0])
	}
	if xaiReq.Messages[1].Role != "user" {
		t.Errorf("message 1 = %+v, want user", xaiReq.Messages[1])
	}
}

func TestXAIBuildRequest(t *testing.T) {
	p := NewXAIProvider(auth.AuthStore{}, config.DefaultConfig())
	req := &Request{
		Model:  "grok-build",
		System: []SystemBlock{{Text: "You are helpful."}},
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hello"}}},
		},
		Tools: []ToolDef{
			{Name: "bash", Description: "Run bash", Schema: json.RawMessage(`{"type":"object"}`)},
		},
		MaxTokens: 100,
	}
	xaiReq := p.buildRequest(req)
	if xaiReq.Model != "grok-build" {
		t.Errorf("model = %q", xaiReq.Model)
	}
	if !xaiReq.Stream {
		t.Error("should be streaming")
	}
	if xaiReq.StreamOptions == nil || !xaiReq.StreamOptions.IncludeUsage {
		t.Error("stream_options.include_usage must be enabled for xAI token accounting")
	}
	// System should be first message.
	if len(xaiReq.Messages) < 2 {
		t.Fatalf("expected 2 messages, got %d", len(xaiReq.Messages))
	}
	if xaiReq.Messages[0].Role != "system" {
		t.Error("first message should be system")
	}
	if xaiReq.Messages[1].Role != "user" {
		t.Error("second message should be user")
	}
	// Tools.
	if len(xaiReq.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(xaiReq.Tools))
	}
	if xaiReq.Tools[0].Function.Name != "bash" {
		t.Errorf("tool name = %q", xaiReq.Tools[0].Function.Name)
	}
}

func TestXAIStreamText(t *testing.T) {
	// Simulate OpenAI-compatible SSE response.
	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"hello from grok\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":15,\"completion_tokens\":5}}\n\ndata: [DONE]\n\n"

	server := newFakeSSEServer(sse)
	defer server.Close()

	authStore := auth.AuthStore{
		"xai": {Type: "oauth", Access: "test-token", Expires: 9999999999999},
	}
	p := NewXAIProvider(authStore, config.DefaultConfig())
	// Override the API URL by modifying the provider's client to use the test server.
	// Since XAIProvider hardcodes the URL, we need to test via the SSE parser directly.
	_ = server
	_ = p
}

// TestSSEParseReasoningContent verifies reasoning_content (ollama/DeepSeek-style)
// and reasoning (xAI/alternate) fields are parsed and emitted as EventThinkingDelta.
func TestSSEParseReasoningContent(t *testing.T) {
	sse := "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"thinking...\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"reasoning\":\"more thought\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"answer\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":1}}\n\n" +
		"data: [DONE]\n\n"

	ch := make(chan StreamEvent, 64)
	go pumpXAISSETest(context.Background(), &stringReadCloser{strings.NewReader(sse)}, ch)

	var thinking, text string
	for ev := range ch {
		switch ev.Type {
		case EventThinkingDelta:
			thinking += ev.Text
		case EventTextDelta:
			text += ev.Text
		case EventError:
			t.Fatalf("error: %v", ev.Error)
		}
	}
	if thinking != "thinking...more thought" {
		t.Errorf("thinking = %q, want %q", thinking, "thinking...more thought")
	}
	if text != "answer" {
		t.Errorf("text = %q, want %q", text, "answer")
	}
}

func TestXAISSEParseText(t *testing.T) {
	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"hello from grok\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":15,\"completion_tokens\":5}}\n\ndata: [DONE]\n\n"

	ch := make(chan StreamEvent, 64)
	go pumpXAISSETest(context.Background(), &stringReadCloser{strings.NewReader(sse)}, ch)

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
	if text != "hello from grok" {
		t.Errorf("text = %q, want %q", text, "hello from grok")
	}
	if done == nil {
		t.Fatal("no done event")
	}
	if done.InputTokens != 15 || done.OutputTokens != 5 {
		t.Errorf("usage = %+v, want {15, 5}", done)
	}
}

func TestXAISSEParseReasoningTokens(t *testing.T) {
	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":131,\"completion_tokens\":3,\"completion_tokens_details\":{\"reasoning_tokens\":762},\"total_tokens\":896}}\n\ndata: [DONE]\n\n"

	ch := make(chan StreamEvent, 64)
	go pumpXAISSETest(context.Background(), &stringReadCloser{strings.NewReader(sse)}, ch)

	var done *Usage
	for ev := range ch {
		if ev.Type == EventDone {
			done = ev.Usage
		}
	}
	if done == nil {
		t.Fatal("no done event")
	}
	if done.InputTokens != 131 || done.OutputTokens != 765 {
		t.Errorf("usage = %+v, want input=131 output=765", done)
	}
}

func TestXAISSEParseUsageOnlyChunk(t *testing.T) {
	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"completion_tokens_details\":{\"reasoning_tokens\":8},\"total_tokens\":20}}\n\ndata: [DONE]\n\n"

	ch := make(chan StreamEvent, 64)
	go pumpXAISSETest(context.Background(), &stringReadCloser{strings.NewReader(sse)}, ch)

	var done *Usage
	for ev := range ch {
		if ev.Type == EventDone {
			done = ev.Usage
		}
	}
	if done == nil {
		t.Fatal("no done event")
	}
	if done.InputTokens != 10 || done.OutputTokens != 10 {
		t.Errorf("usage = %+v, want input=10 output=10", done)
	}
}

// TestConvertXAIUsageSplitsCachedTokens verifies convertXAIUsage reads
// prompt_tokens_details.cached_tokens (previously never parsed — cache hits
// were always counted as full-price input) and subtracts it from
// InputTokens, matching the convention convertOpenAIRespUsage already uses.
func TestConvertXAIUsageSplitsCachedTokens(t *testing.T) {
	u := &openaiSSEUsage{PromptTokens: 1000, CompletionTokens: 50, TotalTokens: 1050}
	u.PromptTokensDetails.CachedTokens = 900
	got := convertXAIUsage(u)
	if got.InputTokens != 100 || got.CacheReadTokens != 900 || got.OutputTokens != 50 {
		t.Errorf("usage = %+v, want input=100 cacheRead=900 output=50", got)
	}
	// No cache info at all: behaves exactly as before (input unchanged, no
	// cache read reported).
	plain := convertXAIUsage(&openaiSSEUsage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12})
	if plain.InputTokens != 10 || plain.CacheReadTokens != 0 {
		t.Errorf("usage = %+v, want input=10 cacheRead=0", plain)
	}
}

func TestXAISSEParseToolCall(t *testing.T) {
	sse := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"read\",\"arguments\":\"{\\\"path\\\":\\\"main.go\\\"}\"}}]},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":10}}\n\ndata: [DONE]\n\n"

	ch := make(chan StreamEvent, 64)
	go pumpXAISSETest(context.Background(), &stringReadCloser{strings.NewReader(sse)}, ch)

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
					t.Errorf("tool input = %v, want path=main.go", input)
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
		t.Errorf("tool name = %q, want read", toolCalls[0].Name)
	}
	if done == nil || done.InputTokens != 20 || done.OutputTokens != 10 {
		t.Errorf("usage mismatch: %+v", done)
	}
}

func TestXAISSEParseInterleavedToolCalls(t *testing.T) {
	sse := `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":""}},{"index":1,"id":"call_2","type":"function","function":{"name":"write","arguments":""}}]},"finish_reason":null}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":\"a"}},{"index":1,"function":{"arguments":"{\"path\":\"b"}}]},"finish_reason":null}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":".txt\"}"}},{"index":1,"function":{"arguments":".txt\"}"}}]},"finish_reason":null}]}

data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":20,"completion_tokens":10}}

data: [DONE]

`

	ch := make(chan StreamEvent, 64)
	go pumpXAISSETest(context.Background(), &stringReadCloser{strings.NewReader(sse)}, ch)

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
	if got["call_1"] != "a.txt" || got["call_2"] != "b.txt" {
		t.Fatalf("tool inputs = %#v, want call_1=a.txt call_2=b.txt", got)
	}
}

func TestXAINoCredentials(t *testing.T) {
	p := NewXAIProvider(auth.AuthStore{}, config.DefaultConfig())
	_, err := p.Stream(context.Background(), &Request{Model: "grok-build"})
	if err == nil {
		t.Fatal("expected error with no credentials")
	}
	if !strings.Contains(err.Error(), "credentials") {
		t.Errorf("error should mention credentials, got: %v", err)
	}
}

// stringReadCloser wraps a strings.Reader as an io.ReadCloser.
type stringReadCloser struct {
	*strings.Reader
}

func (s *stringReadCloser) Close() error { return nil }
func TestXAIBuildRequestContentField(t *testing.T) {
	// Every message must have a content field set (not nil) or xAI returns 422.
	p := NewXAIProvider(auth.AuthStore{}, config.DefaultConfig())
	req := &Request{
		Model: "grok-build",
		Messages: []Message{
			// Assistant message with ONLY a tool call (no text).
			{Role: "assistant", Content: []ContentBlock{
				{Type: "tool_use", ToolCallID: "c1", ToolName: "read", ToolInput: json.RawMessage(`{"path":"x"}`)},
			}},
			// Tool result.
			{Role: "tool", Content: []ContentBlock{
				{Type: "tool_result", ToolCallID: "c1", ToolResult: "contents"},
			}},
		},
	}
	xaiReq := p.buildRequest(req)
	for i, m := range xaiReq.Messages {
		if m.Content == nil {
			t.Errorf("message[%d] (role %s) has nil content — xAI requires content field", i, m.Role)
		}
	}
	// The assistant message should have empty-string content + tool calls.
	var asst *xaiMessage
	for i := range xaiReq.Messages {
		if xaiReq.Messages[i].Role == "assistant" {
			asst = &xaiReq.Messages[i]
		}
	}
	if asst == nil {
		t.Fatal("no assistant message")
	}
	if s, ok := asst.Content.(string); !ok || s != "" {
		t.Errorf("assistant content should be empty string, got %v", asst.Content)
	}
	if len(asst.ToolCalls) != 1 {
		t.Errorf("assistant should have 1 tool call, got %d", len(asst.ToolCalls))
	}
	toolMessages := 0
	for _, m := range xaiReq.Messages {
		if m.Role == "tool" {
			toolMessages++
			if m.ToolCallID == "" {
				t.Fatalf("tool message missing tool_call_id: %+v", m)
			}
		}
	}
	if toolMessages != 1 {
		t.Fatalf("tool messages = %d, want 1: %+v", toolMessages, xaiReq.Messages)
	}
}

func TestXAIBuildRequestEffort(t *testing.T) {
	// xAI doesn't use the effort field but the request should still build.
	p := NewXAIProvider(auth.AuthStore{}, config.DefaultConfig())
	req := &Request{
		Model:    "grok-build",
		Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
		Effort:   "high",
	}
	xaiReq := p.buildRequest(req)
	if len(xaiReq.Messages) != 1 {
		t.Errorf("expected 1 message, got %d", len(xaiReq.Messages))
	}
}
