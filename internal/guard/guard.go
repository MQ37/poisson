package guard

import (
	"os"
	"strings"
)

// Classify runs the full classification pipeline and returns whether the
// command is safe, and a reason if it is not.
func Classify(command string) (safe bool, reason string) {
	if os.Getenv("POISSON_SANDBOX") == "1" || os.Getenv("IS_SANDBOX") == "1" {
		return true, ""
	}

	raw := command

	// 1. Dangerous patterns: redirects, pipes into dangerous shells, substitution.
	if hasDangerousPatterns(raw) {
		return false, "dangerous pattern: redirect or pipe into dangerous shell"
	}
	if hasCommandSubstitution(raw) {
		return false, "command substitution ($(…) or backticks)"
	}

	// 2. ANSI escape sequences.
	if containsAnsiEscape(raw) {
		return false, "ANSI escape sequence detected"
	}

	// 3. Split into segments.
	segs := Segments(raw)

	// Collect all tokens across segments for path/env checks.
	var allTokens []string

	for _, seg := range segs {
		tokens := tokenize(seg)
		if len(tokens) == 0 {
			continue
		}
		allTokens = append(allTokens, tokens...)

		// Check first token for destructive commands.
		first := normalizeToken(tokens[0])
		if destructiveCommands[first] {
			return false, "destructive command: " + first
		}
		// Check for dangerous tokens anywhere in the command.
		for _, tk := range tokens {
			nt := normalizeToken(tk)
			if dangerousTokens[nt] {
				return false, "dangerous token: " + nt
			}
		}

		// Prefix match against SAFE list.
		if !isSegmentSafe(tokens) {
			return false, "command not in safe list: " + first
		}

		// Per-command danger detectors.
		if r, unsafe := checkPerCommandDetectors(tokens); unsafe {
			return false, r
		}
	}

	// 4. Sensitive path / env checks across all tokens.
	if touchesDotEnv(allTokens) {
		return false, "touches .env file"
	}
	if touchesEnv(allTokens) {
		return false, "touches environment/shell config file"
	}
	if touchesSensitivePath(allTokens) {
		return false, "touches sensitive path"
	}

	return true, ""
}

// tokenize splits a segment into whitespace-delimited tokens, respecting
// quotes.
func tokenize(seg string) []string {
	var tokens []string
	var cur strings.Builder
	i := 0
	n := len(seg)
	for i < n {
		c := seg[i]
		if c == ' ' || c == '\t' {
			if cur.Len() > 0 {
				tokens = append(tokens, cur.String())
				cur.Reset()
			}
			i++
			continue
		}
		if c == '\'' {
			cur.WriteByte(c)
			i++
			for i < n {
				cur.WriteByte(seg[i])
				if seg[i] == '\'' {
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
				if seg[i] == '\\' && i+1 < n {
					cur.WriteByte(seg[i])
					cur.WriteByte(seg[i+1])
					i += 2
					continue
				}
				cur.WriteByte(seg[i])
				if seg[i] == '"' {
					i++
					break
				}
				i++
			}
			continue
		}
		cur.WriteByte(c)
		i++
	}
	if cur.Len() > 0 {
		tokens = append(tokens, cur.String())
	}
	return tokens
}

// isSegmentSafe checks whether the segment's command prefix matches the SAFE
// list. It compares the normalized command plus any subcommand tokens.
func isSegmentSafe(tokens []string) bool {
	if len(tokens) == 0 {
		return false
	}
	cmd := normalizeToken(tokens[0])

	// Build the candidate prefix from the first 1–3 tokens, normalized.
	candidates := []string{cmd}
	if len(tokens) >= 2 {
		candidates = append(candidates, cmd+" "+normalizeToken(tokens[1]))
	}
	if len(tokens) >= 3 {
		candidates = append(candidates, cmd+" "+normalizeToken(tokens[1])+" "+normalizeToken(tokens[2]))
	}

	return matchesSafePrefixes(candidates, SAFE)
}

func matchesSafePrefixes(candidates, safeList []string) bool {
	for _, safe := range safeList {
		for _, c := range candidates {
			if c == safe || strings.HasPrefix(c+" ", safe+" ") {
				return true
			}
			if strings.HasPrefix(c, safe) {
				if len(c) == len(safe) || c[len(safe)] == ' ' {
					return true
				}
			}
		}
	}
	return false
}

// checkPerCommandDetectors runs the per-command danger detectors for a
// segment's tokens. Returns (reason, unsafe).
func checkPerCommandDetectors(tokens []string) (string, bool) {
	if len(tokens) == 0 {
		return "", false
	}
	cmd := normalizeToken(tokens[0])
	switch cmd {
	case "find":
		if findHasDangerousFlag(tokens) {
			return "find with dangerous flag (-exec/-delete/...)", true
		}
	case "gh":
		if len(tokens) >= 2 && normalizeToken(tokens[1]) == "api" {
			if ghApiIsMutating(tokens) {
				return "gh api with mutating method", true
			}
		}
	case "git":
		if gitSubIsMutating(tokens) {
			return "git mutating subcommand", true
		}
		if gitHasOutputFlag(tokens) {
			return "git with output redirect flag", true
		}
	case "rg":
		if rgHasDangerousFlag(tokens) {
			return "rg with dangerous flag", true
		}
	case "sed":
		if sedHasDangerousFlag(tokens) {
			return "sed with in-place edit (-i)", true
		}
		if sedScriptIsDangerous(tokens) {
			return "sed script with dangerous command (w/e)", true
		}
	case "tree":
		if treeHasDangerousFlag(tokens) {
			return "tree with output file flag", true
		}
	case "yq":
		if yqHasDangerousFlag(tokens) {
			return "yq with in-place flag", true
		}
	case "tail":
		if tailHasFollowFlag(tokens) {
			return "tail with follow flag", true
		}
	}
	return "", false
}
