package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/auth"
	"github.com/mq37/poisson/internal/config"
	"github.com/mq37/poisson/internal/provider"
	"github.com/mq37/poisson/internal/testutil"
)

func TestFormatAnthropicUsageLine(t *testing.T) {
	// usageWindow (FiveHour/SevenDay's type) is unexported; set its
	// promoted fields directly rather than naming the type.
	u := &provider.AnthropicUsageLimits{
		ExtraUsageEnabled: true,
		ExtraUsed:         4.66,
		ExtraLimit:        200,
		ExtraCurrency:     "EUR",
	}
	u.FiveHour.UtilizationPct = 31
	u.SevenDay.UtilizationPct = 29
	got := formatAnthropicUsageLine("anthropic", u)
	for _, want := range []string{"5h 31% used, 69% left", "7d 29% used, 71% left", "extra usage: 4.66/200.00 EUR"} {
		if !strings.Contains(got, want) {
			t.Errorf("output %q missing %q", got, want)
		}
	}
}

func TestFormatAnthropicUsageLine_NoExtraUsage(t *testing.T) {
	u := &provider.AnthropicUsageLimits{}
	u.FiveHour.UtilizationPct = 10
	u.SevenDay.UtilizationPct = 5
	got := formatAnthropicUsageLine("anthropic", u)
	if strings.Contains(got, "extra usage") {
		t.Errorf("output %q should not mention extra usage when disabled", got)
	}
}

func TestFormatCodexUsageLine(t *testing.T) {
	u := &provider.CodexUsage{UsedPercent: 40, ResetCreditsAvailable: 2}
	got := formatCodexUsageLine("openai", u)
	for _, want := range []string{"7d 40% used, 60% left", "2 reset credit(s)"} {
		if !strings.Contains(got, want) {
			t.Errorf("output %q missing %q", got, want)
		}
	}
}

func TestSubscriptionLine_NotLoggedIn(t *testing.T) {
	meta, _ := config.ProviderMetaByID("openrouter")
	line := subscriptionLine(context.Background(), meta, auth.AuthStore{}, config.DefaultConfig())
	if !strings.Contains(line, "not logged in") || !strings.Contains(line, "px login openrouter") {
		t.Errorf("line = %q, want not-logged-in hint", line)
	}
}

// TestSubscriptionLine_APIKeyProviderNoUsageAPI covers openrouter with an
// api_key entry: configured, but no usage endpoint to poll — must not
// attempt a network call.
func TestSubscriptionLine_APIKeyProviderNoUsageAPI(t *testing.T) {
	meta, _ := config.ProviderMetaByID("openrouter")
	store := auth.AuthStore{"openrouter": {Type: "api_key", Key: "sk-test"}}
	line := subscriptionLine(context.Background(), meta, store, config.DefaultConfig())
	if !strings.Contains(line, "logged in (API key)") || !strings.Contains(line, "no usage API") {
		t.Errorf("line = %q, want logged-in/no-usage-API", line)
	}
}

// TestSubscriptionLine_XAIOAuthNoUsageAPI covers xai: OAuth-authenticated
// but no usage endpoint exists for it, so it must fall into the same
// no-network default branch as an API-key provider.
func TestSubscriptionLine_XAIOAuthNoUsageAPI(t *testing.T) {
	meta, _ := config.ProviderMetaByID("xai")
	store := auth.AuthStore{"xai": {Type: "oauth", Access: "t", Expires: 9999999999999}}
	line := subscriptionLine(context.Background(), meta, store, config.DefaultConfig())
	if !strings.Contains(line, "logged in (OAuth)") || !strings.Contains(line, "no usage API") {
		t.Errorf("line = %q, want logged-in/no-usage-API", line)
	}
}

// TestFetchSubscriptionsReport_SkipsLocalProviders confirms ollama/llamacpp
// (NeedsAuth false, run locally) never show up in the report — only
// subscription/API-key providers are reported. Uses an isolated HOME (no
// auth.json there) so this reads no real credentials and never reaches a
// live provider network endpoint.
func TestFetchSubscriptionsReport_SkipsLocalProviders(t *testing.T) {
	testutil.TempHome(t)
	report := fetchSubscriptionsReport(context.Background(), config.DefaultConfig())
	for _, id := range []string{"ollama", "llamacpp"} {
		if strings.Contains(report, id) {
			t.Errorf("report unexpectedly mentions local provider %q:\n%s", id, report)
		}
	}
	for _, id := range []string{"anthropic", "xai", "openai", "openrouter"} {
		if !strings.Contains(report, id) {
			t.Errorf("report missing subscription provider %q:\n%s", id, report)
		}
	}
}
