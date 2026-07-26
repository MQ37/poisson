package provider

// LlamaCppProvider talks to a local llama-server instance. llama-server
// implements the identical OpenAI-compatible /v1/chat/completions wire
// format as Ollama, so it's a thin ID-only wrapper around OllamaProvider —
// no separate request/response plumbing needed. Its /api/tags equivalent
// doesn't exist, so live Models() calls fail; that's fine, curated
// KnownModels entries (see models.go) take precedence in the picker.
type LlamaCppProvider struct {
	*OllamaProvider
}

// NewLlamaCppProvider returns a provider backed by a llama-server instance at
// baseURL (e.g. "http://localhost:11212", alpaca's default serve port).
func NewLlamaCppProvider(baseURL, model string) *LlamaCppProvider {
	return &LlamaCppProvider{OllamaProvider: NewOllamaProvider(baseURL, model)}
}

// ID returns "llamacpp".
func (p *LlamaCppProvider) ID() string { return "llamacpp" }
