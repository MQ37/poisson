package tui

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// mdSegment is one prose or fenced-code chunk from assistant markdown.
type mdSegment struct {
	code bool
	lang string
	text string
}

// splitFenceSegments splits markdown on triple-backtick fences.
func splitFenceSegments(src string) []mdSegment {
	src = strings.ReplaceAll(src, "\r\n", "\n")
	if !strings.Contains(src, "```") {
		return []mdSegment{{text: src}}
	}
	var out []mdSegment
	rest := src
	for {
		i := strings.Index(rest, "```")
		if i < 0 {
			if rest != "" {
				out = append(out, mdSegment{text: rest})
			}
			break
		}
		if i > 0 {
			out = append(out, mdSegment{text: rest[:i]})
		}
		rest = rest[i+3:]
		lang := ""
		if nl := strings.Index(rest, "\n"); nl >= 0 {
			lang = strings.TrimSpace(rest[:nl])
			rest = rest[nl+1:]
		} else {
			// Unclosed fence opener — treat remainder as plain text.
			out = append(out, mdSegment{text: "```" + rest})
			break
		}
		close := strings.Index(rest, "```")
		if close < 0 {
			out = append(out, mdSegment{code: true, lang: lang, text: strings.TrimSuffix(rest, "\n")})
			break
		}
		code := rest[:close]
		if strings.HasSuffix(code, "\n") {
			code = code[:len(code)-1]
		}
		out = append(out, mdSegment{code: true, lang: lang, text: code})
		rest = rest[close+3:]
		if strings.HasPrefix(rest, "\n") {
			rest = rest[1:]
		}
	}
	if len(out) == 0 {
		return []mdSegment{{text: src}}
	}
	return out
}

// layoutRichMarkdown renders prose + fenced code blocks for assistant/thinking.
func layoutRichMarkdown(raw string, width int, prefix string) []string {
	segments := splitFenceSegments(raw)
	var out []string
	first := true
	for _, seg := range segments {
		if seg.code {
			p := ""
			if first {
				p = prefix
			}
			out = append(out, renderCodeBlock(seg.lang, seg.text, width, p)...)
			first = false
			continue
		}
		p := ""
		if first {
			p = prefix
		}
		lines := renderMarkdown(seg.text, width, p)
		out = append(out, lines...)
		if len(lines) > 0 {
			first = false
		}
	}
	if len(out) == 0 {
		return []string{prefix + reset}
	}
	return out
}

// renderCodeBlock draws a bordered, optionally highlighted code block.
func renderCodeBlock(lang, code string, width int, prefix string) []string {
	if width < 8 {
		width = 8
	}
	inner := width - 4 // │ + space + content + space + │
	if inner < 1 {
		inner = 1
	}
	label := strings.TrimSpace(lang)
	if label == "" {
		label = "code"
	}
	top := boxTop(label, width)
	bottom := boxBottom(width)
	highlighted := highlightCode(label, code)
	srcLines := strings.Split(highlighted, "\n")
	if len(srcLines) == 1 && srcLines[0] == "" {
		srcLines = nil
	}
	var out []string
	if prefix != "" {
		top = prefix + top
	}
	out = append(out, top+reset)
	for _, ln := range srcLines {
		for _, chunk := range wrapANSI(ln, inner) {
			out = append(out, boxSide(chunk, inner)+reset)
		}
	}
	out = append(out, bottom+reset)
	return out
}

func boxTop(lang string, width int) string {
	title := "─ " + lang + " "
	fill := width - visibleWidth("╭"+title+"╮")
	if fill < 0 {
		fill = 0
	}
	return fgGray + "╭" + title + strings.Repeat("─", fill) + "╮"
}

func boxBottom(width int) string {
	fill := width - 2
	if fill < 0 {
		fill = 0
	}
	return fgGray + "╰" + strings.Repeat("─", fill) + "╯"
}

func boxSide(content string, inner int) string {
	pad := inner - visibleWidth(content)
	if pad < 0 {
		content = truncateToWidth(content, inner)
		pad = inner - visibleWidth(content)
	}
	if pad < 0 {
		pad = 0
	}
	return fgGray + "│ " + reset + content + strings.Repeat(" ", pad) + fgGray + " │"
}

// highlightCode applies lightweight keyword highlighting for common langs.
func highlightCode(lang, src string) string {
	lang = normalizeLang(lang)
	lines := strings.Split(src, "\n")
	for i, ln := range lines {
		lines[i] = highlightLine(lang, ln)
	}
	return strings.Join(lines, "\n")
}

func normalizeLang(lang string) string {
	lang = strings.ToLower(strings.TrimSpace(lang))
	switch lang {
	case "js":
		return "javascript"
	case "ts":
		return "typescript"
	case "sh", "shell", "zsh":
		return "bash"
	case "yml":
		return "yaml"
	case "py":
		return "python"
	default:
		return lang
	}
}

func highlightLine(lang, line string) string {
	if lang == "text" || lang == "plaintext" || lang == "" {
		return fgYellow + line + reset
	}
	if lang == "json" {
		return highlightJSON(line)
	}
	keywords := langKeywords[lang]
	if len(keywords) == 0 {
		return fgYellow + line + reset
	}
	return scanHighlight(line, keywords, langComment(lang))
}

func langComment(lang string) string {
	switch lang {
	case "bash", "python", "yaml":
		return "#"
	case "go", "javascript", "typescript":
		return "//"
	default:
		return ""
	}
}

func scanHighlight(line string, keywords []string, commentPrefix string) string {
	if commentPrefix != "" {
		if idx := strings.Index(line, commentPrefix); idx >= 0 {
			head := scanHighlight(line[:idx], keywords, "")
			tail := dim + line[idx:] + reset
			return head + tail
		}
	}
	var b strings.Builder
	i := 0
	for i < len(line) {
		if line[i] == '"' || line[i] == '\'' || line[i] == '`' {
			end := findStringEnd(line, i)
			b.WriteString(fgMagenta)
			b.WriteString(line[i:end])
			b.WriteString(reset)
			i = end
			continue
		}
		if i+1 < len(line) && (line[i] == '0' && (line[i+1] == 'x' || line[i+1] == 'X')) {
			j := i + 2
			for j < len(line) && ((line[j] >= '0' && line[j] <= '9') ||
				(line[j] >= 'a' && line[j] <= 'f') || (line[j] >= 'A' && line[j] <= 'F')) {
				j++
			}
			if j > i+2 {
				b.WriteString(fgCyan)
				b.WriteString(line[i:j])
				b.WriteString(reset)
				i = j
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(line[i:])
		if unicode.IsLetter(r) || r == '_' {
			j := i + size
			for j < len(line) {
				r2, sz := utf8.DecodeRuneInString(line[j:])
				if !unicode.IsLetter(r2) && !unicode.IsDigit(r2) && r2 != '_' {
					break
				}
				j += sz
			}
			word := line[i:j]
			if isKeyword(word, keywords) {
				b.WriteString(fgCyan + bold)
				b.WriteString(word)
				b.WriteString(reset)
			} else {
				b.WriteString(fgYellow)
				b.WriteString(word)
				b.WriteString(reset)
			}
			i = j
			continue
		}
		b.WriteString(fgYellow)
		b.WriteByte(line[i])
		b.WriteString(reset)
		i++
	}
	return b.String()
}

func findStringEnd(s string, start int) int {
	quote := s[start]
	escaped := false
	for i := start + 1; i < len(s); i++ {
		if escaped {
			escaped = false
			continue
		}
		if s[i] == '\\' {
			escaped = true
			continue
		}
		if s[i] == quote {
			return i + 1
		}
	}
	return len(s)
}

func isKeyword(word string, keywords []string) bool {
	for _, kw := range keywords {
		if word == kw {
			return true
		}
	}
	return false
}

func highlightJSON(line string) string {
	trim := strings.TrimSpace(line)
	if strings.HasPrefix(trim, "//") || strings.HasPrefix(trim, "#") {
		return dim + line + reset
	}
	var b strings.Builder
	i := 0
	for i < len(line) {
		c := line[i]
		switch c {
		case '"':
			end := findStringEnd(line, i)
			b.WriteString(fgMagenta)
			b.WriteString(line[i:end])
			b.WriteString(reset)
			i = end
		default:
			if c == '-' || (c >= '0' && c <= '9') {
				j := i + 1
				for j < len(line) && (line[j] == '.' || line[j] == '-' ||
					(line[j] >= '0' && line[j] <= '9') || line[j] == 'e' || line[j] == 'E') {
					j++
				}
				b.WriteString(fgCyan)
				b.WriteString(line[i:j])
				b.WriteString(reset)
				i = j
				continue
			}
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
				j := i + 1
				for j < len(line) && ((line[j] >= 'a' && line[j] <= 'z') ||
					(line[j] >= 'A' && line[j] <= 'Z')) {
					j++
				}
				word := line[i:j]
				if word == "true" || word == "false" || word == "null" {
					b.WriteString(fgCyan + bold)
					b.WriteString(word)
					b.WriteString(reset)
				} else {
					b.WriteString(fgYellow)
					b.WriteString(word)
					b.WriteString(reset)
				}
				i = j
				continue
			}
			b.WriteString(fgYellow)
			b.WriteByte(c)
			b.WriteString(reset)
			i++
		}
	}
	return b.String()
}

var langKeywords = map[string][]string{
	"go": {
		"func", "return", "if", "else", "for", "range", "var", "const", "type",
		"struct", "interface", "package", "import", "string", "int", "bool",
		"nil", "error", "map", "chan", "go", "defer", "select", "switch",
		"case", "default", "break", "continue",
	},
	"python": {
		"def", "return", "if", "elif", "else", "for", "while", "import",
		"from", "class", "True", "False", "None", "and", "or", "not", "in",
		"lambda", "with", "as", "try", "except", "finally", "raise", "pass",
	},
	"javascript": {
		"function", "return", "if", "else", "for", "while", "const", "let",
		"var", "class", "import", "export", "from", "async", "await", "new",
		"true", "false", "null", "undefined", "typeof", "switch", "case",
	},
	"typescript": {
		"function", "return", "if", "else", "for", "while", "const", "let",
		"var", "class", "import", "export", "from", "async", "await", "new",
		"true", "false", "null", "undefined", "typeof", "switch", "case",
		"interface", "type", "enum", "implements", "extends",
	},
	"bash": {
		"if", "then", "else", "elif", "fi", "for", "do", "done", "while",
		"case", "esac", "function", "export", "local", "return", "echo",
	},
	"yaml": {"true", "false", "null"},
	"markdown": {},
}