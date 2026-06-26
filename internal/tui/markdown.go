package tui

import (
	"strings"
	"unicode/utf8"
)

// renderMarkdown turns markdown source into hard-wrapped ANSI lines at width.
// Supports a small LLM-oriented subset: headers, bullets, bold/italic/code/
// strike, and [text](url) links.
func renderMarkdown(src string, width int, basePrefix string) []string {
	if width < 1 {
		width = 1
	}
	src = strings.ReplaceAll(src, "\r\n", "\n")
	lines := strings.Split(src, "\n")
	var out []string
	for _, ln := range lines {
		styled := styleMarkdownLine(strings.TrimRight(ln, " \t"))
		chunks := wrapANSI(styled, width)
		for i, chunk := range chunks {
			prefix := basePrefix
			if i > 0 {
				prefix = ""
			}
			out = append(out, prefix+chunk+reset)
		}
	}
	if len(out) == 0 {
		return []string{basePrefix + reset}
	}
	return out
}

func styleMarkdownLine(line string) string {
	trimmed := strings.TrimSpace(line)
	switch {
	case strings.HasPrefix(trimmed, "### "):
		return bold + fgCyan + trimmed[4:] + reset
	case strings.HasPrefix(trimmed, "## "):
		return bold + fgCyan + trimmed[3:] + reset
	case strings.HasPrefix(trimmed, "# "):
		return bold + fgCyan + trimmed[2:] + reset
	case strings.HasPrefix(trimmed, "- "):
		return dim + "• " + reset + renderInline(trimmed[2:])
	case strings.HasPrefix(trimmed, "* "):
		return dim + "• " + reset + renderInline(trimmed[2:])
	default:
		return renderInline(line)
	}
}

// renderInline applies inline markdown spans to a single line.
func renderInline(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		switch {
		case strings.HasPrefix(s[i:], "**"):
			if end := strings.Index(s[i+2:], "**"); end >= 0 {
				b.WriteString(bold)
				b.WriteString(s[i+2 : i+2+end])
				b.WriteString(reset)
				i += 4 + end
				continue
			}
		case strings.HasPrefix(s[i:], "~~"):
			if end := strings.Index(s[i+2:], "~~"); end >= 0 {
				b.WriteString(dim)
				b.WriteString(s[i+2 : i+2+end])
				b.WriteString(reset)
				i += 4 + end
				continue
			}
		case s[i] == '`':
			if end := strings.Index(s[i+1:], "`"); end >= 0 {
				b.WriteString(fgYellow)
				b.WriteString(s[i+1 : i+1+end])
				b.WriteString(reset)
				i += 2 + end
				continue
			}
		case s[i] == '*':
			if end := strings.Index(s[i+1:], "*"); end >= 0 && !strings.HasPrefix(s[i+1:], "*") {
				b.WriteString(italic)
				b.WriteString(s[i+1 : i+1+end])
				b.WriteString(reset)
				i += 2 + end
				continue
			}
		case s[i] == '[':
			if close := strings.Index(s[i:], "]"); close > 1 {
				label := s[i+1 : i+close]
				rest := s[i+close+1:]
				if strings.HasPrefix(rest, "(") {
					if paren := strings.Index(rest, ")"); paren > 1 {
						url := rest[1:paren]
						b.WriteString(underline + fgBlue)
						b.WriteString(label)
						b.WriteString(reset)
						b.WriteString(dim + " (" + url + ")" + reset)
						i += close + 1 + paren + 1
						continue
					}
				}
			}
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		b.WriteString(s[i : i+size])
		i += size
	}
	return b.String()
}

// wrapANSI hard-wraps an ANSI-bearing string to width visible columns.
func wrapANSI(src string, width int) []string {
	if width < 1 {
		width = 1
	}
	plain := stripANSI(src)
	if utf8.RuneCountInString(plain) <= width {
		return []string{src}
	}
	var out []string
	var chunk strings.Builder
	vis := 0
	inEsc := false
	i := 0
	flush := func() {
		if chunk.Len() > 0 {
			out = append(out, chunk.String())
			chunk.Reset()
			vis = 0
		}
	}
	for i < len(src) {
		if src[i] == 0x1b {
			inEsc = true
			chunk.WriteByte(src[i])
			i++
			continue
		}
		if inEsc {
			chunk.WriteByte(src[i])
			if src[i] >= 0x40 && src[i] <= 0x7e {
				inEsc = false
			}
			i++
			continue
		}
		_, size := utf8.DecodeRuneInString(src[i:])
		if vis >= width {
			flush()
		}
		chunk.WriteString(src[i : i+size])
		vis++
		i += size
	}
	flush()
	if len(out) == 0 {
		return []string{src}
	}
	return out
}