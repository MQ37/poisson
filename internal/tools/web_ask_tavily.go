package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// tavilySearchURL is Tavily's search endpoint. Its keyless tier (no account,
// no API key — send X-Tavily-Access-Mode: keyless instead of a Bearer token)
// is rate-limited but returns the exact same response schema as a keyed call,
// including an optional LLM-synthesized answer — see
// https://docs.tavily.com/documentation/keyless. A var (not a const) so
// tests can point it at an httptest server.
var tavilySearchURL = "https://api.tavily.com/search"

const (
	tavilyMaxBytes    = 1 << 20 // 1 MiB: cap search response (OOM guard)
	tavilyErrMaxBytes = 4 << 10
)

// execTavilySearch runs the Tavily backend for WebAskTool: include_answer
// asks Tavily for a synthesized answer alongside ranked sources, so — unlike
// exa, which returns bare results — this needs no separate summarization
// pass on poisson's side. Returns Tavily's own JSON verbatim (answer +
// results), same passthrough convention as execExaSearch.
func execTavilySearch(ctx context.Context, query string, num int) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"query":          query,
		"max_results":    num,
		"include_answer": "basic",
	})

	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, tavilySearchURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tavily-Access-Mode", "keyless")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("tavily request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, tavilyErrMaxBytes))
		if resp.StatusCode == http.StatusTooManyRequests {
			return "", fmt.Errorf("tavily keyless rate limit reached. Try again later or use provider=grok/exa instead")
		}
		return "", fmt.Errorf("tavily HTTP %d: %s", resp.StatusCode, sanitizeHTTPErrorBody(raw))
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, tavilyMaxBytes))
	if err != nil {
		return "", fmt.Errorf("read tavily response: %w", err)
	}
	return string(data), nil
}
