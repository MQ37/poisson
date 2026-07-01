package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// Pricing holds per-model token pricing (USD per 1M tokens).
type Pricing struct {
	InputPerMTok      float64
	OutputPerMTok     float64
	CacheReadPerMTok  float64
	CacheWritePerMTok float64
}

// ProviderConfig selects the default provider.
type ProviderConfig struct {
	Default string // anthropic | ollama | xai
}

// AnthropicConfig holds Anthropic provider settings.
type AnthropicConfig struct {
	Model  string
	APIKey string // optional; if empty, use OAuth or auth.json
}

// XAIConfig holds xAI provider settings.
type XAIConfig struct {
	Model string
}

// OllamaConfig holds Ollama provider settings.
type OllamaConfig struct {
	BaseURL string
	Model   string
}

// CompactionConfig controls auto-compaction.
type CompactionConfig struct {
	Threshold float64 // fraction of context window (0.0–1.0)
	Model     string  // summarization model (empty = session model)
}

// StealthConfig holds Anthropic Claude Code stealth constants.
type StealthConfig struct {
	CCVersion    string
	CCEntrypoint string
	CCHSalt      string
	CCHPositions []int
}

// TUIConfig controls REPL display.
type TUIConfig struct {
	Theme      string // dark | light
	ShowTokens bool
	ShowCost   bool
}

// Config is the fully parsed and defaulted Poisson configuration.
type Config struct {
	Provider   ProviderConfig
	Anthropic  AnthropicConfig
	XAI        XAIConfig
	Ollama     OllamaConfig
	Compaction CompactionConfig
	Stealth    StealthConfig
	TUI        TUIConfig
	// Pricing is keyed [provider][model] → Pricing.
	Pricing map[string]map[string]Pricing
}

// defaultConfig returns a Config populated with all built-in defaults.
// DefaultStealthConfig returns the built-in stealth constants.
func DefaultStealthConfig() StealthConfig {
	return StealthConfig{
		CCVersion:    "2.1.156",
		CCEntrypoint: "sdk-cli",
		CCHSalt:      "59cf53e54c78",
		CCHPositions: []int{4, 7, 20},
	}
}

// DefaultConfig returns the built-in default configuration.
func DefaultConfig() *Config {
	return defaultConfig()
}

func defaultConfig() *Config {
	return &Config{
		Provider: ProviderConfig{
			Default: "ollama",
		},
		Anthropic: AnthropicConfig{
			Model: "claude-opus-4-8",
		},
		XAI: XAIConfig{
			Model: "grok-build",
		},
		Ollama: OllamaConfig{
			BaseURL: "http://localhost:11434",
			Model:   "glm-5.2:cloud",
		},
		Compaction: CompactionConfig{
			Threshold: 0.85,
			Model:     "",
		},
		Stealth: StealthConfig{
			CCVersion:    "2.1.156",
			CCEntrypoint: "sdk-cli",
			CCHSalt:      "59cf53e54c78",
			CCHPositions: []int{4, 7, 20},
		},
		TUI: TUIConfig{
			Theme:      "dark",
			ShowTokens: true,
			ShowCost:   true,
		},
		Pricing: map[string]map[string]Pricing{
			"anthropic": {
				"claude-opus-4-8": {
					InputPerMTok:      5.0,
					OutputPerMTok:     25.0,
					CacheReadPerMTok:  0.5,
					CacheWritePerMTok: 3.0,
				},
			},
			"xai": {
				"grok-build": {InputPerMTok: 1.0, OutputPerMTok: 2.0},
			},
			"ollama": {
				"*": {},
			},
		},
	}
}

// defaultConfigToml is written to ~/.poisson/config.toml when it doesn't exist.
// It's a commented-out template of every option so the user can uncomment
// to override.
const defaultConfigToml = `# Poisson configuration — ~/.poisson/config.toml
# All options are commented out; Poisson uses built-in defaults.
# Uncomment and edit to override.

# Default provider + model
[provider]
# default = "ollama"             # anthropic | ollama | xai

[anthropic]
# model = "claude-opus-4-8"
# If auth.json has OAuth tokens for anthropic, stealth mode is active.
# Otherwise set an API key here or in auth.json.
# api_key = "sk-ant-..."

[xai]
# model = "grok-build"

[ollama]
# base_url = "http://localhost:11434"
# model = "glm-5.2:cloud"

[compaction]
# threshold = 0.85               # fraction of context window (0.0–1.0)
# model = ""                     # model for summarization (default: session model)

[stealth]
# Anthropic Claude Code stealth constants.
# cc_version = "2.1.156"
# cc_entrypoint = "sdk-cli"
# cch_salt = "59cf53e54c78"
# cch_positions = [4, 7, 20]     # character positions sampled from first user msg

[tui]
# theme = "dark"                 # dark | light
# show_tokens = true             # show context % in status bar
# show_cost = true               # show $ cost in status bar

# Pricing per 1M tokens (USD). OAuth/subscription providers default to 0.
# Values here override the built-in defaults. Use nested tables, not inline tables.
# [pricing.anthropic.claude-opus-4-8]
# input = 5.0
# output = 25.0
# cache_read = 0.5
# cache_write = 3.0
# [pricing.xai.grok-build]
# input = 1.0
# output = 2.0
# [pricing.ollama.glm-5.2:cloud]
# input = 0
# output = 0
# [pricing.ollama.minimax-m3:cloud]
# input = 0
# output = 0
`

// ConfigDir returns the path to ~/.poisson/, creating it (mode 0700) if missing.
func ConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		// Fall back to a relative ".poisson" rather than panicking.
		home = "."
	}
	dir := filepath.Join(home, ".poisson")
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		_ = os.MkdirAll(dir, 0o700)
	}
	return dir
}

// ConfigPath returns the path to ~/.poisson/config.toml.
func ConfigPath() string {
	return filepath.Join(ConfigDir(), "config.toml")
}

// Load reads ~/.poisson/config.toml, parses it, applies defaults, and returns
// a populated *Config. If config.toml does not exist, it is created with
// commented-out defaults and the built-in default Config is returned.
func Load() (*Config, error) {
	path := ConfigPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Create the default config file for discoverability.
			if werr := os.WriteFile(path, []byte(defaultConfigToml), 0o600); werr != nil {
				return nil, fmt.Errorf("create default config: %w", werr)
			}
			return defaultConfig(), nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	parsed, err := Parse(string(data))
	if err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	return mapToConfig(parsed)
}

// mapToConfig applies parsed TOML values on top of the built-in defaults.
func mapToConfig(m map[string]interface{}) (*Config, error) {
	cfg := defaultConfig()

	if v, ok := lookup(m, "provider", "default"); ok {
		cfg.Provider.Default = asString(v)
	}

	if v, ok := lookup(m, "anthropic", "model"); ok {
		cfg.Anthropic.Model = asString(v)
	}
	if v, ok := lookup(m, "anthropic", "api_key"); ok {
		cfg.Anthropic.APIKey = asString(v)
	}

	if v, ok := lookup(m, "xai", "model"); ok {
		cfg.XAI.Model = asString(v)
	}

	if v, ok := lookup(m, "ollama", "base_url"); ok {
		cfg.Ollama.BaseURL = asString(v)
	}
	if v, ok := lookup(m, "ollama", "model"); ok {
		cfg.Ollama.Model = asString(v)
	}

	if v, ok := lookup(m, "compaction", "threshold"); ok {
		f, err := asFloat(v)
		if err != nil {
			return nil, fmt.Errorf("compaction.threshold: %w", err)
		}
		cfg.Compaction.Threshold = f
	}
	if v, ok := lookup(m, "compaction", "model"); ok {
		cfg.Compaction.Model = asString(v)
	}

	if v, ok := lookup(m, "stealth", "cc_version"); ok {
		cfg.Stealth.CCVersion = asString(v)
	}
	if v, ok := lookup(m, "stealth", "cc_entrypoint"); ok {
		cfg.Stealth.CCEntrypoint = asString(v)
	}
	if v, ok := lookup(m, "stealth", "cch_salt"); ok {
		cfg.Stealth.CCHSalt = asString(v)
	}
	if v, ok := lookup(m, "stealth", "cch_positions"); ok {
		pos, err := asIntArray(v)
		if err != nil {
			return nil, fmt.Errorf("stealth.cch_positions: %w", err)
		}
		cfg.Stealth.CCHPositions = pos
	}

	if v, ok := lookup(m, "tui", "theme"); ok {
		cfg.TUI.Theme = asString(v)
	}
	if v, ok := lookup(m, "tui", "show_tokens"); ok {
		cfg.TUI.ShowTokens = asBool(v)
	}
	if v, ok := lookup(m, "tui", "show_cost"); ok {
		cfg.TUI.ShowCost = asBool(v)
	}

	// Pricing: [pricing.<provider>.<model>] → input, output, cache_read, cache_write
	if pr, ok := m["pricing"]; ok {
		prMap, ok := pr.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("pricing: expected table")
		}
		for provider, pval := range prMap {
			pmap, ok := pval.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("pricing.%s: expected table", provider)
			}
			if cfg.Pricing[provider] == nil {
				cfg.Pricing[provider] = map[string]Pricing{}
			}
			for model, mval := range pmap {
				mmap, ok := mval.(map[string]interface{})
				if !ok {
					return nil, fmt.Errorf("pricing.%s.%s: expected table", provider, model)
				}
				p := cfg.Pricing[provider][model] // start from existing default if any
				if v, ok := mmap["input"]; ok {
					f, err := asFloat(v)
					if err != nil {
						return nil, fmt.Errorf("pricing.%s.%s.input: %w", provider, model, err)
					}
					p.InputPerMTok = f
				}
				if v, ok := mmap["output"]; ok {
					f, err := asFloat(v)
					if err != nil {
						return nil, fmt.Errorf("pricing.%s.%s.output: %w", provider, model, err)
					}
					p.OutputPerMTok = f
				}
				if v, ok := mmap["cache_read"]; ok {
					f, err := asFloat(v)
					if err != nil {
						return nil, fmt.Errorf("pricing.%s.%s.cache_read: %w", provider, model, err)
					}
					p.CacheReadPerMTok = f
				}
				if v, ok := mmap["cache_write"]; ok {
					f, err := asFloat(v)
					if err != nil {
						return nil, fmt.Errorf("pricing.%s.%s.cache_write: %w", provider, model, err)
					}
					p.CacheWritePerMTok = f
				}
				cfg.Pricing[provider][model] = p
			}
		}
	}

	return cfg, nil
}

// lookup fetches m[a][b] as a value, ok=false if any step is missing or
// not a table.
func lookup(m map[string]interface{}, path ...string) (interface{}, bool) {
	cur := m
	for i, p := range path {
		v, ok := cur[p]
		if !ok {
			return nil, false
		}
		if i == len(path)-1 {
			return v, true
		}
		cur, ok = v.(map[string]interface{})
		if !ok {
			return nil, false
		}
	}
	return nil, false
}

func asString(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func asBool(v interface{}) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}

// asFloat converts an int or a float (from TOML) to float64.
// Our minimal parser only emits ints, but we accept floats defensively.
func asFloat(v interface{}) (float64, error) {
	switch n := v.(type) {
	case int:
		return float64(n), nil
	case int64:
		return float64(n), nil
	case float64:
		return n, nil
	case string:
		f, err := strconv.ParseFloat(n, 64)
		if err != nil {
			return 0, fmt.Errorf("expected number, got %q", n)
		}
		return f, nil
	default:
		return 0, fmt.Errorf("expected number, got %T", v)
	}
}

func asIntArray(v interface{}) ([]int, error) {
	arr, ok := v.([]interface{})
	if !ok {
		return nil, fmt.Errorf("expected array, got %T", v)
	}
	out := make([]int, len(arr))
	for i, e := range arr {
		n, ok := e.(int)
		if !ok {
			return nil, fmt.Errorf("expected int in array, got %T", e)
		}
		out[i] = n
	}
	return out, nil
}

