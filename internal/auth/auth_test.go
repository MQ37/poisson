package auth

import (
	"os"
	"path/filepath"
	"testing"

	"poisson/internal/testutil"
)

func TestLoadEmpty(t *testing.T) {
	testutil.TempHome(t)

	store, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(store) != 0 {
		t.Errorf("expected empty store, got %d entries", len(store))
	}
}

func TestSaveAndLoad(t *testing.T) {
	tmpHome := testutil.TempHome(t)

	store := AuthStore{
		"anthropic": {Type: "oauth", Access: "tok123", Refresh: "ref456", Expires: 9999999999999},
		"ollama":    {Type: "none"},
	}
	if err := Save(store); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Verify file permissions.
	path := filepath.Join(tmpHome, ".poisson", "auth.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %o, want 0600", info.Mode().Perm())
	}

	// Load and verify.
	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded["anthropic"].Access != "tok123" {
		t.Errorf("access = %q, want tok123", loaded["anthropic"].Access)
	}
	if loaded["anthropic"].Refresh != "ref456" {
		t.Errorf("refresh = %q, want ref456", loaded["anthropic"].Refresh)
	}
	if loaded["ollama"].Type != "none" {
		t.Errorf("ollama type = %q, want 'none'", loaded["ollama"].Type)
	}
}

func TestIsOAuth(t *testing.T) {
	store := AuthStore{
		"anthropic": {Type: "oauth", Access: "tok"},
		"ollama":    {Type: "none"},
	}
	if !IsOAuth(store, "anthropic") {
		t.Error("anthropic should be OAuth")
	}
	if IsOAuth(store, "ollama") {
		t.Error("ollama should not be OAuth")
	}
	if IsOAuth(store, "nonexistent") {
		t.Error("nonexistent should not be OAuth")
	}
}

func TestGetAPIKey(t *testing.T) {
	store := AuthStore{
		"anthropic": {Type: "api_key", Key: "sk-ant-123"},
		"ollama":    {Type: "none"},
	}
	if got := GetAPIKey(store, "anthropic"); got != "sk-ant-123" {
		t.Errorf("anthropic key = %q, want sk-ant-123", got)
	}
	if got := GetAPIKey(store, "ollama"); got != "" {
		t.Errorf("ollama key = %q, want empty", got)
	}
}

func TestIsExpired(t *testing.T) {
	// Expired token.
	expired := AuthEntry{Type: "oauth", Expires: 1000}
	if !IsExpired(expired, 0) {
		t.Error("token with expires=1000 should be expired")
	}

	// Not expired.
	future := AuthEntry{Type: "oauth", Expires: 999999999999999}
	if IsExpired(future, 5*60*1000) {
		t.Error("token far in future should not be expired")
	}

	// Non-OAuth never expires.
	apiKey := AuthEntry{Type: "api_key", Key: "sk-test"}
	if IsExpired(apiKey, 0) {
		t.Error("api_key should never be expired")
	}
}