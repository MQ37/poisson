package tui

import (
	"strings"
	"testing"
)

func TestRenderHeader(t *testing.T) {
	s := StatusSnapshot{
		Cwd:           "/home/mq/workdir/poisson",
		Model:         "ollama/glm-5.2:cloud",
		ContextTokens: 87000,
		ContextWindow: 280000,
		ShowTokens:    true,
	}
	line := stripANSI(s.RenderHeader(80))
	if !strings.Contains(line, "workdir") {
		t.Fatalf("missing cwd: %q", line)
	}
	if !strings.Contains(line, "87") || !strings.Contains(line, "280") {
		t.Fatalf("missing token counts: %q", line)
	}
}

func TestRenderHeaderShowsAvgTokensPerSec(t *testing.T) {
	s := StatusSnapshot{
		Cwd:             "/home/mq/workdir/poisson",
		Model:           "x",
		AvgTokensPerSec: 62.5,
	}
	line := stripANSI(s.RenderHeader(80))
	if !strings.Contains(line, "63 tok/s") && !strings.Contains(line, "62 tok/s") {
		t.Fatalf("expected avg tok/s in header, got %q", line)
	}
	// Zero avg must not render a stray "0 tok/s".
	s.AvgTokensPerSec = 0
	line = stripANSI(s.RenderHeader(80))
	if strings.Contains(line, "tok/s") {
		t.Fatalf("zero avg must not render tok/s, got %q", line)
	}
}

// TestRenderHeaderSpinsWhileCompacting is the reported gap: compacting gave
// no live sign anything was happening — the header spinner only ever showed
// while Thinking, never while Compacting.
func TestRenderHeaderSpinsWhileCompacting(t *testing.T) {
	s := StatusSnapshot{
		Cwd:           "/home/mq/workdir/poisson",
		Model:         "ollama/glm-5.2:cloud",
		ContextTokens: 87000,
		ContextWindow: 280000,
		Compacting:    true,
		SpinnerFrame:  0,
	}
	line := stripANSI(s.RenderHeader(80))
	if !strings.Contains(line, spinnerChar(0)) {
		t.Fatalf("expected spinner char while compacting, got %q", line)
	}
	if !strings.Contains(line, "compacting") {
		t.Fatalf("expected a 'compacting' label, got %q", line)
	}
}

// TestRenderHeaderShowsAnthropicUsage confirms the 5h/7-day/extra-usage
// segment renders when populated (Anthropic OAuth only — see
// internal/provider/anthropic_usage.go).
func TestRenderHeaderShowsAnthropicUsage(t *testing.T) {
	s := StatusSnapshot{
		Cwd:   "/home/mq/workdir/poisson",
		Model: "anthropic/claude-opus-5",
		AnthropicUsage: &AnthropicUsageView{
			FiveHourPct:   31,
			SevenDayPct:   29,
			ExtraEnabled:  true,
			ExtraUsed:     4.66,
			ExtraLimit:    200,
			ExtraCurrency: "EUR",
		},
	}
	line := stripANSI(s.RenderHeader(120))
	for _, want := range []string{"5h 31%", "7d 29%", "4.66/200.00 EUR"} {
		if !strings.Contains(line, want) {
			t.Errorf("header %q missing %q", line, want)
		}
	}
}

// TestRenderHeaderHidesExtraUsageWhenDisabled confirms the extra-usage part
// is skipped when the account doesn't have it enabled, while 5h/7d still show.
func TestRenderHeaderHidesExtraUsageWhenDisabled(t *testing.T) {
	s := StatusSnapshot{
		Cwd:   "/home/mq/workdir/poisson",
		Model: "anthropic/claude-opus-5",
		AnthropicUsage: &AnthropicUsageView{
			FiveHourPct: 31,
			SevenDayPct: 29,
		},
	}
	line := stripANSI(s.RenderHeader(120))
	if strings.Contains(line, "EUR") || strings.Contains(line, "4.66") {
		t.Errorf("expected no extra-usage segment, got %q", line)
	}
	if !strings.Contains(line, "5h 31%") || !strings.Contains(line, "7d 29%") {
		t.Errorf("header %q missing 5h/7d", line)
	}
}

// TestRenderHeaderNoAnthropicUsage confirms nothing at all renders for
// non-Anthropic providers (AnthropicUsage left nil).
func TestRenderHeaderNoAnthropicUsage(t *testing.T) {
	s := StatusSnapshot{
		Cwd:   "/home/mq/workdir/poisson",
		Model: "ollama/glm-5.2:cloud",
	}
	line := stripANSI(s.RenderHeader(120))
	if strings.Contains(line, "5h") || strings.Contains(line, "7d") {
		t.Errorf("expected no usage segment for non-Anthropic provider, got %q", line)
	}
}

// TestRenderHeaderShowsCodexUsage mirrors TestRenderHeaderShowsAnthropicUsage
// for the OpenAI/Codex side (see internal/provider/openai_usage.go).
func TestRenderHeaderShowsCodexUsage(t *testing.T) {
	s := StatusSnapshot{
		Cwd:   "/home/mq/workdir/poisson",
		Model: "openai/gpt-5.5",
		CodexUsage: &CodexUsageView{
			UsedPercent:           37,
			ResetCreditsAvailable: 2,
		},
	}
	line := stripANSI(s.RenderHeader(120))
	for _, want := range []string{"7d 37%", "2 resets"} {
		if !strings.Contains(line, want) {
			t.Errorf("header %q missing %q", line, want)
		}
	}
}

// TestRenderHeaderCodexUsageSingularReset confirms the "N reset(s)" label
// doesn't pluralize a lone remaining credit.
func TestRenderHeaderCodexUsageSingularReset(t *testing.T) {
	s := StatusSnapshot{
		Cwd:        "/home/mq/workdir/poisson",
		Model:      "openai/gpt-5.5",
		CodexUsage: &CodexUsageView{UsedPercent: 10, ResetCreditsAvailable: 1},
	}
	line := stripANSI(s.RenderHeader(120))
	if !strings.Contains(line, "1 reset") {
		t.Errorf("header %q missing singular '1 reset'", line)
	}
	if strings.Contains(line, "1 resets") {
		t.Errorf("header %q incorrectly pluralized a single reset credit", line)
	}
}

// TestRenderHeaderNoCodexUsage confirms nothing renders for non-OpenAI
// providers (CodexUsage left nil).
func TestRenderHeaderNoCodexUsage(t *testing.T) {
	s := StatusSnapshot{
		Cwd:   "/home/mq/workdir/poisson",
		Model: "ollama/glm-5.2:cloud",
	}
	line := stripANSI(s.RenderHeader(120))
	if strings.Contains(line, "reset") {
		t.Errorf("expected no Codex usage segment for non-OpenAI provider, got %q", line)
	}
}
