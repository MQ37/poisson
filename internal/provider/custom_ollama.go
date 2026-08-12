package provider

// CustomOllamaProvider wraps OllamaProvider with a caller-supplied ID, for
// user-defined [custom_providers.<name>] instances (config.CustomProviderConfig,
// type = "ollama") — same OpenAI-compatible wire format and live /api/tags
// model listing as the built-in Ollama/LlamaCpp providers, just under a name
// the user picked so N instances (e.g. one local, one on a remote host) can
// coexist and each show up as their own entry in /providers and /model.
// Identical pattern to LlamaCppProvider, which is the same idea for one
// fixed extra ID.
type CustomOllamaProvider struct {
	*OllamaProvider
	id string
}

// NewCustomOllamaProvider returns a provider backed by the Ollama-compatible
// instance at baseURL, reporting id instead of "ollama".
func NewCustomOllamaProvider(id, baseURL, model string) *CustomOllamaProvider {
	return &CustomOllamaProvider{OllamaProvider: NewOllamaProvider(baseURL, model), id: id}
}

// ID returns the configured instance name.
func (p *CustomOllamaProvider) ID() string { return p.id }
