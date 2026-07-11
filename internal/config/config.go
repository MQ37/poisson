package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

// OpenAIConfig holds OpenAI provider settings.
type OpenAIConfig struct {
	Model string
}

// OllamaConfig holds Ollama provider settings.
type OllamaConfig struct {
	BaseURL string
	Model   string
}

// CompactionConfig controls auto-compaction.
type CompactionConfig struct {
	Threshold     float64 // fraction of context window (0.0–1.0)
	ReserveTokens int     // absolute headroom; compact at min(threshold*window, window-reserve)
	Model         string  // summarization model (empty = session model)
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

// ModelOverride holds user-declared metadata for one provider/model pair,
// from [models.<provider>.<model>] in config.toml. It layers on top of (or,
// for a model the code has never heard of, entirely replaces) the built-in
// registry in provider.KnownModels — see provider.MergedModelSettings.
//
// Every field's zero value means "not set here" and falls back to the
// built-in default: ContextWindow 0, EffortLevels nil. Vision and
// AdaptiveThinking are pointers for the same reason a plain bool can't tell
// "the user wrote false" apart from "the user didn't mention this field".
type ModelOverride struct {
	ContextWindow    int
	EffortLevels     []string // empty (non-nil) slice explicitly means "no effort support"
	Vision           *bool
	AdaptiveThinking *bool
}

// DefaultEffort is the reasoning effort applied when the user hasn't chosen one,
// so a level is always shown in the status bar.
const DefaultEffort = "medium"

// effortLevels are the accepted reasoning-effort levels.
var effortLevels = map[string]bool{"low": true, "medium": true, "high": true, "xhigh": true, "max": true}

// Config is the fully parsed and defaulted Poisson configuration.
type Config struct {
	Provider   ProviderConfig
	Anthropic  AnthropicConfig
	XAI        XAIConfig
	OpenAI     OpenAIConfig
	Ollama     OllamaConfig
	Compaction CompactionConfig
	Stealth    StealthConfig
	TUI        TUIConfig
	Effort     string // reasoning effort: low | medium | high | xhigh | max
	// Pricing is keyed [provider][model] → Pricing.
	Pricing map[string]map[string]Pricing
	// ModelOverrides is keyed [provider][model] → ModelOverride.
	ModelOverrides map[string]map[string]ModelOverride
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
		OpenAI: OpenAIConfig{
			Model: "gpt-5.5",
		},
		Ollama: OllamaConfig{
			BaseURL: "http://localhost:11434",
			Model:   "glm-5.2:cloud",
		},
		Compaction: CompactionConfig{
			Threshold:     0.85,
			ReserveTokens: 16384,
			Model:         "",
		},
		Stealth: DefaultStealthConfig(),
		TUI: TUIConfig{
			Theme:      "dark",
			ShowTokens: true,
			ShowCost:   true,
		},
		Effort: DefaultEffort,
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
		ModelOverrides: map[string]map[string]ModelOverride{},
	}
}

// defaultConfigToml is written to ~/.poisson/config.toml when it doesn't exist.
// It's a commented-out template of every option so the user can uncomment
// to override.
const defaultConfigToml = `# Poisson configuration — ~/.poisson/config.toml
# All options are commented out; Poisson uses built-in defaults.
# Uncomment and edit to override.

# Reasoning effort applied to every request (main agent, subagents, bash-risk
# checks). Higher = more thinking, more cost/latency.
# effort = "medium"              # low | medium | high | xhigh | max

# Default provider + model
[provider]
# default = "ollama"             # anthropic | ollama | xai | openai

[anthropic]
# model = "claude-opus-4-8"
# If auth.json has OAuth tokens for anthropic, stealth mode is active.
# Otherwise set an API key here or in auth.json.
# api_key = "sk-ant-..."

[xai]
# model = "grok-build"

[openai]
# GPT via the ChatGPT Codex subscription (run: px login openai).
# model = "gpt-5.5"

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
# Quote model names containing '.' (e.g. "glm-5.2:cloud") so they aren't split.
# [pricing.anthropic.claude-opus-4-8]
# input = 5.0
# output = 25.0
# cache_read = 0.5
# cache_write = 3.0
# [pricing.xai.grok-build]
# input = 1.0
# output = 2.0
# [pricing.ollama."glm-5.2:cloud"]
# input = 0
# output = 0
# [pricing.ollama."minimax-m3:cloud"]
# input = 0
# output = 0
# [pricing.ollama."kimi-k2.7-code:cloud"]
# input = 0
# output = 0

# Model metadata — context window, supported reasoning-effort levels, vision,
# adaptive thinking. Teaches Poisson about a model the code doesn't know
# about (an unlisted model still works without this, just with a generic
# fallback context window and no effort/vision/adaptive-thinking support),
# or overrides a built-in entry. Every field optional; omitted ones keep
# whatever the built-in registry already has for that model. Quote model
# names containing '.' or ':' the same as in [pricing.*] above.
# [models.anthropic."claude-opus-4-9"]
# context_window = 1000000
# effort_levels = ["low", "medium", "high", "xhigh", "max"]
# vision = true
# adaptive_thinking = true
# [models.ollama."qwen3-coder:cloud"]
# context_window = 262144
# effort_levels = ["high", "max"]
# vision = false
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
		s, err := asString(v)
		if err != nil {
			return nil, fmt.Errorf("provider.default: %w", err)
		}
		cfg.Provider.Default = s
	}

	if v, ok := lookup(m, "effort"); ok {
		e, err := asString(v)
		if err != nil {
			return nil, fmt.Errorf("effort: %w", err)
		}
		if !effortLevels[e] {
			return nil, fmt.Errorf("effort: unknown level %q (want low|medium|high|xhigh|max)", e)
		}
		cfg.Effort = e
	}

	if v, ok := lookup(m, "anthropic", "model"); ok {
		s, err := asString(v)
		if err != nil {
			return nil, fmt.Errorf("anthropic.model: %w", err)
		}
		cfg.Anthropic.Model = s
	}
	if v, ok := lookup(m, "anthropic", "api_key"); ok {
		s, err := asString(v)
		if err != nil {
			return nil, fmt.Errorf("anthropic.api_key: %w", err)
		}
		cfg.Anthropic.APIKey = s
	}

	if v, ok := lookup(m, "xai", "model"); ok {
		s, err := asString(v)
		if err != nil {
			return nil, fmt.Errorf("xai.model: %w", err)
		}
		cfg.XAI.Model = s
	}

	if v, ok := lookup(m, "openai", "model"); ok {
		s, err := asString(v)
		if err != nil {
			return nil, fmt.Errorf("openai.model: %w", err)
		}
		cfg.OpenAI.Model = s
	}

	if v, ok := lookup(m, "ollama", "base_url"); ok {
		s, err := asString(v)
		if err != nil {
			return nil, fmt.Errorf("ollama.base_url: %w", err)
		}
		cfg.Ollama.BaseURL = s
	}
	if v, ok := lookup(m, "ollama", "model"); ok {
		s, err := asString(v)
		if err != nil {
			return nil, fmt.Errorf("ollama.model: %w", err)
		}
		cfg.Ollama.Model = s
	}

	// Top-level `model = "<provider>/<model>"` is the one-liner default: it sets
	// both the default provider and that provider's model. A bare value (no
	// slash) applies to the current default provider. Parsed last so it wins over
	// provider.default and the per-provider model keys.
	if v, ok := lookup(m, "model"); ok {
		s, err := asString(v)
		if err != nil {
			return nil, fmt.Errorf("model: %w", err)
		}
		if prov, mdl, hasSlash := strings.Cut(s, "/"); hasSlash {
			cfg.Provider.Default = prov
			if err := setProviderModel(cfg, prov, mdl); err != nil {
				return nil, fmt.Errorf("model: %w", err)
			}
		} else if err := setProviderModel(cfg, cfg.Provider.Default, s); err != nil {
			return nil, fmt.Errorf("model: %w", err)
		}
	}

	if v, ok := lookup(m, "compaction", "threshold"); ok {
		f, err := asFloat(v)
		if err != nil {
			return nil, fmt.Errorf("compaction.threshold: %w", err)
		}
		cfg.Compaction.Threshold = f
	}
	if v, ok := lookup(m, "compaction", "reserve_tokens"); ok {
		f, err := asFloat(v)
		if err != nil {
			return nil, fmt.Errorf("compaction.reserve_tokens: %w", err)
		}
		cfg.Compaction.ReserveTokens = int(f)
	}
	if v, ok := lookup(m, "compaction", "model"); ok {
		s, err := asString(v)
		if err != nil {
			return nil, fmt.Errorf("compaction.model: %w", err)
		}
		cfg.Compaction.Model = s
	}

	if v, ok := lookup(m, "stealth", "cc_version"); ok {
		s, err := asString(v)
		if err != nil {
			return nil, fmt.Errorf("stealth.cc_version: %w", err)
		}
		cfg.Stealth.CCVersion = s
	}
	if v, ok := lookup(m, "stealth", "cc_entrypoint"); ok {
		s, err := asString(v)
		if err != nil {
			return nil, fmt.Errorf("stealth.cc_entrypoint: %w", err)
		}
		cfg.Stealth.CCEntrypoint = s
	}
	if v, ok := lookup(m, "stealth", "cch_salt"); ok {
		s, err := asString(v)
		if err != nil {
			return nil, fmt.Errorf("stealth.cch_salt: %w", err)
		}
		cfg.Stealth.CCHSalt = s
	}
	if v, ok := lookup(m, "stealth", "cch_positions"); ok {
		pos, err := asIntArray(v)
		if err != nil {
			return nil, fmt.Errorf("stealth.cch_positions: %w", err)
		}
		cfg.Stealth.CCHPositions = pos
	}

	if v, ok := lookup(m, "tui", "theme"); ok {
		s, err := asString(v)
		if err != nil {
			return nil, fmt.Errorf("tui.theme: %w", err)
		}
		cfg.TUI.Theme = s
	}
	if v, ok := lookup(m, "tui", "show_tokens"); ok {
		b, err := asBool(v)
		if err != nil {
			return nil, fmt.Errorf("tui.show_tokens: %w", err)
		}
		cfg.TUI.ShowTokens = b
	}
	if v, ok := lookup(m, "tui", "show_cost"); ok {
		b, err := asBool(v)
		if err != nil {
			return nil, fmt.Errorf("tui.show_cost: %w", err)
		}
		cfg.TUI.ShowCost = b
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

	// Model metadata overrides: [models.<provider>.<model>] → context_window,
	// effort_levels, vision, adaptive_thinking. Teaches Poisson about a model
	// unlisted in provider.KnownModels, or overrides one that is listed.
	if md, ok := m["models"]; ok {
		mdMap, ok := md.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("models: expected table")
		}
		for provider, pval := range mdMap {
			pmap, ok := pval.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("models.%s: expected table", provider)
			}
			if cfg.ModelOverrides[provider] == nil {
				cfg.ModelOverrides[provider] = map[string]ModelOverride{}
			}
			for model, mval := range pmap {
				mmap, ok := mval.(map[string]interface{})
				if !ok {
					return nil, fmt.Errorf("models.%s.%s: expected table", provider, model)
				}
				mo := cfg.ModelOverrides[provider][model] // start from existing override if any
				if v, ok := mmap["context_window"]; ok {
					f, err := asFloat(v)
					if err != nil {
						return nil, fmt.Errorf("models.%s.%s.context_window: %w", provider, model, err)
					}
					mo.ContextWindow = int(f)
				}
				if v, ok := mmap["effort_levels"]; ok {
					levels, err := asStringArray(v)
					if err != nil {
						return nil, fmt.Errorf("models.%s.%s.effort_levels: %w", provider, model, err)
					}
					mo.EffortLevels = levels
				}
				if v, ok := mmap["vision"]; ok {
					b, err := asBool(v)
					if err != nil {
						return nil, fmt.Errorf("models.%s.%s.vision: %w", provider, model, err)
					}
					mo.Vision = &b
				}
				if v, ok := mmap["adaptive_thinking"]; ok {
					b, err := asBool(v)
					if err != nil {
						return nil, fmt.Errorf("models.%s.%s.adaptive_thinking: %w", provider, model, err)
					}
					mo.AdaptiveThinking = &b
				}
				cfg.ModelOverrides[provider][model] = mo
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

// asString coerces a TOML value to string, rejecting non-string types.
// setProviderModel points a provider's Model field at model. Used by the
// top-level `model = "<provider>/<model>"` config knob.
func setProviderModel(cfg *Config, prov, model string) error {
	switch prov {
	case "anthropic":
		cfg.Anthropic.Model = model
	case "openai":
		cfg.OpenAI.Model = model
	case "xai":
		cfg.XAI.Model = model
	case "ollama":
		cfg.Ollama.Model = model
	default:
		return fmt.Errorf("unknown provider %q (want anthropic|openai|xai|ollama)", prov)
	}
	return nil
}

func asString(v interface{}) (string, error) {
	if s, ok := v.(string); ok {
		return s, nil
	}
	return "", fmt.Errorf("expected string, got %T", v)
}

// asBool coerces a TOML value to bool, rejecting non-bool types.
func asBool(v interface{}) (bool, error) {
	if b, ok := v.(bool); ok {
		return b, nil
	}
	return false, fmt.Errorf("expected boolean, got %T", v)
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

// asStringArray returns a non-nil (possibly empty) []string, distinguishing
// "key present, empty array" from "key absent" for ModelOverride.EffortLevels.
func asStringArray(v interface{}) ([]string, error) {
	arr, ok := v.([]interface{})
	if !ok {
		return nil, fmt.Errorf("expected array, got %T", v)
	}
	out := make([]string, len(arr))
	for i, e := range arr {
		s, ok := e.(string)
		if !ok {
			return nil, fmt.Errorf("expected string in array, got %T", e)
		}
		out[i] = s
	}
	return out, nil
}
