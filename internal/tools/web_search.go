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
//
// With an Anthropic session, provider=anthropic switches to Anthropic's own
// server-side web search (the same web_search_20250305 tool Claude Code's
// WebSearch delegates to), which returns links plus a synthesized summary and
// is immune to DuckDuckGo's bot challenge.
type WebSearchTool struct {
	anthropic AnthropicWebBackend // nil unless the active provider is Anthropic
	usage     WebUsageFn          // nil unless a host wired cost accounting
}

// NewWebSearchTool creates the search tool. Pass nil for anthropic on every
// provider but Anthropic — see AnthropicWebBackend.
func NewWebSearchTool(anthropic AnthropicWebBackend) *WebSearchTool {
	return &WebSearchTool{anthropic: anthropic}
}

// SetUsageFn wires the sink that banks the Anthropic backend's helper-call
// spend onto the session (see WebUsageFn).
func (t *WebSearchTool) SetUsageFn(fn WebUsageFn) { t.usage = fn }

func (t *WebSearchTool) Name() string { return "web_search" }

// ResolveDefaultProvider implements DefaultProviderResolver — web_search's
// default is always duckduckgo, whether or not the anthropic backend is
// even wired for this session (that only affects whether provider=anthropic
// is accepted, not the default).
func (t *WebSearchTool) ResolveDefaultProvider() string { return "duckduckgo" }

func (t *WebSearchTool) Description() string {
	desc := "Search the web via DuckDuckGo. Returns a plain list of links with titles and short descriptions — no AI summary. Fast, free, no account. Use web_ask instead when you want a synthesized answer with sources."
	if t.anthropic != nil {
		desc += " provider=anthropic uses Anthropic's server-side web search instead (links plus a synthesized summary, billed to the Anthropic account); available only while the session runs on Anthropic."
	}
	return desc
}

func (t *WebSearchTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {"type": "string", "description": "Search query"},
			"num": {"type": "integer", "description": "Number of results (default: 10)"},
			"provider": {"type": "string", "description": "duckduckgo | anthropic (default: duckduckgo; anthropic requires an Anthropic session)"}
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
		Query    string `json:"query"`
		Num      int    `json:"num"`
		Provider string `json:"provider"`
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

	switch params.Provider {
	case "", "duckduckgo":
	case "anthropic":
		if t.anthropic == nil {
			return ToolResult{Error: "provider=anthropic needs an Anthropic session (switch with /model anthropic/<model>); use provider=duckduckgo instead"}, nil
		}
		out, spend, err := t.anthropic.WebSearch(ctx, params.Query, params.Num)
		// Recorded before the error check: a search that came back empty (or
		// failed to encode) was still billed.
		t.usage.record(WebCall{
			Purpose: webPurposeSearch, Provider: "anthropic", Model: spend.Model,
			Usage: spend.Usage, SearchRequests: spend.SearchRequests,
		})
		if err != nil {
			return ToolResult{Error: err.Error()}, nil
		}
		return ToolResult{Content: out}, nil
	default:
		return ToolResult{Error: fmt.Sprintf("unknown provider %q (use duckduckgo or anthropic)", params.Provider)}, nil
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
