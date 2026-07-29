package provider

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/auth"
)

// webSearchSSE is a trimmed real response (cc-sniff capture 0029): the
// server_tool_use block, a web_search_tool_result carrying two results, then
// the helper model's prose.
const webSearchSSE = `event: message_start
data: {"type":"message_start","message":{"model":"claude-haiku-4-5-20251001","usage":{"input_tokens":2351}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\": \"x\"}"}}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[{"type":"web_search_result","title":"First","url":"https://one.example","encrypted_content":"SECRET","page_age":null},{"type":"web_search_result","title":"Second","url":"https://two.example","encrypted_content":"ALSOSECRET"}]}}

event: content_block_start
data: {"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"Simplicity "}}

event: content_block_delta
data: {"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"wins."}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":461,"server_tool_use":{"web_search_requests":1}}}

event: message_stop
data: {"type":"message_stop"}
`

func oauthProvider(t *testing.T, url string) *AnthropicProvider {
	t.Helper()
	store := auth.AuthStore{"anthropic": auth.AuthEntry{
		Type: "oauth", Access: "tok", Refresh: "ref", Expires: 1 << 62,
	}}
	p := NewAnthropicProvider(store, nil)
	p.baseURL = url
	return p
}

// TestAnthropicWebSearch_RequestShape pins the wire shape against the captured
// original: small model, the server-side web_search tool with max_uses, the
// stealth system-block layout, and the helper-specific beta list.
func TestAnthropicWebSearch_RequestShape(t *testing.T) {
	server := newFakeSSEServer(webSearchSSE)
	defer server.Close()
	p := oauthProvider(t, server.URL)

	if _, _, err := p.WebSearch(context.Background(), "suckless philosophy", 0); err != nil {
		t.Fatalf("WebSearch: %v", err)
	}

	var body struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
		Stream    bool   `json:"stream"`
		System    []struct {
			Text string `json:"text"`
		} `json:"system"`
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Type    string `json:"type"`
			Name    string `json:"name"`
			MaxUses int    `json:"max_uses"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(server.lastBody, &body); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if body.Model != anthropicWebModel {
		t.Errorf("model = %q, want %q", body.Model, anthropicWebModel)
	}
	if !body.Stream || body.MaxTokens != anthropicWebMaxTokens {
		t.Errorf("stream = %v, max_tokens = %d", body.Stream, body.MaxTokens)
	}
	if len(body.Tools) != 1 || body.Tools[0].Type != anthropicWebSearchTool ||
		body.Tools[0].Name != "web_search" || body.Tools[0].MaxUses != anthropicWebSearchMaxUses {
		t.Errorf("tools = %+v, want the server-side web_search tool", body.Tools)
	}
	if len(body.System) != 3 {
		t.Fatalf("system blocks = %d, want 3 (billing, identity, task)", len(body.System))
	}
	if !strings.HasPrefix(body.System[0].Text, "x-anthropic-billing-header:") {
		t.Errorf("system[0] = %q, want the billing header", body.System[0].Text)
	}
	if body.System[1].Text != claudeCodeIdentity {
		t.Errorf("system[1] = %q, want the identity line", body.System[1].Text)
	}
	if body.System[2].Text != anthropicWebSearchSystem {
		t.Errorf("system[2] = %q, want the search task line", body.System[2].Text)
	}
	if len(body.Messages) != 1 || body.Messages[0].Content[0].Text != "Perform a web search for the query: suckless philosophy" {
		t.Errorf("messages = %+v", body.Messages)
	}
	if got := server.lastRequest.Header.Get("anthropic-beta"); got != stealthWebBetaHeader() {
		t.Errorf("anthropic-beta = %q, want the helper beta list", got)
	}
}

// TestAnthropicWebSearch_ResultShape checks the string handed to the caller:
// query, JSON links, prose — and no encrypted_content, which Claude Code also
// strips before the result reaches its main loop.
func TestAnthropicWebSearch_ResultShape(t *testing.T) {
	server := newFakeSSEServer(webSearchSSE)
	defer server.Close()
	p := oauthProvider(t, server.URL)

	out, spend, err := p.WebSearch(context.Background(), "suckless philosophy", 0)
	if err != nil {
		t.Fatalf("WebSearch: %v", err)
	}
	// The fixture's message_delta carries no input_tokens (real captures do —
	// see webSearchSSE's doc comment), so the merge must fall back to
	// message_start's 2351.
	if spend.Model != anthropicWebModel || spend.InputTokens != 2351 || spend.OutputTokens != 461 || spend.SearchRequests != 1 {
		t.Errorf("spend = %+v", spend)
	}
	for _, want := range []string{
		`Web search results for query: "suckless philosophy"`,
		`"title":"First","url":"https://one.example"`,
		`"title":"Second","url":"https://two.example"`,
		"Simplicity wins.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\ngot: %s", want, out)
		}
	}
	if strings.Contains(out, "SECRET") {
		t.Error("encrypted_content leaked into the result")
	}
}

// TestAnthropicWebSearch_TrimsToMaxResults keeps the tool's num argument
// meaningful: Anthropic returns up to max_uses*results regardless.
func TestAnthropicWebSearch_TrimsToMaxResults(t *testing.T) {
	server := newFakeSSEServer(webSearchSSE)
	defer server.Close()
	p := oauthProvider(t, server.URL)

	out, _, err := p.WebSearch(context.Background(), "q", 1)
	if err != nil {
		t.Fatalf("WebSearch: %v", err)
	}
	if strings.Contains(out, "two.example") {
		t.Errorf("second link survived num=1:\n%s", out)
	}
}

// TestAnthropicWebSearch_UsesMessageDeltaInputWhenPresent pins the merge's
// priority the other way around from the trimmed fixture above: a real
// capture's message_delta DOES carry the full-loop input_tokens (2351+7506=
// 9857, capture 0029), and that authoritative total must win over
// message_start's first-iteration-only 2351.
func TestAnthropicWebSearch_UsesMessageDeltaInputWhenPresent(t *testing.T) {
	sse := `event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":2351}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}

event: message_delta
data: {"type":"message_delta","usage":{"input_tokens":9857,"output_tokens":461,"server_tool_use":{"web_search_requests":1}}}
`
	server := newFakeSSEServer(sse)
	defer server.Close()
	p := oauthProvider(t, server.URL)

	_, spend, err := p.WebSearch(context.Background(), "q", 0)
	if err != nil {
		t.Fatalf("WebSearch: %v", err)
	}
	if spend.InputTokens != 9857 || spend.OutputTokens != 461 || spend.SearchRequests != 1 {
		t.Errorf("spend = %+v, want the message_delta total (9857/461/1)", spend)
	}
}

// TestAnthropicWebFetchSummarize_UsageHasNoSearchRequests: WebFetch's helper
// call carries no server-side tool, so its spend must never claim a search
// happened — that would double-bill the fee on top of what WebSearch already
// recorded for an unrelated call.
func TestAnthropicWebFetchSummarize_UsageHasNoSearchRequests(t *testing.T) {
	sse := `event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":200}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"It is a placeholder page."}}

event: message_delta
data: {"type":"message_delta","usage":{"output_tokens":88}}
`
	server := newFakeSSEServer(sse)
	defer server.Close()
	p := oauthProvider(t, server.URL)

	_, spend, err := p.WebFetchSummarize(context.Background(), "page", "q")
	if err != nil {
		t.Fatalf("WebFetchSummarize: %v", err)
	}
	if spend.SearchRequests != 0 || spend.InputTokens != 200 || spend.OutputTokens != 88 {
		t.Errorf("spend = %+v", spend)
	}
}

func TestAnthropicWebSearch_EmptyQueryRejected(t *testing.T) {
	p := oauthProvider(t, "http://127.0.0.1:1")
	if _, _, err := p.WebSearch(context.Background(), "  ", 0); err == nil {
		t.Fatal("want an error for an empty query")
	}
}

// TestAnthropicWebFetchSummarize_RequestShape pins WebFetch's helper call: no
// tools, the page pasted in as markdown, the prompt, then Claude Code's
// verbatim guardrails.
func TestAnthropicWebFetchSummarize_RequestShape(t *testing.T) {
	sse := `event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"It is a placeholder page."}}

event: message_stop
data: {"type":"message_stop"}
`
	server := newFakeSSEServer(sse)
	defer server.Close()
	p := oauthProvider(t, server.URL)

	out, spend, err := p.WebFetchSummarize(context.Background(), "# Example Domain\n\nReserved.", "What does this page say?")
	if err != nil {
		t.Fatalf("WebFetchSummarize: %v", err)
	}
	if out != "It is a placeholder page." {
		t.Errorf("answer = %q", out)
	}
	if spend.Model != anthropicWebModel {
		t.Errorf("spend.Model = %q, want %q", spend.Model, anthropicWebModel)
	}

	var body struct {
		Tools    []any `json:"tools"`
		Messages []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
		System []struct {
			Text string `json:"text"`
		} `json:"system"`
	}
	if err := json.Unmarshal(server.lastBody, &body); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if len(body.Tools) != 0 {
		t.Errorf("tools = %v, want none", body.Tools)
	}
	if len(body.System) != 2 {
		t.Errorf("system blocks = %d, want 2 (billing, identity)", len(body.System))
	}
	user := body.Messages[0].Content[0].Text
	for _, want := range []string{"Web page content:", "# Example Domain", "What does this page say?", anthropicWebFetchGuardrails} {
		if !strings.Contains(user, want) {
			t.Errorf("user text missing %q\ngot: %s", want, user)
		}
	}
}

func TestAnthropicWebFetchSummarize_EmptyPageRejected(t *testing.T) {
	p := oauthProvider(t, "http://127.0.0.1:1")
	if _, _, err := p.WebFetchSummarize(context.Background(), "   ", "q"); err == nil {
		t.Fatal("want an error for empty page content")
	}
}

// TestAnthropicWeb_StreamErrorSurfaces: an SSE error event must become the
// caller's error, not a silently empty answer.
func TestAnthropicWeb_StreamErrorSurfaces(t *testing.T) {
	server := newFakeSSEServer("event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"overloaded\"}}\n")
	defer server.Close()
	p := oauthProvider(t, server.URL)

	if _, _, err := p.WebSearch(context.Background(), "q", 0); err == nil || !strings.Contains(err.Error(), "overloaded") {
		t.Fatalf("err = %v, want the stream error", err)
	}
}

// TestAnthropicWeb_APIKeyAuthSkipsStealthBlocks: the billing/identity blocks
// are stealth-OAuth artifacts; an API-key session must not send them, and must
// not send the OAuth-only beta list either.
func TestAnthropicWeb_APIKeyAuthSkipsStealthBlocks(t *testing.T) {
	server := newFakeSSEServer(webSearchSSE)
	defer server.Close()
	p := NewAnthropicProvider(auth.AuthStore{"anthropic": auth.AuthEntry{Type: "api_key", Key: "sk-test"}}, nil)
	p.baseURL = server.URL

	if _, _, err := p.WebSearch(context.Background(), "q", 0); err != nil {
		t.Fatalf("WebSearch: %v", err)
	}
	var body struct {
		System []struct {
			Text string `json:"text"`
		} `json:"system"`
	}
	if err := json.Unmarshal(server.lastBody, &body); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if len(body.System) != 1 || body.System[0].Text != anthropicWebSearchSystem {
		t.Errorf("system = %+v, want only the task line", body.System)
	}
	if got := server.lastRequest.Header.Get("anthropic-beta"); got != "" {
		t.Errorf("anthropic-beta = %q, want none on API-key auth", got)
	}
	if got := server.lastRequest.Header.Get("x-api-key"); got != "sk-test" {
		t.Errorf("x-api-key = %q", got)
	}
}
