package tui

import (
	"encoding/base64"
	"fmt"
)

// formatOsc52 builds the OSC 52 clipboard-set sequence for text (system
// clipboard). Works over SSH; kitty and most modern terminals support it.
// Terminated with BEL (not ST) — matches kitty's own documented/maintainer-
// verified examples exactly, avoiding any terminal-specific ST parsing quirk.
func formatOsc52(text string) string {
	if text == "" {
		return ""
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	return fmt.Sprintf("\x1b]52;c;%s\a", encoded)
}
