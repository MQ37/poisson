package tui

import (
	"encoding/base64"
	"fmt"
	"io"
	"os"
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

// osc52Copy sends text to the system clipboard via OSC 52 on /dev/tty.
// Errors are ignored — unsupported or redirected terminals simply no-op.
func osc52Copy(text string) error {
	if text == "" {
		return nil
	}
	f, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	return osc52CopyTo(text, f)
}