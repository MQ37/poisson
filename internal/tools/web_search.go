package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	webSearchURL       = "https://html.duckduckgo.com/html/"
	webSearchUserAgent = "Mozilla/5.0 (X11; Linux x86_64; rv:151.0) Gecko/20100101 Firefox/151.0"
	webSearchMaxBytes  = 2 << 20 // 2 MiB: cap SERP HTML (OOM guard)
)

// resultLinkRe and resultSnippetRe extract DuckDuckGo's static HTML SERP
// (https://html.duckduckgo.com/html/) result blocks. This markup has been
// the stable no-JS scrape target of ddgs/duckduckgo_search for years —
// unlike DDG's AI chat endpoint, it has no anti-bot challenge.
var (
	resultLinkRe    = regexp.MustCompile(`<a rel="nofollow" class="result__a" href="([^"]+)">(.*?)</a>`)
	resultSnippetRe = regexp.MustCompile(`<a class="result__snippet"[^>]*>(.*?)</a>`)
	tagStripRe      = regexp.MustCompile(`<[^>]+>`)
)

// ddgChallengeMarker is text unique to DuckDuckGo's bot-challenge interstitial
// ("Unfortunately, bots use DuckDuckGo too." / "Select all squares containing
// a duck"), served instead of a SERP once a client IP is rate-limited. It has
// no result__a/result__snippet blocks, so it used to fall through to the
// generic error path, which dumped raw interstitial HTML as the tool error.
const ddgChallengeMarker = "anomaly-modal"

func isDDGChallenge(body []byte) bool {
	return strings.Contains(string(body), ddgChallengeMarker)
}

// WebSearchTool returns a plain list of links + descriptions from
// DuckDuckGo's web index. No AI, no summarization, no auth — the cheapest,
// fastest, always-available search primitive. Use WebAskTool when a
// synthesized answer is wanted instead of raw links.
type WebSearchTool struct{}

func NewWebSearchTool() *WebSearchTool { return &WebSearchTool{} }

func (t *WebSearchTool) Name() string { return "web_search" }

func (t *WebSearchTool) Description() string {
	return "Search the web via DuckDuckGo. Returns a plain list of links with titles and short descriptions — no AI summary. Fast, free, no account. Use web_ask instead when you want a synthesized answer with sources."
}

func (t *WebSearchTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {"type": "string", "description": "Search query"},
			"num": {"type": "integer", "description": "Number of results (default: 10)"}
		},
		"required": ["query"]
	}`)
}

type webSearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

func (t *WebSearchTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	var params struct {
		Query string `json:"query"`
		Num   int    `json:"num"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return ToolResult{Error: "invalid input: " + err.Error()}, nil
	}
	if params.Query == "" {
		return ToolResult{Error: "query is required"}, nil
	}
	if params.Num <= 0 {
		params.Num = 10
	}

	results, err := doWebSearch(ctx, params.Query, params.Num)
	if err != nil {
		return ToolResult{Error: err.Error()}, nil
	}

	out, _ := json.Marshal(results)
	return ToolResult{Content: string(out)}, nil
}

func doWebSearch(ctx context.Context, query string, num int) ([]webSearchResult, error) {
	searchCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	reqURL := webSearchURL + "?" + url.Values{"q": {query}}.Encode()
	req, err := http.NewRequestWithContext(searchCtx, "GET", reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", webSearchUserAgent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("web_search request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, webSearchMaxBytes))
	if err != nil {
		return nil, fmt.Errorf("web_search read response: %w", err)
	}

	if isDDGChallenge(body) {
		return nil, fmt.Errorf("web_search: DuckDuckGo blocked this request with a bot challenge (rate-limited). Wait a bit before retrying, or use web_ask instead")
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("web_search HTTP %d", resp.StatusCode)
	}

	return parseWebSearchResults(string(body), num), nil
}

func parseWebSearchResults(html string, num int) []webSearchResult {
	links := resultLinkRe.FindAllStringSubmatch(html, -1)
	snippets := resultSnippetRe.FindAllStringSubmatch(html, -1)

	results := make([]webSearchResult, 0, num)
	for i, link := range links {
		if len(results) >= num {
			break
		}
		target := decodeDDGRedirect(link[1])
		if target == "" {
			continue
		}
		snippet := ""
		if i < len(snippets) {
			snippet = cleanResultText(snippets[i][1])
		}
		results = append(results, webSearchResult{
			Title:   cleanResultText(link[2]),
			URL:     target,
			Snippet: snippet,
		})
	}
	return results
}

// decodeDDGRedirect extracts the real target URL from DuckDuckGo's
// "//duckduckgo.com/l/?uddg=<url-encoded>&rut=..." redirect href.
func decodeDDGRedirect(href string) string {
	href = decodeEntities(href) // href arrives with "&amp;" between query params
	i := strings.Index(href, "uddg=")
	if i == -1 {
		return ""
	}
	rest := href[i+len("uddg="):]
	if j := strings.IndexByte(rest, '&'); j != -1 {
		rest = rest[:j]
	}
	target, err := url.QueryUnescape(rest)
	if err != nil {
		return ""
	}
	return target
}

func cleanResultText(s string) string {
	s = tagStripRe.ReplaceAllString(s, "")
	s = decodeEntities(s)
	return strings.TrimSpace(s)
}
