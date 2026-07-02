package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"poisson/internal/config"
)

const (
	fetchMaxBytes    = 2 << 20 // 2 MiB: cap extracted page text (OOM guard)
	fetchErrMaxBytes = 4 << 10 // 4 KiB: cap error bodies
)

// FetchTool uses the local Ollama instance's web_fetch API to extract
// text content from a web page URL.
type FetchTool struct {
	ollamaBaseURL string
}

// NewFetchTool creates a fetch tool. ollamaBaseURL defaults to http://localhost:11434.
func NewFetchTool(ollamaBaseURL string) *FetchTool {
	if ollamaBaseURL == "" {
		ollamaBaseURL = "http://localhost:11434"
	}
	return &FetchTool{ollamaBaseURL: ollamaBaseURL}
}

func (t *FetchTool) Name() string { return "fetch" }

func (t *FetchTool) Description() string {
	return "Fetch and extract text content from a web page URL using the local Ollama instance's web_fetch API."
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

// IsOllamaReachable checks if the Ollama instance is running.
func IsOllamaReachable(cfg *config.Config) bool {
	baseURL := "http://localhost:11434"
	if cfg != nil && cfg.Ollama.BaseURL != "" {
		baseURL = cfg.Ollama.BaseURL
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(baseURL + "/api/tags")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}
