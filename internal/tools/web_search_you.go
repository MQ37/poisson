package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// youSearchURL is you.com's Search API. It has a keyless free tier — no
// account, no API key — IP-throttled to roughly 100 queries/day. See
// https://you.com/docs/welcome. A var (not a const) so tests can point it at
// an httptest server.
var youSearchURL = "https://api.you.com/v1/agents/search"

const (
	youMaxBytes    = 1 << 20 // 1 MiB: cap search response (OOM guard)
	youErrMaxBytes = 4 << 10
)

// execYouSearch runs the you.com backend for WebSearchTool: a plain web+news
// result search, no synthesis (unlike Tavily's answer field). Returns
// you.com's own JSON verbatim, same passthrough convention as
// execFirecrawlSearch.
func execYouSearch(ctx context.Context, query string) (string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	reqURL := youSearchURL + "?" + url.Values{"query": {query}}.Encode()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("you.com request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, youErrMaxBytes))
		if resp.StatusCode == http.StatusTooManyRequests {
			return "", fmt.Errorf("you.com keyless daily limit reached. Try again later or use provider=duckduckgo instead")
		}
		return "", fmt.Errorf("you.com HTTP %d: %s", resp.StatusCode, sanitizeHTTPErrorBody(raw))
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, youMaxBytes))
	if err != nil {
		return "", fmt.Errorf("read you.com response: %w", err)
	}
	return string(data), nil
}
