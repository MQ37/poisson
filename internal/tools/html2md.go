package tools

import (
	"fmt"
	"strconv"
	"strings"
)

// This file hand-rolls an HTML tokenizer and a streaming HTML→Markdown
// converter for FetchTool's non-Ollama path, using only the standard
// library — no third-party HTML/markdown package. It targets "good enough
// for a model to read the page's content", not a spec-compliant HTML5
// parser or a lossless Markdown round-trip.

// htmlToken is one item from tokenizeHTML: an open/self-closing tag, a
// close tag, or a run of raw text between tags.
type htmlToken struct {
	kind  string // "open", "selfclose", "close", "text"
	name  string
	attrs map[string]string
	text  string
}

// voidElements never have a closing tag (HTML5 void elements).
var voidElements = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true,
	"hr": true, "img": true, "input": true, "link": true, "meta": true,
	"param": true, "source": true, "track": true, "wbr": true,
}

// tokenizeHTML scans raw HTML into a flat token stream. script/style bodies
// are "raw text" per the HTML spec (they may contain unescaped < and >, e.g.
// `if (a < b)`), so their content is consumed and discarded here rather than
// fed back through the generic tag scanner, which would otherwise misread
// stray angle brackets as tags and desync the rest of the parse.
func tokenizeHTML(s string) []htmlToken {
	var toks []htmlToken
	n := len(s)
	i := 0
	for i < n {
		if s[i] != '<' {
			j := strings.IndexByte(s[i:], '<')
			if j < 0 {
				toks = append(toks, htmlToken{kind: "text", text: s[i:]})
				break
			}
			toks = append(toks, htmlToken{kind: "text", text: s[i : i+j]})
			i += j
			continue
		}
		if strings.HasPrefix(s[i:], "<!--") {
			end := strings.Index(s[i+4:], "-->")
			if end < 0 {
				break
			}
			i += 4 + end + 3
			continue
		}
		if strings.HasPrefix(s[i:], "<!") || strings.HasPrefix(s[i:], "<?") {
			end := strings.IndexByte(s[i:], '>')
			if end < 0 {
				break
			}
			i += end + 1
			continue
		}
		if i+1 < n && s[i+1] == '/' {
			end := strings.IndexByte(s[i:], '>')
			if end < 0 {
				break
			}
			name := strings.ToLower(strings.TrimSpace(s[i+2 : i+end]))
			toks = append(toks, htmlToken{kind: "close", name: name})
			i += end + 1
			continue
		}
		end := indexTagEnd(s, i)
		if end < 0 {
			break
		}
		raw := strings.TrimSpace(s[i+1 : end])
		selfClose := strings.HasSuffix(raw, "/")
		raw = strings.TrimSuffix(raw, "/")
		name, attrs := parseTagNameAttrs(raw)
		if name == "" {
			i = end + 1
			continue
		}
		if name == "script" || name == "style" {
			closeTag := "</" + name
			rest := s[end+1:]
			idx := indexFoldCase(rest, closeTag)
			if idx < 0 {
				i = n
			} else {
				gt := strings.IndexByte(rest[idx:], '>')
				if gt < 0 {
					i = n
				} else {
					i = end + 1 + idx + gt + 1
				}
			}
			continue
		}
		if selfClose || voidElements[name] {
			toks = append(toks, htmlToken{kind: "selfclose", name: name, attrs: attrs})
		} else {
			toks = append(toks, htmlToken{kind: "open", name: name, attrs: attrs})
		}
		i = end + 1
	}
	return toks
}

// indexTagEnd returns the index of the '>' that closes the tag starting at
// s[start] ('<'), ignoring '>' inside a quoted attribute value (e.g.
// <a title="a > b">).
func indexTagEnd(s string, start int) int {
	var inQuote byte
	for k := start + 1; k < len(s); k++ {
		c := s[k]
		if inQuote != 0 {
			if c == inQuote {
				inQuote = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			inQuote = c
		case '>':
			return k
		}
	}
	return -1
}

// indexFoldCase is a case-insensitive strings.Index for the small ASCII tag
// names this file looks for (script/style).
func indexFoldCase(s, substr string) int {
	ls, lsub := strings.ToLower(s), strings.ToLower(substr)
	return strings.Index(ls, lsub)
}

func isHTMLSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\f'
}

// parseTagNameAttrs splits raw tag content (everything between '<' and the
// closing '>', minus a trailing '/') into a lowercased tag name and its
// attributes.
func parseTagNameAttrs(raw string) (string, map[string]string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	i := 0
	for i < len(raw) && !isHTMLSpace(raw[i]) {
		i++
	}
	name := strings.ToLower(raw[:i])
	rest := raw[i:]
	var attrs map[string]string
	for {
		for len(rest) > 0 && isHTMLSpace(rest[0]) {
			rest = rest[1:]
		}
		if rest == "" {
			break
		}
		j := 0
		for j < len(rest) && rest[j] != '=' && !isHTMLSpace(rest[j]) {
			j++
		}
		if j == 0 {
			break
		}
		attrName := strings.ToLower(rest[:j])
		rest = rest[j:]
		for len(rest) > 0 && isHTMLSpace(rest[0]) {
			rest = rest[1:]
		}
		val := ""
		if strings.HasPrefix(rest, "=") {
			rest = rest[1:]
			for len(rest) > 0 && isHTMLSpace(rest[0]) {
				rest = rest[1:]
			}
			if len(rest) > 0 && (rest[0] == '"' || rest[0] == '\'') {
				q := rest[0]
				end := strings.IndexByte(rest[1:], q)
				if end < 0 {
					val = rest[1:]
					rest = ""
				} else {
					val = rest[1 : 1+end]
					rest = rest[1+end+1:]
				}
			} else {
				k := 0
				for k < len(rest) && !isHTMLSpace(rest[k]) {
					k++
				}
				val = rest[:k]
				rest = rest[k:]
			}
		}
		if attrs == nil {
			attrs = make(map[string]string)
		}
		attrs[attrName] = val
	}
	return name, attrs
}

// htmlEntities covers the common named entities seen in real pages; anything
// else falls through unresolved (left as literal "&name;" text).
var htmlEntities = map[string]string{
	"amp": "&", "lt": "<", "gt": ">", "quot": "\"", "apos": "'", "nbsp": " ",
	"mdash": "\u2014", "ndash": "\u2013", "hellip": "\u2026",
	"copy": "\u00a9", "reg": "\u00ae", "trade": "\u2122",
	"lsquo": "\u2018", "rsquo": "\u2019", "ldquo": "\u201c", "rdquo": "\u201d",
	"laquo": "\u00ab", "raquo": "\u00bb", "middot": "\u00b7", "bull": "\u2022",
	"dagger": "\u2020", "sect": "\u00a7", "para": "\u00b6",
	"deg": "\u00b0", "plusmn": "\u00b1", "times": "\u00d7", "divide": "\u00f7",
	"euro": "\u20ac", "pound": "\u00a3", "yen": "\u00a5", "cent": "\u00a2",
}

// decodeEntities resolves &name; / &#NN; / &#xHH; references.
func decodeEntities(s string) string {
	if !strings.Contains(s, "&") {
		return s
	}
	var b strings.Builder
	i := 0
	for i < len(s) {
		if s[i] != '&' {
			b.WriteByte(s[i])
			i++
			continue
		}
		end := strings.IndexByte(s[i:], ';')
		if end <= 0 || end > 12 {
			b.WriteByte(s[i])
			i++
			continue
		}
		ent := s[i+1 : i+end]
		switch {
		case strings.HasPrefix(ent, "#x") || strings.HasPrefix(ent, "#X"):
			if v, err := strconv.ParseInt(ent[2:], 16, 32); err == nil {
				b.WriteRune(rune(v))
				i += end + 1
				continue
			}
		case strings.HasPrefix(ent, "#"):
			if v, err := strconv.ParseInt(ent[1:], 10, 32); err == nil {
				b.WriteRune(rune(v))
				i += end + 1
				continue
			}
		default:
			if repl, ok := htmlEntities[ent]; ok {
				b.WriteString(repl)
				i += end + 1
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// collapseWhitespace applies HTML's whitespace-collapsing rule (any run of
// spaces/tabs/newlines becomes one space); callers skip this inside <pre>.
func collapseWhitespace(s string) string {
	var b strings.Builder
	lastSpace := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isHTMLSpace(c) {
			if !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
			continue
		}
		b.WriteByte(c)
		lastSpace = false
	}
	return b.String()
}

func collapseBlankLines(s string) string {
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return s
}

// skipTextTags are containers whose own markup is walked (to stay in sync
// with the tag stream) but whose text content is never emitted.
var skipTextTags = map[string]bool{
	"head": true, "noscript": true, "template": true, "svg": true,
}

// blockTags just need blank-line separation before/after; they carry no
// markdown syntax of their own.
var blockTags = map[string]bool{
	"p": true, "div": true, "section": true, "article": true,
	"main": true, "header": true, "footer": true, "nav": true, "aside": true,
}

type listCtx struct {
	ordered bool
	n       int
}

// fenceLangFromClass finds a "language-X" / "lang-X" token in a class
// attribute (the convention Docusaurus/Prism/highlight.js all use to mark
// which language a code block was highlighted as) and returns "X", or "" if
// none is present.
func fenceLangFromClass(class string) string {
	for _, tok := range strings.Fields(class) {
		if v, ok := strings.CutPrefix(tok, "language-"); ok {
			return v
		}
		if v, ok := strings.CutPrefix(tok, "lang-"); ok {
			return v
		}
	}
	return ""
}

// htmlToMarkdown converts an HTML document (or fragment) to a best-effort
// Markdown rendering — headings, paragraphs, bold/italic/code, links,
// images, lists, blockquotes, fenced code blocks, horizontal rules, and
// simple table rows. Never errors: malformed/unclosed HTML degrades to
// whatever could be recovered rather than failing the fetch.
func htmlToMarkdown(src string) string {
	toks := tokenizeHTML(src)

	stack := []*strings.Builder{{}}
	cur := func() *strings.Builder { return stack[len(stack)-1] }
	push := func() { stack = append(stack, &strings.Builder{}) }
	pop := func() string {
		if len(stack) <= 1 {
			// An orphan closing tag (e.g. a stray "</a>" with no matching
			// open — common in sloppy real-world HTML) reaches here with
			// nothing of its own ever pushed. Popping the last remaining
			// builder would empty the stack entirely and panic the next
			// cur() call (index out of range). Degrade to "no content"
			// instead, per this function's own doc comment: malformed
			// HTML should never fail the fetch.
			return ""
		}
		s := stack[len(stack)-1].String()
		stack = stack[:len(stack)-1]
		return s
	}

	var hrefs []string
	var lists []*listCtx
	skipDepth := 0
	preDepth := 0

	blockBreak := func() {
		trimmed := strings.TrimRight(cur().String(), " \t\n")
		b := cur()
		b.Reset()
		if trimmed == "" {
			return
		}
		b.WriteString(trimmed)
		b.WriteString("\n\n")
	}

	writeText := func(raw string) {
		if skipDepth > 0 {
			return
		}
		if preDepth > 0 {
			cur().WriteString(decodeEntities(raw))
			return
		}
		txt := decodeEntities(collapseWhitespace(raw))
		if strings.TrimSpace(txt) == "" {
			return
		}
		cur().WriteString(txt)
	}

	for _, tok := range toks {
		switch tok.kind {
		case "text":
			writeText(tok.text)

		case "open", "selfclose":
			name := tok.name
			switch {
			case skipTextTags[name]:
				if tok.kind == "open" {
					skipDepth++
				}
			case name == "h1" || name == "h2" || name == "h3" || name == "h4" || name == "h5" || name == "h6":
				blockBreak()
				cur().WriteString(strings.Repeat("#", int(name[1]-'0')) + " ")
			case blockTags[name]:
				blockBreak()
			case name == "br":
				cur().WriteString("  \n")
			case name == "hr":
				blockBreak()
				cur().WriteString("---\n\n")
			case name == "pre":
				blockBreak()
				preDepth++
				cur().WriteString("```" + fenceLangFromClass(tok.attrs["class"]) + "\n")
			case name == "blockquote":
				blockBreak()
				push()
			case name == "ul":
				blockBreak()
				lists = append(lists, &listCtx{ordered: false})
			case name == "ol":
				blockBreak()
				lists = append(lists, &listCtx{ordered: true})
			case name == "li":
				str := strings.TrimRight(cur().String(), " \t")
				if str != "" && !strings.HasSuffix(str, "\n") {
					str += "\n"
				}
				b := cur()
				b.Reset()
				b.WriteString(str)
				indent := strings.Repeat("  ", max(0, len(lists)-1))
				if len(lists) > 0 && lists[len(lists)-1].ordered {
					lists[len(lists)-1].n++
					b.WriteString(fmt.Sprintf("%s%d. ", indent, lists[len(lists)-1].n))
				} else {
					b.WriteString(indent + "- ")
				}
			case name == "table":
				blockBreak()
			case name == "tr" || name == "td" || name == "th":
				push()
			case name == "a":
				hrefs = append(hrefs, tok.attrs["href"])
				push()
			case name == "img":
				if src := tok.attrs["src"]; src != "" {
					cur().WriteString(fmt.Sprintf("![%s](%s)", tok.attrs["alt"], src))
				}
			case name == "strong" || name == "b" || name == "em" || name == "i" || (name == "code" && preDepth == 0):
				push()
			}

		case "close":
			name := tok.name
			switch {
			case skipTextTags[name]:
				if skipDepth > 0 {
					skipDepth--
				}
			case name == "h1" || name == "h2" || name == "h3" || name == "h4" || name == "h5" || name == "h6":
				cur().WriteString("\n\n")
			case blockTags[name]:
				str := strings.TrimRight(cur().String(), " \t")
				b := cur()
				b.Reset()
				b.WriteString(str)
				b.WriteString("\n\n")
			case name == "pre":
				preDepth--
				str := cur().String()
				if str != "" && !strings.HasSuffix(str, "\n") {
					str += "\n"
				}
				b := cur()
				b.Reset()
				b.WriteString(str)
				b.WriteString("```\n\n")
			case name == "blockquote":
				content := strings.TrimRight(pop(), "\n")
				for _, ln := range strings.Split(content, "\n") {
					cur().WriteString("> " + ln + "\n")
				}
				cur().WriteString("\n")
			case name == "ul" || name == "ol":
				if len(lists) > 0 {
					lists = lists[:len(lists)-1]
				}
				cur().WriteString("\n")
			case name == "li":
				str := cur().String()
				if str != "" && !strings.HasSuffix(str, "\n") {
					cur().WriteString("\n")
				}
			case name == "table":
				cur().WriteString("\n")
			case name == "tr":
				content := pop()
				if strings.TrimSpace(content) != "" {
					cur().WriteString(content + "|\n")
				}
			case name == "td" || name == "th":
				content := strings.TrimSpace(pop())
				cur().WriteString("| " + content + " ")
			case name == "a":
				text := strings.TrimSpace(pop())
				href := ""
				if len(hrefs) > 0 {
					href = hrefs[len(hrefs)-1]
					hrefs = hrefs[:len(hrefs)-1]
				}
				switch {
				case text == "":
				case href == "":
					cur().WriteString(text)
				default:
					cur().WriteString(fmt.Sprintf("[%s](%s)", text, href))
				}
			case name == "strong" || name == "b":
				if text := strings.TrimSpace(pop()); text != "" {
					cur().WriteString("**" + text + "**")
				}
			case name == "em" || name == "i":
				if text := strings.TrimSpace(pop()); text != "" {
					cur().WriteString("*" + text + "*")
				}
			case name == "code" && preDepth == 0:
				if text := strings.TrimSpace(pop()); text != "" {
					cur().WriteString("`" + text + "`")
				}
			}
		}
	}

	// Malformed/unclosed tags can leave extra buffers pushed — flatten
	// whatever's left onto the root instead of losing it.
	for len(stack) > 1 {
		s := pop()
		cur().WriteString(s)
	}

	result := collapseBlankLines(cur().String())
	result = strings.TrimSpace(result)
	if result == "" {
		return result
	}
	return result + "\n"
}
