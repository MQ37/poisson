package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/auth"
)

// fakeJWT builds an unsigned JWT whose auth claim carries the given account id.
func fakeJWT(t *testing.T, accountID string) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	claims := map[string]any{jwtAccountClaim: map[string]string{"chatgpt_account_id": accountID}}
	payloadJSON, _ := json.Marshal(claims)
	payload := base64.RawURLEncoding.EncodeToString(payloadJSON)
	return header + "." + payload + ".sig"
}

func TestExtractAccountID(t *testing.T) {
	id, err := extractAccountID(fakeJWT(t, "acc_42"))
	if err != nil || id != "acc_42" {
		t.Fatalf("extractAccountID = %q, %v; want acc_42, nil", id, err)
	}
	if _, err := extractAccountID("not-a-jwt"); err == nil {
		t.Error("malformed token should error")
	}
	empty := base64.RawURLEncoding.EncodeToString([]byte(`{}`))
	if _, err := extractAccountID("h." + empty + ".s"); err == nil {
		t.Error("token without account claim should error")
	}
}

func TestMapOpenAIEffort(t *testing.T) {
	cases := map[string]string{"": "", "low": "low", "medium": "medium", "high": "high", "xhigh": "xhigh", "max": "xhigh"}
	for in, want := range cases {
		if got := mapOpenAIEffort(in); got != want {
			t.Errorf("mapOpenAIEffort(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildRequestResponsesFormat(t *testing.T) {
	p := &OpenAIProvider{}
	req := &Request{
		Model:  "gpt-5.5",
		Effort: "max",
		System: []SystemBlock{{Text: "You are Poisson."}},
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "list files"}}},
			{Role: "assistant", Content: []ContentBlock{
				{Type: "text", Text: "sure"},
				{Type: "tool_use", ToolCallID: "call_1", ToolName: "bash", ToolInput: json.RawMessage(`{"cmd":"ls"}`)},
			}},
			{Role: "tool", Content: []ContentBlock{{Type: "tool_result", ToolCallID: "call_1", ToolResult: "a.txt"}}},
		},
		Tools: []ToolDef{{Name: "bash", Description: "run", Schema: json.RawMessage(`{"type":"object"}`)}},
	}
	body := p.buildRequest(req)

	if body.Store || !body.Stream || body.Instructions != "You are Poisson." {
		t.Errorf("header fields wrong: %+v", body)
	}
	if body.Reasoning == nil || body.Reasoning.Effort != "xhigh" {
		t.Errorf("reasoning effort = %+v, want xhigh (max→xhigh)", body.Reasoning)
	}
	// Expect: user message, assistant message, function_call, function_call_output.
	if len(body.Input) != 4 {
		t.Fatalf("input items = %d, want 4: %+v", len(body.Input), body.Input)
	}
	if body.Input[0].Type != "message" || body.Input[0].Role != "user" || body.Input[0].Content[0].Type != "input_text" {
		t.Errorf("user item wrong: %+v", body.Input[0])
	}
	if body.Input[1].Role != "assistant" || body.Input[1].Content[0].Type != "output_text" {
		t.Errorf("assistant item wrong: %+v", body.Input[1])
	}
	if body.Input[2].Type != "function_call" || body.Input[2].CallID != "call_1" || body.Input[2].Name != "bash" {
		t.Errorf("function_call item wrong: %+v", body.Input[2])
	}
	if body.Input[3].Type != "function_call_output" || body.Input[3].CallID != "call_1" || body.Input[3].Output != "a.txt" {
		t.Errorf("function_call_output item wrong: %+v", body.Input[3])
	}
	if len(body.Tools) != 1 || body.Tools[0].Type != "function" || body.Tools[0].Name != "bash" {
		t.Errorf("tools wrong: %+v", body.Tools)
	}
}

func TestBuildRequestPromptCacheKey(t *testing.T) {
	p := &OpenAIProvider{}
	// A normal session id is emitted verbatim as prompt_cache_key.
	body := p.buildRequest(&Request{Model: "gpt-5.5", CacheKey: "sess-abc123"})
	if body.PromptCacheKey != "sess-abc123" {
		t.Errorf("prompt_cache_key = %q, want sess-abc123", body.PromptCacheKey)
	}
	// Over-long keys are clamped to 64 runes (Responses API limit).
	long := strings.Repeat("x", 100)
	if got := p.buildRequest(&Request{Model: "gpt-5.5", CacheKey: long}).PromptCacheKey; len([]rune(got)) != 64 {
		t.Errorf("clamped key len = %d, want 64", len([]rune(got)))
	}
	// Empty key is omitted from the JSON body.
	blob, _ := json.Marshal(p.buildRequest(&Request{Model: "gpt-5.5"}))
	if strings.Contains(string(blob), "prompt_cache_key") {
		t.Errorf("empty cache key must be omitted, got: %s", blob)
	}
}

const cannedResponsesSSE = `event: response.output_item.added
data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"bash","arguments":""}}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"cmd\":"}

event: response.function_call_arguments.done
data: {"type":"response.function_call_arguments.done","output_index":0,"arguments":"{\"cmd\":\"ls\"}"}

event: response.reasoning_summary_text.delta
data: {"type":"response.reasoning_summary_text.delta","delta":"thinking"}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"Hello"}

event: response.completed
data: {"type":"response.completed","response":{"usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":20},"output_tokens":30,"total_tokens":130}}}

`

func drain(ch <-chan StreamEvent) []StreamEvent {
	var out []StreamEvent
	for ev := range ch {
		out = append(out, ev)
	}
	return out
}

func TestPumpResponsesSSE(t *testing.T) {
	ch := make(chan StreamEvent, 64)
	go pumpOpenAIResponsesSSE(context.Background(), io.NopCloser(strings.NewReader(cannedResponsesSSE)), ch)
	events := drain(ch)

	var text, thinking, toolStop string
	var start, done bool
	var usage *Usage
	for _, ev := range events {
		switch ev.Type {
		case EventTextDelta:
			text += ev.Text
		case EventThinkingDelta:
			thinking += ev.Text
		case EventToolUseStart:
			start = ev.ToolCall != nil && ev.ToolCall.ID == "call_1" && ev.ToolCall.Name == "bash"
		case EventToolUseStop:
			toolStop = string(ev.ToolCall.Input)
		case EventDone:
			done = true
			usage = ev.Usage
		}
	}
	if !start {
		t.Error("missing tool-use start for call_1/bash")
	}
	if toolStop != `{"cmd":"ls"}` {
		t.Errorf("tool stop args = %q, want {\"cmd\":\"ls\"}", toolStop)
	}
	if text != "Hello" {
		t.Errorf("text = %q, want Hello", text)
	}
	if !strings.Contains(thinking, "thinking") {
		t.Errorf("thinking = %q, want to contain 'thinking'", thinking)
	}
	if !done || usage == nil {
		t.Fatal("missing EventDone with usage")
	}
	if usage.InputTokens != 80 || usage.CacheReadTokens != 20 || usage.OutputTokens != 30 {
		t.Errorf("usage = %+v, want input=80 cacheRead=20 output=30", usage)
	}
}

func TestPumpResponsesSSEErrorsAlwaysHaveDetails(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{
			name: "nested error",
			data: `data: {"type":"error","error":{"type":"invalid_request_error","message":"context window exceeded"}}` + "\n\n",
			want: "OpenAI error invalid_request_error: context window exceeded",
		},
		{
			name: "top-level error",
			data: `data: {"type":"error","code":"rate_limit_exceeded","message":"try later"}` + "\n\n",
			want: "OpenAI error rate_limit_exceeded: try later",
		},
		{
			name: "failed response",
			data: `data: {"type":"response.failed","response":{"error":{"code":"server_error","message":"request failed"}}}` + "\n\n",
			want: "OpenAI error server_error: request failed",
		},
		{
			name: "empty error",
			data: `data: {"type":"error"}` + "\n\n",
			want: "OpenAI request failed: provider returned no details",
		},
		{
			name: "unexpected EOF",
			data: "",
			want: "OpenAI stream ended before completion",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch := make(chan StreamEvent, 1)
			go pumpOpenAIResponsesSSE(context.Background(), io.NopCloser(strings.NewReader(tt.data)), ch)
			events := drain(ch)
			if len(events) != 1 || events[0].Type != EventError || events[0].Error == nil {
				t.Fatalf("events = %+v, want one EventError", events)
			}
			if got := events[0].Error.Error(); got != tt.want {
				t.Fatalf("error = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOpenAIStreamEmptyErrorBodyUsesHTTPStatus(t *testing.T) {
	token := fakeJWT(t, "acc_99")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	p := &OpenAIProvider{
		auth:     auth.AuthStore{"openai": {Type: "oauth", Access: token, Expires: 1 << 62}},
		client:   server.Client(),
		endpoint: server.URL,
	}
	_, err := p.Stream(context.Background(), &Request{Model: "gpt-5.5"})
	if err == nil {
		t.Fatal("Stream succeeded, want HTTP error")
	}
	want := "OpenAI API error (status 400): Bad Request"
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
}

func TestOpenAIStreamEndToEnd(t *testing.T) {
	token := fakeJWT(t, "acc_99")
	var gotAuth, gotAccount, gotOriginator, gotModel string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccount = r.Header.Get("chatgpt-account-id")
		gotOriginator = r.Header.Get("originator")
		var body openaiRespRequest
		json.NewDecoder(r.Body).Decode(&body)
		gotModel = body.Model
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, cannedResponsesSSE)
	}))
	defer server.Close()

	p := &OpenAIProvider{
		auth:     auth.AuthStore{"openai": {Type: "oauth", Access: token, Expires: 1 << 62}},
		client:   server.Client(),
		endpoint: server.URL,
	}
	ch, err := p.Stream(context.Background(), &Request{Model: "gpt-5.5"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := drain(ch)

	if gotAuth != "Bearer "+token {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotAccount != "acc_99" {
		t.Errorf("chatgpt-account-id = %q, want acc_99", gotAccount)
	}
	if gotOriginator != "poisson" {
		t.Errorf("originator = %q, want poisson", gotOriginator)
	}
	if gotModel != "gpt-5.5" {
		t.Errorf("model = %q, want gpt-5.5", gotModel)
	}
	var sawText, sawDone bool
	for _, ev := range events {
		if ev.Type == EventTextDelta && ev.Text == "Hello" {
			sawText = true
		}
		if ev.Type == EventDone {
			sawDone = true
		}
	}
	if !sawText || !sawDone {
		t.Errorf("expected text + done events, got %d events", len(events))
	}
}
