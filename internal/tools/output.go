package tools

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const maxToolOutputBytes = 50 * 1024

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
	return utf8SafePrefix(s, maxToolOutputBytes) + fmt.Sprintf("\n\n... (tool output truncated at %d bytes)\n", maxToolOutputBytes)
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
