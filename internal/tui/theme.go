package tui

import (
	"os"
	"strings"
)

// reverseVideo swaps fg/bg without depending on the active theme's colors;
// reset (theme-defined) turns it back off. Used for the mouse text selection
// highlight.
const reverseVideo = "\x1b[7m"

// detectTruecolor returns true if the environment indicates 24-bit color support.
// Checks COLORTERM=truecolor|24bit and TERM containing 24bit/truecolor.
func detectTruecolor() bool {
	ct := os.Getenv("COLORTERM")
	if ct == "truecolor" || ct == "24bit" {
		return true
	}
	term := os.Getenv("TERM")
	if strings.Contains(term, "24bit") || strings.Contains(term, "truecolor") {
		return true
	}
	return false
}

var (
	// themeName is the active semantic theme: "dark" (default) or "light".
	themeName string = "dark"
	// truecolor is whether 24-bit sequences are in use.
	truecolor bool
)

// applyTheme selects light/dark and 16/truecolor palettes, then updates
// the color variables used by the rest of the package. Unknown theme falls
// back to dark.
func applyTheme(theme string) {
	if theme != "light" && theme != "dark" {
		theme = "dark"
	}
	themeName = theme
	truecolor = detectTruecolor()

	if truecolor {
		if theme == "light" {
			applyLightTruecolor()
		} else {
			applyDarkTruecolor()
		}
	} else {
		if theme == "light" {
			applyLight16()
		} else {
			applyDark16()
		}
	}
}

// applyDark16 wires the classic 16-color dark palette.
func applyDark16() {
	reset = "\x1b[0m"
	bold = "\x1b[1m"
	dim = "\x1b[2m"
	italic = "\x1b[3m"
	underline = "\x1b[4m"

	fgBlack = "\x1b[30m"
	fgRed = "\x1b[31m"
	fgGreen = "\x1b[32m"
	fgYellow = "\x1b[33m"
	fgBlue = "\x1b[34m"
	fgMagenta = "\x1b[35m"
	fgCyan = "\x1b[36m"
	fgGray = "\x1b[90m"

	bgBlack = "\x1b[40m"
	bgDarkRed = "\x1b[41m"
	bgYellow = "\x1b[43m"
	bgBlue = "\x1b[44m"
	bgMagenta = "\x1b[45m"
	// 16-color: plain green/red backgrounds for diff lines.
	bgDiffAdd = "\x1b[42m"
	bgDiffDel = "\x1b[41m"
}

// applyLight16 uses darker foreground codes for readability on light profiles.
func applyLight16() {
	reset = "\x1b[0m"
	bold = "\x1b[1m"
	dim = "\x1b[2m"
	italic = "\x1b[3m"
	underline = "\x1b[4m"

	fgBlack = "\x1b[30m"
	fgRed = "\x1b[31m"
	fgGreen = "\x1b[32m"
	fgYellow = "\x1b[33m"
	fgBlue = "\x1b[34m"
	fgMagenta = "\x1b[35m"
	fgCyan = "\x1b[36m"
	fgGray = "\x1b[90m"

	bgBlack = "\x1b[47m"
	bgDarkRed = "\x1b[41m"
	bgYellow = "\x1b[43m"
	bgBlue = "\x1b[44m"
	bgMagenta = "\x1b[45m"
	bgDiffAdd = "\x1b[42m"
	bgDiffDel = "\x1b[41m"
}

// applyDarkTruecolor uses 24-bit RGB tuned for dark terminals.
func applyDarkTruecolor() {
	reset = "\x1b[0m"
	bold = "\x1b[1m"
	dim = "\x1b[2m"
	italic = "\x1b[3m"
	underline = "\x1b[4m"

	// Refined RGB for dark background terminals.
	fgBlack = "\x1b[38;2;50;50;50m"
	fgRed = "\x1b[38;2;220;80;60m"
	fgGreen = "\x1b[38;2;80;200;120m"
	fgYellow = "\x1b[38;2;230;200;70m"
	fgBlue = "\x1b[38;2;100;160;255m"
	fgMagenta = "\x1b[38;2;200;110;200m"
	fgCyan = "\x1b[38;2;90;200;220m"
	fgGray = "\x1b[38;2;150;150;150m"

	bgBlack = "\x1b[48;2;40;40;40m"
	bgDarkRed = "\x1b[48;2;90;35;35m"
	bgYellow = "\x1b[48;2;90;75;25m"
	bgBlue = "\x1b[48;2;35;55;100m"
	bgMagenta = "\x1b[48;2;80;35;80m"
	// Muted green/red fills that keep fg text readable on dark terminals.
	bgDiffAdd = "\x1b[48;2;28;56;36m"
	bgDiffDel = "\x1b[48;2;72;32;32m"
}

// applyLightTruecolor uses 24-bit RGB tuned for light terminals (darker
// accents so they remain readable on light backgrounds).
func applyLightTruecolor() {
	reset = "\x1b[0m"
	bold = "\x1b[1m"
	dim = "\x1b[2m"
	italic = "\x1b[3m"
	underline = "\x1b[4m"

	// Darker, more saturated for light bg.
	fgBlack = "\x1b[38;2;30;30;30m"
	fgRed = "\x1b[38;2;180;40;30m"
	fgGreen = "\x1b[38;2;30;130;60m"
	fgYellow = "\x1b[38;2;150;110;20m"
	fgBlue = "\x1b[38;2;30;90;190m"
	fgMagenta = "\x1b[38;2;130;50;130m"
	fgCyan = "\x1b[38;2;20;110;140m"
	fgGray = "\x1b[38;2;100;100;100m"

	bgBlack = "\x1b[48;2;230;230;230m"
	bgDarkRed = "\x1b[48;2;255;210;210m"
	bgYellow = "\x1b[48;2;255;245;190m"
	bgBlue = "\x1b[48;2;210;225;255m"
	bgMagenta = "\x1b[48;2;245;210;255m"
	bgDiffAdd = "\x1b[48;2;210;240;215m"
	bgDiffDel = "\x1b[48;2;255;220;220m"
}

func init() {
	// Default to dark (honors COLORTERM at process start for truecolor).
	applyTheme("dark")
}
