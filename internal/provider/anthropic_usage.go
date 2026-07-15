package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"

	"poisson/internal/auth"
)

// usageTTL bounds how often UsageLimits actually hits the network — Claude
// Code's own usage indicator polls infrequently, and this is a background
// status-bar number, not something worth spending a request on every render.
const usageTTL = 5 * time.Minute

// usageWindow is one of Anthropic's rolling usage windows (5-hour session,
// 7-day/weekly).
type usageWindow struct {
	UtilizationPct float64
	ResetsAt       time.Time
}

// AnthropicUsageLimits is the parsed response of GET /api/oauth/usage — the same
// endpoint Claude Code's CLI polls to show its own 5h/weekly usage indicator
// (confirmed via a MITM capture of a real Claude Code session).
type AnthropicUsageLimits struct {
	FiveHour usageWindow
	SevenDay usageWindow

	// Pay-as-you-go balance beyond the plan's included usage. Zero-valued
	// (ExtraUsageEnabled false) when the account doesn't have it turned on.
	ExtraUsageEnabled bool
	ExtraUsed         float64
	ExtraLimit        float64
	ExtraCurrency     string

	FetchedAt time.Time
}

// UsageLimits returns the account's current 5h/7-day usage, refreshing from
// Anthropic at most every usageTTL (cached value returned otherwise). OAuth
// only: the endpoint lives under /api/oauth/ and isn't meaningful for
// API-key billing, so this returns an error immediately without a network
// call when auth is by key. On a transient fetch error, the last
// successfully fetched snapshot is returned instead of failing the caller —
// a background poll shouldn't blank a number already on screen over one
// dropped request.
func (p *AnthropicProvider) UsageLimits(ctx context.Context) (*AnthropicUsageLimits, error) {
	p.authMu.Lock()
	isOAuth := auth.IsOAuth(p.auth, "anthropic")
	p.authMu.Unlock()
	if !isOAuth {
		return nil, fmt.Errorf("usage limits require Anthropic OAuth login")
	}

	if cached := p.CachedUsageLimits(); cached != nil && time.Since(cached.FetchedAt) < usageTTL {
		return cached, nil
	}

	fresh, err := p.fetchUsage(ctx)
	if err != nil {
		if cached := p.CachedUsageLimits(); cached != nil {
			return cached, nil
		}
		return nil, err
	}

	p.usageMu.Lock()
	p.usageCache = fresh
	p.usageMu.Unlock()
	return fresh, nil
}

// CachedUsageLimits returns the last successfully fetched usage snapshot
// without a network call or TTL check — nil if none has been fetched yet.
// Safe to call from a render/status-sync path.
func (p *AnthropicProvider) CachedUsageLimits() *AnthropicUsageLimits {
	p.usageMu.Lock()
	defer p.usageMu.Unlock()
	return p.usageCache
}

func (p *AnthropicProvider) fetchUsage(ctx context.Context) (*AnthropicUsageLimits, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	do := func() (*http.Response, error) {
		p.authMu.Lock()
		access := p.auth["anthropic"].Access
		p.authMu.Unlock()
		req, err := http.NewRequestWithContext(fetchCtx, "GET", p.baseURL+"/api/oauth/usage", nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Authorization", "Bearer "+access)
		req.Header.Set("anthropic-beta", "oauth-2025-04-20")
		if p.config != nil {
			req.Header.Set("user-agent", "claude-cli/"+p.config.Stealth.CCVersion)
		} else {
			req.Header.Set("user-agent", "claude-cli/2.1.156")
		}
		return p.client.Do(req)
	}

	resp, err := do()
	if err != nil {
		return nil, fmt.Errorf("fetch usage: %w", err)
	}
	if resp.StatusCode == 401 {
		resp.Body.Close()
		p.authMu.Lock()
		refreshed, rerr := auth.RefreshAnthropicToken(p.auth["anthropic"].Refresh)
		if rerr == nil {
			p.auth["anthropic"] = *refreshed
			_ = auth.Save(p.auth)
		}
		p.authMu.Unlock()
		if rerr != nil {
			return nil, fmt.Errorf("token expired, refresh failed: %w", rerr)
		}
		resp, err = do()
		if err != nil {
			return nil, fmt.Errorf("fetch usage: %w", err)
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		return nil, fmt.Errorf("usage API error (status %d): %s", resp.StatusCode, string(body))
	}

	var raw struct {
		FiveHour struct {
			Utilization float64   `json:"utilization"`
			ResetsAt    time.Time `json:"resets_at"`
		} `json:"five_hour"`
		SevenDay struct {
			Utilization float64   `json:"utilization"`
			ResetsAt    time.Time `json:"resets_at"`
		} `json:"seven_day"`
		ExtraUsage struct {
			IsEnabled     bool    `json:"is_enabled"`
			MonthlyLimit  float64 `json:"monthly_limit"`
			UsedCredits   float64 `json:"used_credits"`
			Currency      string  `json:"currency"`
			DecimalPlaces int     `json:"decimal_places"`
		} `json:"extra_usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode usage response: %w", err)
	}

	// monthly_limit/used_credits are minor units (e.g. cents for EUR) — the
	// same convention the response's own "spend" block uses (amount_minor);
	// decimal_places says how many digits to shift back for display.
	scale := math.Pow(10, float64(raw.ExtraUsage.DecimalPlaces))
	if scale == 0 {
		scale = 1
	}

	return &AnthropicUsageLimits{
		FiveHour: usageWindow{UtilizationPct: raw.FiveHour.Utilization, ResetsAt: raw.FiveHour.ResetsAt},
		SevenDay: usageWindow{UtilizationPct: raw.SevenDay.Utilization, ResetsAt: raw.SevenDay.ResetsAt},

		ExtraUsageEnabled: raw.ExtraUsage.IsEnabled,
		ExtraUsed:         raw.ExtraUsage.UsedCredits / scale,
		ExtraLimit:        raw.ExtraUsage.MonthlyLimit / scale,
		ExtraCurrency:     raw.ExtraUsage.Currency,

		FetchedAt: time.Now(),
	}, nil
}
