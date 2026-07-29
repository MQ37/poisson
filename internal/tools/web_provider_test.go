package tools

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/provider"
)

// fakeAnthropicWeb stands in for the real provider-backed Anthropic backend,
// recording what the tools hand it.
type fakeAnthropicWeb struct {
	searchQuery    string
	searchNum      int
	searchOut      string
	searchSpend    provider.WebHelperUsage
	searchErr      error
	page, prompt   string
	summarizeOut   string
	summarizeSpend provider.WebHelperUsage
	summarizeErr   error
}

func (f *fakeAnthropicWeb) WebSearch(_ context.Context, query string, maxResults int) (string, provider.WebHelperUsage, error) {
	f.searchQuery, f.searchNum = query, maxResults
	return f.searchOut, f.searchSpend, f.searchErr
}

func (f *fakeAnthropicWeb) WebFetchSummarize(_ context.Context, page, prompt string) (string, provider.WebHelperUsage, error) {
	f.page, f.prompt = page, prompt
	return f.summarizeOut, f.summarizeSpend, f.summarizeErr
}

// TestWebSearch_AnthropicProviderUsesBackend covers the happy path: the tool
// passes query and num through and returns the backend's text verbatim, with
// no DuckDuckGo request involved.
func TestWebSearch_AnthropicProviderUsesBackend(t *testing.T) {
	be := &fakeAnthropicWeb{
		searchOut:   "Web search results for query: \"go slices\"\n\nLinks: []\n\nprose",
		searchSpend: provider.WebHelperUsage{Usage: provider.Usage{InputTokens: 100, OutputTokens: 20}, Model: "claude-haiku-4-5", SearchRequests: 1},
	}
	tool := NewWebSearchTool(be)
	var recorded []WebCall
	tool.SetUsageFn(func(c WebCall) { recorded = append(recorded, c) })
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"query": "go slices", "num": 4, "provider": "anthropic",
	}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if res.Content != be.searchOut {
		t.Errorf("content = %q, want the backend's output", res.Content)
	}
	if be.searchQuery != "go slices" || be.searchNum != 4 {
		t.Errorf("backend got (%q, %d), want (\"go slices\", 4)", be.searchQuery, be.searchNum)
	}
	if len(recorded) != 1 || recorded[0].Provider != "anthropic" || recorded[0].Model != "claude-haiku-4-5" ||
		recorded[0].Usage.InputTokens != 100 || recorded[0].SearchRequests != 1 || recorded[0].Purpose != webPurposeSearch {
		t.Errorf("recorded = %+v, want the backend's spend banked as %s", recorded, webPurposeSearch)
	}
}

// TestWebSearch_AnthropicProviderRejectedWithoutBackend is the gate: on any
// non-Anthropic session the backend is nil and the call must fail loudly
// instead of silently falling back to DuckDuckGo (which would hide that the
// requested provider never ran).
func TestWebSearch_AnthropicProviderRejectedWithoutBackend(t *testing.T) {
	res, err := NewWebSearchTool(nil).Execute(context.Background(), mustJSON(t, map[string]any{
		"query": "go slices", "provider": "anthropic",
	}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(res.Error, "needs an Anthropic session") {
		t.Fatalf("error = %q, want an Anthropic-session error", res.Error)
	}
	if res.Content != "" {
		t.Errorf("content = %q, want empty", res.Content)
	}
}

// TestWebSearch_AnthropicBackendErrorSurfaces keeps backend failures visible:
// an explicitly requested provider must not silently degrade to DuckDuckGo.
func TestWebSearch_AnthropicBackendErrorSurfaces(t *testing.T) {
	be := &fakeAnthropicWeb{
		searchErr:   errors.New("anthropic web helper HTTP 429: slow down"),
		searchSpend: provider.WebHelperUsage{Usage: provider.Usage{InputTokens: 50}, Model: "claude-haiku-4-5"},
	}
	var recorded []WebCall
	tool := NewWebSearchTool(be)
	tool.SetUsageFn(func(c WebCall) { recorded = append(recorded, c) })
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"query": "q", "provider": "anthropic",
	}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(res.Error, "HTTP 429") {
		t.Fatalf("error = %q, want the backend error", res.Error)
	}
	// Anthropic bills the search whether or not the caller found the reply
	// useful — a 429 mid-stream still carries the tokens spent before it hit.
	if len(recorded) != 1 || recorded[0].Usage.InputTokens != 50 {
		t.Errorf("recorded = %+v, want the spend banked even on a backend error", recorded)
	}
}

func TestWebSearch_UnknownProviderRejected(t *testing.T) {
	res, err := NewWebSearchTool(nil).Execute(context.Background(), mustJSON(t, map[string]any{
		"query": "q", "provider": "bing",
	}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(res.Error, `unknown provider "bing"`) {
		t.Fatalf("error = %q, want an unknown-provider error", res.Error)
	}
}

// TestWebSearch_AnthropicBackendOnlyAdvertisedWhenAvailable checks the
// description the model reads: mentioning provider=anthropic on a session that
// can't use it would invite a call that can only fail.
func TestWebSearch_AnthropicBackendOnlyAdvertisedWhenAvailable(t *testing.T) {
	if strings.Contains(NewWebSearchTool(nil).Description(), "provider=anthropic") {
		t.Error("nil backend must not advertise provider=anthropic")
	}
	if !strings.Contains(NewWebSearchTool(&fakeAnthropicWeb{}).Description(), "provider=anthropic") {
		t.Error("live backend must advertise provider=anthropic")
	}
}

// TestFetch_AnthropicProviderFetchesLocallyThenSummarizes covers the ported
// Claude Code WebFetch shape: the page is fetched in-process and converted to
// Markdown, and only that Markdown (plus the prompt) reaches the model.
func TestFetch_AnthropicProviderFetchesLocallyThenSummarizes(t *testing.T) {
	allowLoopbackFetchForTest(t)
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte("<h1>Title</h1><p>Hello <strong>world</strong>.</p>"))
	}))
	defer srv.Close()

	be := &fakeAnthropicWeb{
		summarizeOut:   "The page greets the world.",
		summarizeSpend: provider.WebHelperUsage{Usage: provider.Usage{InputTokens: 30, OutputTokens: 8}, Model: "claude-haiku-4-5"},
	}
	var recorded []WebCall
	tool := NewFetchTool("", be)
	tool.SetUsageFn(func(c WebCall) { recorded = append(recorded, c) })
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"url": srv.URL, "provider": "anthropic", "prompt": "What does this page say?",
	}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if res.Content != "The page greets the world." {
		t.Errorf("content = %q, want the summarizer's answer", res.Content)
	}
	if hits != 1 {
		t.Errorf("page fetched %d times, want 1 (locally)", hits)
	}
	if be.page != "# Title\n\nHello **world**.\n" {
		t.Errorf("backend page = %q, want the converted Markdown", be.page)
	}
	if be.prompt != "What does this page say?" {
		t.Errorf("backend prompt = %q", be.prompt)
	}
	if len(recorded) != 1 || recorded[0].Provider != "anthropic" || recorded[0].Model != "claude-haiku-4-5" ||
		recorded[0].Usage.InputTokens != 30 || recorded[0].Purpose != webPurposeFetch {
		t.Errorf("recorded = %+v, want the summarizer's spend banked as %s", recorded, webPurposeFetch)
	}
}

// TestFetch_AnthropicProviderRejectedWithoutBackend: the gate, and it must
// refuse *before* fetching anything.
func TestFetch_AnthropicProviderRejectedWithoutBackend(t *testing.T) {
	allowLoopbackFetchForTest(t)
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer srv.Close()

	res, err := NewFetchTool("", nil).Execute(context.Background(), mustJSON(t, map[string]any{
		"url": srv.URL, "provider": "anthropic",
	}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(res.Error, "needs an Anthropic session") {
		t.Fatalf("error = %q, want an Anthropic-session error", res.Error)
	}
	if hits != 0 {
		t.Errorf("page fetched %d times, want 0", hits)
	}
}

// TestFetch_AnthropicProviderPropagatesFetchFailure: a dead page must report
// the fetch error, not an empty summary from the model.
func TestFetch_AnthropicProviderPropagatesFetchFailure(t *testing.T) {
	allowLoopbackFetchForTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	be := &fakeAnthropicWeb{summarizeOut: "should never be used"}
	res, err := NewFetchTool("", be).Execute(context.Background(), mustJSON(t, map[string]any{
		"url": srv.URL, "provider": "anthropic",
	}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(res.Error, "status 404") {
		t.Fatalf("error = %q, want the 404", res.Error)
	}
	if be.page != "" {
		t.Errorf("summarizer was called with %q, want no call", be.page)
	}
}

// TestFetch_AnthropicProviderKeepsSSRFGuard is the parity check that matters
// most: provider=anthropic must not become a way around the loopback/private-IP
// block, since the page is still fetched from this machine.
func TestFetch_AnthropicProviderKeepsSSRFGuard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("loopback page must never be fetched")
	}))
	defer srv.Close()

	be := &fakeAnthropicWeb{summarizeOut: "should never be used"}
	res, err := NewFetchTool("", be).Execute(context.Background(), mustJSON(t, map[string]any{
		"url": srv.URL, "provider": "anthropic",
	}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(res.Error, "refusing to fetch internal address") {
		t.Fatalf("error = %q, want the SSRF refusal", res.Error)
	}
	if be.page != "" {
		t.Errorf("summarizer got %q, want no call", be.page)
	}
}

// TestFetch_AnthropicProviderPassesNonHTMLThrough: a JSON/plain-text page has
// nothing to convert, and the summarizer must still see its content.
func TestFetch_AnthropicProviderPassesNonHTMLThrough(t *testing.T) {
	allowLoopbackFetchForTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	be := &fakeAnthropicWeb{summarizeOut: "ok is true"}
	res, err := NewFetchTool("", be).Execute(context.Background(), mustJSON(t, map[string]any{
		"url": srv.URL, "provider": "anthropic",
	}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if be.page != `{"ok":true}` {
		t.Errorf("summarizer page = %q, want the raw JSON", be.page)
	}
}

// TestFetch_AnthropicProviderDefaultPrompt: the tool leaves the default to the
// backend rather than inventing its own wording.
func TestFetch_AnthropicProviderDefaultPrompt(t *testing.T) {
	allowLoopbackFetchForTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("content"))
	}))
	defer srv.Close()

	be := &fakeAnthropicWeb{summarizeOut: "summary"}
	if _, err := NewFetchTool("", be).Execute(context.Background(), mustJSON(t, map[string]any{
		"url": srv.URL, "provider": "anthropic",
	})); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if be.prompt != "" {
		t.Errorf("prompt = %q, want it left empty for the backend's default", be.prompt)
	}
}

// TestFetch_PromptRejectedOnNonAnthropicBackend: a prompt only means something
// to the summarizing backend. Silently dropping it would hand the model a
// whole-page dump as if it answered the question it asked. Without an
// Anthropic backend at all, the error must say so plainly rather than send
// the model on a retry with provider=anthropic that can only fail again.
func TestFetch_PromptRejectedOnNonAnthropicBackend(t *testing.T) {
	res, err := NewFetchTool("", nil).Execute(context.Background(), mustJSON(t, map[string]any{
		"url": "https://example.com", "provider": "curl", "prompt": "what is this?",
	}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(res.Error, "prompt is not supported by provider=curl") || !strings.Contains(res.Error, "needs an Anthropic session") {
		t.Fatalf("error = %q, want a prompt rejection that doesn't dangle provider=anthropic", res.Error)
	}
	if strings.Contains(res.Error, "pass provider=anthropic") {
		t.Fatalf("error = %q, must not suggest an unavailable backend", res.Error)
	}
}

// TestFetch_PromptRejectedOnCurlWithAnthropicAvailable: when the Anthropic
// backend IS available, the rejection should point at it — this is the
// case the original message covered correctly.
func TestFetch_PromptRejectedOnCurlWithAnthropicAvailable(t *testing.T) {
	res, err := NewFetchTool("", &fakeAnthropicWeb{}).Execute(context.Background(), mustJSON(t, map[string]any{
		"url": "https://example.com", "provider": "curl", "prompt": "what is this?",
	}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(res.Error, "prompt is only supported by provider=anthropic") || !strings.Contains(res.Error, "pass provider=anthropic") {
		t.Fatalf("error = %q, want the anthropic-available rejection", res.Error)
	}
}

// TestFetch_OllamaProviderRejectedWhenUnavailable: asking for the Ollama
// backend on a non-Ollama session fails loudly rather than quietly fetching
// directly, which would hide that the requested extraction never happened.
func TestFetch_OllamaProviderRejectedWhenUnavailable(t *testing.T) {
	res, err := NewFetchTool("", nil).Execute(context.Background(), mustJSON(t, map[string]any{
		"url": "https://example.com", "provider": "ollama",
	}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(res.Error, "needs a reachable Ollama session") {
		t.Fatalf("error = %q, want an Ollama-session error", res.Error)
	}
}

// TestFetch_CurlProviderIgnoresOllama: an explicit provider=curl must bypass
// the Ollama proxy even on an Ollama session.
func TestFetch_CurlProviderIgnoresOllama(t *testing.T) {
	allowLoopbackFetchForTest(t)
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("direct"))
	}))
	defer page.Close()
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("Ollama backend must not be used for provider=curl")
	}))
	defer ollama.Close()

	res, err := NewFetchTool(ollama.URL, nil).Execute(context.Background(), mustJSON(t, map[string]any{
		"url": page.URL, "provider": "curl",
	}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Content != "direct" {
		t.Fatalf("content = %q, want the directly fetched page", res.Content)
	}
}

// TestFetch_DefaultBackendUnchanged pins the pre-existing default: Ollama when
// it's available, plain fetch otherwise — never Anthropic, which spends tokens.
func TestFetch_DefaultBackendUnchanged(t *testing.T) {
	if got := NewFetchTool("http://localhost:11434", nil).defaultBackend(); got != fetchViaOllama {
		t.Errorf("default with Ollama = %q, want ollama", got)
	}
	if got := NewFetchTool("", &fakeAnthropicWeb{}).defaultBackend(); got != fetchViaCurl {
		t.Errorf("default with only Anthropic = %q, want curl", got)
	}
}

func TestFetch_UnknownProviderRejected(t *testing.T) {
	res, err := NewFetchTool("", nil).Execute(context.Background(), mustJSON(t, map[string]any{
		"url": "https://example.com", "provider": "wget",
	}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(res.Error, `unknown provider "wget"`) {
		t.Fatalf("error = %q, want an unknown-provider error", res.Error)
	}
}
