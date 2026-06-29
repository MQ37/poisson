package tui

import (
	"strings"
	"testing"
)

func TestPoissonPMFAlphaOne(t *testing.T) {
	p0 := poissonPMF(1, 0)
	p1 := poissonPMF(1, 1)
	if mathAbs(p0-0.367879) > 0.001 || mathAbs(p1-0.367879) > 0.001 {
		t.Fatalf("P(0)=P(1)≈e^-1, got P(0)=%.4f P(1)=%.4f", p0, p1)
	}
}

func TestPoissonIntroANSILines(t *testing.T) {
	applyTheme("dark")
	lines := poissonIntroANSILines("v0.1.0", "ollama", "llama3")
	plain := stripANSI(strings.Join(lines, "\n"))
	for _, want := range []string{"Poisson", "λ=1", "*", "└", " 0", " 8", " k", "v0.1.0"} {
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
	if !strings.Contains(plain.String(), "Poisson") {
		t.Fatalf("layout missing title:\n%s", plain.String())
	}
}

func mathAbs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}