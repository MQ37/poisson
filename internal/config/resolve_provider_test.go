package config

import "testing"

// TestResolveProviderMetaBuiltin checks a built-in ID resolves the same as
// plain ProviderMetaByID, with cfg ignored.
func TestResolveProviderMetaBuiltin(t *testing.T) {
	cfg := DefaultConfig()
	meta, ok := ResolveProviderMeta("ollama", cfg)
	if !ok || meta.ID != "ollama" {
		t.Fatalf("ResolveProviderMeta(ollama) = %+v, %v", meta, ok)
	}
}

// TestResolveProviderMetaCustom checks a custom provider resolves with
// NeedsAuth false and a Model accessor into the real CustomProviderConfig.
func TestResolveProviderMetaCustom(t *testing.T) {
	cfg := DefaultConfig()
	cfg.CustomProviders["bastion"] = &CustomProviderConfig{
		Type: "ollama", BaseURL: "http://bastion-host:11434", Model: "laguna-s-2.1:q4_K_M",
	}
	meta, ok := ResolveProviderMeta("bastion", cfg)
	if !ok {
		t.Fatal("expected bastion to resolve")
	}
	if meta.ID != "bastion" {
		t.Errorf("ID = %q, want bastion", meta.ID)
	}
	if meta.NeedsAuth {
		t.Error("NeedsAuth = true, want false")
	}
	if got := *meta.Model(cfg); got != "laguna-s-2.1:q4_K_M" {
		t.Errorf("Model(cfg) = %q, want laguna-s-2.1:q4_K_M", got)
	}
	// Mutating through the accessor must write back to the real config
	// field — the top-level `model = "bastion/x"` one-liner and
	// defaultConfig-style init both rely on this being a real pointer.
	*meta.Model(cfg) = "gemma4:26b"
	if cfg.CustomProviders["bastion"].Model != "gemma4:26b" {
		t.Errorf("mutation through accessor did not write back, got %q", cfg.CustomProviders["bastion"].Model)
	}
}

// TestResolveProviderMetaUnknown checks a name that is neither built-in nor
// custom fails cleanly.
func TestResolveProviderMetaUnknown(t *testing.T) {
	cfg := DefaultConfig()
	if _, ok := ResolveProviderMeta("bogus", cfg); ok {
		t.Fatal("expected bogus to not resolve")
	}
}

// TestResolveProviderMetaNilConfig checks a nil cfg still resolves built-ins
// (matches ProviderMetaByID's own no-cfg behavior) without panicking.
func TestResolveProviderMetaNilConfig(t *testing.T) {
	meta, ok := ResolveProviderMeta("anthropic", nil)
	if !ok || meta.ID != "anthropic" {
		t.Fatalf("ResolveProviderMeta(anthropic, nil) = %+v, %v", meta, ok)
	}
	if _, ok := ResolveProviderMeta("bastion", nil); ok {
		t.Fatal("expected no custom lookup with nil cfg")
	}
}

// TestAllProviderMetaIncludesCustom checks the merged list has built-ins
// plus every custom provider, custom entries sorted by name for a
// deterministic /providers picker.
func TestAllProviderMetaIncludesCustom(t *testing.T) {
	cfg := DefaultConfig()
	cfg.CustomProviders["zeta"] = &CustomProviderConfig{Type: "ollama", BaseURL: "http://z:11434"}
	cfg.CustomProviders["alpha"] = &CustomProviderConfig{Type: "ollama", BaseURL: "http://a:11434"}

	all := AllProviderMeta(cfg)
	if len(all) != len(Providers)+2 {
		t.Fatalf("len(all) = %d, want %d", len(all), len(Providers)+2)
	}
	// Built-ins keep their registry order, custom entries appended after,
	// sorted alphabetically.
	tail := all[len(Providers):]
	if tail[0].ID != "alpha" || tail[1].ID != "zeta" {
		t.Errorf("tail = [%s, %s], want [alpha, zeta]", tail[0].ID, tail[1].ID)
	}
}

// TestAllProviderMetaNilConfig checks nil cfg returns just the built-ins.
func TestAllProviderMetaNilConfig(t *testing.T) {
	all := AllProviderMeta(nil)
	if len(all) != len(Providers) {
		t.Fatalf("len(all) = %d, want %d", len(all), len(Providers))
	}
}
