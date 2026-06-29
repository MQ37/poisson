package tui

import (
	"fmt"
	"math"
	"strings"
)

const (
	introPlotH  = 5
	introMaxK   = 8
	introXColW  = 2 // runes per k tick (plot + labels stay aligned)
	introYLabel = "    │"
)

// InstallStartupIntro paints the welcome chart into scrollback (visible inside the TUI).
func (t *TUI) InstallStartupIntro(version, provider, model string) {
	for _, line := range poissonIntroANSILines(version, provider, model) {
		t.scroll.appendIntroLine(line)
	}
	t.introScrollTop = true
}

func poissonIntroANSILines(version, provider, model string) []string {
	const lambda = 1.0
	probs := make([]float64, introMaxK+1)
	ymax := 0.0
	for k := 0; k <= introMaxK; k++ {
		probs[k] = poissonPMF(lambda, k)
		if probs[k] > ymax {
			ymax = probs[k]
		}
	}
	if ymax < 0.01 {
		ymax = 0.4
	}

	rows := make([]int, introMaxK+1)
	for k := 0; k <= introMaxK; k++ {
		rows[k] = probRow(probs[k], ymax, introPlotH)
	}

	plotW := (introMaxK + 1) * introXColW
	grid := make([][]rune, introPlotH)
	for r := 0; r < introPlotH; r++ {
		grid[r] = make([]rune, plotW)
		for c := range grid[r] {
			grid[r][c] = ' '
		}
	}
	for k := 0; k <= introMaxK; k++ {
		x := k*introXColW + introXColW/2
		grid[rows[k]][x] = '*'
	}
	for k := 1; k <= introMaxK; k++ {
		drawSegment(grid, (k-1)*introXColW+introXColW/2, rows[k-1], k*introXColW+introXColW/2, rows[k])
	}

	lineColor := fgCyan + bold
	axisColor := dim

	var out []string
	out = append(out, "")
	out = append(out, bold+fgCyan+"Poisson"+reset+dim+"  λ=1"+reset)
	out = append(out, "")

	for r := 0; r < introPlotH; r++ {
		y := ymax * float64(introPlotH-1-r) / float64(introPlotH-1)
		yLabel := introYLabel
		if r == 0 || r == introPlotH-1 || r == introPlotH/2 {
			yLabel = fmt.Sprintf("%4.2f│", y)
		}
		var plot strings.Builder
		plot.WriteString(axisColor + yLabel + reset)
		plot.WriteString(lineColor)
		for _, ch := range grid[r] {
			plot.WriteRune(ch)
		}
		plot.WriteString(reset)
		if r == 0 {
			plot.WriteString(axisColor + " P" + reset)
		}
		out = append(out, plot.String())
	}

	xAxis := axisColor + "    └" + strings.Repeat("─", plotW) + reset
	out = append(out, xAxis)
	out = append(out, axisColor+"     "+introXLabels()+"  k"+reset)
	out = append(out, "")
	out = append(out, dim+"Poisson "+version+" · "+provider+"/"+model+reset)
	out = append(out, "")
	return out
}

func probRow(p, ymax float64, height int) int {
	if height < 2 {
		return 0
	}
	if p <= 0 {
		return height - 1
	}
	r := height - 1 - int(math.Round(p/ymax*float64(height-1)))
	if r < 0 {
		return 0
	}
	if r >= height {
		return height - 1
	}
	return r
}

func drawSegment(grid [][]rune, x0, y0, x1, y1 int) {
	dx, dy := absInt(x1-x0), absInt(y1-y0)
	sx, sy := 1, 1
	if x0 > x1 {
		sx = -1
	}
	if y0 > y1 {
		sy = -1
	}
	err := dx - dy
	x, y := x0, y0
	for {
		if y >= 0 && y < len(grid) && x >= 0 && x < len(grid[0]) && grid[y][x] == ' ' {
			grid[y][x] = '·'
		}
		if x == x1 && y == y1 {
			break
		}
		e2 := 2 * err
		if e2 > -dy {
			err -= dy
			x += sx
		}
		if e2 < dx {
			err += dx
			y += sy
		}
	}
}

func introXLabels() string {
	var b strings.Builder
	for k := 0; k <= introMaxK; k++ {
		b.WriteString(fmt.Sprintf("%*d", introXColW, k))
	}
	return b.String()
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

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}