// Package pricing resolves per-model token rates from config and built-in defaults.
// Rates are not stored in the DB — api_calls.cost is computed at write time.
package pricing

import (
	"strings"

	"poisson/internal/config"
)

// Rates holds per-1M-token USD rates.
type Rates struct {
	InputPerMTok      float64
	OutputPerMTok     float64
	CacheReadPerMTok  float64
	CacheWritePerMTok float64
}

var builtIn = map[string]map[string]Rates{
	"anthropic": {
		// input, output, cacheRead (0.1x), cacheWrite. Poisson uses the 1-hour
		// cache pool, whose writes bill at 2x input ($10/MTok for Opus 4.8).
		"claude-opus-4-8": {5.0, 25.0, 0.5, 10.0},
	},
	"xai": {
		"grok-build": {1.0, 2.0, 0, 0},
	},
	"ollama": {
		"*": {0, 0, 0, 0},
	},
}

// Lookup returns pricing for (provider, model). Config overrides beat built-ins.
func Lookup(cfg *config.Config, provider, model string) (Rates, bool) {
	if cfg != nil && cfg.Pricing != nil {
		if prov, ok := cfg.Pricing[provider]; ok {
			if r, ok := prov[model]; ok {
				return Rates(r), true
			}
			if r, ok := lookupWildcardConfig(prov, model); ok {
				return r, true
			}
		}
	}
	if prov, ok := builtIn[provider]; ok {
		if r, ok := prov[model]; ok {
			return r, true
		}
		if r, ok := lookupWildcardRates(prov, model); ok {
			return r, true
		}
	}
	return Rates{}, false
}

func lookupWildcardRates(prov map[string]Rates, model string) (Rates, bool) {
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
				cp := r
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