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
	if poissonPMF(1, 0)+poissonPMF(1, 1)+poissonPMF(1, 2) < 0.9 {
		t.Fatal("expected most mass in k=0..2 for λ=1")
	}
}

func TestPrintStartupIntro(t *testing.T) {
	var b strings.Builder
	PrintStartupIntro(&b, "v0.1.0", "ollama", "llama3")
	out := b.String()
	for _, want := range []string{"Poisson", "λ = 1", "█", "k=0:", "v0.1.0", "ollama/llama3"} {
		if !strings.Contains(out, want) {
			t.Fatalf("intro missing %q:\n%s", want, out)
		}
	}
}

func mathAbs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}