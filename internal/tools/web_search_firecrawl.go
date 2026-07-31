package tools

import (
	"context"
	"fmt"

	"github.com/mq37/poisson/internal/mcpclient"
)

// firecrawlMCPURL is Firecrawl's hosted remote MCP server. Its keyless tier
// (no account, no API key) exposes firecrawl_search and firecrawl_scrape
// with reduced, per-IP rate limits — see
// https://www.firecrawl.dev/blog/firecrawl-keyless-launch. A var (not a
// const) so tests can point it at an httptest server.
var firecrawlMCPURL = "https://mcp.firecrawl.dev/v2/mcp"

// execFirecrawlSearch runs the firecrawl_search tool over MCP and returns its
// text content verbatim — Firecrawl's own JSON (ranked web/news/image result
// groups, no synthesis), same passthrough convention as execExaSearch.
func execFirecrawlSearch(ctx context.Context, query string, limit int) (string, error) {
	res, err := mcpclient.CallTool(ctx, firecrawlMCPURL, "firecrawl_search", map[string]any{
		"query": query,
		"limit": limit,
	})
	if err != nil {
		return "", fmt.Errorf("firecrawl_search: %w", err)
	}
	if res.IsError {
		return "", fmt.Errorf("firecrawl_search: %s", res.Text)
	}
	return res.Text, nil
}
