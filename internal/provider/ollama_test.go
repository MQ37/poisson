package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// --- Types test (always runs, no Ollama required) ---------------------

// TestTypes verifies the provider type definitions behave as documented.
func TestTypes(t *testing.T) {
	req := &Request{
		Model: "gemma4:12b",
		System: []SystemBlock{
			{Text: "You are helpful.", CacheCtl: ""},
			{Text: "Be terse.", CacheCtl: "ephemeral"},
		},
		Messages: []Message{
			{
				Role: "user",
				Content: []ContentBlock{
					{Type: "text", Text: "Hello"},
				},
			},
			{
				Role: "assistant",
				Content: []ContentBlock{
					{Type: "text", Text: "Hi there!"},
					{
						Type:       "tool_use",
						ToolCallID: "call_1",
						ToolName:   "get_weather",
						ToolInput:  json.RawMessage(`{"city":"Paris"}`),
					},
				},
			},
			{
				Role: "tool",
				Content: []ContentBlock{
					{
						Type:       "tool_result",
						ToolCallID: "call_1",
						ToolResult: "Sunny, 22C",
					},
				},
			},
		},
		Tools: []ToolDef{
			{
				Name:        "get_weather",
				Description: "Get weather for a city",
				Schema:      json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`),
			},
		},
		MaxTokens: 256,
	}
	ttemp := 0.7
	req.Temperature = &ttemp

	if req.Model != "gemma4:12b" {
		t.Fatalf("Model = %q, want %q", req.Model, "gemma4:12b")
	}
	if len(req.System) != 2 {
		t.Fatalf("len(System) = %d, want 2", len(req.System))
	}
	if req.System[1].CacheCtl != "ephemeral" {
		t.Fatalf("System[1].CacheCtl = %q, want %q", req.System[1].CacheCtl, "ephemeral")
	}
	if len(req.Messages) != 3 {
		t.Fatalf("len(Messages) = %d, want 3", len(req.Messages))
	}
	if req.Messages[0].Role != "user" || req.Messages[0].Content[0].Text != "Hello" {
		t.Fatalf("unexpected first message: %+v", req.Messages[0])
	}
	tu := req.Messages[1].Content[1]
	if tu.Type != "tool_use" || tu.ToolName != "get_weather" || tu.ToolCallID != "call_1" {
		t.Fatalf("unexpected tool_use block: %+v", tu)
	}
	var args map[string]string
	if err := json.Unmarshal(tu.ToolInput, &args); err != nil {
		t.Fatalf("unmarshal ToolInput: %v", err)
	}
	if args["city"] != "Paris" {
		t.Fatalf("ToolInput city = %q, want %q", args["city"], "Paris")
	}
	tr := req.Messages[2].Content[0]
	if tr.Type != "tool_result" || tr.ToolResult != "Sunny, 22C" || tr.ToolCallID != "call_1" {
		t.Fatalf("unexpected tool_result block: %+v", tr)
	}
	if len(req.Tools) != 1 || req.Tools[0].Name != "get_weather" {
		t.Fatalf("unexpected tools: %+v", req.Tools)
	}
	if req.MaxTokens != 256 {
		t.Fatalf("MaxTokens = %d, want 256", req.MaxTokens)
	}
	if req.Temperature == nil || *req.Temperature != 0.7 {
		t.Fatalf("Temperature = %v, want 0.7", req.Temperature)
	}

	if EventTextDelta == EventDone {
		t.Fatal("EventTextDelta must differ from EventDone")
	}
	got := map[StreamEventType]bool{}
	for _, ev := range []StreamEventType{EventTextDelta, EventToolUseStart, EventToolUseDelta, EventToolUseStop, EventDone, EventError} {
		if got[ev] {
			t.Fatalf("duplicate StreamEventType value: %d", ev)
		}
		got[ev] = true
	}

	au := &AnthropicUsage{
		Usage: Usage{InputTokens: 10, OutputTokens: 5, CacheReadTokens: 3, CacheWriteTokens: 1},
	}
	var _ *Usage = &au.Usage
	if au.InputTokens != 10 || au.OutputTokens != 5 || au.CacheReadTokens != 3 || au.CacheWriteTokens != 1 {
		t.Fatalf("unexpected AnthropicUsage: %+v", au)
	}

	res := ToolResult{Content: "ok"}
	if res.Error != "" {
		t.Fatalf("zero-value ToolResult.Error = %q, want empty", res.Error)
	}
}

func TestOllamaModelsUsesModelAsFallbackID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"models":[{"model":"fallback-model","details":{"context_length":12345}}]}`))
	}))
	defer server.Close()

	p := NewOllamaProvider(server.URL, "")
	models, err := p.Models()
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("models = %d", len(models))
	}
	if models[0].ID != "fallback-model" || models[0].Name != "fallback-model" {
		t.Fatalf("model = %+v, want fallback-model id/name", models[0])
	}
	if models[0].ContextWindow != 12345 {
		t.Fatalf("context = %d, want 12345", models[0].ContextWindow)
	}
}

// --- FakeProvider tests (no real API calls) ---------------------------

func TestOllamaBuildRequestUsesOpenAIEndpointFields(t *testing.T) {
	p := NewOllamaProvider("http://localhost:11434", "glm-5.2:cloud")
	temp := 0.4
	req := &Request{
		Model:       "glm-5.2:cloud", // effort-capable model (minimax/kimi expose no effort)
		MaxTokens:   512,
		Temperature: &temp,
		Effort:      "xhigh",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}},
		},
	}
	body := p.buildOllamaRequest(req)
	if !body.Stream || body.StreamOptions == nil || !body.StreamOptions.IncludeUsage {
		t.Fatalf("stream options = %+v, want include_usage", body.StreamOptions)
	}
	if body.MaxTokens != 512 {
		t.Fatalf("max_tokens = %d", body.MaxTokens)
	}
	if body.ReasoningEffort != "high" {
		t.Fatalf("reasoning_effort = %q, want high", body.ReasoningEffort)
	}
	if len(body.Messages) != 1 || body.Messages[0].Content == nil || *body.Messages[0].Content != "hi" {
		t.Fatalf("messages = %+v", body.Messages)
	}
}

func TestOllamaStreamUsesV1ChatCompletions(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()

	p := NewOllamaProvider(server.URL, "test")
	ch, err := p.Stream(context.Background(), &Request{
		Model:    "test",
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
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("path = %q, want /v1/chat/completions", gotPath)
	}
	if gotBody["stream"] != true {
		t.Fatalf("body stream = %v", gotBody["stream"])
	}
	opts, _ := gotBody["stream_options"].(map[string]any)
	if opts == nil || opts["include_usage"] != true {
		t.Fatalf("stream_options = %v", gotBody["stream_options"])
	}
	if text != "ok" {
		t.Fatalf("text = %q", text)
	}
	if done == nil || done.InputTokens != 9 || done.OutputTokens != 2 || done.InputTokensUnknown {
		t.Fatalf("usage = %+v", done)
	}
}

func TestOllamaUsageFallsBackToRequestEstimateWhenPromptZero(t *testing.T) {
	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":0,\"completion_tokens\":62}}\n\n" +
		"data: [DONE]\n\n"
	ch := make(chan StreamEvent, 16)
	go pumpOllamaSSETest(context.Background(), &stringReadCloser{strings.NewReader(sse)}, ch, 42)

	var done *Usage
	for ev := range ch {
		if ev.Type == EventDone {
			done = ev.Usage
		}
	}
	if done == nil {
		t.Fatal("missing done")
	}
	if !done.InputTokensUnknown || done.InputTokens != 42 || done.OutputTokens != 62 {
		t.Fatalf("usage = %+v, want estimated input=42 output=62", done)
	}
}

func TestEstimateOllamaRequestTokens(t *testing.T) {
	req := &Request{
		System: []SystemBlock{{Text: strings.Repeat("a", 40)}},
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: strings.Repeat("b", 40)}}},
		},
	}
	if got := estimateOllamaRequestTokens(req); got != 20 {
		t.Fatalf("estimate = %d, want 20", got)
	}
}

func TestOllamaUsageFromOpenAIUsageChunk(t *testing.T) {
	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":177,\"completion_tokens\":46}}\n\n" +
		"data: [DONE]\n\n"
	ch := make(chan StreamEvent, 16)
	go pumpOllamaSSETest(context.Background(), &stringReadCloser{strings.NewReader(sse)}, ch, 0)

	var done *Usage
	for ev := range ch {
		if ev.Type == EventDone {
			done = ev.Usage
		}
	}
	if done == nil {
		t.Fatal("missing done")
	}
	if done.InputTokensUnknown || done.InputTokens != 177 || done.OutputTokens != 46 {
		t.Fatalf("usage = %+v, want input=177 output=46", done)
	}
}

func TestOllamaToolCallGetsSyntheticID(t *testing.T) {
	sse := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":2,\"function\":{\"name\":\"read\",\"arguments\":\"{\\\"path\\\":\\\"main.go\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}\n\n" +
		"data: [DONE]\n\n"
	ch := make(chan StreamEvent, 16)
	go pumpOllamaSSETest(context.Background(), &stringReadCloser{strings.NewReader(sse)}, ch, 0)

	var start *ToolCall
	var stop *ToolCall
	for ev := range ch {
		switch ev.Type {
		case EventToolUseStart:
			start = ev.ToolCall
		case EventToolUseStop:
			stop = ev.ToolCall
		case EventError:
			t.Fatalf("error: %v", ev.Error)
		}
	}
	if start == nil || start.ID != "idx_2" {
		t.Fatalf("start = %+v, want synthetic id idx_2", start)
	}
	if stop == nil || stop.ID != "idx_2" {
		t.Fatalf("stop = %+v, want synthetic id idx_2", stop)
	}
}

func TestFakeProviderTextResponse(t *testing.T) {
	p := NewFakeProvider("fake", []Model{{ID: "test-model", Name: "Test", ContextWindow: 8192}})
	p.SetResponses([][]StreamEvent{
		FakeTextResponse("hello world", &Usage{InputTokens: 5, OutputTokens: 3}),
	})

	ch, err := p.Stream(context.Background(), &Request{Model: "test-model"})
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
			t.Fatalf("unexpected error: %v", ev.Error)
		}
	}
	if text != "hello world" {
		t.Errorf("text = %q, want %q", text, "hello world")
	}
	if done == nil {
		t.Fatal("no EventDone")
	}
	if done.InputTokens != 5 || done.OutputTokens != 3 {
		t.Errorf("usage = %+v, want {5, 3}", done)
	}
}

func TestFakeProviderToolCall(t *testing.T) {
	p := NewFakeProvider("fake", []Model{{ID: "test-model", ContextWindow: 8192}})
	first, second := FakeToolCallResponse("read", map[string]interface{}{"path": "main.go"}, "The file contains main().")
	p.SetResponses([][]StreamEvent{first, second})

	// First call: should produce a tool call.
	ch, err := p.Stream(context.Background(), &Request{Model: "test-model"})
	if err != nil {
		t.Fatalf("Stream 1: %v", err)
	}
	var toolCalls []*ToolCall
	for ev := range ch {
		if ev.Type == EventToolUseStart && ev.ToolCall != nil {
			toolCalls = append(toolCalls, ev.ToolCall)
		}
	}
	if len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(toolCalls))
	}
	if toolCalls[0].Name != "read" {
		t.Errorf("tool name = %q, want %q", toolCalls[0].Name, "read")
	}

	// Second call: should produce text.
	ch2, err := p.Stream(context.Background(), &Request{Model: "test-model"})
	if err != nil {
		t.Fatalf("Stream 2: %v", err)
	}
	var text string
	for ev := range ch2 {
		if ev.Type == EventTextDelta {
			text += ev.Text
		}
	}
	if text != "The file contains main()." {
		t.Errorf("text = %q, want %q", text, "The file contains main().")
	}
}

func TestFakeProviderCancellation(t *testing.T) {
	p := NewFakeProvider("fake", nil)
	// Emit events slowly via a custom response.
	p.SetResponses([][]StreamEvent{
		{
			{Type: EventTextDelta, Text: "first"},
			{Type: EventTextDelta, Text: "second"},
			{Type: EventDone, Usage: &Usage{}},
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	ch, _ := p.Stream(ctx, &Request{Model: "test"})

	// Read one event, then cancel.
	ev := <-ch
	if ev.Text != "first" {
		t.Fatalf("first event text = %q, want %q", ev.Text, "first")
	}
	cancel()

	// Channel should close without error or done event.
	closeTimeout := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return // success
			}
		case <-closeTimeout:
			t.Fatal("channel did not close after cancel")
		}
	}
}

func TestFakeProviderError(t *testing.T) {
	p := NewFakeProvider("fake", nil)
	testErr := errors.New("simulated provider failure")
	p.SetResponses([][]StreamEvent{FakeErrorResponse(testErr)})

	ch, _ := p.Stream(context.Background(), &Request{Model: "test"})
	var gotErr error
	for ev := range ch {
		if ev.Type == EventError {
			gotErr = ev.Error
		}
	}
	if gotErr == nil {
		t.Fatal("expected error event, got none")
	}
	if gotErr.Error() != "simulated provider failure" {
		t.Errorf("error = %q, want %q", gotErr.Error(), "simulated provider failure")
	}
}

func TestFakeProviderCallCount(t *testing.T) {
	p := NewFakeProvider("fake", []Model{{ID: "m1", ContextWindow: 4096}})
	p.SetResponses([][]StreamEvent{
		FakeTextResponse("one", nil),
		FakeTextResponse("two", nil),
	})

	p.Stream(context.Background(), &Request{Model: "m1"})
	p.Stream(context.Background(), &Request{Model: "m1"})

	if p.CallCount() != 2 {
		t.Errorf("CallCount = %d, want 2", p.CallCount())
	}
}

func TestFakeProviderLastRequest(t *testing.T) {
	p := NewFakeProvider("fake", []Model{{ID: "m1", ContextWindow: 4096}})
	p.SetResponses([][]StreamEvent{FakeTextResponse("ok", nil)})

	req := &Request{
		Model:    "m1",
		Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
	}
	p.Stream(context.Background(), req)

	if p.LastRequest() != req {
		t.Error("LastRequest should point to the same Request object")
	}
	if p.LastRequest().Model != "m1" {
		t.Errorf("LastRequest model = %q", p.LastRequest().Model)
	}
}
