package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/mq37/poisson/internal/auth"
	"github.com/mq37/poisson/internal/config"
	"github.com/mq37/poisson/internal/provider"
)

// cmdSubscriptions reports login status and remaining usage for every
// subscription/API-key provider (anthropic, xai, openai, openrouter),
// independent of whichever provider is the session's currently active one
// — unlike /status and the header, which only ever show the active
// provider's usage. Runs the network fetches in a goroutine (same pattern
// as /openai-reset-usage): usage endpoints carry a 15s timeout each and
// this must never block the input loop.
func cmdSubscriptions(h commandHost) {
	h.Out(styleSystem, "  checking subscriptions...")
	cfg := h.Agent().Config()
	go func() {
		report := fetchSubscriptionsReport(context.Background(), cfg)
		if th, ok := h.(tuiCmdHost); ok {
			th.t.mu.Lock()
			defer th.t.mu.Unlock()
		}
		h.Out(styleSystem, report)
	}()
}

// fetchSubscriptionsReport builds the /subscriptions report text. Loads
// auth from disk itself (not the session's live provider) so it reflects
// every logged-in provider, not just the one the session happens to be
// using right now.
func fetchSubscriptionsReport(ctx context.Context, cfg *config.Config) string {
	authStore, _ := auth.Load()
	var b strings.Builder
	b.WriteString("Subscriptions:\n")
	for _, meta := range config.Providers {
		if !meta.NeedsAuth {
			continue // ollama/llamacpp run locally, nothing to report
		}
		b.WriteString(subscriptionLine(ctx, meta, authStore, cfg))
	}
	return strings.TrimRight(b.String(), "\n")
}

// subscriptionLine reports one provider's login/usage status.
func subscriptionLine(ctx context.Context, meta config.ProviderMeta, authStore auth.AuthStore, cfg *config.Config) string {
	if !provider.IsConfigured(meta.ID, authStore, cfg) {
		return fmt.Sprintf("  %-11s not logged in  (run: px login %s)\n", meta.ID, meta.ID)
	}
	authKind := "API key"
	if auth.IsOAuth(authStore, meta.ID) {
		authKind = "OAuth"
	}

	switch meta.ID {
	case "anthropic":
		ap, ok := provider.NewProvider(meta.ID, authStore, cfg).(*provider.AnthropicProvider)
		if !ok || authKind != "OAuth" {
			return fmt.Sprintf("  %-11s logged in (%s) — usage only tracked for OAuth\n", meta.ID, authKind)
		}
		u, err := ap.UsageLimits(ctx)
		if err != nil {
			return fmt.Sprintf("  %-11s logged in (OAuth) — usage unavailable: %s\n", meta.ID, err.Error())
		}
		return formatAnthropicUsageLine(meta.ID, u)
	case "openai":
		op, ok := provider.NewProvider(meta.ID, authStore, cfg).(*provider.OpenAIProvider)
		if !ok {
			return fmt.Sprintf("  %-11s logged in (%s)\n", meta.ID, authKind)
		}
		u, err := op.UsageLimits(ctx)
		if err != nil {
			return fmt.Sprintf("  %-11s logged in (OAuth) — usage unavailable: %s\n", meta.ID, err.Error())
		}
		return formatCodexUsageLine(meta.ID, u)
	default:
		// xai (Grok) and openrouter have no usage API to poll.
		return fmt.Sprintf("  %-11s logged in (%s) — no usage API\n", meta.ID, authKind)
	}
}

// formatAnthropicUsageLine renders one already-fetched Anthropic usage
// snapshot. Split out from subscriptionLine so the formatting is testable
// without a network call.
func formatAnthropicUsageLine(id string, u *provider.AnthropicUsageLimits) string {
	line := fmt.Sprintf("  %-11s logged in (OAuth) — 5h %.0f%% used, %.0f%% left · 7d %.0f%% used, %.0f%% left\n",
		id, u.FiveHour.UtilizationPct, 100-u.FiveHour.UtilizationPct,
		u.SevenDay.UtilizationPct, 100-u.SevenDay.UtilizationPct)
	if u.ExtraUsageEnabled {
		line += fmt.Sprintf("              extra usage: %.2f/%.2f %s\n", u.ExtraUsed, u.ExtraLimit, u.ExtraCurrency)
	}
	return line
}

// formatCodexUsageLine renders one already-fetched Codex (OpenAI) usage
// snapshot. Split out from subscriptionLine for the same reason as
// formatAnthropicUsageLine above.
func formatCodexUsageLine(id string, u *provider.CodexUsage) string {
	return fmt.Sprintf("  %-11s logged in (OAuth) — 7d %.0f%% used, %.0f%% left · %d reset credit(s)\n",
		id, u.UsedPercent, 100-u.UsedPercent, u.ResetCreditsAvailable)
}
