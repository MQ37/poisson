package tui

import (
	"strings"
	"testing"
)

func TestPoissonIntroANSILines(t *testing.T) {
	applyTheme("dark")
	lines := poissonIntroANSILines("v0.1.0", "ollama", "llama3")
	plain := stripANSI(strings.Join(lines, "\n"))
	for _, want := range []string{"Σ", "Embrace the entropy, probabilities favor the bold.", "v0.1.0"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("intro missing %q:\n%s", want, plain)
		}
	}
}

func TestInstallStartupIntro(t *testing.T) {
	tui := newTUI(nil, "s-test", nil)
	tui.InstallStartupIntro("v0.1.0", "ollama", "m")
	if len(tui.scroll.blocks) == 0 || tui.scroll.blocks[0].kind != blockIntro {
		t.Fatal("expected intro blocks")
	}
	rows, _ := tui.scroll.layoutAll(80)
	var plain strings.Builder
	for _, row := range rows {
		plain.WriteString(stripANSI(row.Text))
		plain.WriteByte('\n')
	}
	if !strings.Contains(plain.String(), "Σ") {
		t.Fatalf("layout missing sigma:\n%s", plain.String())
	}
}
