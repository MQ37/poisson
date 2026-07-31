package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mq37/poisson/internal/mcpclient"
)

// execFirecrawlScrape runs the firecrawl_scrape tool over MCP (same keyless
// server as execFirecrawlSearch — see firecrawlMCPURL) and returns the page's
// Markdown. Firecrawl's own extraction handles JS-rendered pages that a
// plain HTTP GET (fetch's curl backend) would return empty or unrendered.
func execFirecrawlScrape(ctx context.Context, url string) (string, error) {
	res, err := mcpclient.CallTool(ctx, firecrawlMCPURL, "firecrawl_scrape", map[string]any{
		"url":     url,
		"formats": []string{"markdown"},
	})
	if err != nil {
		return "", fmt.Errorf("firecrawl_scrape: %w", err)
	}
	if res.IsError {
		return "", fmt.Errorf("firecrawl_scrape: %s", res.Text)
	}

	var page struct {
		Markdown string `json:"markdown"`
	}
	if err := json.Unmarshal([]byte(res.Text), &page); err != nil {
		// Firecrawl's response shape changed or wasn't JSON — hand back the
		// raw text rather than swallowing it, same fallback style as fetch's
		// existing backends when a body can't be parsed as expected.
		return res.Text, nil
	}
	if page.Markdown == "" {
		return res.Text, nil
	}
	return page.Markdown, nil
}
