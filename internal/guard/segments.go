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
			if c == '\n' {
				flush(&segs, &cur)
				i++
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
