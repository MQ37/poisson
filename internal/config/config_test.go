package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/testutil"
)

// writeTempConfig writes content to a temp config.toml inside a temp HOME,
// then restores the original HOME. Returns the config path.
func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	tmpHome := testutil.TempHome(t)

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
	if cfg.Compaction.Threshold != 0.85 {
		t.Errorf("Compaction.Threshold = %v, want 0.85", cfg.Compaction.Threshold)
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
	if p.CacheWritePerMTok != 10.0 {
		t.Errorf("CacheWritePerMTok = %v, want 10.0", p.CacheWritePerMTok)
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
	tmpHome := testutil.TempHome(t)

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
	tmpHome := testutil.TempHome(t)
	dir := ConfigDir()
	if dir != filepath.Join(tmpHome, ".poisson") {
		t.Errorf("ConfigDir = %q", dir)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Errorf("ConfigDir not a dir: %v", err)
	}
}

func TestConfigPath(t *testing.T) {
	tmpHome := testutil.TempHome(t)
	p := ConfigPath()
	want := filepath.Join(tmpHome, ".poisson", "config.toml")
	if p != want {
		t.Errorf("ConfigPath = %q, want %q", p, want)
	}
}

func TestLoadRejectsWrongTypes(t *testing.T) {
	cases := []struct {
		name    string
		content string
		wantErr string
	}{
		{
			name: "show_tokens as string",
			content: `
[tui]
show_tokens = "yes"
`,
			wantErr: "tui.show_tokens: expected boolean",
		},
		{
			name: "model as integer",
			content: `
[anthropic]
model = 42
`,
			wantErr: "anthropic.model: expected string",
		},
		{
			name: "api_key as integer",
			content: `
[anthropic]
api_key = 123
`,
			wantErr: "anthropic.api_key: expected string",
		},
		{
			name: "effort as bool",
			content: `
effort = true
`,
			wantErr: "effort: expected string",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeTempConfig(t, tc.content)
			_, err := Load()
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
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
	if p.CacheWritePerMTok != 10.0 {
		t.Errorf("CacheWrite = %v, want 10.0 (default)", p.CacheWritePerMTok)
	}
}

func TestLoadModelOverridesFullySpecified(t *testing.T) {
	in := `
[models.anthropic."claude-opus-4-9"]
context_window = 1000000
effort_levels = ["low", "medium", "high", "xhigh", "max"]
vision = true
adaptive_thinking = true

[models.ollama."qwen3-coder:cloud"]
context_window = 262144
effort_levels = ["high", "max"]
vision = false
`
	writeTempConfig(t, in)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	opus := cfg.ModelOverrides["anthropic"]["claude-opus-4-9"]
	if opus.ContextWindow != 1000000 {
		t.Errorf("ContextWindow = %d, want 1000000", opus.ContextWindow)
	}
	if want := []string{"low", "medium", "high", "xhigh", "max"}; !reflect.DeepEqual(opus.EffortLevels, want) {
		t.Errorf("EffortLevels = %v, want %v", opus.EffortLevels, want)
	}
	if opus.Vision == nil || !*opus.Vision {
		t.Errorf("Vision = %v, want true", opus.Vision)
	}
	if opus.AdaptiveThinking == nil || !*opus.AdaptiveThinking {
		t.Errorf("AdaptiveThinking = %v, want true", opus.AdaptiveThinking)
	}

	qwen := cfg.ModelOverrides["ollama"]["qwen3-coder:cloud"]
	if qwen.ContextWindow != 262144 {
		t.Errorf("ContextWindow = %d, want 262144", qwen.ContextWindow)
	}
	if qwen.Vision == nil || *qwen.Vision {
		t.Errorf("Vision = %v, want false (explicit)", qwen.Vision)
	}
	if qwen.AdaptiveThinking != nil {
		t.Errorf("AdaptiveThinking = %v, want nil (unset)", qwen.AdaptiveThinking)
	}
}

func TestLoadModelOverridesPartialKeepsFieldsUnset(t *testing.T) {
	// Only context_window given; everything else must stay unset (nil/empty),
	// not silently coerced to a zero value that would look like an explicit
	// "false"/"no effort support".
	in := `
[models.xai."grok-5"]
context_window = 300000
`
	writeTempConfig(t, in)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	grok := cfg.ModelOverrides["xai"]["grok-5"]
	if grok.ContextWindow != 300000 {
		t.Errorf("ContextWindow = %d, want 300000", grok.ContextWindow)
	}
	if grok.EffortLevels != nil {
		t.Errorf("EffortLevels = %v, want nil (unset)", grok.EffortLevels)
	}
	if grok.Vision != nil {
		t.Errorf("Vision = %v, want nil (unset)", grok.Vision)
	}
	if grok.AdaptiveThinking != nil {
		t.Errorf("AdaptiveThinking = %v, want nil (unset)", grok.AdaptiveThinking)
	}
}

func TestLoadModelOverridesEmptyEffortLevelsMeansNoSupport(t *testing.T) {
	// An explicit empty array must be distinguishable from "not mentioned":
	// it means "no effort support", not "keep the default".
	in := `
[models.ollama."plain-model"]
effort_levels = []
`
	writeTempConfig(t, in)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	m := cfg.ModelOverrides["ollama"]["plain-model"]
	if m.EffortLevels == nil {
		t.Fatal("EffortLevels = nil, want non-nil empty slice")
	}
	if len(m.EffortLevels) != 0 {
		t.Errorf("EffortLevels = %v, want empty", m.EffortLevels)
	}
}

func TestModelKeySetsProviderAndModel(t *testing.T) {
	m, err := Parse("model = \"anthropic/claude-sonnet-5\"\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cfg, err := mapToConfig(m)
	if err != nil {
		t.Fatalf("mapToConfig: %v", err)
	}
	if cfg.Provider.Default != "anthropic" {
		t.Errorf("provider default = %q, want anthropic", cfg.Provider.Default)
	}
	if cfg.Anthropic.Model != "claude-sonnet-5" {
		t.Errorf("anthropic model = %q, want claude-sonnet-5", cfg.Anthropic.Model)
	}
}

func TestModelKeyBareAppliesToDefaultProvider(t *testing.T) {
	m, err := Parse("model = \"llama-9\"\n\n[provider]\ndefault = \"ollama\"\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cfg, err := mapToConfig(m)
	if err != nil {
		t.Fatalf("mapToConfig: %v", err)
	}
	if cfg.Provider.Default != "ollama" || cfg.Ollama.Model != "llama-9" {
		t.Errorf("got %s/%s, want ollama/llama-9", cfg.Provider.Default, cfg.Ollama.Model)
	}
}

func TestModelKeyUnknownProvider(t *testing.T) {
	m, _ := Parse("model = \"bogus/x\"\n")
	if _, err := mapToConfig(m); err == nil {
		t.Fatal("expected error for unknown provider in model key")
	}
}
