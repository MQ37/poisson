package tui

import (
	"fmt"
	"io"
	"math"
	"strings"
)

const introChartHeight = 10

// PrintStartupIntro writes the Poisson λ=1 distribution chart and version line.
func PrintStartupIntro(w io.Writer, version, provider, model string) {
	for _, line := range poissonIntroLines() {
		fmt.Fprintln(w, line)
	}
	fmt.Fprintf(w, "\nPoisson %s · %s/%s\n\n", version, provider, model)
}

func poissonIntroLines() []string {
	const lambda = 1.0
	const maxK = 8

	probs := make([]float64, maxK+1)
	maxP := 0.0
	for k := 0; k <= maxK; k++ {
		probs[k] = poissonPMF(lambda, k)
		if probs[k] > maxP {
			maxP = probs[k]
		}
	}

	var lines []string
	lines = append(lines, "")
	lines = append(lines, centerLine("Poisson", 44))
	lines = append(lines, centerLine("P(k | λ = 1)", 44))
	lines = append(lines, "")

	for row := introChartHeight; row >= 1; row-- {
		threshold := maxP * float64(row) / float64(introChartHeight)
		var b strings.Builder
		b.WriteString("  ")
		for k := 0; k <= maxK; k++ {
			if probs[k] >= threshold {
				b.WriteString("█")
			} else {
				b.WriteString(" ")
			}
		}
		if row == introChartHeight {
			b.WriteString("  P")
		}
		lines = append(lines, b.String())
	}

	var axis strings.Builder
	axis.WriteString("  ")
	for k := 0; k <= maxK; k++ {
		axis.WriteString(fmt.Sprintf("%d", k%10))
	}
	axis.WriteString("  k")
	lines = append(lines, axis.String())

	var legend strings.Builder
	legend.WriteString("  ")
	for k := 0; k <= 3; k++ {
		if k > 0 {
			legend.WriteString("  ")
		}
		legend.WriteString(fmt.Sprintf("k=%d:%.0f%%", k, probs[k]*100))
	}
	lines = append(lines, legend.String())

	return lines
}

func poissonPMF(lambda float64, k int) float64 {
	if k < 0 {
		return 0
	}
	logP := -lambda + float64(k)*math.Log(lambda)
	for i := 2; i <= k; i++ {
		logP -= math.Log(float64(i))
	}
	return math.Exp(logP)
}

func centerLine(s string, width int) string {
	if width <= len(s) {
		return s
	}
	pad := (width - len(s)) / 2
	return strings.Repeat(" ", pad) + s
}