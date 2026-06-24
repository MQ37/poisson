package guard

import "strings"

// Segments splits a bash command string into individual command segments
// at the following separators: ;, |, &&, ||, |(), &().
//
// Quoted strings (single and double) are not split.
// Parentheses nesting is tracked so that |() / &() subshell separators are
// only recognized at the top level.
func Segments(cmd string) []string {
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
