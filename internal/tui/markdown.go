package tui

import (
	"strings"
	"unicode/utf8"
)

// renderMarkdown turns markdown source into hard-wrapped ANSI lines at width.
// Supports headers, bullets, bold/italic/code/strike, links, and GFM tables.
func renderMarkdown(src string, width int, basePrefix string) []string {
	if width < 1 {
		width = 1
	}
	src = strings.ReplaceAll(src, "\r\n", "\n")
	lines := strings.Split(src, "\n")
	var out []string
	first := true
	for i := 0; i < len(lines); i++ {
		if end := tableBlockEnd(lines, i); end > i+1 {
			prefix := ""
			if first {
				prefix = basePrefix
				first = false
			}
			out = append(out, renderMarkdownTable(lines[i:end], width, prefix)...)
			i = end - 1
			continue
		}
		ln := lines[i]
		styled := styleMarkdownLine(strings.TrimRight(ln, " \t"))
		chunks := wrapANSI(styled, width)
		for j, chunk := range chunks {
			prefix := ""
			if first && j == 0 {
				prefix = basePrefix
				first = false
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

// wrapANSI wraps an ANSI-bearing string to width visible columns, breaking at
// word boundaries when possible while preserving escape sequences.
func wrapANSI(src string, width int) []string {
	if width < 1 {
		width = 1
	}
	plain := stripANSI(src)
	if utf8.RuneCountInString(plain) <= width {
		return []string{src}
	}
	plainLines := wrapWords(plain, width)
	var out []string
	rest := src
	for _, pl := range plainLines {
		if pl == "" {
			continue
		}
		rest = skipLeadingPlainWS(rest)
		var seg string
		seg, rest = extractPlainPrefix(rest, utf8.RuneCountInString(pl))
		if seg != "" {
			out = append(out, seg)
		}
	}
	if len(out) == 0 {
		return []string{src}
	}
	return out
}
