package provider

import (
	"context"
	"encoding/json"
	"errors"
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
		Usage:            Usage{InputTokens: 10, OutputTokens: 5},
		CacheReadTokens:  3,
		CacheWriteTokens: 1,
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

// --- FakeProvider tests (no real API calls) ---------------------------

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
