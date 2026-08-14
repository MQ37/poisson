package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Pricing holds per-model token pricing (USD per 1M tokens), plus any
// per-request fee a server-side tool on that model bills on top of tokens.
// Field order must stay in sync with pricing.Rates, which converts between
// the two directly.
type Pricing struct {
	InputPerMTok      float64
	OutputPerMTok     float64
	CacheReadPerMTok  float64
	CacheWritePerMTok float64
	// SearchPerRequest is charged per server-side web search, on top of the
	// tokens the search results add to the prompt (Anthropic bills its
	// web_search tool at $10 / 1000 searches).
	SearchPerRequest float64
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

// OpenRouterConfig holds OpenRouter provider settings. OpenRouter is a
// single OpenAI-compatible endpoint proxying 400+ models from many labs
// (https://openrouter.ai/docs). Auth is a plain API key, not OAuth —
// `px login openrouter` prompts for it and stores it in auth.json (type
// api_key), same storage Anthropic's non-OAuth fallback path uses.
type OpenRouterConfig struct {
	Model   string
	APIKey  string // optional; if empty, use auth.json
	BaseURL string // defaults to https://openrouter.ai/api/v1
}

// OllamaConfig holds Ollama provider settings.
type OllamaConfig struct {
	BaseURL string
	Model   string
}

// LlamaCppConfig holds llama.cpp (llama-server) provider settings. Talks the
// same OpenAI-compatible wire format as Ollama, just against a local
// llama-server instance (see alpaca, workdir/alpaca, which manages those).
type LlamaCppConfig struct {
	BaseURL string
	Model   string
}

// CustomProviderConfig is one user-defined [custom_providers.<name>]
// instance — e.g. a second Ollama daemon on a remote host, under a name the
// user picks. Type currently must be "ollama" (same OpenAI-compatible wire
// format as the built-in Ollama/LlamaCpp providers); the field exists so a
// future second wire format doesn't need a schema change. Model metadata
// (context window, effort levels, vision) and pricing for these instances
// reuse the existing generic [models.<name>.<model>] / [pricing.<name>.<model>]
// tables verbatim — those are already keyed by an arbitrary string, not a
// fixed provider enum, so a custom name works there with no code change.
//
// A pointer (not a value) in Config.CustomProviders so ProviderMeta.Model's
// accessor can return a genuinely mutable pointer into it, the same
// contract the built-in providers' own Model field satisfies.
type CustomProviderConfig struct {
	Type    string // must be "ollama" in v1
	BaseURL string
	Model   string // default model for this instance; "" = user picks via /model
}

// ClassifierConfig controls the bash-command risk classifier (the small LLM
// call behind the approval gate).
//
// Models holds one entry per provider, collected from each provider's own
// [<provider>] classifier = "model" key — the maintainable form once more
// than one provider needs its own classifier (anthropic wants a cheap Sonnet
// while sessions run Opus; xai wants something else entirely). Keys are
// provider ids, values model names as written (which may themselves contain a
// slash, e.g. llamacpp's "unsloth/Laguna-S-2.1-GGUF").
//
// Model is the older single-value form, "model" or "provider/model", kept
// working: a provider-qualified value applies to that provider only, a bare
// one to every provider with no Models entry. Empty everywhere means "use the
// session's own model". The TUI's /classifier-model overrides both per
// provider for the running session.
type ClassifierConfig struct {
	Models map[string]string
	Model  string
}

// CompactionConfig controls auto-compaction.
type CompactionConfig struct {
	Threshold     float64 // fraction of context window (0.0–1.0)
	ReserveTokens int     // absolute headroom; compact at min(threshold*window, window-reserve)
	Model         string  // summarization target: model or provider/model (empty = session model)
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

// DefaultCCVersion is the spoofed Claude Code version used both as
// StealthConfig's built-in default and as the provider package's fallback
// user-agent when no *Config is available — one constant, not two literals
// that could drift apart.
const DefaultCCVersion = "2.1.156"

// effortLevels are the accepted reasoning-effort levels.
var effortLevels = map[string]bool{"low": true, "medium": true, "high": true, "xhigh": true, "max": true}

// Config is the fully parsed and defaulted Poisson configuration.
type Config struct {
	Provider   ProviderConfig
	Anthropic  AnthropicConfig
	XAI        XAIConfig
	OpenAI     OpenAIConfig
	OpenRouter OpenRouterConfig
	Ollama     OllamaConfig
	LlamaCpp   LlamaCppConfig
	Classifier ClassifierConfig
	Compaction CompactionConfig
	Stealth    StealthConfig
	TUI        TUIConfig
	Effort     string // reasoning effort: low | medium | high | xhigh | max
	// Pricing is keyed [provider][model] → Pricing.
	Pricing map[string]map[string]Pricing
	// ModelOverrides is keyed [provider][model] → ModelOverride.
	ModelOverrides map[string]map[string]ModelOverride
	// CustomProviders is keyed by user-chosen instance name, from
	// [custom_providers.<name>] — see CustomProviderConfig.
	CustomProviders map[string]*CustomProviderConfig
}

// defaultConfig returns a Config populated with all built-in defaults.
// DefaultStealthConfig returns the built-in stealth constants.
func DefaultStealthConfig() StealthConfig {
	return StealthConfig{
		CCVersion:    DefaultCCVersion,
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
	cfg := &Config{
		Provider: ProviderConfig{
			Default: "ollama",
		},
		OpenRouter: OpenRouterConfig{
			BaseURL: "https://openrouter.ai/api/v1",
		},
		Ollama: OllamaConfig{
			BaseURL: "http://localhost:11434",
		},
		LlamaCpp: LlamaCppConfig{
			BaseURL: "http://localhost:11212",
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
		Effort:          DefaultEffort,
		Pricing:         defaultPricing(),
		ModelOverrides:  map[string]map[string]ModelOverride{},
		CustomProviders: map[string]*CustomProviderConfig{},
	}
	// Each provider's default model comes from the single Providers registry
	// (providers.go) instead of being repeated here per provider.
	for _, p := range Providers {
		*p.Model(cfg) = p.DefaultModel
	}
	return cfg
}

// defaultPricing is the built-in per-1M-token USD rate table. It is the one
// place these numbers live — internal/pricing falls back to it (via
// DefaultConfig) instead of keeping its own duplicate table.
func defaultPricing() map[string]map[string]Pricing {
	return map[string]map[string]Pricing{
		"anthropic": {
			// cacheRead 0.1x input, cacheWrite 2x input — Poisson's 1h cache pool.
			"claude-opus-5":   {InputPerMTok: 5.0, OutputPerMTok: 25.0, CacheReadPerMTok: 0.5, CacheWritePerMTok: 10.0},
			"claude-sonnet-5": {InputPerMTok: 3.0, OutputPerMTok: 15.0, CacheReadPerMTok: 0.3, CacheWritePerMTok: 6.0},
			// Haiku 4.5 is never a session model: it is the small model behind
			// the web_search/fetch Anthropic backends (provider/anthropic_web.go),
			// which is also the only place the $10/1000 web-search fee applies.
			"claude-haiku-4-5*": {InputPerMTok: 1.0, OutputPerMTok: 5.0, CacheReadPerMTok: 0.1, CacheWritePerMTok: 2.0, SearchPerRequest: 0.01},
		},
		"xai": {
			"grok-build": {InputPerMTok: 1.0, OutputPerMTok: 2.0},
			// grok-4.5: no published prompt-cache rate.
			"grok-4.5": {InputPerMTok: 2.0, OutputPerMTok: 6.0},
			// grok-4.3 backs web_ask's grok backend. Only a fallback: that
			// backend records xAI's own exact cost_in_usd_ticks figure, which
			// also covers the per-search tool fee these rates don't model.
			"grok-4.3": {InputPerMTok: 1.25, OutputPerMTok: 2.5, CacheReadPerMTok: 0.2},
		},
		"openai": {
			// Short-context (<=272K input) standard API rate; poisson talks to
			// the Codex subscription endpoint, so this is informational shadow
			// pricing, not a real bill. cacheWrite 0 — OpenAI's prompt cache is
			// automatic with no separate write charge.
			"gpt-5.5":       {InputPerMTok: 5.0, OutputPerMTok: 30.0, CacheReadPerMTok: 0.5},
			"gpt-5.6-sol":   {InputPerMTok: 5.0, OutputPerMTok: 30.0, CacheReadPerMTok: 0.5},
			"gpt-5.6-terra": {InputPerMTok: 2.5, OutputPerMTok: 15.0, CacheReadPerMTok: 0.25},
			"gpt-5.6-luna":  {InputPerMTok: 1.0, OutputPerMTok: 6.0, CacheReadPerMTok: 0.1},
		},
		"openrouter": {
			// Confirmed live via GET https://openrouter.ai/api/v1/models.
			"deepseek/deepseek-v4-flash-0731": {InputPerMTok: 0.14, OutputPerMTok: 0.28, CacheReadPerMTok: 0.028},
		},
		"ollama": {
			"*": {},
		},
		"llamacpp": {
			"*": {},
		},
	}
}

// defaultConfigTomlTemplate is written to ~/.poisson/config.toml when it
// doesn't exist. It's a commented-out template of every option so the user
// can uncomment to override. "{{PROVIDERS}}" is filled in at use from the
// Providers registry (providers.go) so the doc text can't drift from the
// actual provider list.
const defaultConfigTomlTemplate = `# Poisson configuration — ~/.poisson/config.toml
# All options are commented out; Poisson uses built-in defaults.
# Uncomment and edit to override.

# Reasoning effort applied to every request (main agent, subagents, bash-risk
# checks). Higher = more thinking, more cost/latency.
# effort = "medium"              # low | medium | high | xhigh | max

# One-liner default provider + model override — sets BOTH at once. A bare
# value (no slash) applies to whatever provider.default already is.
# MUST appear here, before any [section] header below — TOML root-level
# keys only attach to the root table up to the first one. Appending this
# line at the end of the file instead (after every section is already
# open) silently attaches it to the LAST section instead, with no model
# ever actually being set — always add it up here, never at the bottom.
# model = "anthropic/claude-opus-5"  # or "ollama/glm-5.2:cloud", etc.

# Default provider + model
[provider]
# default = "ollama"             # {{PROVIDERS}}

[anthropic]
# model = "claude-opus-5"
# classifier = "claude-sonnet-5"  # bash-risk classifier for this provider
# If auth.json has OAuth tokens for anthropic, stealth mode is active.
# Otherwise set an API key here or in auth.json.
# api_key = "sk-ant-..."

[xai]
# model = "grok-build"           # or grok-4.5 (500K ctx, low|medium|high effort)

[openai]
# GPT via the ChatGPT Codex subscription (run: px login openai).
# model = "gpt-5.6-terra"          # or gpt-5.6-sol / gpt-5.6-luna, or legacy gpt-5.5

[openrouter]
# OpenRouter (https://openrouter.ai) — one OpenAI-compatible endpoint for
# 400+ models across many labs. Auth is a plain API key: run
# px login openrouter (prompts and stores it in auth.json), or set it here.
# model = "deepseek/deepseek-v4-flash-0731"
# api_key = "sk-or-..."
# base_url = "https://openrouter.ai/api/v1"

[ollama]
# base_url = "http://localhost:11434"
# model = "glm-5.2:cloud"

[llamacpp]
# Local llama-server (see workdir/alpaca), OpenAI-compatible wire format.
# base_url = "http://localhost:11212"
# model = "unsloth/Laguna-S-2.1-GGUF"

# User-defined provider instance — e.g. a second Ollama daemon on a remote
# host, under a name you pick. Works everywhere a built-in provider does:
# /providers, /model, px -p <name>/<model>, subagent provider pinning.
# type must be "ollama" (only wire format supported today). base_url is
# required; model is optional (pick one later via /model). Repeat the table
# under a different name for another instance (e.g. one local, one remote).
# [custom_providers.bastion]
# type = "ollama"
# base_url = "http://bastion-host:11434"
# model = "laguna-s-2.1:q4_K_M"
#
# Curated model list and context windows reuse the SAME [models.<name>.*]
# table as any built-in provider (see the [models.*] examples further
# down) — no separate schema. Omit entirely to fall back to live discovery
# via that instance's own /api/tags.
# [models.bastion."laguna-s-2.1:q4_K_M"]
# context_window = 262144

[classifier]
# Model that rates bash-command risk for the approval gate. The classifier
# always runs on the session's provider — only the model differs. A small,
# fast model is the right choice: the answer is one word, and inheriting an
# expensive session model means paying its rate once per gated command.
# Change it live with /classifier-model (per provider, current session only).
#
# Per provider, set it next to that provider's model:
#   [anthropic]
#   model = "claude-opus-5"
#   classifier = "claude-sonnet-5"
#
# The key below is the fallback for providers with no classifier of their own.
# Bare = every such provider; "provider/model" = that provider only.
# model = ""                     # model or provider/model (default: session model)

[compaction]
# threshold = 0.85               # fraction of context window (0.0–1.0)
# model = ""                     # model or provider/model for summarization (default: session model)

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
# [pricing.anthropic.claude-opus-5]
# input = 5.0
# output = 25.0
# cache_read = 0.5
# cache_write = 10.0
# [pricing.xai.grok-build]
# input = 1.0
# output = 2.0
# [pricing.xai."grok-4.5"]
# input = 2.0
# output = 6.0
# search_per_request bills a server-side web search on top of its tokens
# (Anthropic's web_search tool, used by web_search/fetch provider=anthropic).
# [pricing.anthropic."claude-haiku-4-5*"]
# input = 1.0
# output = 5.0
# search_per_request = 0.01
# [pricing.ollama."glm-5.2:cloud"]
# input = 0
# output = 0
# [pricing.ollama."minimax-m3:cloud"]
# input = 0
# output = 0
# [pricing.ollama."kimi-k2.7-code:cloud"]
# input = 0
# output = 0
# [pricing.llamacpp."unsloth/Laguna-S-2.1-GGUF"]
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

// defaultConfigToml renders defaultConfigTomlTemplate with the current
// provider list.
func defaultConfigToml() string {
	return strings.ReplaceAll(defaultConfigTomlTemplate, "{{PROVIDERS}}", strings.Join(ProviderIDs(), " | "))
}

// ConfigDir returns the path to ~/.poisson/, creating it (mode 0700) if
// missing. Exits the process if the home directory can't be resolved —
// silently substituting the working directory here would scatter config/db
// state per-invocation-directory instead of failing loudly (e.g. under
// cron/systemd with $HOME unset), and every caller of this function assumes
// it always names the same real, stable location.
func ConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "poisson: cannot resolve home directory: %v\n", err)
		os.Exit(1)
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
			if werr := os.WriteFile(path, []byte(defaultConfigToml()), 0o600); werr != nil {
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

// noModelTables lists top-level tables that have no "model" field of their
// own. If a "model" key ends up inside one anyway, it's not a real setting
// for that table — it's almost certainly the documented top-level
// `model = "provider/model"` one-liner override (see the comment on that
// block below), misplaced there because TOML root-level keys must appear
// BEFORE the first [section] header in the file. The shipped config.toml
// template already opens every section, so appending the override at the
// end — the single most natural edit a user would make — silently binds it
// to whichever table happens to be last ([tui], via the shipped template)
// instead of erroring or doing what was obviously meant. Providers
// (anthropic/xai/openai/ollama/llamacpp) and classifier/compaction all have
// a legitimate "model" field of their own, so they're deliberately excluded
// here — only tables where "model" can never mean anything real are checked.
var noModelTables = []string{"tui", "stealth", "provider"}

// mapToConfig applies parsed TOML values on top of the built-in defaults.
func mapToConfig(m map[string]interface{}) (*Config, error) {
	cfg := defaultConfig()

	for _, table := range noModelTables {
		tbl, ok := lookup(m, table)
		if !ok {
			continue
		}
		tblMap, ok := tbl.(map[string]interface{})
		if !ok {
			continue
		}
		if _, has := tblMap["model"]; has {
			return nil, fmt.Errorf(
				"%s.model is not a real setting — did you mean the top-level `model = \"provider/model\"` override? "+
					"it must appear BEFORE any [section] header in the file, not appended after one (see config.toml's own top-of-file example)",
				table)
		}
	}

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

	if v, ok := lookup(m, "openrouter", "model"); ok {
		s, err := asString(v)
		if err != nil {
			return nil, fmt.Errorf("openrouter.model: %w", err)
		}
		cfg.OpenRouter.Model = s
	}
	if v, ok := lookup(m, "openrouter", "api_key"); ok {
		s, err := asString(v)
		if err != nil {
			return nil, fmt.Errorf("openrouter.api_key: %w", err)
		}
		cfg.OpenRouter.APIKey = s
	}
	if v, ok := lookup(m, "openrouter", "base_url"); ok {
		s, err := asString(v)
		if err != nil {
			return nil, fmt.Errorf("openrouter.base_url: %w", err)
		}
		cfg.OpenRouter.BaseURL = s
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

	if v, ok := lookup(m, "llamacpp", "base_url"); ok {
		s, err := asString(v)
		if err != nil {
			return nil, fmt.Errorf("llamacpp.base_url: %w", err)
		}
		cfg.LlamaCpp.BaseURL = s
	}
	if v, ok := lookup(m, "llamacpp", "model"); ok {
		s, err := asString(v)
		if err != nil {
			return nil, fmt.Errorf("llamacpp.model: %w", err)
		}
		cfg.LlamaCpp.Model = s
	}

	// Custom providers must be parsed before the top-level `model =` knob
	// below, which may target one by name (setProviderModel resolves
	// through cfg.CustomProviders too).
	if err := parseCustomProviders(cfg, m); err != nil {
		return nil, err
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

	if v, ok := lookup(m, "classifier", "model"); ok {
		s, err := asString(v)
		if err != nil {
			return nil, fmt.Errorf("classifier.model: %w", err)
		}
		cfg.Classifier.Model = s
	}
	// [<provider>] classifier = "model" — the durable per-provider pin, next
	// to that provider's own model where it belongs.
	for _, provider := range ProviderIDs() {
		v, ok := lookup(m, provider, "classifier")
		if !ok {
			continue
		}
		s, err := asString(v)
		if err != nil {
			return nil, fmt.Errorf("%s.classifier: %w", provider, err)
		}
		if s = strings.TrimSpace(s); s == "" {
			continue
		}
		// The table already names the provider, so a provider-qualified value
		// is at best redundant and at worst contradictory — accept the
		// redundant spelling, reject the contradiction instead of picking a
		// winner. A leading segment that is NOT a provider id is part of the
		// model name (llamacpp and ollama both use HF-style
		// "unsloth/Laguna-S-2.1-GGUF" names), so it must survive untouched.
		if p, mdl, qualified := strings.Cut(s, "/"); qualified {
			if _, isProvider := ProviderMetaByID(strings.TrimSpace(p)); isProvider {
				if strings.TrimSpace(p) != provider {
					return nil, fmt.Errorf(
						"%s.classifier = %q: the table already names the provider; write %q",
						provider, s, strings.TrimSpace(mdl))
				}
				s = strings.TrimSpace(mdl)
			}
		}
		if cfg.Classifier.Models == nil {
			cfg.Classifier.Models = map[string]string{}
		}
		cfg.Classifier.Models[provider] = s
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
				if v, ok := mmap["search_per_request"]; ok {
					f, err := asFloat(v)
					if err != nil {
						return nil, fmt.Errorf("pricing.%s.%s.search_per_request: %w", provider, model, err)
					}
					p.SearchPerRequest = f
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

// customProviderTypes lists the wire formats a [custom_providers.<name>]
// instance may declare. Only "ollama" exists today (same OpenAI-compatible
// format the built-in Ollama/LlamaCpp providers already speak); listed as a
// set instead of a single string comparison so a second format later is a
// one-line addition here.
var customProviderTypes = map[string]bool{"ollama": true}

// parseCustomProviders parses [custom_providers.<name>] tables into
// cfg.CustomProviders. Each name becomes a new provider ID recognized
// throughout Poisson (config.ResolveProviderMeta, the /providers and /model
// pickers, px -p, subagent spawning) — see internal/provider/factory.go and
// internal/config/providers.go for where that ID is actually resolved and
// constructed. Model metadata and pricing for these instances are NOT
// parsed here: they reuse the existing generic [models.<name>.<model>] and
// [pricing.<name>.<model>] tables above verbatim, which are already keyed
// by an arbitrary string.
func parseCustomProviders(cfg *Config, m map[string]interface{}) error {
	cp, ok := m["custom_providers"]
	if !ok {
		return nil
	}
	cpMap, ok := cp.(map[string]interface{})
	if !ok {
		return fmt.Errorf("custom_providers: expected table")
	}
	for name, val := range cpMap {
		if strings.Contains(name, "/") {
			return fmt.Errorf("custom_providers.%s: name must not contain '/' (ambiguous with provider/model parsing)", name)
		}
		if _, builtin := ProviderMetaByID(name); builtin {
			return fmt.Errorf("custom_providers.%s: name collides with a built-in provider — pick a different name", name)
		}
		tbl, ok := val.(map[string]interface{})
		if !ok {
			return fmt.Errorf("custom_providers.%s: expected table", name)
		}
		typ, err := asString(tbl["type"])
		if err != nil || strings.TrimSpace(typ) == "" {
			return fmt.Errorf("custom_providers.%s.type: required (want %q)", name, "ollama")
		}
		typ = strings.TrimSpace(typ)
		if !customProviderTypes[typ] {
			return fmt.Errorf("custom_providers.%s.type: unsupported type %q (want %q)", name, typ, "ollama")
		}
		baseURL, err := asString(tbl["base_url"])
		if err != nil || strings.TrimSpace(baseURL) == "" {
			return fmt.Errorf("custom_providers.%s.base_url: required", name)
		}
		model := ""
		if v, has := tbl["model"]; has {
			model, err = asString(v)
			if err != nil {
				return fmt.Errorf("custom_providers.%s.model: %w", name, err)
			}
		}
		cfg.CustomProviders[name] = &CustomProviderConfig{
			Type:    typ,
			BaseURL: strings.TrimSpace(baseURL),
			Model:   model,
		}
	}
	return nil
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

// setProviderModel points a provider's Model field at model. Used by the
// top-level `model = "<provider>/<model>"` config knob — resolves through
// cfg.CustomProviders too (parseCustomProviders runs before this is ever
// called), so the one-liner works for a custom provider by name just like
// a built-in one.
func setProviderModel(cfg *Config, prov, model string) error {
	meta, ok := ResolveProviderMeta(prov, cfg)
	if !ok {
		return fmt.Errorf("unknown provider %q (want %s, or a [custom_providers.*] name)", prov, strings.Join(ProviderIDs(), "|"))
	}
	*meta.Model(cfg) = model
	return nil
}

// asString coerces a TOML value to string, rejecting non-string types.

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
