package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mq37/poisson/internal/config"
)

const (
	fetchMaxBytes    = 2 << 20 // 2 MiB: cap extracted page text (OOM guard)
	fetchErrMaxBytes = 4 << 10 // 4 KiB: cap error bodies
)

// FetchTool fetches a URL and returns its readable content. With a non-empty
// ollamaBaseURL it proxies through the local Ollama instance's own web_fetch
// API (its own extraction, already good and worth reusing when available).
// Otherwise (any other provider, or Ollama unreachable) it fetches the page
// itself and converts HTML to Markdown with a hand-rolled converter
// (html2md.go) — no third-party HTML/markdown package, matching this
// project's stdlib-first dependency policy.
type FetchTool struct {
	ollamaBaseURL string // empty means: fetch directly, no Ollama
}

// NewFetchTool creates a fetch tool. Pass "" for ollamaBaseURL to always use
// the direct (non-Ollama) path; pass a real base URL (already resolved to its
// default if unset) to proxy through Ollama's web_fetch API instead.
func NewFetchTool(ollamaBaseURL string) *FetchTool {
	return &FetchTool{ollamaBaseURL: ollamaBaseURL}
}

func (t *FetchTool) Name() string { return "fetch" }

func (t *FetchTool) Description() string {
	return "Fetch a URL and extract its readable text content (docs, articles, specs). Prefer this over bash `curl`/`wget` when you just need a page's content — skips bash's risk-classification step entirely. Not for HTTP API testing: no custom methods, headers, or status-code inspection — use bash for that."
}

func (t *FetchTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"url": {"type": "string", "description": "URL to fetch"}
		},
		"required": ["url"]
	}`)
}

func (t *FetchTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	var params struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return ToolResult{Error: "invalid input: " + err.Error()}, nil
	}
	if params.URL == "" {
		return ToolResult{Error: "url is required"}, nil
	}
	if t.ollamaBaseURL == "" {
		return t.fetchDirect(ctx, params.URL)
	}

	body, _ := json.Marshal(map[string]string{"url": params.URL})
	fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
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
		return ToolResult{Error: fmt.Sprintf("fetch failed (status %d): %s", resp.StatusCode, string(raw))}, nil
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, fetchMaxBytes))
	if err != nil {
		return ToolResult{Error: "read response: " + err.Error()}, nil
	}

	return ToolResult{Content: string(data)}, nil
}

// fetchDirect fetches url itself (no Ollama) and converts HTML responses to
// Markdown; non-HTML responses (plain text, JSON, existing Markdown, ...)
// are returned as-is since there's nothing to convert.
func (t *FetchTool) fetchDirect(ctx context.Context, url string) (ToolResult, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(fetchCtx, "GET", url, nil)
	if err != nil {
		return ToolResult{Error: "create request: " + err.Error()}, nil
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; poisson-fetch/1.0)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.9,*/*;q=0.8")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ToolResult{Error: "fetch failed: " + err.Error()}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, fetchErrMaxBytes))
		return ToolResult{Error: fmt.Sprintf("fetch failed (status %d): %s", resp.StatusCode, string(raw))}, nil
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
