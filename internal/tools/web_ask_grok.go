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

	"poisson/internal/auth"
)

const (
	grokResponsesURL = "https://api.x.ai/v1/responses"
	grokModel        = "grok-4.3"
	grokMaxBytes     = 2 << 20 // 2 MiB: cap Responses API reply (OOM guard)
	grokErrMaxBytes  = 4 << 10
)

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
func execGrokSearch(ctx context.Context, store auth.AuthStore, query string, num int) (string, error) {
	entry, err := auth.EnsureXAIFresh(store, 5*60*1000)
	if err != nil {
		return "", err
	}

	result, statusCode, err := doGrokSearch(ctx, query, num, entry.Access)
	if err != nil && statusCode == 401 {
		refreshed, rerr := auth.ForceRefreshXAI(store, entry.Refresh)
		if rerr != nil {
			return "", fmt.Errorf("token expired, refresh failed: %w", rerr)
		}
		result, _, err = doGrokSearch(ctx, query, num, refreshed.Access)
	}
	return result, err
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

func doGrokSearch(ctx context.Context, query string, num int, accessToken string) (result string, statusCode int, err error) {
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
		return "", 0, reqErr
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, doErr := http.DefaultClient.Do(req)
	if doErr != nil {
		return "", 0, fmt.Errorf("xAI Responses API request: %w", doErr)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, grokErrMaxBytes))
		return "", resp.StatusCode, fmt.Errorf("xAI Responses API HTTP %d: %s", resp.StatusCode, string(raw))
	}

	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, grokMaxBytes))
	if readErr != nil {
		return "", resp.StatusCode, fmt.Errorf("read response: %w", readErr)
	}

	var data map[string]any
	if jsonErr := json.Unmarshal(raw, &data); jsonErr != nil {
		return "", resp.StatusCode, fmt.Errorf("decode response: %w", jsonErr)
	}
	if apiErr, ok := data["error"].(map[string]any); ok {
		msg, _ := apiErr["message"].(string)
		if msg == "" {
			msg = "unknown error"
		}
		return "", resp.StatusCode, fmt.Errorf("xAI returned an error: %s", msg)
	}

	out, _ := json.Marshal(map[string]any{"results": extractGrokResults(data, num)})
	return string(out), resp.StatusCode, nil
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
