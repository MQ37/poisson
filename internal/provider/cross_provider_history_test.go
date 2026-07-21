package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

// anthropicThinkingHistory builds an assistant message shaped exactly like
// one Anthropic's Stream would have produced and agent.go's
// buildAssistantBlocks would have persisted: a thinking block, a redacted
// thinking block, ordinary text, and a tool call. It stands in for a real
// session's history after a mid-session /model switch away from Anthropic —
// the scenario that produced a real 400 (input[N].output missing; fixed in
// 197b35c) once already. These tests guard the sibling risk: that "thinking"
// (Anthropic-only) content blocks replayed under a different provider's
// buildRequest are silently dropped, not mis-serialized into a field that
// provider's API rejects.
func anthropicThinkingHistory() []Message {
	return []Message{
		{Role: "user", Content: []ContentBlock{{Type: "text", Text: "list files"}}},
		{Role: "assistant", Content: []ContentBlock{
			{Type: "thinking", Thinking: "let me check", ThinkingSignature: "sig-1"},
			{Type: "thinking", Redacted: true, ThinkingSignature: "encrypted-blob"},
			{Type: "text", Text: "sure"},
			{Type: "tool_use", ToolCallID: "call_1", ToolName: "bash", ToolInput: json.RawMessage(`{"cmd":"ls"}`)},
		}},
		{Role: "tool", Content: []ContentBlock{{Type: "tool_result", ToolCallID: "call_1", ToolResult: "a.txt"}}},
	}
}

// TestOpenAIBuildRequestDropsThinkingBlocksFromOtherProviderHistory verifies
// the Codex Responses API builder ignores Anthropic-only "thinking" blocks
// left over in history from before a /model switch, instead of emitting a
// malformed item.
func TestOpenAIBuildRequestDropsThinkingBlocksFromOtherProviderHistory(t *testing.T) {
	p := &OpenAIProvider{}
	body := p.buildRequest(&Request{Model: "gpt-5.5", Messages: anthropicThinkingHistory()})

	if len(body.Input) != 4 {
		t.Fatalf("input items = %d, want 4 (user, assistant, function_call, function_call_output — no thinking item), got %+v", len(body.Input), body.Input)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if strings.Contains(string(raw), "sig-1") || strings.Contains(string(raw), "encrypted-blob") {
		t.Errorf("request leaked Anthropic thinking signature onto the wire: %s", raw)
	}
	// The function_call_output for call_1 is a separate "tool" message and
	// must still be present (its own turn, unaffected by the assistant
	// message's thinking blocks).
	foundOutput := false
	for _, item := range body.Input {
		if item.Type == "function_call_output" && item.Output != nil && *item.Output == "a.txt" {
			foundOutput = true
		}
	}
	if !foundOutput {
		t.Errorf("function_call_output for call_1 missing: %+v", body.Input)
	}
}

// TestXAIBuildRequestDropsThinkingBlocksFromOtherProviderHistory verifies
// the xAI chat-completions builder ignores Anthropic-only "thinking" blocks.
func TestXAIBuildRequestDropsThinkingBlocksFromOtherProviderHistory(t *testing.T) {
	p := &XAIProvider{}
	ar := p.buildRequest(&Request{Model: "grok-5", Messages: anthropicThinkingHistory()})

	raw, err := json.Marshal(ar)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if strings.Contains(string(raw), "sig-1") || strings.Contains(string(raw), "encrypted-blob") {
		t.Errorf("request leaked Anthropic thinking signature onto the wire: %s", raw)
	}
	var assistantMsg *xaiMessage
	for i := range ar.Messages {
		if ar.Messages[i].Role == "assistant" {
			assistantMsg = &ar.Messages[i]
		}
	}
	if assistantMsg == nil {
		t.Fatal("no assistant message built")
	}
	if len(assistantMsg.ToolCalls) != 1 || assistantMsg.ToolCalls[0].Function.Name != "bash" {
		t.Errorf("assistant message tool call wrong: %+v", assistantMsg.ToolCalls)
	}
}

// TestOllamaBuildRequestDropsThinkingBlocksFromOtherProviderHistory verifies
// the Ollama chat-completions builder ignores Anthropic-only "thinking"
// blocks.
func TestOllamaBuildRequestDropsThinkingBlocksFromOtherProviderHistory(t *testing.T) {
	p := &OllamaProvider{}
	out := p.buildOllamaRequest(&Request{Model: "qwen3", Messages: anthropicThinkingHistory()})

	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if strings.Contains(string(raw), "sig-1") || strings.Contains(string(raw), "encrypted-blob") {
		t.Errorf("request leaked Anthropic thinking signature onto the wire: %s", raw)
	}
	var assistantMsg *ollamaOpenAIMessage
	for i := range out.Messages {
		if out.Messages[i].Role == "assistant" {
			assistantMsg = &out.Messages[i]
		}
	}
	if assistantMsg == nil {
		t.Fatal("no assistant message built")
	}
	if len(assistantMsg.ToolCalls) != 1 || assistantMsg.ToolCalls[0].Function.Name != "bash" {
		t.Errorf("assistant message tool call wrong: %+v", assistantMsg.ToolCalls)
	}
}
