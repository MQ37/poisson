package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mq37/poisson/internal/config"
)

const (
	fetchMaxBytes    = 2 << 20 // 2 MiB: cap extracted page text (OOM guard)
	fetchErrMaxBytes = 4 << 10 // 4 KiB: cap error bodies
	fetchTimeout     = 30 * time.Second
	fetchDialTimeout = 10 * time.Second
)

// blockedFetchIP reports whether ip must never be reached by fetchDirect: a
// model-supplied URL (or one lifted from fetched page content via prompt
// injection) could otherwise pull in cloud metadata endpoints
// (169.254.169.254) or any service on the host's own loopback/private
// network. A var (not a plain func) so tests can loosen it — see
// allowLoopbackFetchForTest — since httptest.NewServer always listens on
// 127.0.0.1.
var blockedFetchIP = func(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}

// safeFetchDialContext resolves addr itself and dials the checked IP
// directly (rather than handing the hostname to net.Dialer, which would
// re-resolve it) so a DNS response that changes between this check and the
// actual connection can't slip an internal IP past blockedFetchIP.
func safeFetchDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		if blockedFetchIP(ip) {
			return nil, fmt.Errorf("refusing to fetch internal address %s", ip)
		}
	}
	dialer := &net.Dialer{Timeout: fetchDialTimeout}
	return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
}

// safeFetchClient is used only for fetchDirect's model-supplied URL — never
// for the ollamaBaseURL proxy call above, which targets the locally
// configured Ollama instance, not an untrusted URL.
var safeFetchClient = &http.Client{Transport: &http.Transport{DialContext: safeFetchDialContext}}

// Fetch backends, selectable per call via the tool's provider argument.
const (
	// fetchViaCurl fetches the page in-process (Go's HTTP client, not the
	// curl binary — the name is the model-facing spelling for "plain HTTP
	// GET, no model in the loop") and converts HTML to Markdown.
	fetchViaCurl = "curl"
	// fetchViaOllama proxies to the local Ollama instance's own web_fetch API.
	fetchViaOllama = "ollama"
	// fetchViaAnthropic fetches like curl, then answers `prompt` against the
	// page with Anthropic's small model — Claude Code's WebFetch, ported.
	fetchViaAnthropic = "anthropic"
)

// FetchTool fetches a URL and returns its readable content. Three backends:
//
//   - curl: fetch in-process and convert HTML to Markdown with a hand-rolled
//     converter (html2md.go) — no third-party HTML/markdown package, matching
//     this project's stdlib-first dependency policy. Always available.
//   - ollama: proxy through the local Ollama instance's own web_fetch API (its
//     own extraction, already good and worth reusing when available). Only
//     while an Ollama session is active and Ollama is reachable.
//   - anthropic: curl, then a small-model summarization pass answering the
//     caller's prompt (see AnthropicWebBackend). Only while an Anthropic
//     session is active.
//
// The default is ollama when it's available, else curl — the pre-existing
// behavior, unchanged.
type FetchTool struct {
	ollamaBaseURL string              // empty means: Ollama backend unavailable
	anthropic     AnthropicWebBackend // nil means: Anthropic backend unavailable
	usage         WebUsageFn          // nil means: no cost accounting wired
}

// NewFetchTool creates a fetch tool. Pass "" for ollamaBaseURL and nil for
// anthropic to leave only the curl backend available; pass a real Ollama base
// URL (already resolved to its default if unset) and/or an Anthropic backend
// to offer those too.
func NewFetchTool(ollamaBaseURL string, anthropic AnthropicWebBackend) *FetchTool {
	return &FetchTool{ollamaBaseURL: ollamaBaseURL, anthropic: anthropic}
}

// SetUsageFn wires the sink that banks the Anthropic summarizer's spend onto
// the session (see WebUsageFn).
func (t *FetchTool) SetUsageFn(fn WebUsageFn) { t.usage = fn }

func (t *FetchTool) Name() string { return "fetch" }

func (t *FetchTool) Description() string {
	desc := "Fetch a URL and extract its readable text content (docs, articles, specs). Prefer this over bash `curl`/`wget` when you just need a page's content — skips bash's risk-classification step entirely. Not for HTTP API testing: no custom methods, headers, or status-code inspection — use bash for that."
	if t.anthropic != nil {
		desc += " provider=anthropic answers `prompt` against the page with a small model instead of returning the whole page — cheaper context when you only need one fact out of a long document."
	}
	return desc
}

func (t *FetchTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"url": {"type": "string", "description": "URL to fetch"},
			"provider": {"type": "string", "description": "curl | ollama | anthropic (default: ollama when an Ollama session is active, else curl). ollama needs an Ollama session, anthropic an Anthropic session."},
			"prompt": {"type": "string", "description": "anthropic only: question to answer against the page (default: summarize it)"}
		},
		"required": ["url"]
	}`)
}

func (t *FetchTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	var params struct {
		URL      string `json:"url"`
		Provider string `json:"provider"`
		Prompt   string `json:"prompt"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return ToolResult{Error: "invalid input: " + err.Error()}, nil
	}
	if params.URL == "" {
		return ToolResult{Error: "url is required"}, nil
	}

	backend := params.Provider
	if backend == "" {
		backend = t.defaultBackend()
	}
	// A prompt on any other backend would be silently dropped, and the model
	// would read a whole-page dump as if it were an answer to its question.
	if params.Prompt != "" && backend != fetchViaAnthropic {
		return ToolResult{Error: t.promptUnsupportedError(backend)}, nil
	}

	switch backend {
	case fetchViaCurl:
		return t.fetchDirect(ctx, params.URL)
	case fetchViaAnthropic:
		return t.fetchViaAnthropicBackend(ctx, params.URL, params.Prompt)
	case fetchViaOllama:
		if t.ollamaBaseURL == "" {
			return ToolResult{Error: "provider=ollama needs a reachable Ollama session (switch with /model ollama/<model>); use provider=curl instead"}, nil
		}
	default:
		return ToolResult{Error: fmt.Sprintf("unknown provider %q (use curl, ollama or anthropic)", params.Provider)}, nil
	}

	body, _ := json.Marshal(map[string]string{"url": params.URL})
	fetchCtx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(fetchCtx, "POST", t.ollamaBaseURL+"/api/fetch", bytesReader(body))
	if err != nil {
		return ToolResult{Error: "create request: " + err.Error()}, nil
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ToolResult{Error: "fetch failed: " + err.Error()}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, fetchErrMaxBytes))
		return ToolResult{Error: fmt.Sprintf("fetch failed (status %d): %s", resp.StatusCode, sanitizeHTTPErrorBody(raw))}, nil
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, fetchMaxBytes))
	if err != nil {
		return ToolResult{Error: "read response: " + err.Error()}, nil
	}

	return ToolResult{Content: string(data)}, nil
}

// promptUnsupportedError explains a prompt handed to a backend that cannot
// answer one. It only suggests provider=anthropic when this session actually
// has that backend: suggesting it otherwise costs the model a whole round trip
// to discover the retry fails with "needs an Anthropic session" too.
func (t *FetchTool) promptUnsupportedError(backend string) string {
	if t.anthropic == nil {
		return fmt.Sprintf("prompt is not supported by provider=%s, and the summarizing provider=anthropic backend needs an Anthropic session (this session runs on another provider) — drop the prompt and read the page instead", backend)
	}
	return fmt.Sprintf("prompt is only supported by provider=anthropic, not %q — drop it, or pass provider=anthropic", backend)
}

// defaultBackend keeps the pre-provider-argument behavior: Ollama's own
// web_fetch when this session can use it, else a plain fetch. Anthropic is
// never a default — it spends tokens, so the model has to ask for it.
func (t *FetchTool) defaultBackend() string {
	if t.ollamaBaseURL != "" {
		return fetchViaOllama
	}
	return fetchViaCurl
}

// fetchViaAnthropicBackend fetches the page through the same guarded direct
// path as provider=curl, then hands the extracted markdown to Anthropic's
// small model. The fetch stays local (matching Claude Code's WebFetch), so
// the SSRF guard in safeFetchDialContext still governs which addresses a
// model-supplied URL can reach.
func (t *FetchTool) fetchViaAnthropicBackend(ctx context.Context, rawURL, prompt string) (ToolResult, error) {
	if t.anthropic == nil {
		return ToolResult{Error: "provider=anthropic needs an Anthropic session (switch with /model anthropic/<model>); use provider=curl instead"}, nil
	}
	page, err := t.fetchDirect(ctx, rawURL)
	if err != nil || page.Error != "" {
		return page, err
	}
	answer, spend, aerr := t.anthropic.WebFetchSummarize(ctx, page.Content, prompt)
	// Recorded before the error check: a helper call that answered with nothing
	// was still billed for the page it read.
	t.usage.record(WebCall{
		Purpose: webPurposeFetch, Provider: "anthropic", Model: spend.Model, Usage: spend.Usage,
	})
	if aerr != nil {
		return ToolResult{Error: aerr.Error()}, nil
	}
	return ToolResult{Content: answer}, nil
}

// fetchDirect fetches url itself (no Ollama) and converts HTML responses to
// Markdown; non-HTML responses (plain text, JSON, existing Markdown, ...)
// are returned as-is since there's nothing to convert.
func (t *FetchTool) fetchDirect(ctx context.Context, rawURL string) (ToolResult, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ToolResult{Error: "invalid url: " + err.Error()}, nil
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ToolResult{Error: "url must be http or https"}, nil
	}

	fetchCtx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(fetchCtx, "GET", rawURL, nil)
	if err != nil {
		return ToolResult{Error: "create request: " + err.Error()}, nil
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; poisson-fetch/1.0)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.9,*/*;q=0.8")

	resp, err := safeFetchClient.Do(req)
	if err != nil {
		return ToolResult{Error: "fetch failed: " + err.Error()}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, fetchErrMaxBytes))
		return ToolResult{Error: fmt.Sprintf("fetch failed (status %d): %s", resp.StatusCode, sanitizeHTTPErrorBody(raw))}, nil
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, fetchMaxBytes))
	if err != nil {
		return ToolResult{Error: "read response: " + err.Error()}, nil
	}

	if !strings.Contains(resp.Header.Get("Content-Type"), "html") {
		return ToolResult{Content: string(data)}, nil
	}
	return ToolResult{Content: htmlToMarkdown(string(data))}, nil
}

// bytesReader wraps []byte as io.Reader.
type bytesReaderImpl struct {
	b []byte
	i int
}

func bytesReader(b []byte) io.Reader { return &bytesReaderImpl{b: b} }
func (r *bytesReaderImpl) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}

// OllamaBaseURL resolves the configured Ollama base URL, defaulting to
// http://localhost:11434 — shared by IsOllamaReachable and callers that then
// go on to actually use Ollama, so a reachability check and the request that
// follows it are never checked against two different URLs.
func OllamaBaseURL(cfg *config.Config) string {
	if cfg != nil && cfg.Ollama.BaseURL != "" {
		return cfg.Ollama.BaseURL
	}
	return "http://localhost:11434"
}

// IsOllamaReachable checks if the Ollama instance is running.
func IsOllamaReachable(cfg *config.Config) bool {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(OllamaBaseURL(cfg) + "/api/tags")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}
