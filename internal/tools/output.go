package tools

import (
	"fmt"
	"os"
	"strings"
	"unicode/utf8"
)

const maxToolOutputBytes = 5 * 1024

// toolSpillDir is where oversized tool output is written in full so the model
// can read it back on demand. Overridable in tests.
var toolSpillDir = "/tmp"

// TrimToolResult bounds tool output before it reaches the model, store, or UI.
func TrimToolResult(result ToolResult) ToolResult {
	result.Content = trimToolText(result.Content)
	result.Error = trimToolText(result.Error)
	return result
}

func trimToolText(s string) string {
	s = sanitizeToolText(s)
	if len(s) <= maxToolOutputBytes {
		return s
	}
	prefix := utf8SafePrefix(s, maxToolOutputBytes)
	if path, err := spillToolOutput(s); err == nil {
		return prefix + fmt.Sprintf(
			"\n\n... (tool output truncated: showing %d of %d bytes. Full output saved to %s — read that path if you need the rest.)\n",
			len(prefix), len(s), path)
	}
	// Spill failed — still report the true size so the model isn't misled.
	return prefix + fmt.Sprintf(
		"\n\n... (tool output truncated: showing %d of %d bytes.)\n",
		len(prefix), len(s))
}

// spillToolOutput writes the full tool output to a temp file, returning its path.
func spillToolOutput(s string) (string, error) {
	f, err := os.CreateTemp(toolSpillDir, "poisson-tool-*.txt")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		return "", err
	}
	return f.Name(), nil
}

func sanitizeToolText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			i = skipEscape(s, i)
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			i++
			continue
		}
		if r == '\n' || r == '\t' || (r >= 0x20 && r != 0x7f) {
			b.WriteRune(r)
		}
		i += size
	}
	return b.String()
}

func skipEscape(s string, i int) int {
	i++
	if i >= len(s) {
		return i
	}
	if s[i] == '[' {
		i++
		for i < len(s) && (s[i] < 0x40 || s[i] > 0x7e) {
			i++
		}
		if i < len(s) {
			i++
		}
		return i
	}
	if s[i] == ']' {
		i++
		for i < len(s) {
			if s[i] == '\a' {
				return i + 1
			}
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
			i++
		}
		return i
	}
	return i + 1
}

func utf8SafePrefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && (s[n]&0xc0) == 0x80 {
		n--
	}
	return s[:n]
}
