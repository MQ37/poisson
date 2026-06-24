package config

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// Parse parses a minimal TOML document into a nested map[string]interface{}.
//
// Supported subset:
//   - '#' line comments (anywhere on a line outside a string)
//   - [section] and [section.subsection] headers (nested tables)
//   - key = value pairs:
//     strings  ("..." with \" \\ \n escapes)
//     integers  (decimal, optional + or - sign)
//     booleans  (true / false)
//     arrays    ([ a, b, c ]) of any mix of the above
//
// NOT supported (will error if encountered): inline tables, multiline
// strings, datetimes, dotted keys on the same line as a value.
//
// The returned map is fully nested: a header [a.b.c] produces
// map["a"].(map)["b"].(map)["c"] = map.
func Parse(data string) (map[string]interface{}, error) {
	root := map[string]interface{}{}
	// currentTable is the map the next key=value should be written into.
	var currentTable map[string]interface{} = root

	lines := strings.Split(data, "\n")
	for i := 0; i < len(lines); i++ {
		raw := lines[i]
		lineNo := i + 1

		// Strip comments and whitespace. We need to be careful not to strip
		// '#' inside strings, so we scan char by char.
		stripped, err := stripComment(raw)
		if err != nil {
			return nil, fmt.Errorf("line %d: %v", lineNo, err)
		}
		line := strings.TrimSpace(stripped)
		if line == "" {
			continue
		}

		// Header line: [section] or [section.subsection]
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") {
				return nil, fmt.Errorf("line %d: malformed table header: %q", lineNo, raw)
			}
			inner := strings.TrimSpace(line[1 : len(line)-1])
			if inner == "" {
				return nil, fmt.Errorf("line %d: empty table header", lineNo)
			}
			tbl, err := descendTable(root, inner)
			if err != nil {
				return nil, fmt.Errorf("line %d: %v", lineNo, err)
			}
			currentTable = tbl
			continue
		}

		// key = value
		key, val, err := parseKeyValue(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %v", lineNo, err)
		}
		if _, exists := currentTable[key]; exists {
			return nil, fmt.Errorf("line %d: duplicate key %q in table", lineNo, key)
		}
		currentTable[key] = val
	}

	return root, nil
}

// descendTable walks/creates nested maps for a dotted header path.
func descendTable(root map[string]interface{}, path string) (map[string]interface{}, error) {
	parts := strings.Split(path, ".")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			return nil, fmt.Errorf("empty section name in %q", path)
		}
		// Bare keys must be alphanumeric + underscore + hyphen per TOML.
		if !validBareKey(p) {
			return nil, fmt.Errorf("invalid table name segment %q", p)
		}
		existing, ok := root[p]
		if !ok {
			sub := map[string]interface{}{}
			root[p] = sub
			root = sub
			continue
		}
		sub, ok := existing.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("section %q conflicts with non-table value", p)
		}
		root = sub
	}
	return root, nil
}

func validBareKey(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == ':') {
			return false
		}
	}
	return true
}

// parseKeyValue splits "key = value" and parses the value.
func parseKeyValue(line string) (string, interface{}, error) {
	// Find the first '=' that's not inside a string. Since the key part is a
	// bare key (no quotes per our subset), we can just find the first '='.
	eq := strings.IndexByte(line, '=')
	if eq < 0 {
		return "", nil, fmt.Errorf("expected key = value, got %q", line)
	}
	key := strings.TrimSpace(line[:eq])
	valStr := strings.TrimSpace(line[eq+1:])
	if key == "" {
		return "", nil, fmt.Errorf("empty key in %q", line)
	}
	if !validBareKey(key) {
		return "", nil, fmt.Errorf("invalid key %q", key)
	}
	val, err := parseValue(valStr)
	if err != nil {
		return "", nil, err
	}
	return key, val, nil
}

// parseValue parses a single TOML value (string, int, bool, array).
func parseValue(s string) (interface{}, error) {
	if s == "" {
		return nil, fmt.Errorf("empty value")
	}
	switch s[0] {
	case '"':
		return parseString(s)
	case '[':
		return parseArray(s)
	}
	// literal: true / false / number
	switch s {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	cleaned := strings.ReplaceAll(s, "_", "")
	// integer (no '.' or 'e')
	if !strings.ContainsAny(cleaned, ".eE") {
		n, err := strconv.ParseInt(cleaned, 10, 64)
		if err == nil {
			return int(n), nil
		}
	}
	// float
	f, err := strconv.ParseFloat(cleaned, 64)
	if err == nil {
		return f, nil
	}
	return nil, fmt.Errorf("invalid value %q (expected string, number, bool, or array)", s)
}

// parseString parses a double-quoted string starting at s[0]. Returns the
// parsed string and consumes the entire input (trailing content after the
// closing quote is an error).
func parseString(s string) (string, error) {
	if len(s) < 2 || s[0] != '"' {
		return "", fmt.Errorf("invalid string literal")
	}
	var b strings.Builder
	i := 1
	for i < len(s) {
		c := s[i]
		if c == '"' {
			// closing quote
			rest := strings.TrimSpace(s[i+1:])
			if rest != "" {
				return "", fmt.Errorf("unexpected trailing content after string: %q", rest)
			}
			return b.String(), nil
		}
		if c == '\\' {
			if i+1 >= len(s) {
				return "", fmt.Errorf("unterminated escape sequence in string")
			}
			esc := s[i+1]
			switch esc {
			case '"':
				b.WriteByte('"')
			case '\\':
				b.WriteByte('\\')
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			default:
				return "", fmt.Errorf("invalid escape \\%c in string", esc)
			}
			i += 2
			continue
		}
		b.WriteByte(c)
		i++
	}
	return "", fmt.Errorf("unterminated string literal")
}

// parseArray parses a TOML array literal "[ a, b, c ]".
func parseArray(s string) (interface{}, error) {
	if len(s) < 2 || s[0] != '[' {
		return nil, fmt.Errorf("invalid array literal")
	}
	if s[len(s)-1] != ']' {
		return nil, fmt.Errorf("unterminated array literal")
	}
	inner := strings.TrimSpace(s[1 : len(s)-1])
	if inner == "" {
		return []interface{}{}, nil
	}
	// Split on commas not inside strings.
	var elems []string
	var cur strings.Builder
	inStr := false
	depth := 0
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		if inStr {
			cur.WriteByte(c)
			if c == '\\' && i+1 < len(inner) {
				cur.WriteByte(inner[i+1])
				i++
				continue
			}
			if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
			cur.WriteByte(c)
		case '[':
			depth++
			cur.WriteByte(c)
		case ']':
			depth--
			cur.WriteByte(c)
		case ',':
			if depth == 0 {
				elems = append(elems, cur.String())
				cur.Reset()
				continue
			}
			cur.WriteByte(c)
		default:
			cur.WriteByte(c)
		}
	}
	if inStr {
		return nil, fmt.Errorf("unterminated string inside array")
	}
	elems = append(elems, cur.String())

	out := make([]interface{}, 0, len(elems))
	for _, e := range elems {
		e = strings.TrimSpace(e)
		if e == "" {
			continue // allow trailing comma
		}
		v, err := parseValue(e)
		if err != nil {
			return nil, fmt.Errorf("in array element %q: %v", e, err)
		}
		out = append(out, v)
	}
	return out, nil
}

// stripComment removes a trailing '#' comment from a line, respecting
// double-quoted strings. Returns the line content before the comment.
func stripComment(line string) (string, error) {
	inStr := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if inStr {
			if c == '\\' && i+1 < len(line) {
				i++
				continue
			}
			if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '#':
			return line[:i], nil
		}
	}
	if inStr {
		return "", fmt.Errorf("unterminated string literal")
	}
	return line, nil
}
