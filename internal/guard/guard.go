package guard

import (
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

// PipesIntoDangerousShell reports whether raw pipes output into a shell
// interpreter (bash, sh, zsh, python, …) — e.g. "curl ... | bash". Exported
// so agent.AssessBashRisk can fast-path this straight to BashRiskHigh: the
// content that shell will execute arrives over stdin, which no static
// argv-token check (isDestructiveCommand's descendShellScript included) can
// ever inspect, so there is no safe way to classify it as anything but
// unconditionally dangerous.
func PipesIntoDangerousShell(raw string) bool { return pipesIntoDangerousShell(raw) }

// HasCommandSubstitution reports whether raw contains $(...) or backtick
// (or <(...) process) substitution outside a quoted string. Exported for
// the same reason as PipesIntoDangerousShell: substituted content is
// opaque to any argv-token-based fast-path check, so a command containing
// it must be fast-pathed to BashRiskHigh rather than left to a single LLM
// call with no deterministic floor.
func HasCommandSubstitution(raw string) bool { return hasCommandSubstitution(raw) }

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

	// A leading "{" is only safe to skip past (see skipGroupTokens) if the
	// group is well-formed: *some* segment leads with "{" (an opener
	// exists at all — a bare "}" alone, with no opener anywhere, is just
	// as malformed as an opener with no closer) *and* some other segment
	// is the matching bare "}". Otherwise the "{" never closes
	// (malformed/incomplete input, which real bash would itself reject as
	// a syntax error and never execute at all) and treating whatever
	// follows it as a clean, standalone command would let something like
	// "{ CAt" resolve to a bare, trusted-looking "cat" and pass the SAFE
	// list — found by fuzzing (FuzzClassifySafeInvariant), along with the
	// bare-"}"-alone variant.
	hasOpenBrace, hasCloseBrace := false, false
	for _, s := range segs {
		t := strings.TrimSpace(s)
		if t == "}" {
			hasCloseBrace = true
			continue
		}
		if toks := tokenize(t); len(toks) > 0 && toks[0] == "{" {
			hasOpenBrace = true
		}
	}
	braceGroupWellFormed := hasOpenBrace && hasCloseBrace

	// Collect all tokens across segments for path/env checks.
	var allTokens []string

	for _, seg := range segs {
		tokens := tokenize(seg)
		if len(tokens) == 0 {
			continue
		}
		allTokens = append(allTokens, tokens...)
		// Skip leading pure-syntax grouping tokens ("{", "}" — a brace
		// group's interior operators already leak to the top level in
		// rawSegments, see guard.Segments doc, but its opening "{" stays
		// glued to the segment via whitespace, not an operator, so it's
		// still the segment's tokens[0] here). "(" / ")" are handled by
		// Segments' group-flattening before segments ever reach this loop
		// — a leading "(" surviving to this point means it never closed,
		// so skipGroupTokens deliberately leaves it alone (see its doc).
		tokens = skipGroupTokens(tokens, braceGroupWellFormed)
		if len(tokens) == 0 {
			// A bare "}" segment is the harmless tail of a brace group
			// whose opening "{" (and everything inside) was already fully
			// checked as its own segment(s) above — nothing left to check.
			// Gated on braceGroupWellFormed too: a "}" with no "{" opener
			// anywhere (e.g. the whole command is just "}") is exactly as
			// malformed as an unmatched "{" and must fail closed the same
			// way, not be waved through as if it were a validated pair.
			if braceGroupWellFormed && strings.TrimSpace(seg) == "}" {
				continue
			}
			// Anything else that's nothing but grouping punctuation (e.g.
			// a bare, unbalanced "(" that flattenGroup couldn't unwrap, or
			// a "{" with no matching close anywhere) has no recognizable
			// command at all — that's strictly less information than an
			// ordinary segment, so it fails closed (blocked, not silently
			// skipped) rather than falling through with nothing left to
			// check and no reason ever set to unsafe.
			return false, "command not in safe list: " + seg
		}

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

	// 4. Sensitive path / env checks. Dotenv/shell-rc basenames are
	// token-local (no workdir join needed). Everything else walks the
	// command with cd-aware workdir tracking so `cd ~/.aws && cat
	// credentials` is judged against the directory the shell would be in.
	if touchesDotEnv(allTokens) {
		return false, "touches .env file"
	}
	if touchesEnv(allTokens) {
		return false, "touches environment/shell config file"
	}
	if touchesSensitiveCommand(raw, workdir) {
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

// shellInterpreters is shellInterpreterNames (safe_list.go) as a set — used
// by IsGitCommit to look one level into a "sh -c '...'"-style wrapped command.
var shellInterpreters = stringSet(shellInterpreterNames, map[string]bool{})

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

// IsEnvAssignment is IsEnvAssignment exported for callers outside this
// package (agent's deterministic risk escalators) that need to recognize a
// leading "NAME=value" prefix the same way the git-commit detector does.
func IsEnvAssignment(token string) bool { return isEnvAssignment(token) }

// IsShellInterpreter reports whether name (already normalized) is a shell
// that executes a script via "-c '<commands>'" — bash, sh, zsh, dash, ksh,
// fish. Exported for the same reason as IsEnvAssignment.
func IsShellInterpreter(name string) bool { return shellInterpreters[name] }

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
// agent.WrapRiskGatedApproval.
func IsGitCommit(command string) bool {
	return anyGitInvocationMatches(command, func(sub string, _ []string) bool {
		return sub == "commit"
	})
}

// IsGitDangerous reports whether any segment of command invokes a git
// subcommand that permanently discards work or history: commit (see
// IsGitCommit), rm (deletes tracked files), checkout/restore with a "--"
// pathspec separator (discards uncommitted changes to those paths), reset
// --hard (discards uncommitted changes repo-wide), or push/branch/tag with
// a force flag (can overwrite or lose remote/ref history with no
// fast-forward safety check). Same traversal rules as IsGitCommit (env
// prefix, global options, one level into a shell wrapper); same hard-stop
// use: never let an LLM risk classifier auto-approve these.
func IsGitDangerous(command string) bool {
	return anyGitInvocationMatches(command, gitSubcommandIsDangerous)
}

// GitInvocationIsDangerous reports whether tokens — already tokenized and
// normalized, with tokens[0] == "git" — invokes a dangerous git subcommand
// (see IsGitDangerous). Exported for callers that have already resolved
// past their own wrapper prefix (sudo, timeout, an env-assignment, ...) via
// their own logic and just need the git-specific judgment applied to the
// remaining tokens, without re-parsing the original string (which would
// have to rediscover the same wrapper prefix all over again).
func GitInvocationIsDangerous(tokens []string) bool {
	if len(tokens) == 0 || normalizeToken(tokens[0]) != "git" {
		return false
	}
	return gitSubcommandIsDangerous(gitSubcommandAfter(tokens, 0), tokens)
}

func gitSubcommandIsDangerous(sub string, tokens []string) bool {
	switch sub {
	case "commit", "rm":
		return true
	case "checkout", "restore":
		return tokenPresent(tokens, "--")
	case "reset":
		return hasAnyFlag(tokens, "", "--hard")
	case "push":
		return hasAnyFlag(tokens, "-f", "--force") ||
			hasAnyFlag(tokens, "", "--force-with-lease") ||
			hasAnyFlag(tokens, "", "--force-if-includes")
	case "branch", "tag":
		return hasAnyFlag(tokens, "-f", "--force")
	}
	return false
}

func tokenPresent(tokens []string, want string) bool {
	for _, t := range tokens {
		if t == want {
			return true
		}
	}
	return false
}

func hasAnyFlag(tokens []string, short, long string) bool {
	for _, t := range tokens {
		if matchesFlag(t, short, long) {
			return true
		}
	}
	return false
}

// anyGitInvocationMatches walks every segment of command looking for a git
// invocation — directly, past a leading env-assignment prefix, past global
// git options before the subcommand, or one level into a shell-wrapped
// invocation ("sh -c 'git ...'") — and reports whether match returns true
// for the subcommand found (sub) given the remaining tokens starting at
// "git" (so match can inspect flags anywhere in the invocation, not just
// the subcommand word itself).
func anyGitInvocationMatches(command string, match func(sub string, tokens []string) bool) bool {
	for _, seg := range Segments(command) {
		tokens := tokenize(seg)
		i := 0
		for i < len(tokens) && isEnvAssignment(tokens[i]) {
			i++
		}
		if i >= len(tokens) {
			continue
		}
		if normalizeToken(tokens[i]) == "git" {
			sub := gitSubcommandAfter(tokens, i)
			if match(sub, tokens[i:]) {
				return true
			}
		}
		// One level into a shell wrapper: "sh -c 'git commit -m foo'",
		// "bash -c \"git add -A && git commit\"". Not a real shell parse —
		// just a textual "does the script argument mention git as a
		// separate word" heuristic, which only ever adds an extra human
		// confirmation in the false-positive case, never removes one.
		if shellInterpreters[normalizeToken(tokens[i])] {
			for _, arg := range tokens[i+1:] {
				if anyGitInvocationMatches(strings.Trim(arg, `'"`), match) {
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
		// Unquoted '(' / ')' are always their own shell operator tokens in
		// real bash — a word can never legitimately contain one unescaped,
		// unquoted (subshells don't need surrounding whitespace: "(rm -rf
		// x)" is valid). Without this, "(rm" glues into one token that no
		// per-command detector (which switches on tokens[0]) ever
		// recognizes as "rm" — see guard.Segments' group-flattening, which
		// depends on this split to find the real command inside a subshell.
		if c == '(' || c == ')' {
			if cur.Len() > 0 {
				tokens = append(tokens, cur.String())
				cur.Reset()
			}
			tokens = append(tokens, string(c))
			i++
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
	cmd := safeListCommandName(tokens[0])
	if cmd == "" {
		return false
	}

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
