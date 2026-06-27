package tui

import (
	"strings"
	"unicode/utf8"
)

// wrapWords wraps text at word boundaries when spaces are present; long tokens and
// space-free strings fall back to hard wrap at width runes.
func wrapWords(text string, width int) []string {
	if width < 1 {
		width = 1
	}
	runes := []rune(text)
	if len(runes) == 0 {
		return []string{""}
	}
	if len(runes) <= width {
		return []string{text}
	}
	if !hasBreakableSpace(text) {
		return wrapHard(text, width)
	}

	var out []string
	start := 0
	i := 0
	lastSpace := -1
	for i <= len(runes) {
		if i < len(runes) && isBreakSpace(runes[i]) {
			lastSpace = i
		}
		lineLen := i - start
		if i == len(runes) || lineLen >= width {
			if lineLen >= width && lastSpace > start {
				out = append(out, string(runes[start:lastSpace]))
				start = lastSpace + 1
				for start < len(runes) && isBreakSpace(runes[start]) {
					start++
				}
				i = start
				lastSpace = -1
				continue
			}
			if i == len(runes) {
				if start < len(runes) {
					out = append(out, string(runes[start:]))
				}
				break
			}
			end := start + width
			if end > len(runes) {
				end = len(runes)
			}
			out = append(out, string(runes[start:end]))
			start = end
			for start < len(runes) && isBreakSpace(runes[start]) {
				start++
			}
			i = start
			lastSpace = -1
			continue
		}
		i++
	}
	return out
}

func wrapHard(text string, width int) []string {
	runes := []rune(text)
	if len(runes) <= width {
		return []string{text}
	}
	var out []string
	for len(runes) > width {
		out = append(out, string(runes[:width]))
		runes = runes[width:]
	}
	if len(runes) > 0 {
		out = append(out, string(runes))
	}
	return out
}

func hasBreakableSpace(text string) bool {
	for _, r := range text {
		if isBreakSpace(r) {
			return true
		}
	}
	return false
}

func isBreakSpace(r rune) bool {
	return r == ' ' || r == '\t'
}

// appendANSISequence copies one escape sequence at i in src into b; returns index after it.
func appendANSISequence(src string, i int, b *strings.Builder) int {
	if i >= len(src) || src[i] != 0x1b {
		return i
	}
	start := i
	i++
	if i < len(src) && src[i] == '[' {
		i++
		for i < len(src) && (src[i] < 0x40 || src[i] > 0x7e) {
			i++
		}
		if i < len(src) {
			i++
		}
		b.WriteString(src[start:i])
		return i
	}
	if i < len(src) && src[i] == ']' {
		i++
		for i < len(src) && src[i] != 0x07 {
			if src[i] == 0x1b && i+1 < len(src) && src[i+1] == '\\' {
				i += 2
				break
			}
			i++
		}
		if i < len(src) && src[i] == 0x07 {
			i++
		}
		b.WriteString(src[start:i])
		return i
	}
	if i < len(src) {
		i++
	}
	b.WriteString(src[start:i])
	return i
}

// skipANSISequence advances past one escape sequence at i; returns index after it.
func skipANSISequence(src string, i int) int {
	if i >= len(src) || src[i] != 0x1b {
		return i
	}
	i++
	if i < len(src) && src[i] == '[' {
		i++
		for i < len(src) && (src[i] < 0x40 || src[i] > 0x7e) {
			i++
		}
		if i < len(src) {
			i++
		}
		return i
	}
	if i < len(src) && src[i] == ']' {
		i++
		for i < len(src) && src[i] != 0x07 {
			if src[i] == 0x1b && i+1 < len(src) && src[i+1] == '\\' {
				return i + 2
			}
			i++
		}
		if i < len(src) && src[i] == 0x07 {
			i++
		}
		return i
	}
	if i < len(src) {
		i++
	}
	return i
}

// extractPlainPrefix returns the prefix of src containing n visible (non-ANSI) runes.
func extractPlainPrefix(src string, n int) (segment, rest string) {
	if n <= 0 {
		return "", src
	}
	var b strings.Builder
	vis := 0
	i := 0
	for i < len(src) && vis < n {
		if src[i] == 0x1b {
			i = appendANSISequence(src, i, &b)
			continue
		}
		_, size := utf8.DecodeRuneInString(src[i:])
		b.WriteString(src[i : i+size])
		vis++
		i += size
	}
	return b.String(), src[i:]
}

// skipLeadingPlainWS advances past leading visible whitespace in an ANSI string.
func skipLeadingPlainWS(src string) string {
	i := 0
	for i < len(src) {
		if src[i] == 0x1b {
			i = skipANSISequence(src, i)
			continue
		}
		r, size := utf8.DecodeRuneInString(src[i:])
		if !isBreakSpace(r) {
			break
		}
		i += size
	}
	return src[i:]
}