package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
)

// fakeSSEServer is a test HTTP server that returns a canned SSE response
// and captures the last request (headers + body) for inspection.
type fakeSSEServer struct {
	*httptest.Server
	lastRequest *http.Request
	lastBody    []byte
}

func newFakeSSEServer(response string) *fakeSSEServer {
	f := &fakeSSEServer{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.lastRequest = r
		// Read the body here, before the handler returns — r.Body isn't
		// reliably readable afterward once the connection may be reused.
		f.lastBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		io.Copy(w, strings.NewReader(response))
	}))
	return f
}

func pumpXAISSETest(ctx context.Context, body io.ReadCloser, ch chan<- StreamEvent) {
	pumpOpenAIChatCompletionsSSE(ctx, body, ch, openaiSSEConfig{
		ConvertUsage: func(u *openaiSSEUsage, _ int) *Usage { return convertXAIUsage(u) },
		ErrPrefix:    "xAI",
	})
}

// SetBaseURLForTests points the provider at a local test server instead of
// the real Anthropic API. Exported (unlike the in-package p.baseURL field
// itself) so cross-package tests — internal/agent's Force*UsageLimits
// wiring tests — can prove the agent-layer entry point reaches this
// provider without a live network call. Production code never calls this.
func (p *AnthropicProvider) SetBaseURLForTests(url string) { p.baseURL = url }

// SetWebBaseURLForTests is SetBaseURLForTests's OpenAI/Codex counterpart —
// points the usage/reset "web" backend (chatgpt.com) at a local test server.
func (p *OpenAIProvider) SetWebBaseURLForTests(url string) { p.webBaseURL = url }

func pumpOllamaSSETest(ctx context.Context, body io.ReadCloser, ch chan<- StreamEvent, inputEstimate int) {
	pumpOpenAIChatCompletionsSSE(ctx, body, ch, openaiSSEConfig{
		InputEstimate:          inputEstimate,
		ConvertUsage:           convertOllamaUsage,
		EmitToolDeltas:         true,
		AllowNameOnlyToolStart: true,
		FailOnParseError:       true,
		EnsureDoneOnEOF:        true,
		ErrPrefix:              "ollama",
	})
}
