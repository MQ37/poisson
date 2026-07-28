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
