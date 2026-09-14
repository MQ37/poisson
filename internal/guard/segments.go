package guard

import "strings"

// Segments splits a bash command string into individual command segments
// at the following separators: ;, newline, |, &&, ||, |(), &().
//
// Quoted strings (single and double) are not split.
// Parentheses nesting is tracked so that |() / &() subshell separators are
// only recognized at the top level.
//
// A segment that, once trimmed, is entirely wrapped by one top-level
// "(...)" subshell or "{ ...; }" group is recursively flattened into its
// own inner segments rather than returned as one opaque blob — real bash
// runs a group's contents as ordinary commands, just scoped, so
// "(rm -rf x)" and "{ echo hi; rm -rf x; }" must be exactly as visible to
// every per-command detector as their unwrapped equivalents. Without this,
// the group's first "token" is a bare grouping character no detector
// recognizes as a command name, and any statement after the first inside a
// multi-statement "(...)" group (parens are depth-tracked, so its internal
// ";"/"&&"/"||" never reaches the top level on their own) is never even
// looked at.
func Segments(cmd string) []string {
	var out []string
	for _, seg := range rawSegments(cmd) {
		out = append(out, flattenGroup(seg)...)
	}
	return out
}

// flattenGroup recursively unwraps seg if it is entirely one top-level
// "(...)" or "{...}" group, returning its interior's own segments. Returns
// seg unchanged (as a one-element slice) if it isn't such a group.
func flattenGroup(seg string) []string {
	t := strings.TrimSpace(seg)
	if len(t) < 2 || (t[0] != '(' && t[0] != '{') {
		return []string{seg}
	}
	if !closesAtEnd(t) {
		return []string{seg}
	}
	inner := strings.TrimSpace(t[1 : len(t)-1])
	if inner == "" {
		return nil
	}
	return Segments(inner)
}

// closesAtEnd reports whether t — which starts with '(' or '{' — has its
// balancing close at the very last byte, i.e. t is one fully-enclosing
// group rather than a group followed by trailing text or two adjacent
// groups. Assumes well-formed (balanced) input; malformed input just
// returns false, leaving the segment unflattened (fails safe — it's still
// scanned as an opaque blob exactly as before, never auto-approved).
func closesAtEnd(t string) bool {
	var wantClose byte
	switch t[0] {
	case '(':
		wantClose = ')'
	case '{':
		wantClose = '}'
	default:
		return false
	}
	depth := 0
	inSingle, inDouble := false, false
	for i := 0; i < len(t); i++ {
		c := t[i]
		switch {
		case inSingle:
			if c == '\'' {
				inSingle = false
			}
		case inDouble:
			if c == '\\' && i+1 < len(t) {
				i++
			} else if c == '"' {
				inDouble = false
			}
		case c == '\'':
			inSingle = true
		case c == '"':
			inDouble = true
		case c == '(' || c == '{':
			depth++
		case c == ')' || c == '}':
			depth--
			if depth < 0 {
				return false
			}
			if depth == 0 {
				return i == len(t)-1 && c == wantClose
			}
		}
	}
	return false
}

// rawSegments is Segments' top-level splitter, before group-flattening.
func rawSegments(cmd string) []string {
	var segs []string
	var cur strings.Builder
	i := 0
	n := len(cmd)
	depth := 0 // parenthesis nesting
	// pendingHeredocs are "<<WORD" markers seen on the line currently being
	// scanned, queued in the order they appeared — consumed (and dropped,
	// never split into their own segments) the moment the line's own
	// terminating newline is reached. See detectHeredocStart/skipHeredocBodies.
	var pendingHeredocs []heredocDelim

	for i < n {
		c := cmd[i]

		// Handle quoted strings — copy verbatim, don't split.
		if c == '\'' {
			cur.WriteByte(c)
			i++
			for i < n {
				cur.WriteByte(cmd[i])
				if cmd[i] == '\'' {
					i++
					break
				}
				i++
			}
			continue
		}
		if c == '"' {
			cur.WriteByte(c)
			i++
			for i < n {
				cur.WriteByte(cmd[i])
				if cmd[i] == '\\' && i+1 < n {
					// escape next char inside double quotes
					cur.WriteByte(cmd[i+1])
					i += 2
					continue
				}
				if cmd[i] == '"' {
					i++
					break
				}
				i++
			}
			continue
		}

		// Track parentheses.
		if c == '(' {
			depth++
			cur.WriteByte(c)
			i++
			continue
		}
		if c == ')' {
			if depth > 0 {
				depth--
			}
			cur.WriteByte(c)
			i++
			continue
		}

		// Only split at top level (depth == 0).
		if depth == 0 {
			// Heredoc redirect ("<<WORD", "<<-WORD", quoted or bare) — record
			// the delimiter so the newline handler below skips its body
			// instead of splitting it into fake top-level segments. The
			// operator text itself is left untouched here and copied into cur
			// normally by the per-character fallthrough at the bottom of this
			// loop, same as any other ordinary text.
			if c == '<' {
				if d, ok := detectHeredocStart(cmd, i); ok {
					pendingHeredocs = append(pendingHeredocs, d)
				}
			}
			// && separator
			if c == '&' && i+1 < n && cmd[i+1] == '&' {
				flush(&segs, &cur)
				i += 2
				continue
			}
			// || separator
			if c == '|' && i+1 < n && cmd[i+1] == '|' {
				flush(&segs, &cur)
				i += 2
				continue
			}
			// ; separator
			if c == ';' {
				flush(&segs, &cur)
				i++
				continue
			}
			// newline separator — bash terminates a command at a newline just
			// like ';'. Without this, "echo hi\nrm -rf x" is one segment and only
			// the first token (echo) is classified, hiding the rm.
			//
			// A newline that ends a line with pending heredoc markers doesn't
			// just separate segments, though — real bash reads the following
			// lines as that redirect's stdin data, not as new commands, up to
			// each delimiter's own terminator line. Skip that data (see
			// skipHeredocBodies) instead of splitting it: without this,
			// `ssh host <<'EOF'` / `sudo ...` / `EOF` exposed "sudo ..." as its
			// own fresh top-level segment — a bare local sudo invocation that
			// was actually just remote heredoc body text.
			if c == '\n' {
				flush(&segs, &cur)
				if len(pendingHeredocs) > 0 {
					i = skipHeredocBodies(cmd, i+1, pendingHeredocs)
					pendingHeredocs = pendingHeredocs[:0]
				} else {
					i++
				}
				continue
			}
			// | separator (single pipe) — but not || (handled above)
			if c == '|' {
				// |() subshell: pipe into a subshell
				// Check if the rest after | is (
				j := i + 1
				for j < n && (cmd[j] == ' ' || cmd[j] == '\t') {
					j++
				}
				if j < n && cmd[j] == '(' {
					flush(&segs, &cur)
					i++ // skip | — the ( will be consumed next iteration
					continue
				}
				// plain pipe
				flush(&segs, &cur)
				i++
				continue
			}
			// &() subshell — process substitution / background subshell
			if c == '&' {
				j := i + 1
				for j < n && (cmd[j] == ' ' || cmd[j] == '\t') {
					j++
				}
				if j < n && cmd[j] == '(' {
					flush(&segs, &cur)
					i++ // skip & — the ( will be consumed next iteration
					continue
				}
				// single & — background operator
				flush(&segs, &cur)
				i++
				continue
			}
		}

		cur.WriteByte(c)
		i++
	}
	flush(&segs, &cur)
	return segs
}

func flush(segs *[]string, cur *strings.Builder) {
	s := strings.TrimSpace(cur.String())
	if s != "" {
		*segs = append(*segs, s)
	}
	cur.Reset()
}

// heredocDelim is a "<<WORD"/"<<-WORD" marker found while scanning a line,
// naming the terminator skipOneHeredocBody looks for.
type heredocDelim struct {
	word      string
	stripTabs bool // "<<-" variant: terminator line may have leading tabs
}

// detectHeredocStart reports whether cmd[i:] begins a heredoc redirect, and
// if so its delimiter word. "<<<" (a here-string — inline, no following
// body) and a bare "<<" with nothing usable after it are deliberately not
// treated as heredocs: ok is false and the caller's normal per-character
// scan handles those bytes exactly as before. The operator text itself
// ("<<WORD") is NOT consumed here — only looked ahead at — so the ordinary
// loop still copies it into the current segment untouched; only the body
// on subsequent lines (see skipHeredocBodies) needs skipping.
func detectHeredocStart(cmd string, i int) (heredocDelim, bool) {
	n := len(cmd)
	if i+1 >= n || cmd[i] != '<' || cmd[i+1] != '<' {
		return heredocDelim{}, false
	}
	j := i + 2
	if j < n && cmd[j] == '<' {
		return heredocDelim{}, false // "<<<" here-string, no body to skip
	}
	stripTabs := false
	if j < n && cmd[j] == '-' {
		stripTabs = true
		j++
	}
	for j < n && (cmd[j] == ' ' || cmd[j] == '\t') {
		j++
	}
	if j >= n {
		return heredocDelim{}, false
	}
	var word strings.Builder
	switch cmd[j] {
	case '\'':
		j++
		for j < n && cmd[j] != '\'' {
			word.WriteByte(cmd[j])
			j++
		}
	case '"':
		j++
		for j < n && cmd[j] != '"' {
			if cmd[j] == '\\' && j+1 < n {
				j++
			}
			word.WriteByte(cmd[j])
			j++
		}
	default:
		for j < n {
			c := cmd[j]
			if c == ' ' || c == '\t' || c == '\n' || c == ';' || c == '&' || c == '|' || c == '(' || c == ')' || c == '<' || c == '>' {
				break
			}
			if c == '\\' && j+1 < n {
				j++
				word.WriteByte(cmd[j])
				j++
				continue
			}
			word.WriteByte(c)
			j++
		}
	}
	if word.Len() == 0 {
		return heredocDelim{}, false
	}
	return heredocDelim{word: word.String(), stripTabs: stripTabs}, true
}

// skipHeredocBodies advances past the body (and terminator line) of each
// pending heredoc, in the order their "<<WORD" markers appeared, starting
// right after the newline that ends the line declaring them. Body content
// is never written into any segment — it's stdin data for the redirected
// command, not a shell command of its own — which is what stops e.g.
// `ssh host <<'EOF'` / `sudo apt update` / `EOF` from exposing "sudo apt
// update" as a fresh top-level local segment.
func skipHeredocBodies(cmd string, start int, delims []heredocDelim) int {
	pos := start
	for _, d := range delims {
		pos = skipOneHeredocBody(cmd, pos, d)
	}
	return pos
}

// skipOneHeredocBody returns the index right after d's terminator line, or
// len(cmd) if the terminator never appears — fails safe by swallowing the
// rest of the string as body data rather than guessing where it ends, same
// spirit as closesAtEnd's "malformed input stays unflattened" fallback.
func skipOneHeredocBody(cmd string, start int, d heredocDelim) int {
	n := len(cmd)
	lineStart := start
	for lineStart <= n {
		nl := strings.IndexByte(cmd[lineStart:], '\n')
		var line string
		var nextLineStart int
		if nl < 0 {
			line = cmd[lineStart:]
			nextLineStart = n
		} else {
			line = cmd[lineStart : lineStart+nl]
			nextLineStart = lineStart + nl + 1
		}
		candidate := strings.TrimSuffix(line, "\r")
		if d.stripTabs {
			candidate = strings.TrimLeft(candidate, "\t")
		}
		if candidate == d.word {
			return nextLineStart
		}
		if nl < 0 {
			return n // terminator never found — swallow to end, fail safe
		}
		lineStart = nextLineStart
	}
	return n
}
