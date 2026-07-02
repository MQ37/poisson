package tui

import (
	"encoding/base64"
	"fmt"
	"io"
)

// formatOsc52 builds the OSC 52 clipboard set sequence for text (system clipboard).
func formatOsc52(text string) string {
	if text == "" {
		return ""
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	return fmt.Sprintf("\x1b]52;c;%s\x1b\\", encoded)
}

// osc52CopyTo writes an OSC 52 clipboard sequence to w. No-op for empty text.
func osc52CopyTo(text string, w io.Writer) error {
	seq := formatOsc52(text)
	if seq == "" {
		return nil
	}
	_, err := io.WriteString(w, seq)
	return err
}
