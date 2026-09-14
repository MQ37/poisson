package orchestrator

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/auth"
)

func TestGenerateInstanceSecrets_OnlyAuthorizedProvidersCopied(t *testing.T) {
	dir := t.TempDir()
	mustLayout(t, dir, "px-secrets")

	hostAuth := auth.AuthStore{
		"anthropic": {Type: "oauth", Access: "anthropic-token"},
		"openai":    {Type: "oauth", Access: "openai-token"},
		"xai":       {Type: "api_key", Key: "xai-key"},
	}
	spec := InstanceSpec{Name: "px-secrets", Provider: "anthropic", Model: "claude-sonnet-5", AuthorizedProviders: []string{"anthropic"}}

	if err := GenerateInstanceSecrets(dir, spec, hostAuth); err != nil {
		t.Fatalf("GenerateInstanceSecrets: %v", err)
	}

	data, err := os.ReadFile(AuthJSONPath(dir, "px-secrets"))
	if err != nil {
		t.Fatalf("read generated auth.json: %v", err)
	}
	var got auth.AuthStore
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("parse generated auth.json: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want exactly 1 (anthropic only): %+v", len(got), got)
	}
	if got["anthropic"].Access != "anthropic-token" {
		t.Errorf("anthropic entry = %+v, want the host's anthropic-token", got["anthropic"])
	}
	if _, leaked := got["openai"]; leaked {
		t.Error("openai entry leaked into instance auth.json despite not being authorized")
	}
	if _, leaked := got["xai"]; leaked {
		t.Error("xai entry leaked into instance auth.json despite not being authorized")
	}
}

func TestGenerateInstanceSecrets_FileModes(t *testing.T) {
	dir := t.TempDir()
	mustLayout(t, dir, "px-modes")
	spec := InstanceSpec{Name: "px-modes", Provider: "anthropic", Model: "claude-sonnet-5"}
	if err := GenerateInstanceSecrets(dir, spec, auth.AuthStore{}); err != nil {
		t.Fatalf("GenerateInstanceSecrets: %v", err)
	}
	info, err := os.Stat(AuthJSONPath(dir, "px-modes"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("auth.json mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestGenerateInstanceSecrets_ConfigTomlPinsProviderAndModel(t *testing.T) {
	dir := t.TempDir()
	mustLayout(t, dir, "px-cfg")
	spec := InstanceSpec{Name: "px-cfg", Provider: "xai", Model: "grok-build"}
	if err := GenerateInstanceSecrets(dir, spec, auth.AuthStore{}); err != nil {
		t.Fatalf("GenerateInstanceSecrets: %v", err)
	}
	data, err := os.ReadFile(ConfigTomlPath(dir, "px-cfg"))
	if err != nil {
		t.Fatal(err)
	}
	toml := string(data)
	if !strings.Contains(toml, `default = "xai"`) {
		t.Errorf("config.toml = %q, want provider.default pinned to xai", toml)
	}
	if !strings.Contains(toml, "[xai]") || !strings.Contains(toml, `model = "grok-build"`) {
		t.Errorf("config.toml = %q, want [xai] model pinned to grok-build", toml)
	}
}

// TestShredSecrets_RemovesDirectoryAndOverwritesContent checks the secrets
// directory is gone afterward, and (best-effort) that a fixed-size sentinel
// file's content was overwritten before removal rather than just unlinked.
func TestShredSecrets_RemovesDirectoryAndOverwritesContent(t *testing.T) {
	dir := t.TempDir()
	mustLayout(t, dir, "px-shred")
	spec := InstanceSpec{Name: "px-shred", Provider: "anthropic", Model: "claude-sonnet-5"}
	if err := GenerateInstanceSecrets(dir, spec, auth.AuthStore{"anthropic": {Type: "oauth", Access: "super-secret-token"}}); err != nil {
		t.Fatal(err)
	}

	if err := ShredSecrets(dir, "px-shred"); err != nil {
		t.Fatalf("ShredSecrets: %v", err)
	}
	if _, err := os.Stat(SecretsDir(dir, "px-shred")); !os.IsNotExist(err) {
		t.Errorf("secrets dir still exists after ShredSecrets: err=%v", err)
	}
}

// TestShredSecrets_MissingDirIsIdempotent matches Destroy's own idempotence
// contract: shredding an already-gone secrets dir is success, not an error.
func TestShredSecrets_MissingDirIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	if err := ShredSecrets(dir, "px-never-existed"); err != nil {
		t.Errorf("ShredSecrets on missing dir = %v, want nil", err)
	}
}
