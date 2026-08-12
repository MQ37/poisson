package provider

import "testing"

// TestCustomOllamaProviderID checks the wrapper reports the caller-supplied
// ID instead of OllamaProvider's hardcoded "ollama" — this is what lets N
// user-named instances coexist (config.CustomProviderConfig, type="ollama").
func TestCustomOllamaProviderID(t *testing.T) {
	p := NewCustomOllamaProvider("bastion", "http://bastion-host:11434", "laguna-s-2.1:q4_K_M")
	if got := p.ID(); got != "bastion" {
		t.Errorf("ID() = %q, want bastion", got)
	}
}

// TestCustomOllamaProviderReusesOllamaWireFormat checks the wrapper is a
// thin ID-only layer (same pattern as LlamaCppProvider): building a request
// goes through the embedded OllamaProvider unchanged.
func TestCustomOllamaProviderReusesOllamaWireFormat(t *testing.T) {
	p := NewCustomOllamaProvider("bastion", "http://bastion-host:11434", "laguna-s-2.1:q4_K_M")
	req := &Request{Model: ""}
	built := p.buildOllamaRequest(req)
	if built.Model != "laguna-s-2.1:q4_K_M" {
		t.Errorf("buildOllamaRequest defaulted Model = %q, want the instance's configured model", built.Model)
	}
}
