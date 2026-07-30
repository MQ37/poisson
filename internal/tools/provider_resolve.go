package tools

import "encoding/json"

// DefaultProviderResolver is implemented by a tool whose "provider" argument
// has more than one possible value and picks a default when the model omits
// it (web_ask: grok if xAI OAuth is present, else exa; fetch: ollama if an
// Ollama session is reachable, else curl; web_search: always duckduckgo).
// ResolveDefaultProvider reports which backend a call with no explicit
// "provider" will actually run on, given the tool's current wiring — it
// takes no arguments because that wiring (auth store, Ollama base URL, …) is
// already a field on the tool, fixed at construction/reload time, not
// per-call.
type DefaultProviderResolver interface {
	ResolveDefaultProvider() string
}

// InjectResolvedProvider rewrites a tool call's input so its "provider"
// field always names the backend that will actually run, even when the
// model left it out. Without this, an omitted "provider" is indistinguishable
// from "the default", and the default itself depends on runtime state (an
// xAI OAuth login, a reachable Ollama session, …) that the tool call's JSON
// never carries — a card built straight from that JSON can't say which
// backend ran without asking the tool.
//
// A no-op (returns input unchanged) when: the tool isn't registered, doesn't
// implement DefaultProviderResolver, already has an explicit non-empty
// "provider", the input isn't a JSON object, or the resolver reports "".
func InjectResolvedProvider(reg *Registry, name string, input json.RawMessage) json.RawMessage {
	tool, ok := reg.Get(name)
	if !ok {
		return input
	}
	resolver, ok := tool.(DefaultProviderResolver)
	if !ok {
		return input
	}
	var probe struct {
		Provider string `json:"provider"`
	}
	if json.Unmarshal(input, &probe) != nil || probe.Provider != "" {
		return input
	}
	def := resolver.ResolveDefaultProvider()
	if def == "" {
		return input
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(input, &fields) != nil {
		return input
	}
	encoded, err := json.Marshal(def)
	if err != nil {
		return input
	}
	fields["provider"] = encoded
	out, err := json.Marshal(fields)
	if err != nil {
		return input
	}
	return out
}

// InjectResolvedProviders is InjectResolvedProvider plus batch awareness:
// batch's own input has no "provider" field itself, but each nested call
// does, and batch is tool-agnostic — its own card must reflect whichever
// backend each nested web_ask/web_search/fetch actually defaulted to, same
// as if that call had run standalone.
func InjectResolvedProviders(reg *Registry, name string, input json.RawMessage) json.RawMessage {
	if name != "batch" {
		return InjectResolvedProvider(reg, name, input)
	}
	var raw struct {
		Calls []json.RawMessage `json:"calls"`
	}
	if json.Unmarshal(input, &raw) != nil || len(raw.Calls) == 0 {
		return input
	}
	changed := false
	for i, c := range raw.Calls {
		var call struct {
			Tool  string          `json:"tool"`
			Input json.RawMessage `json:"input"`
		}
		if json.Unmarshal(c, &call) != nil || call.Tool == "" {
			continue
		}
		resolved := InjectResolvedProvider(reg, reg.Canonical(call.Tool), call.Input)
		if string(resolved) == string(call.Input) {
			continue
		}
		call.Input = resolved
		encoded, err := json.Marshal(call)
		if err != nil {
			continue
		}
		raw.Calls[i] = encoded
		changed = true
	}
	if !changed {
		return input
	}
	out, err := json.Marshal(raw)
	if err != nil {
		return input
	}
	return out
}
