package tools

import "fmt"

const maxToolOutputBytes = 50 * 1024

// TrimToolResult bounds tool output before it reaches the model, store, or UI.
func TrimToolResult(result ToolResult) ToolResult {
	result.Content = trimToolText(result.Content)
	result.Error = trimToolText(result.Error)
	return result
}

func trimToolText(s string) string {
	if len(s) <= maxToolOutputBytes {
		return s
	}
	return utf8SafePrefix(s, maxToolOutputBytes) + fmt.Sprintf("\n\n... (tool output truncated at %d bytes)\n", maxToolOutputBytes)
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
