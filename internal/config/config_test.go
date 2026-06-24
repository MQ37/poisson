package config

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTempConfig writes content to a temp config.toml inside a temp HOME,
// then restores the original HOME. Returns the config path.
func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	tmpHome := t.TempDir()
	origHome, hadHome := os.LookupEnv("HOME")
	os.Setenv("HOME", tmpHome)
	t.Cleanup(func() {
		if hadHome {
			os.Setenv("HOME", origHome)
		} else {
			os.Unsetenv("HOME")
		}
	})

	dir := filepath.Join(tmpHome, ".poisson")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadDefaults(t *testing.T) {
	writeTempConfig(t, "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Provider.Default != "ollama" {
		t.Errorf("Provider.Default = %q, want ollama", cfg.Provider.Default)
	}
	if cfg.Anthropic.Model != "claude-opus-4-8" {
		t.Errorf("Anthropic.Model = %q", cfg.Anthropic.Model)
	}
	if cfg.Ollama.BaseURL != "http://localhost:11434" {
		t.Errorf("Ollama.BaseURL = %q", cfg.Ollama.BaseURL)
	}
	if cfg.Ollama.Model != "glm-5.2:cloud" {
		t.Errorf("Ollama.Model = %q", cfg.Ollama.Model)
	}
	if cfg.XAI.Model != "grok-build" {
		t.Errorf("XAI.Model = %q", cfg.XAI.Model)
	}
	if cfg.Compaction.Threshold != 0.8 {
		t.Errorf("Compaction.Threshold = %v, want 0.8", cfg.Compaction.Threshold)
	}
	if cfg.Compaction.Model != "" {
		t.Errorf("Compaction.Model = %q, want empty", cfg.Compaction.Model)
	}
	if cfg.Stealth.CCVersion != "2.1.156" {
		t.Errorf("Stealth.CCVersion = %q", cfg.Stealth.CCVersion)
	}
	if cfg.Stealth.CCEntrypoint != "sdk-cli" {
		t.Errorf("Stealth.CCEntrypoint = %q", cfg.Stealth.CCEntrypoint)
	}
	if cfg.Stealth.CCHSalt != "59cf53e54c78" {
		t.Errorf("Stealth.CCHSalt = %q", cfg.Stealth.CCHSalt)
	}
	wantPos := []int{4, 7, 20}
	if len(cfg.Stealth.CCHPositions) != len(wantPos) {
		t.Errorf("CCHPositions len = %d, want %d", len(cfg.Stealth.CCHPositions), len(wantPos))
	} else {
		for i, p := range wantPos {
			if cfg.Stealth.CCHPositions[i] != p {
				t.Errorf("CCHPositions[%d] = %d, want %d", i, cfg.Stealth.CCHPositions[i], p)
			}
		}
	}
	if cfg.TUI.Theme != "dark" {
		t.Errorf("TUI.Theme = %q", cfg.TUI.Theme)
	}
	if !cfg.TUI.ShowTokens {
		t.Errorf("TUI.ShowTokens = false")
	}
	if !cfg.TUI.ShowCost {
		t.Errorf("TUI.ShowCost = false")
	}

	// Built-in pricing default for claude-sonnet-4-20250514
	ant, ok := cfg.Pricing["anthropic"]
	if !ok {
		t.Fatal("no anthropic pricing")
	}
	p, ok := ant["claude-opus-4-8"]
	if !ok {
		t.Fatal("no pricing for claude-opus-4-8")
	}
	if p.InputPerMTok != 5.0 {
		t.Errorf("InputPerMTok = %v, want 5.0", p.InputPerMTok)
	}
	if p.OutputPerMTok != 25.0 {
		t.Errorf("OutputPerMTok = %v, want 25.0", p.OutputPerMTok)
	}
	if p.CacheReadPerMTok != 0.5 {
		t.Errorf("CacheReadPerMTok = %v, want 0.5", p.CacheReadPerMTok)
	}
	if p.CacheWritePerMTok != 3.0 {
		t.Errorf("CacheWritePerMTok = %v, want 3.0", p.CacheWritePerMTok)
	}
}

func TestLoadOverrides(t *testing.T) {
	in := `
[provider]
default = "ollama"

[anthropic]
model = "claude-opus-4-20250514"
api_key = "sk-ant-test"

[xai]
model = "grok-3"

[ollama]
base_url = "http://my-host:1234"
model = "llama3:8b"

[compaction]
threshold = 0.5
model = "gpt-4o"

[stealth]
cc_version = "9.9.9"
cc_entrypoint = "custom"
cch_salt = "abcdef"
cch_positions = [1, 2, 3, 99]

[guard]
extra_safe = ["make", "cargo build", "just"]

[tui]
theme = "light"
show_tokens = false
show_cost = false
`
	writeTempConfig(t, in)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Provider.Default != "ollama" {
		t.Errorf("Provider.Default = %q", cfg.Provider.Default)
	}
	if cfg.Anthropic.Model != "claude-opus-4-20250514" {
		t.Errorf("Anthropic.Model = %q", cfg.Anthropic.Model)
	}
	if cfg.Anthropic.APIKey != "sk-ant-test" {
		t.Errorf("Anthropic.APIKey = %q", cfg.Anthropic.APIKey)
	}
	if cfg.XAI.Model != "grok-3" {
		t.Errorf("XAI.Model = %q", cfg.XAI.Model)
	}
	if cfg.Ollama.BaseURL != "http://my-host:1234" {
		t.Errorf("Ollama.BaseURL = %q", cfg.Ollama.BaseURL)
	}
	if cfg.Ollama.Model != "llama3:8b" {
		t.Errorf("Ollama.Model = %q", cfg.Ollama.Model)
	}
	if cfg.Compaction.Threshold != 0.5 {
		t.Errorf("Compaction.Threshold = %v", cfg.Compaction.Threshold)
	}
	if cfg.Compaction.Model != "gpt-4o" {
		t.Errorf("Compaction.Model = %q", cfg.Compaction.Model)
	}
	if cfg.Stealth.CCVersion != "9.9.9" {
		t.Errorf("Stealth.CCVersion = %q", cfg.Stealth.CCVersion)
	}
	if cfg.Stealth.CCEntrypoint != "custom" {
		t.Errorf("Stealth.CCEntrypoint = %q", cfg.Stealth.CCEntrypoint)
	}
	if cfg.Stealth.CCHSalt != "abcdef" {
		t.Errorf("Stealth.CCHSalt = %q", cfg.Stealth.CCHSalt)
	}
	wantPos := []int{1, 2, 3, 99}
	if len(cfg.Stealth.CCHPositions) != len(wantPos) {
		t.Errorf("CCHPositions = %v, want %v", cfg.Stealth.CCHPositions, wantPos)
	} else {
		for i, p := range wantPos {
			if cfg.Stealth.CCHPositions[i] != p {
				t.Errorf("CCHPositions[%d] = %d", i, cfg.Stealth.CCHPositions[i])
			}
		}
	}
	wantSafe := []string{"make", "cargo build", "just"}
	if len(cfg.Guard.ExtraSafe) != len(wantSafe) {
		t.Errorf("ExtraSafe = %v, want %v", cfg.Guard.ExtraSafe, wantSafe)
	} else {
		for i, s := range wantSafe {
			if cfg.Guard.ExtraSafe[i] != s {
				t.Errorf("ExtraSafe[%d] = %q, want %q", i, cfg.Guard.ExtraSafe[i], s)
			}
		}
	}
	if cfg.TUI.Theme != "light" {
		t.Errorf("TUI.Theme = %q", cfg.TUI.Theme)
	}
	if cfg.TUI.ShowTokens {
		t.Errorf("TUI.ShowTokens = true, want false")
	}
	if cfg.TUI.ShowCost {
		t.Errorf("TUI.ShowCost = true, want false")
	}
}

func TestLoadPricingNested(t *testing.T) {
	in := `
[pricing.anthropic.claude-sonnet-4-20250514]
input = 3.0
output = 15.0
cache_read = 0.3
cache_write = 3.75

[pricing.anthropic.claude-opus-4-20250514]
input = 15.0
output = 75.0
cache_read = 1.5
cache_write = 18.75

[pricing.xai.grok-3]
input = 5.0
output = 15.0

[pricing.ollama.llama3:8b]
input = 0
output = 0
`
	writeTempConfig(t, in)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	ant := cfg.Pricing["anthropic"]
	if p := ant["claude-sonnet-4-20250514"]; p.InputPerMTok != 3.0 || p.OutputPerMTok != 15.0 || p.CacheReadPerMTok != 0.3 || p.CacheWritePerMTok != 3.75 {
		t.Errorf("sonnet pricing wrong: %+v", p)
	}
	if p := ant["claude-opus-4-20250514"]; p.InputPerMTok != 15.0 || p.OutputPerMTok != 75.0 || p.CacheReadPerMTok != 1.5 || p.CacheWritePerMTok != 18.75 {
		t.Errorf("opus pricing wrong: %+v", p)
	}
	xai := cfg.Pricing["xai"]
	if p := xai["grok-3"]; p.InputPerMTok != 5.0 || p.OutputPerMTok != 15.0 || p.CacheReadPerMTok != 0 || p.CacheWritePerMTok != 0 {
		t.Errorf("xai grok-3 pricing wrong: %+v", p)
	}
	oll := cfg.Pricing["ollama"]
	if p := oll["llama3:8b"]; p.InputPerMTok != 0 || p.OutputPerMTok != 0 {
		t.Errorf("ollama pricing wrong: %+v", p)
	}
}

func TestLoadCreatesConfigIfMissing(t *testing.T) {
	// Set HOME to a temp dir without creating the .poisson dir; Load should
	// create the config file and return defaults.
	tmpHome := t.TempDir()
	origHome, hadHome := os.LookupEnv("HOME")
	os.Setenv("HOME", tmpHome)
	t.Cleanup(func() {
		if hadHome {
			os.Setenv("HOME", origHome)
		} else {
			os.Unsetenv("HOME")
		}
	})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Provider.Default != "ollama" {
		t.Errorf("default not applied: %q", cfg.Provider.Default)
	}
	// File should now exist.
	path := filepath.Join(tmpHome, ".poisson", "config.toml")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config.toml not created: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read created config: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("created config is empty")
	}
}

func TestConfigDirCreates(t *testing.T) {
	tmpHome := t.TempDir()
	origHome, hadHome := os.LookupEnv("HOME")
	os.Setenv("HOME", tmpHome)
	t.Cleanup(func() {
		if hadHome {
			os.Setenv("HOME", origHome)
		} else {
			os.Unsetenv("HOME")
		}
	})
	dir := ConfigDir()
	if dir != filepath.Join(tmpHome, ".poisson") {
		t.Errorf("ConfigDir = %q", dir)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Errorf("ConfigDir not a dir: %v", err)
	}
}

func TestConfigPath(t *testing.T) {
	tmpHome := t.TempDir()
	origHome, hadHome := os.LookupEnv("HOME")
	os.Setenv("HOME", tmpHome)
	t.Cleanup(func() {
		if hadHome {
			os.Setenv("HOME", origHome)
		} else {
			os.Unsetenv("HOME")
		}
	})
	p := ConfigPath()
	want := filepath.Join(tmpHome, ".poisson", "config.toml")
	if p != want {
		t.Errorf("ConfigPath = %q, want %q", p, want)
	}
}

func TestLoadPricingPartialOverrideKeepsDefault(t *testing.T) {
	// Override only output for claude-opus-4-8; other fields keep
	// the built-in default.
	in := `
[pricing.anthropic.claude-opus-4-8]
output = 99.0
`
	writeTempConfig(t, in)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	p := cfg.Pricing["anthropic"]["claude-opus-4-8"]
	if p.InputPerMTok != 5.0 {
		t.Errorf("Input = %v, want 5.0 (default)", p.InputPerMTok)
	}
	if p.OutputPerMTok != 99.0 {
		t.Errorf("Output = %v, want 99.0 (override)", p.OutputPerMTok)
	}
	if p.CacheReadPerMTok != 0.5 {
		t.Errorf("CacheRead = %v, want 0.5 (default)", p.CacheReadPerMTok)
	}
	if p.CacheWritePerMTok != 3.0 {
		t.Errorf("CacheWrite = %v, want 3.0 (default)", p.CacheWritePerMTok)
	}
}
