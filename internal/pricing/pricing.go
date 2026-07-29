// Package pricing resolves per-model token rates from config and built-in defaults.
// Rates are not stored in the DB — api_calls.cost is computed at write time.
package pricing

import (
	"strings"

	"github.com/mq37/poisson/internal/config"
)

// Rates holds per-1M-token USD rates, plus the per-request fee a server-side
// search on this model bills on top of tokens. Field order must match
// config.Pricing: Lookup converts one to the other directly.
type Rates struct {
	InputPerMTok      float64
	OutputPerMTok     float64
	CacheReadPerMTok  float64
	CacheWritePerMTok float64
	SearchPerRequest  float64
}

// Lookup returns pricing for (provider, model). Built-in rates live in
// config.DefaultConfig().Pricing (config/config.go's defaultPricing) — the
// one place they're defined; a nil or zero-value cfg falls back to that same
// default, so there's no separate built-in table to keep in sync here.
func Lookup(cfg *config.Config, provider, model string) (Rates, bool) {
	if cfg == nil || cfg.Pricing == nil {
		cfg = config.DefaultConfig()
	}
	prov, ok := cfg.Pricing[provider]
	if !ok {
		return Rates{}, false
	}
	if r, ok := prov[model]; ok {
		return Rates(r), true
	}
	return lookupWildcardConfig(prov, model)
}

func lookupWildcardConfig(prov map[string]config.Pricing, model string) (Rates, bool) {
	var best *Rates
	bestLen := -1
	for pattern, r := range prov {
		if !strings.Contains(pattern, "*") {
			continue
		}
		prefix := pattern
		if idx := strings.IndexByte(pattern, '*'); idx >= 0 {
			prefix = pattern[:idx]
		}
		if prefix == "" || strings.HasPrefix(model, prefix) {
			if best == nil || len(prefix) > bestLen {
				cp := Rates(r)
				best = &cp
				bestLen = len(prefix)
			}
		}
	}
	if best != nil {
		return *best, true
	}
	return Rates{}, false
}

// SearchCost returns the USD fee for requests server-side web searches on
// (provider, model), which Anthropic bills on top of the tokens the results
// add to the prompt. Unknown pricing → 0.
func SearchCost(cfg *config.Config, provider, model string, requests int) float64 {
	if requests <= 0 {
		return 0
	}
	r, ok := Lookup(cfg, provider, model)
	if !ok {
		return 0
	}
	return float64(requests) * r.SearchPerRequest
}

// ComputeCost returns USD cost for a single API call. Unknown pricing → 0.
func ComputeCost(cfg *config.Config, provider, model string, input, output, cacheRead, cacheWrite int) float64 {
	r, ok := Lookup(cfg, provider, model)
	if !ok {
		return 0
	}
	const perM = 1e6
	return float64(input)/perM*r.InputPerMTok +
		float64(output)/perM*r.OutputPerMTok +
		float64(cacheRead)/perM*r.CacheReadPerMTok +
		float64(cacheWrite)/perM*r.CacheWritePerMTok
}
