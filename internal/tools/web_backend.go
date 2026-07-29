package tools

import (
	"context"

	"github.com/mq37/poisson/internal/provider"
)

// AnthropicWebBackend is the Anthropic-side half of the web tools: a
// server-side web search, and a small-model summarization pass over page
// content the caller already fetched (see internal/provider/anthropic_web.go,
// and cc-sniff/docs/claude-code-web-tools.md for the captured original).
//
// It is nil unless the active provider is Anthropic — the credentials, and
// the billing, belong to that provider, so offering these backends while a
// session runs on Ollama or OpenAI would either fail or silently spend on an
// account the user didn't select. Every switch of provider re-registers both
// tools (agent.ReloadConfigDependentTools), so the backend appears and
// disappears with the provider.
type AnthropicWebBackend interface {
	// WebSearch returns links plus a synthesized summary for query, and what
	// the helper call spent.
	WebSearch(ctx context.Context, query string, maxResults int) (string, provider.WebHelperUsage, error)
	// WebFetchSummarize answers prompt against already-fetched page content,
	// and reports what the helper call spent.
	WebFetchSummarize(ctx context.Context, pageMarkdown, prompt string) (string, provider.WebHelperUsage, error)
}

// Web tool api_calls purposes. One per backend that spends an account's
// tokens, so `px cost` / cost-eval can tell web spend from turn spend.
const (
	webPurposeSearch = "web_search"
	webPurposeFetch  = "web_fetch"
	webPurposeAsk    = "web_ask"
)

// WebCall is one API call a web tool's backend made on its own, outside
// provider.Stream — the Anthropic search/summarize helper model, or the Grok
// synthesis call behind web_ask. Stream is where usage accounting lives, so
// without handing these calls to a WebUsageFn their tokens never reach the
// session's api_calls rows and every cost figure poisson reports undercounts
// them.
type WebCall struct {
	Purpose string // webPurpose* above
	// Provider and Model are the account and model actually billed — the
	// helper model, not the session's, since web_ask's Grok backend can run
	// while the session is on a different provider entirely.
	Provider string
	Model    string
	Usage    provider.Usage
	// Cost, when > 0, is the provider's own authoritative figure for this
	// call (xAI reports exact USD per request, tool fees included) and is
	// recorded verbatim. Zero means: price the tokens from the rate table.
	Cost float64
	// SearchRequests is the number of server-side web searches billed on top
	// of tokens (Anthropic's web_search tool). Ignored when Cost is set,
	// which already includes the provider's own tool fees.
	SearchRequests int
}

// WebUsageFn banks a WebCall against the session. Nil on a tool means the
// host wired no accounting (tests, or a registry built without an agent);
// calls are then executed exactly as before, just unrecorded.
type WebUsageFn func(WebCall)

// record hands call to fn unless there is nothing to bank: a backend that
// reported neither tokens nor cost (e.g. exa or DuckDuckGo, which are free)
// must not leave a row of zeroes behind.
func (fn WebUsageFn) record(call WebCall) {
	if fn == nil || call.Model == "" {
		return
	}
	if call.Usage == (provider.Usage{}) && call.Cost == 0 {
		return
	}
	fn(call)
}
