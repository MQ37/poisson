package tui

import (
	"strings"
	"testing"
)

func TestThemeDefaultsToDark(t *testing.T) {
	// Force no truecolor for deterministic base check.
	t.Setenv("COLORTERM", "")
	t.Setenv("TERM", "xterm")
	applyTheme("dark")

	if themeName != "dark" {
		t.Errorf("themeName = %q, want dark", themeName)
	}
	if truecolor {
		t.Error("truecolor true with no COLORTERM/24bit TERM")
	}
	// 16-color codes are short
	if !strings.HasPrefix(fgBlue, "\x1b[34m") {
		t.Errorf("fgBlue = %q, want 16-color prefix", fgBlue)
	}
}

func TestThemeLightSelection(t *testing.T) {
	t.Setenv("COLORTERM", "")
	t.Setenv("TERM", "xterm")
	applyTheme("light")

	if themeName != "light" {
		t.Errorf("themeName = %q, want light", themeName)
	}
	// same 16 codes for light16 (profile dependent)
	if !strings.HasPrefix(fgBlue, "\x1b[34m") {
		t.Errorf("fgBlue (light16) = %q", fgBlue)
	}
}

func TestThemeUnknownFallsBackToDark(t *testing.T) {
	t.Setenv("COLORTERM", "")
	t.Setenv("TERM", "xterm")
	applyTheme("neon")
	if themeName != "dark" {
		t.Errorf("unknown theme fell to %q, want dark", themeName)
	}
}

func TestThemeTruecolorDetection(t *testing.T) {
	// truecolor via COLORTERM
	t.Setenv("COLORTERM", "truecolor")
	t.Setenv("TERM", "xterm")
	applyTheme("dark")
	if !truecolor {
		t.Error("expected truecolor with COLORTERM=truecolor")
	}
	if !strings.Contains(fgBlue, "38;2;") {
		t.Errorf("fgBlue under truecolor = %q, want 24-bit", fgBlue)
	}
	if !strings.Contains(fgBlue, "100;160;255") {
		t.Errorf("fgBlue dark truecolor rgb = %q", fgBlue)
	}

	// 24bit alias
	t.Setenv("COLORTERM", "24bit")
	applyTheme("light")
	if !truecolor {
		t.Error("expected truecolor with COLORTERM=24bit")
	}
	if !strings.Contains(fgBlue, "30;90;190") {
		t.Errorf("fgBlue light truecolor rgb = %q", fgBlue)
	}

	// via TERM
	t.Setenv("COLORTERM", "")
	t.Setenv("TERM", "xterm-24bit")
	applyTheme("dark")
	if !truecolor {
		t.Error("expected truecolor with TERM=xterm-24bit")
	}
}

func TestTheme16ColorNoEnv(t *testing.T) {
	t.Setenv("COLORTERM", "")
	t.Setenv("TERM", "xterm-256color") // 256 but not 24bit -> 16 fallback per our detect
	applyTheme("dark")
	if truecolor {
		t.Error("256color without explicit 24bit should not be truecolor")
	}
	if strings.Contains(fgYellow, ";2;") {
		t.Errorf("fgYellow under 16color = %q", fgYellow)
	}
}

func TestThemeColorDiffersLightVsDark(t *testing.T) {
	t.Setenv("COLORTERM", "truecolor")
	t.Setenv("TERM", "")
	applyTheme("dark")
	darkBlue := fgBlue
	applyTheme("light")
	lightBlue := fgBlue
	if darkBlue == lightBlue {
		t.Errorf("dark and light blue are identical under truecolor: %q", darkBlue)
	}
}
