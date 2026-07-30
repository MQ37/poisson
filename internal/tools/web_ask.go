package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mq37/poisson/internal/auth"
)

// WebAskTool asks the web a question and returns an AI-synthesized answer
// with sources — as opposed to WebSearchTool's plain link list. Two
// backends: "grok" (xAI's server-side web_search + synthesis, reusing the
// same OAuth token the xai chat provider already holds — no separate
// login) and "exa" (exa.ai's free keyless landing API). Defaults to grok
// when xAI OAuth credentials exist, falling back to exa on any grok error
// so a transient xAI outage or an unauthenticated session never hard-fails
// the tool outright.
type WebAskTool struct {
	auth  auth.AuthStore // shared reference with the xai chat provider; may be nil
	usage WebUsageFn     // nil unless a host wired cost accounting
}

func NewWebAskTool(store auth.AuthStore) *WebAskTool { return &WebAskTool{auth: store} }

// SetUsageFn wires the sink that banks the grok backend's spend onto the
// session (see WebUsageFn). The exa backend is free and records nothing.
func (t *WebAskTool) SetUsageFn(fn WebUsageFn) { t.usage = fn }

func (t *WebAskTool) Name() string { return "web_ask" }

// ResolveDefaultProvider implements DefaultProviderResolver — mirrors
// Execute's own default: grok when this session holds xAI OAuth
// credentials, else exa.
func (t *WebAskTool) ResolveDefaultProvider() string {
	if hasXAIAuth(t.auth) {
		return "grok"
	}
	return "exa"
}

func (t *WebAskTool) Description() string {
	return "Ask the web a question and get an AI-synthesized answer with sources. " +
		"provider=grok (default when logged in via `px login xai`) uses xAI Grok's " +
		"server-side web_search + synthesis; provider=exa (default fallback, always " +
		"available, no account) uses exa.ai's free keyless search + summary. " +
		"Use web_search instead when you just want a plain list of links."
}

func (t *WebAskTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {"type": "string", "description": "Question or search query"},
			"num": {"type": "integer", "description": "Number of results (default: 10, max: 100 for exa)"},
			"provider": {"type": "string", "description": "grok | exa (default: grok if logged in, else exa)"},
			"type": {"type": "string", "description": "exa only: keyword | neural (default: keyword)"},
			"verbose": {"type": "boolean", "description": "exa only: include full text excerpts (default: false)"}
		},
		"required": ["query"]
	}`)
}

func (t *WebAskTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	var params struct {
		Query    string `json:"query"`
		Num      int    `json:"num"`
		Provider string `json:"provider"`
		Type     string `json:"type"`
		Verbose  bool   `json:"verbose"`
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

	provider := params.Provider
	if provider == "" {
		if hasXAIAuth(t.auth) {
			provider = "grok"
		} else {
			provider = "exa"
		}
	}

	if provider == "grok" {
		result, spend, err := execGrokSearch(ctx, t.auth, params.Query, params.Num)
		// Recorded whatever happens next: xAI billed the call even when the
		// answer was unusable and this falls back to exa below.
		t.usage.record(spend)
		if err == nil {
			return ToolResult{Content: result}, nil
		}
		if params.Provider == "grok" {
			// Explicit request for grok — surface the real error, don't mask it.
			return ToolResult{Error: err.Error()}, nil
		}
		// Auto-selected grok as the default; fall back to exa rather than
		// hard-failing on a transient xAI outage or missing credentials.
		result, exaErr := execExaSearch(ctx, params.Query, params.Num, params.Type, params.Verbose)
		if exaErr != nil {
			return ToolResult{Error: fmt.Sprintf("grok failed (%v), exa fallback also failed (%v)", err, exaErr)}, nil
		}
		return ToolResult{Content: result}, nil
	}

	if provider != "exa" {
		return ToolResult{Error: fmt.Sprintf("unknown provider %q (use grok or exa)", provider)}, nil
	}
	result, err := execExaSearch(ctx, params.Query, params.Num, params.Type, params.Verbose)
	if err != nil {
		return ToolResult{Error: err.Error()}, nil
	}
	return ToolResult{Content: result}, nil
}
