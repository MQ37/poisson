package guard

import (
	"os"
	"strings"
)

// Tokenize splits a single command segment into whitespace-delimited tokens,
// honoring quotes and unquoted-backslash escapes exactly like Classify's own
// per-segment parsing (see tokenize) — exported so callers outside this
// package (agent.isDestructiveCommand and friends) that need to recognize a
// command name can't accidentally fall back to a naive strings.Fields split
// a simple `\rm` or quote-spliced command name would slip past.
func Tokenize(seg string) []string { return tokenize(seg) }

// NormalizeToken trims quotes/whitespace, strips a leading path prefix, and
// lowercases a token — exported for the same reason as Tokenize.
func NormalizeToken(token string) string { return normalizeToken(token) }

// Classify runs the full classification pipeline and returns whether the
// command is safe, and a reason if it is not. Equivalent to
// ClassifyInDir(command, "") — relative-path sensitivity/symlink checks
// resolve against the process's own cwd instead of a caller-supplied workdir.
func Classify(command string) (safe bool, reason string) {
	return ClassifyInDir(command, "")
}

// ClassifyInDir is Classify with an explicit workdir, used to resolve
// relative path tokens (and the symlinks they might point through) for the
// sensitive-path check — a bash tool call carries its own workdir, which
// may differ from px's process cwd.
func ClassifyInDir(command, workdir string) (safe bool, reason string) {
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
	if touchesSensitivePath(allTokens, workdir) {
		return false, "touches sensitive path"
	}

	return true, ""
}

// gitGlobalOptsWithValue are git global options (given before the subcommand)
// that consume the following token as a separate argument, e.g. "git -C
// /repo commit" — "/repo" is not the subcommand. Options given as "--opt=value"
// (one token) or that take no value at all (--no-pager, --bare, -p, ...) need
// no special casing: the scan in IsGitCommit just skips any "-"-prefixed
// token until it finds one that isn't.
var gitGlobalOptsWithValue = map[string]bool{
	"-C": true, "-c": true, "--git-dir": true, "--work-tree": true,
	"--namespace": true, "--exec-path": true, "--super-prefix": true,
}

// shellInterpreters mirrors dangerousTokens' shell subset — used by
// IsGitCommit to look one level into a "sh -c '...'"-style wrapped command.
var shellInterpreters = map[string]bool{
	"bash": true, "sh": true, "zsh": true, "dash": true, "ksh": true, "fish": true,
}

// isEnvAssignment reports whether token is a shell "NAME=value" prefix (e.g.
// "GIT_AUTHOR_NAME=x" in "GIT_AUTHOR_NAME=x git commit ..."), which runs the
// command that follows exactly like a plain invocation from the caller's
// point of view.
func isEnvAssignment(token string) bool {
	eq := strings.IndexByte(token, '=')
	if eq <= 0 {
		return false
	}
	for i, r := range token[:eq] {
		switch {
		case r == '_', r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

// gitSubcommandAfter returns the subcommand token following "git" in tokens
// (tokens[gitIdx] must already be "git" — skipping any leading environment
// assignments before gitIdx is the caller's job), skipping any global options
// between "git" and the subcommand — e.g. "-C /repo", "--no-pager", "-c
// name=value" — so "git -C /repo --no-pager commit" resolves to "commit",
// not "-C". Returns "" if there's no subcommand token.
func gitSubcommandAfter(tokens []string, gitIdx int) string {
	i := gitIdx + 1
	for i < len(tokens) {
		t := tokens[i]
		if !strings.HasPrefix(t, "-") {
			return normalizeToken(t)
		}
		if gitGlobalOptsWithValue[t] {
			i += 2 // flag + its separate-argument value
			continue
		}
		i++ // flag alone, or an "--opt=value" glued form
	}
	return ""
}

// IsGitCommit reports whether any segment of command invokes `git commit` —
// directly ("git commit -m foo"), through a leading env-assignment prefix
// ("GIT_AUTHOR_NAME=x git commit"), past global git options before the
// subcommand ("git -C /repo --no-pager commit"), or one level into a
// shell-wrapped invocation ("sh -c 'git commit -m foo'"). Committing changes
// the repository's permanent history, so callers use this for a hard rule:
// always ask a human, never let an LLM risk classifier auto-approve it — see
// agent.WrapRiskGatedApproval. Unlike Classify, this never consults
// POISSON_SANDBOX: a sandboxed commit still needs the same hard stop as an
// unsandboxed one.
func IsGitCommit(command string) bool {
	for _, seg := range Segments(command) {
		tokens := tokenize(seg)
		i := 0
		for i < len(tokens) && isEnvAssignment(tokens[i]) {
			i++
		}
		if i >= len(tokens) {
			continue
		}
		if normalizeToken(tokens[i]) == "git" && gitSubcommandAfter(tokens, i) == "commit" {
			return true
		}
		// One level into a shell wrapper: "sh -c 'git commit -m foo'",
		// "bash -c \"git add -A && git commit\"". Not a real shell parse —
		// just a textual "does the script argument mention git and commit as
		// separate words" heuristic, which only ever adds an extra human
		// confirmation in the false-positive case, never removes one.
		if shellInterpreters[normalizeToken(tokens[i])] {
			for _, arg := range tokens[i+1:] {
				if IsGitCommit(strings.Trim(arg, `'"`)) {
					return true
				}
			}
		}
	}
	return false
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
		// Unquoted backslash: bash drops the backslash and passes the next
		// byte through literally (e.g. `\-i` execs as `-i`). Without this, a
		// single backslash defeats every flag-based detector below.
		if c == '\\' && i+1 < n {
			cur.WriteByte(seg[i+1])
			i += 2
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
