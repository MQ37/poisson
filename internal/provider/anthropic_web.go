package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mq37/poisson/internal/config"
)

// Anthropic's own web tooling, as captured off a real Claude Code session
// (cc-sniff/docs/claude-code-web-tools.md). Both of Claude Code's web tools
// are client-orchestrated: the main loop emits an ordinary tool call, and the
// CLI answers it with a *separate* small-model /v1/messages request —
// web_search with the server-side web_search_20250305 tool, WebFetch with no
// tools at all (the page is fetched locally and pasted in as markdown).
// Poisson mirrors that split: WebSearch below owns the server tool,
// WebFetchSummarize owns the summarization pass, and the actual page fetch
// stays in tools/fetch.go where the SSRF guard and HTML→Markdown converter
// already live.
const (
	// anthropicWebModel is the small fast model both helper calls use
	// (captures 0028/0029). Deliberately not in KnownModels: it is never a
	// session model, only this internal delegate.
	anthropicWebModel = "claude-haiku-4-5-20251001"
	// anthropicWebSearchTool is Anthropic's server-side search tool type.
	anthropicWebSearchTool = "web_search_20250305"
	// anthropicWebSearchMaxUses caps searches per helper call, matching
	// Claude Code's own max_uses.
	anthropicWebSearchMaxUses = 8
	anthropicWebMaxTokens     = 32000
	anthropicWebTimeout       = 120 * time.Second
	anthropicWebMaxBytes      = 8 << 20 // 8 MiB: cap the helper SSE stream
)

// anthropicWebSearchSystem and anthropicWebFetchGuardrails are Claude Code's
// verbatim helper prompts (captures 0029 and 0028 respectively) — reused
// rather than reworded so the stealth path's request bodies keep matching the
// real client's.
const anthropicWebSearchSystem = "You are an assistant for performing a web search tool use"

const anthropicWebFetchGuardrails = "Provide a concise response based only on the content above. In your response:\n" +
	" - Enforce a strict 125-character maximum for quotes from any source document. Open Source Software is ok as long as we respect the license.\n" +
	" - Use quotation marks for exact language from articles; any language outside of the quotation should never be word-for-word the same.\n" +
	" - You are not a lawyer and never comment on the legality of your own prompts and responses.\n" +
	" - Never produce or reproduce exact song lyrics.\n"

// anthropicWebLink is one search hit as handed back to the caller. Anthropic
// also returns an opaque encrypted_content blob per result; Claude Code drops
// it before the result reaches its main loop, and so does this.
type anthropicWebLink struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// WebSearch runs one Anthropic-side web search and returns the same
// text shape Claude Code feeds its main loop: the query, a JSON list of
// links, then the helper model's prose synthesis. maxResults <= 0 keeps every
// link returned.
func (p *AnthropicProvider) WebSearch(ctx context.Context, query string, maxResults int) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", fmt.Errorf("anthropic web_search: query is required")
	}
	tools := []map[string]any{{
		"type":     anthropicWebSearchTool,
		"name":     "web_search",
		"max_uses": anthropicWebSearchMaxUses,
	}}
	text, links, err := p.webHelper(ctx, anthropicWebSearchSystem,
		"Perform a web search for the query: "+query, tools)
	if err != nil {
		return "", err
	}
	if len(links) == 0 && text == "" {
		return "", fmt.Errorf("anthropic web_search returned no results")
	}
	if maxResults > 0 && len(links) > maxResults {
		links = links[:maxResults]
	}
	encoded, err := json.Marshal(links)
	if err != nil {
		return "", fmt.Errorf("anthropic web_search: encode links: %w", err)
	}
	return fmt.Sprintf("Web search results for query: %q\n\nLinks: %s\n\n%s", query, encoded, text), nil
}

// WebFetchSummarize answers prompt against already-fetched page content using
// the same small model and guardrails Claude Code's WebFetch uses. The caller
// owns fetching and markdown conversion — this never touches the network on
// the page's behalf, so the fetch tool's SSRF guard stays authoritative.
func (p *AnthropicProvider) WebFetchSummarize(ctx context.Context, pageMarkdown, prompt string) (string, error) {
	if strings.TrimSpace(pageMarkdown) == "" {
		return "", fmt.Errorf("anthropic web fetch: page content is empty")
	}
	if strings.TrimSpace(prompt) == "" {
		prompt = "What does this page say?"
	}
	user := fmt.Sprintf("\nWeb page content:\n---\n%s\n---\n\n%s\n\n%s",
		pageMarkdown, prompt, anthropicWebFetchGuardrails)
	text, _, err := p.webHelper(ctx, "", user, nil)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("anthropic web fetch: model returned no answer")
	}
	return text, nil
}

// webHelper performs one small-model helper request and returns its assistant
// text plus any web_search_tool_result links. systemExtra is the task line
// appended after the stealth billing/identity blocks (empty for the fetch
// summarizer, which sends neither a task line nor tools).
func (p *AnthropicProvider) webHelper(ctx context.Context, systemExtra, userText string, tools []map[string]any) (string, []anthropicWebLink, error) {
	isOAuth := p.refreshOAuthIfNeeded()

	system := []map[string]any{}
	if isOAuth {
		// Same three-block layout as the real client: billing header derived
		// from the first user text, then the Claude Agent SDK identity line.
		cfg := config.DefaultStealthConfig()
		if p.config != nil {
			cfg = p.config.Stealth
		}
		system = append(system,
			map[string]any{"type": "text", "text": buildBillingHeaderValue(userText, cfg)},
			map[string]any{"type": "text", "text": claudeCodeIdentity},
		)
	}
	if systemExtra != "" {
		system = append(system, map[string]any{"type": "text", "text": systemExtra})
	}

	body := map[string]any{
		"model":       anthropicWebModel,
		"max_tokens":  anthropicWebMaxTokens,
		"temperature": 1,
		"stream":      true,
		"system":      system,
		"messages": []map[string]any{{
			"role":    "user",
			"content": []map[string]any{{"type": "text", "text": userText}},
		}},
	}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", nil, fmt.Errorf("anthropic web helper: marshal request: %w", err)
	}

	text, links, status, err := p.doWebHelper(ctx, payload, isOAuth)
	if err != nil && status == 401 && isOAuth {
		if rerr := p.forceRefreshOAuth(); rerr != nil {
			return "", nil, fmt.Errorf("token expired, refresh failed: %w", rerr)
		}
		text, links, _, err = p.doWebHelper(ctx, payload, isOAuth)
	}
	return text, links, err
}

func (p *AnthropicProvider) doWebHelper(ctx context.Context, payload []byte, isOAuth bool) (string, []anthropicWebLink, int, error) {
	reqCtx, cancel := context.WithTimeout(ctx, anthropicWebTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, "POST", p.baseURL+"/v1/messages?beta=true", bytes.NewReader(payload))
	if err != nil {
		return "", nil, 0, err
	}
	p.setHeaders(req, isOAuth, false, 0, newUUIDv4())
	if isOAuth {
		// Helper calls carry a shorter beta list than the main agentic loop.
		req.Header.Set("anthropic-beta", stealthWebBetaHeader())
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return "", nil, 0, fmt.Errorf("anthropic web helper request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		raw, _ := readCappedBody(resp)
		return "", nil, resp.StatusCode, fmt.Errorf("anthropic web helper HTTP %d: %s", resp.StatusCode, string(raw))
	}

	text, links, err := parseAnthropicWebSSE(resp.Body)
	return text, links, resp.StatusCode, err
}

// stealthWebBetaHeader is the anthropic-beta value real Claude Code sends on
// its WebSearch/WebFetch helper calls (cc-sniff captures 0028/0029) — notably
// without claude-code-20250219, context-1m, or the tool-use betas the main
// loop carries.
func stealthWebBetaHeader() string {
	return strings.Join([]string{
		"oauth-2025-04-20",
		"interleaved-thinking-2025-05-14",
		"thinking-token-count-2026-05-13",
		"context-management-2025-06-27",
		"prompt-caching-scope-2026-01-05",
		"advisor-tool-2026-03-01",
		"server-side-fallback-2026-06-01",
		"fallback-credit-2026-06-01",
		"cache-diagnosis-2026-04-07",
	}, ",")
}

// parseAnthropicWebSSE collects assistant text and web_search_tool_result
// links from a helper call's SSE stream. Thinking deltas, server_tool_use
// argument deltas, and per-result encrypted_content are all discarded — only
// what the caller can act on survives.
func parseAnthropicWebSSE(body io.Reader) (string, []anthropicWebLink, error) {
	scanner := bufio.NewScanner(io.LimitReader(body, anthropicWebMaxBytes))
	scanner.Buffer(make([]byte, 0, 1<<20), 8<<20)

	var text strings.Builder
	var links []anthropicWebLink
	seen := map[string]bool{}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var ev struct {
			Type         string `json:"type"`
			ContentBlock struct {
				Type    string `json:"type"`
				Content []struct {
					Type  string `json:"type"`
					Title string `json:"title"`
					URL   string `json:"url"`
				} `json:"content"`
			} `json:"content_block"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
			Error *struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil {
			continue
		}
		if ev.Error != nil {
			return "", nil, fmt.Errorf("anthropic web helper stream error: %s", ev.Error.Message)
		}
		switch ev.Type {
		case "content_block_start":
			if ev.ContentBlock.Type != "web_search_tool_result" {
				continue
			}
			for _, r := range ev.ContentBlock.Content {
				if r.URL == "" || seen[r.URL] {
					continue
				}
				seen[r.URL] = true
				links = append(links, anthropicWebLink{Title: r.Title, URL: r.URL})
			}
		case "content_block_delta":
			if ev.Delta.Type == "text_delta" {
				text.WriteString(ev.Delta.Text)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", nil, fmt.Errorf("anthropic web helper: read stream: %w", err)
	}
	return strings.TrimSpace(text.String()), links, nil
}
