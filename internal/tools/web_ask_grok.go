package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/mq37/poisson/internal/auth"
	"github.com/mq37/poisson/internal/provider"
)

const (
	grokModel       = "grok-4.3"
	grokMaxBytes    = 2 << 20 // 2 MiB: cap Responses API reply (OOM guard)
	grokErrMaxBytes = 4 << 10
)

// grokResponsesURL is a var (not a const) so tests can point it at an
// httptest server instead of the real xAI API.
var grokResponsesURL = "https://api.x.ai/v1/responses"

// hasXAIAuth reports whether xAI OAuth credentials exist, without refreshing
// — used to pick web_ask's default provider (grok if logged in, else exa).
func hasXAIAuth(store auth.AuthStore) bool {
	if store == nil {
		return false
	}
	entry, ok := store["xai"]
	return ok && entry.Type == "oauth" && entry.Access != ""
}

// execGrokSearch runs the xAI Grok backend for WebAskTool: POST to the
// Responses API with tools=[web_search], reusing the same OAuth token
// XAIProvider (px's xai chat provider) already holds — no separate login.
// Auth refresh/save goes through auth.EnsureXAIFresh/ForceRefreshXAI, which
// share a single lock with XAIProvider over the same AuthStore map (both
// hold the same instance, wired once in main.go) — required since Go map
// writes aren't safe under concurrent, unsynchronized access.
func execGrokSearch(ctx context.Context, store auth.AuthStore, query string, num int) (string, WebCall, error) {
	entry, err := auth.EnsureXAIFresh(store, 5*60*1000)
	if err != nil {
		return "", WebCall{}, err
	}

	result, spend, statusCode, err := doGrokSearch(ctx, query, num, entry.Access)
	if err != nil && statusCode == 401 {
		refreshed, rerr := auth.ForceRefreshXAI(store)
		if rerr != nil {
			return "", WebCall{}, fmt.Errorf("token expired, refresh failed: %w", rerr)
		}
		result, spend, _, err = doGrokSearch(ctx, query, num, refreshed.Access)
	}
	return result, spend, err
}

// grokPrompt asks Grok to use its web_search tool and return a plain JSON
// results list — mirrors grok-search's link-mode prompt exactly (same
// three-tier extraction fallback below covers the same response shapes).
func grokPrompt(query string, num int) string {
	return fmt.Sprintf(
		"Use the web_search tool to find current information for the query below, "+
			"then respond with ONLY a single JSON object — no prose, no markdown "+
			"fences, no inline citation links — matching this exact schema:\n\n"+
			`{"results": [{"title": "string", "url": "string", `+
			`"description": "2-4 sentence summary with key facts and data points"}]}`+"\n\n"+
			"Return at most %d results, ordered by relevance, with absolute "+
			"https:// URLs. Read each page thoroughly and include specific details — "+
			"numbers, quotes, dates, not just a vague one-liner. "+
			`If no usable results exist, return {"results": []}.`+"\n\n"+
			"Query: %s", num, query)
}

// grokUsageJSON is the Responses API's usage object (probed live against
// api.x.ai). cost_in_usd_ticks is xAI's own exact price for the call at 1e10
// ticks per USD — tool fees and cache discounts included, which no local rate
// table can reproduce, so it wins over pricing.ComputeCost when present.
type grokUsageJSON struct {
	InputTokens        int `json:"input_tokens"`
	InputTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokens   int   `json:"output_tokens"`
	CostInUSDTicks int64 `json:"cost_in_usd_ticks"`
}

// grokUSDPerTick converts cost_in_usd_ticks to dollars (1 USD = 1e10 ticks,
// per xAI's cost-tracking docs).
const grokUSDPerTick = 1e-10

// grokSpend maps a Responses API reply's usage object onto a recordable
// WebCall. Cached tokens are reported inside input_tokens, so they are split
// out the same way the OpenAI Responses path does (convertOpenAIRespUsage).
func grokSpend(body []byte) WebCall {
	var envelope struct {
		Usage grokUsageJSON `json:"usage"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return WebCall{}
	}
	u := envelope.Usage
	return WebCall{
		Purpose:  webPurposeAsk,
		Provider: "xai",
		Model:    grokModel,
		Usage: provider.Usage{
			InputTokens:     u.InputTokens - u.InputTokensDetails.CachedTokens,
			OutputTokens:    u.OutputTokens,
			CacheReadTokens: u.InputTokensDetails.CachedTokens,
		},
		Cost: float64(u.CostInUSDTicks) * grokUSDPerTick,
	}
}

func doGrokSearch(ctx context.Context, query string, num int, accessToken string) (result string, spend WebCall, statusCode int, err error) {
	payload := map[string]any{
		"model":   grokModel,
		"input":   []map[string]string{{"role": "user", "content": grokPrompt(query, num)}},
		"tools":   []map[string]string{{"type": "web_search"}},
		"include": []string{"no_inline_citations"},
	}
	body, _ := json.Marshal(payload)

	reqCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, reqErr := http.NewRequestWithContext(reqCtx, "POST", grokResponsesURL, bytes.NewReader(body))
	if reqErr != nil {
		return "", WebCall{}, 0, reqErr
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, doErr := http.DefaultClient.Do(req)
	if doErr != nil {
		return "", WebCall{}, 0, fmt.Errorf("xAI Responses API request: %w", doErr)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, grokErrMaxBytes))
		return "", WebCall{}, resp.StatusCode, fmt.Errorf("xAI Responses API HTTP %d: %s", resp.StatusCode, sanitizeHTTPErrorBody(raw))
	}

	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, grokMaxBytes))
	if readErr != nil {
		return "", WebCall{}, resp.StatusCode, fmt.Errorf("read response: %w", readErr)
	}
	// Parsed before anything can go wrong below: a reply that arrived was
	// billed, whether or not poisson can make sense of its contents.
	spend = grokSpend(raw)

	var data map[string]any
	if jsonErr := json.Unmarshal(raw, &data); jsonErr != nil {
		return "", spend, resp.StatusCode, fmt.Errorf("decode response: %w", jsonErr)
	}
	if apiErr, ok := data["error"].(map[string]any); ok {
		msg, _ := apiErr["message"].(string)
		if msg == "" {
			msg = "unknown error"
		}
		return "", spend, resp.StatusCode, fmt.Errorf("xAI returned an error: %s", msg)
	}

	out, _ := json.Marshal(map[string]any{"results": extractGrokResults(data, num)})
	return string(out), spend, resp.StatusCode, nil
}

var grokJSONBlockRe = regexp.MustCompile(`(?s)\{.*\}`)

// extractGrokResults pulls [{title,url,description}] from a Responses API
// reply. Three-tier strategy, same as grok-search: parse the requested JSON
// out of the output text; fall back to url_citation annotations; last-ditch
// fall back to a raw citations list.
func extractGrokResults(data map[string]any, num int) []map[string]string {
	var textBlocks []string
	var annotations []map[string]any

	for _, item := range asSlice(data["output"]) {
		m, ok := item.(map[string]any)
		if !ok || m["type"] != "message" {
			continue
		}
		for _, chunk := range asSlice(m["content"]) {
			cm, ok := chunk.(map[string]any)
			if !ok || cm["type"] != "output_text" {
				continue
			}
			if text, ok := cm["text"].(string); ok && text != "" {
				textBlocks = append(textBlocks, text)
			}
			for _, a := range asSlice(cm["annotations"]) {
				if am, ok := a.(map[string]any); ok {
					annotations = append(annotations, am)
				}
			}
		}
	}

	for _, block := range textBlocks {
		if results := parseGrokJSONResults(block, num); results != nil {
			return results
		}
	}

	if len(annotations) > 0 {
		return parseGrokAnnotations(annotations, num)
	}

	var results []map[string]string
	for _, c := range asSlice(data["citations"]) {
		if u, ok := c.(string); ok && u != "" {
			results = append(results, map[string]string{"title": "", "url": u, "description": ""})
			if len(results) >= num {
				break
			}
		}
	}
	return results
}

func parseGrokJSONResults(text string, num int) []map[string]string {
	candidates := []string{text}
	if m := grokJSONBlockRe.FindString(text); m != "" && m != text {
		candidates = append([]string{m}, candidates...)
	}
	for _, candidate := range candidates {
		var obj struct {
			Results []map[string]string `json:"results"`
		}
		if json.Unmarshal([]byte(candidate), &obj) != nil {
			continue
		}
		if len(obj.Results) == 0 {
			continue
		}
		var normalized []map[string]string
		for _, row := range obj.Results {
			if row["url"] == "" {
				continue
			}
			normalized = append(normalized, row)
			if len(normalized) >= num {
				break
			}
		}
		if len(normalized) > 0 {
			return normalized
		}
	}
	return nil
}

func parseGrokAnnotations(annotations []map[string]any, num int) []map[string]string {
	seen := map[string]bool{}
	var results []map[string]string
	for _, ann := range annotations {
		if ann["type"] != "url_citation" {
			continue
		}
		u, _ := ann["url"].(string)
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		results = append(results, map[string]string{"title": "", "url": u, "description": ""})
		if len(results) >= num {
			break
		}
	}
	return results
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}
