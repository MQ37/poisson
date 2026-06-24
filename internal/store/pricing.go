package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// Pricing holds the per-1M-token USD rates for a (provider, model) pair.
type Pricing struct {
	InputPerMTok      float64
	OutputPerMTok     float64
	CacheReadPerMTok  float64
	CacheWritePerMTok float64
}

// builtInPricing are the default rates from SPEC §3.4. These are seeded
// into the model_pricing table on first run; config overrides take
// precedence (applied by callers of SeedPricing that pass overrides).
//
// Wildcard model keys (for example "*") are stored verbatim and matched by
// prefix at lookup time when an exact match is not found.
var builtInPricing = []struct {
	Provider string
	Model    string
	Pricing
}{
	{"anthropic", "claude-opus-4-8", Pricing{5.0, 25.0, 0.5, 3.0}},
	{"xai", "grok-build", Pricing{1.0, 2.0, 0, 0}},
	{"ollama", "*", Pricing{0, 0, 0, 0}},
}

// SeedPricing inserts the built-in default pricing rows into model_pricing
// using INSERT OR IGNORE so existing (possibly config-overridden) rows are
// preserved on re-seeding.
func (s *Store) SeedPricing() error {
	for _, p := range builtInPricing {
		_, err := s.db.Exec(
			`INSERT OR IGNORE INTO model_pricing
			 (provider, model, input_per_mtok, output_per_mtok,
			  cache_read_per_mtok, cache_write_per_mtok)
			 VALUES (?,?,?,?,?,?)`,
			p.Provider, p.Model,
			p.InputPerMTok, p.OutputPerMTok,
			p.CacheReadPerMTok, p.CacheWritePerMTok)
		if err != nil {
			return fmt.Errorf("seed pricing %s/%s: %w", p.Provider, p.Model, err)
		}
	}
	return nil
}

// ErrPricingNotFound is returned when no pricing row matches.
var ErrPricingNotFound = errors.New("store: pricing not found")

// GetPricing returns the pricing for a (provider, model) pair. It first
// tries an exact match; if none exists, it tries wildcard prefix matches
// (e.g. "claude-opus-4-*") and the provider-level "*" fallback.
func (s *Store) GetPricing(provider, model string) (Pricing, error) {
	// 1. Exact match.
	var p Pricing
	row := s.db.QueryRow(
		`SELECT input_per_mtok, output_per_mtok, cache_read_per_mtok, cache_write_per_mtok
		 FROM model_pricing WHERE provider = ? AND model = ?`,
		provider, model)
	err := row.Scan(&p.InputPerMTok, &p.OutputPerMTok, &p.CacheReadPerMTok, &p.CacheWritePerMTok)
	if err == nil {
		return p, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return p, fmt.Errorf("get pricing exact: %w", err)
	}

	// 2. Wildcard prefix match — find rows like "prefix-*" for this provider
	//    where the requested model starts with the prefix before "*".
	rows, err := s.db.Query(
		`SELECT model, input_per_mtok, output_per_mtok, cache_read_per_mtok, cache_write_per_mtok
		 FROM model_pricing WHERE provider = ? AND model LIKE '%*%'`, provider)
	if err != nil {
		return p, fmt.Errorf("get pricing wildcard: %w", err)
	}
	defer rows.Close()
	var wildcardP *Pricing
	var wildcardPrefixLen int
	for rows.Next() {
		var m string
		var wp Pricing
		if err := rows.Scan(&m, &wp.InputPerMTok, &wp.OutputPerMTok, &wp.CacheReadPerMTok, &wp.CacheWritePerMTok); err != nil {
			return p, fmt.Errorf("get pricing wildcard scan: %w", err)
		}
		prefix := m
		if idx := indexByte(m, '*'); idx >= 0 {
			prefix = m[:idx]
		}
		if prefix == "" {
			// "*" matches anything (provider-level fallback).
			if wildcardP == nil || wildcardPrefixLen == 0 {
				wildcardP = &wp
				wildcardPrefixLen = 0
			}
			continue
		}
		if hasPrefix(model, prefix) {
			// Prefer the longest matching prefix.
			if wildcardP == nil || len(prefix) > wildcardPrefixLen {
				wildcardP = &wp
				wildcardPrefixLen = len(prefix)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return p, fmt.Errorf("get pricing wildcard rows: %w", err)
	}
	if wildcardP != nil {
		return *wildcardP, nil
	}
	return p, ErrPricingNotFound
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func hasPrefix(s, prefix string) bool {
	if len(prefix) > len(s) {
		return false
	}
	return s[:len(prefix)] == prefix
}

// ComputeCost computes the USD cost for a single API call given the
// (provider, model) pricing and exact token counts.
//
//	cost = (input/1e6) * input_per_mtok
//	     + (output/1e6) * output_per_mtok
//	     + (cacheRead/1e6) * cache_read_per_mtok
//	     + (cacheWrite/1e6) * cache_write_per_mtok
func (s *Store) ComputeCost(provider, model string, input, output, cacheRead, cacheWrite int) float64 {
	p, err := s.GetPricing(provider, model)
	if err != nil {
		// No pricing found → treat as free (e.g. OAuth subscription models).
		return 0
	}
	const perM = 1e6
	return float64(input)/perM*p.InputPerMTok +
		float64(output)/perM*p.OutputPerMTok +
		float64(cacheRead)/perM*p.CacheReadPerMTok +
		float64(cacheWrite)/perM*p.CacheWritePerMTok
}
