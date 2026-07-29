package tools

import "context"

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
	// WebSearch returns links plus a synthesized summary for query.
	WebSearch(ctx context.Context, query string, maxResults int) (string, error)
	// WebFetchSummarize answers prompt against already-fetched page content.
	WebFetchSummarize(ctx context.Context, pageMarkdown, prompt string) (string, error)
}
