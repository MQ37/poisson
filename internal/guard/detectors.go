package guard

import (
	"os"
	"path/filepath"
	"strings"
)

// hasCommandSubstitution reports whether the raw command contains $(...) or
// backtick substitution outside of quoted strings.
func hasCommandSubstitution(raw string) bool {
	i := 0
	n := len(raw)
	for i < n {
		c := raw[i]
		if c == '\'' {
			i++
			for i < n && raw[i] != '\'' {
				i++
			}
			i++
			continue
		}
		if c == '"' {
			i++
			for i < n {
				if raw[i] == '\\' && i+1 < n {
					i += 2
					continue
				}
				if raw[i] == '"' {
					i++
					break
				}
				if raw[i] == '`' {
					return true
				}
				if raw[i] == '$' && i+1 < n && raw[i+1] == '(' {
					return true
				}
				i++
			}
			continue
		}
		if c == '`' {
			return true
		}
		if c == '$' && i+1 < n && raw[i+1] == '(' {
			return true
		}
		// Process substitution: <(cmd) / >(cmd) both hand a live command's
		// stdout/stdin to the outer command via /dev/fd, same trust boundary
		// as $(...) or backticks. >( is already caught by the redirect check
		// (hasRedirectToFile), but <( is not caught anywhere else.
		if c == '<' && i+1 < n && raw[i+1] == '(' {
			return true
		}
		i++
	}
	return false
}

// hasDangerousPatterns checks the raw command string for dangerous patterns:
// output redirects that overwrite/truncate files (>, >>), and pipes into
// dangerous shells.
func hasDangerousPatterns(raw string) bool {
	// Redirect to a file: > or >> at the top level (not inside quotes).
	if hasRedirectToFile(raw) {
		return true
	}
	// Pipe into a dangerous shell.
	if pipesIntoDangerousShell(raw) {
		return true
	}
	return false
}

// hasRedirectToFile detects > or >> that redirect output to a file (not 2> or
// similar, but the SPEC treats all redirects as requiring approval). We scan
// outside of quoted regions.
func hasRedirectToFile(raw string) bool {
	i := 0
	n := len(raw)
	for i < n {
		c := raw[i]
		if c == '\'' {
			i++
			for i < n && raw[i] != '\'' {
				i++
			}
			i++
			continue
		}
		if c == '"' {
			i++
			for i < n {
				if raw[i] == '\\' && i+1 < n {
					i += 2
					continue
				}
				if raw[i] == '"' {
					i++
					break
				}
				i++
			}
			continue
		}
		if c == '>' {
			return true
		}
		i++
	}
	return false
}

// pipesIntoDangerousShell checks whether the command pipes output into a
// dangerous shell interpreter (bash, sh, zsh, python, etc.).
func pipesIntoDangerousShell(raw string) bool {
	segs := strings.Split(raw, "|")
	for _, seg := range segs[1:] {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		// Get first word.
		fields := strings.Fields(seg)
		if len(fields) == 0 {
			continue
		}
		first := normalizeToken(fields[0])
		if dangerousTokens[first] {
			return true
		}
	}
	return false
}

// containsAnsiEscape reports whether the raw command contains ANSI escape
// sequences.
func containsAnsiEscape(raw string) bool {
	return ansiEscapeRe.MatchString(raw)
}

// touchesDotEnv reports whether any token references a .env file by name
// (exact basename match).
func touchesDotEnv(tokens []string) bool {
	for _, t := range tokens {
		t = strings.Trim(t, "'\"")
		base := filepath.Base(t)
		if base == ".env" || strings.HasPrefix(base, ".env.") {
			return true
		}
	}
	return false
}

// touchesEnv reports whether any token references environment files.
func touchesEnv(tokens []string) bool {
	for _, t := range tokens {
		t = strings.Trim(t, "'\"")
		base := filepath.Base(t)
		if base == ".env" ||
			strings.HasPrefix(base, ".env.") ||
			base == ".bashrc" ||
			base == ".zshrc" ||
			base == ".profile" ||
			base == ".bash_profile" {
			return true
		}
	}
	return false
}

// expandPathToken quote-strips (including mid-token quotes like ~/".ssh")
// and tilde-expands a single path-shaped token. Uses stripEmbeddedQuotes —
// not strings.Trim — so quote-smuggling can't hide a sensitive path from
// the dir-pattern tables (ls ~/".ssh" is the same real path as ls ~/.ssh).
func expandPathToken(token string) string {
	t := stripEmbeddedQuotes(strings.TrimSpace(token))
	if t == "" {
		return ""
	}
	if strings.HasPrefix(t, "~") {
		t = os.ExpandEnv(strings.Replace(t, "~", "$HOME", 1))
	}
	return t
}

// resolveAgainst joins a possibly-relative path against base (when base is
// non-empty and path is relative). base itself may be "" — then path is
// returned unchanged (absolute paths always pass through).
func resolveAgainst(base, path string) string {
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) || base == "" {
		return path
	}
	return filepath.Join(base, path)
}

// tokenLooksLikePath reports whether a token is worth checking as a path.
// Flags (`-la`, `--json`) and pure env-assignment prefixes are skipped.
func tokenLooksLikePath(token string) bool {
	if token == "" || strings.HasPrefix(token, "-") {
		return false
	}
	if isEnvAssignment(token) {
		return false
	}
	return true
}

// touchesSensitivePath reports whether any token references a sensitive
// path, either directly or through a symlink that resolves into one.
// workdir (may be "") resolves relative tokens — both for the literal
// basename/dir-pattern check AND for symlink lookup — so a bare
// `credentials` after `cd ~/.aws` (or with workdir already in that dir) is
// caught the same way an absolute `~/.aws/credentials` is. Callers that
// only have the raw command string should prefer touchesSensitiveCommand,
// which also walks leading `cd` segments to update the effective workdir.
func touchesSensitivePath(tokens []string, workdir string) bool {
	for _, raw := range tokens {
		if !tokenLooksLikePath(raw) {
			continue
		}
		expanded := expandPathToken(raw)
		if expanded == "" {
			continue
		}
		// Literal form first (absolute, tilde-expanded, or a sensitive
		// basename like "credentials" / "id_rsa" with no directory).
		if pathTextIsSensitive(expanded) {
			return true
		}
		// Join relative tokens against workdir so `cat auth.json` inside
		// ~/.poisson (workdir already there, or set by a prior cd) is
		// judged as ~/.poisson/auth.json, not as a free-floating name.
		candidate := resolveAgainst(workdir, expanded)
		if candidate != expanded && pathTextIsSensitive(candidate) {
			return true
		}
		// Symlink follow on the resolved candidate: a token that looks
		// harmless by name (including a bare relative filename) can still
		// point at a sensitive file. A token that isn't really a path
		// (grep pattern, hostname, ...) just fails the stat (ENOENT) and
		// falls through unchanged.
		if candidate == "" {
			continue
		}
		if resolved := ResolveSymlinkTarget(candidate); resolved != candidate {
			if pathTextIsSensitive(resolved) {
				return true
			}
		}
	}
	return false
}

// touchesSensitiveCommand walks every segment of command, tracking a
// leading `cd <dir>` chain so relative path tokens in later segments are
// resolved against the directory the shell would be in — the hole that
// let `cd ~/.poisson && cat auth.json` auto-approve when only the bare
// basename was checked. workdir is the bash tool's starting directory
// (sticky/session cwd); may be "".
func touchesSensitiveCommand(command, workdir string) bool {
	cwd := workdir
	for _, seg := range Segments(command) {
		tokens := tokenize(seg)
		if len(tokens) == 0 {
			continue
		}
		if touchesSensitivePath(tokens, cwd) {
			return true
		}
		// Advance cwd past a plain `cd <dir>` so the next segment's
		// relative tokens resolve correctly. Only the single-arg form:
		// flags (`cd -P`), bare `cd`, and multi-arg forms leave cwd alone
		// (fails closed for the next segment only if its own tokens are
		// already sensitive; we don't try to simulate every cd variant).
		if dir, ok := plainCdTarget(tokens); ok {
			expanded := expandPathToken(dir)
			if expanded != "" {
				cwd = resolveAgainst(cwd, expanded)
			}
		}
	}
	return false
}

// plainCdTarget reports whether tokens is exactly `cd <dir>` (no flags)
// and returns that directory token. Used only to advance the effective
// workdir while scanning a multi-segment command for sensitive paths.
func plainCdTarget(tokens []string) (dir string, ok bool) {
	if len(tokens) != 2 {
		return "", false
	}
	if normalizeToken(tokens[0]) != "cd" {
		return "", false
	}
	if strings.HasPrefix(tokens[1], "-") {
		return "", false
	}
	return tokens[1], true
}

// pathTextIsSensitive checks a path string (already quote-trimmed and
// tilde-expanded by the caller) against the basename/directory-pattern
// tables, without touching the filesystem.
func pathTextIsSensitive(t string) bool {
	return sensitivePathReasonLiteral(t) != ""
}

// SensitivePathReason reports why a file path is a secret/credential file
// that should never be read or written without explicit human approval, or
// "" if it isn't one. path should already be resolved (absolute, or at
// least joined with the caller's cwd) — it reuses the same basename/
// directory-pattern tables the bash guard checks tokens against, so file
// tools (read/write/edit) and the bash guard agree on what counts as
// sensitive. Also follows symlinks: a file with an innocuous name that
// secretly points at e.g. ~/.ssh/id_rsa is caught via its resolved target,
// not just its literal name.
func SensitivePathReason(path string) string {
	if r := sensitivePathReasonLiteral(path); r != "" {
		return r
	}
	if resolved := ResolveSymlinkTarget(path); resolved != path {
		if r := sensitivePathReasonLiteral(resolved); r != "" {
			return r + " (via symlink)"
		}
	}
	return ""
}

func sensitivePathReasonLiteral(path string) string {
	if path == "" {
		return ""
	}
	base := filepath.Base(path)
	switch {
	case sensitiveExactBasenames[base]:
		return "sensitive file: " + base
	case sshPrivKeyRe.MatchString(base):
		return "SSH private key: " + base
	case base == ".env" || strings.HasPrefix(base, ".env."):
		return "environment secrets file: " + base
	case strings.HasSuffix(base, ".env") && len(base) > 4:
		// secrets.env, prod.env, … — not covered by the ".env." prefix rule.
		return "environment secrets file: " + base
	}
	slashed := filepath.ToSlash(path)
	// /proc/<pid>/environ holds the process environment (often secrets).
	// The static dir-pattern table only lists self/1; any other pid still
	// counts.
	if strings.HasPrefix(slashed, "/proc/") && strings.HasSuffix(slashed, "/environ") {
		return "sensitive path: /proc/*/environ"
	}
	for _, pat := range sensitiveDirPatterns {
		if pathMatchesSensitiveDir(slashed, pat) {
			return "sensitive path: " + pat
		}
	}
	// Context-sensitive basenames (credentials, auth.json, passwd, …) only
	// count when the path has a directory component that already matched
	// above, OR when the parent directory itself is a sensitive dir.
	// Bare `credentials` in a normal project workdir is intentionally not
	// flagged — see contextSensitiveBasenames.
	if contextSensitiveBasenames[base] && pathHasDirComponent(path) {
		parent := filepath.ToSlash(filepath.Dir(path))
		for _, pat := range sensitiveDirPatterns {
			if pathMatchesSensitiveDir(parent, pat) || pathMatchesSensitiveDir(parent+"/", pat) {
				return "sensitive file: " + base
			}
		}
		// /etc/passwd pattern is a file pattern; parent /etc should still
		// count for passwd/shadow/sudoers.
		if base == "passwd" || base == "shadow" || base == "sudoers" {
			if parent == "/etc" || strings.HasSuffix(parent, "/etc") {
				return "sensitive file: " + base
			}
		}
	}
	return ""
}

func pathHasDirComponent(path string) bool {
	// Absolute paths, or any relative path containing a separator.
	return filepath.IsAbs(path) || strings.ContainsRune(path, '/') || strings.ContainsRune(path, filepath.Separator)
}

// pathMatchesSensitiveDir reports whether slashed path hits a sensitive
// directory pattern. Patterns that look like files (/etc/passwd) stay
// exact-substring matches; directory patterns (/.ssh/, /.aws/, …) also
// match the directory itself (path ends in "/.ssh") so `ls ~/.ssh` is
// blocked the same way `cat ~/.ssh/id_rsa` is — without this, listing a
// secret dir auto-approved while reading a file inside it did not.
func pathMatchesSensitiveDir(slashed, pat string) bool {
	if strings.Contains(slashed, pat) {
		return true
	}
	// Directory patterns end in "/". Also match the directory bare
	// (no trailing slash) and a trailing-slash form of the path itself.
	if strings.HasSuffix(pat, "/") {
		dir := strings.TrimSuffix(pat, "/")
		if slashed == dir || strings.HasSuffix(slashed, dir) || strings.HasSuffix(slashed, dir+"/") {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Per-command danger detectors
// ---------------------------------------------------------------------------

// findHasDangerousFlag detects dangerous flags on find: -exec, -execdir,
// -ok, -delete, -fls, -fprint, -fprint0, -fprintf, -print0 is fine.
func findHasDangerousFlag(tokens []string) bool {
	for _, t := range tokens {
		switch t {
		case "-exec", "-execdir", "-ok", "-okdir", "-delete", "-fls", "-fprint", "-fprint0", "-fprintf":
			return true
		}
	}
	return false
}

// ghApiIsMutating detects gh api calls that are not GET (mutations). Per gh's
// own docs ("gh api --help"): the default HTTP method is GET, and only
// switches to POST if -f/--raw-field or -F/--field parameters are given
// (this is the common way to mutate — issue/comment/collaborator/gist
// creation, repo settings, GraphQL mutations — and is far more frequent in
// practice than an explicit --method flag). --method/-X GET is an explicit
// override that always wins, matching gh's own precedence. The "graphql"
// endpoint is also always flagged: the GitHub GraphQL API only accepts
// POST, and a mutation there ("mutation { ... }") looks identical to a
// query call from the CLI's argument shape alone.
func ghApiIsMutating(tokens []string) bool {
	explicitGet := false
	explicitMutate := false
	hasFieldParam := false
	hasGraphqlEndpoint := false
	for i, t := range tokens {
		switch t {
		case "--method", "-X":
			if i+1 < len(tokens) {
				switch strings.ToUpper(tokens[i+1]) {
				case "GET", "HEAD":
					explicitGet = true
				case "POST", "PUT", "PATCH", "DELETE":
					explicitMutate = true
				}
			}
		case "-f", "-F":
			hasFieldParam = true
		default:
			if matchesFlag(t, "", "--raw-field") || matchesFlag(t, "", "--field") {
				hasFieldParam = true
			}
			if strings.EqualFold(t, "graphql") {
				hasGraphqlEndpoint = true
			}
		}
	}
	if explicitGet {
		return false
	}
	return explicitMutate || hasFieldParam || hasGraphqlEndpoint
}

// gitSubIsMutating detects mutating git subcommands (push, commit, rebase,
// reset, clean, merge, cherry-pick, revert, etc.). Subcommands in the SAFE
// list (branch, tag, remote) are not flagged here; their mutating forms are
// handled by flag-specific checks below.
func gitSubIsMutating(tokens []string) bool {
	if len(tokens) < 2 {
		return false
	}
	sub := tokens[1]
	switch sub {
	case "push", "commit", "rebase", "reset", "clean", "merge",
		"cherry-pick", "revert", "am", "apply",
		"init", "clone", "fetch", "pull",
		"mv", "update-ref", "reflog", "config":
		return true
	}
	// stash drop/pop/clear
	if sub == "stash" && len(tokens) >= 3 {
		switch tokens[2] {
		case "drop", "pop", "clear":
			return true
		}
	}
	// branch with delete/move/force flags. -f/--force silently repoints an
	// existing ref (no fast-forward safety check) instead of just creating
	// or listing — just as mutating as delete/move, and previously missed
	// here entirely.
	if sub == "branch" {
		for _, t := range tokens[2:] {
			if t == "-d" || t == "-D" || t == "--delete" || t == "-m" || t == "--move" ||
				matchesFlag(t, "-f", "--force") {
				return true
			}
		}
	}
	// tag with delete/force flags. -f/--force silently repoints an existing
	// tag to an arbitrary commit — same "not just create/list" reasoning
	// as branch above.
	if sub == "tag" {
		for _, t := range tokens[2:] {
			if t == "-d" || t == "--delete" || matchesFlag(t, "-f", "--force") {
				return true
			}
		}
	}
	// remote rm/add/rename.
	if sub == "remote" && len(tokens) >= 3 {
		switch tokens[2] {
		case "rm", "add", "rename", "set-url":
			return true
		}
	}
	return false
}

// gitHasOutputFlag detects flags that write output: -o, --output, --output-file.
func gitHasOutputFlag(tokens []string) bool {
	for _, t := range tokens {
		switch t {
		case "-o", "--output", "--output-file":
			return true
		}
	}
	return false
}

// rgHasDangerousFlag detects dangerous ripgrep flags: --files-without-match
// combined with --delete? rg doesn't delete. Treat -z/--null as potentially
// dangerous for piping to xargs; treat --files as safe. The main concern is
// rg piped to xargs rm. We flag --null-data, unusual flags, and --pre/
// --pre-glob (runs an arbitrary preprocessor command per searched file —
// RCE via a SAFE-listed tool).
func rgHasDangerousFlag(tokens []string) bool {
	for _, t := range tokens {
		switch t {
		case "-z", "--null-data", "--null":
			return true
		}
		if matchesFlag(t, "", "--pre") || matchesFlag(t, "", "--pre-glob") {
			return true
		}
	}
	return false
}

// sedHasDangerousFlag detects sed flags that modify files in place: -i /
// --in-place (including any unambiguous GNU-style abbreviation, e.g. --i).
func sedHasDangerousFlag(tokens []string) bool {
	for _, t := range tokens {
		if matchesFlag(t, "-i", "--in-place") {
			return true
		}
	}
	return false
}

// sedScriptIsDangerous detects dangerous sed scripts: w (write to file), e
// (execute command).
func sedScriptIsDangerous(tokens []string) bool {
	for _, t := range tokens {
		if strings.HasPrefix(t, "-e") || !strings.HasPrefix(t, "-") {
			// This token is a script expression.
			if containsSedDangerousCmd(t) {
				return true
			}
		}
	}
	return false
}

func containsSedDangerousCmd(script string) bool {
	// Check for sed 'w' (write file) or 'e' (execute) commands.
	// These appear as standalone commands or after s/.../.../. A real sed
	// command letter is always isolated on BOTH sides by a non-alpha byte
	// (an address, a preceding '/', ';', or the string's edge) — checking
	// only the left side flags any ordinary English word starting with w/e
	// inside a regex (e.g. "/word/d", "/error/p" both contain a mid-word 'w'
	// or 'e' immediately after a non-alpha '/'), which would block plainly
	// harmless sed scripts from ever being auto-approved.
	for i := 0; i < len(script); i++ {
		c := script[i]
		if c == '\'' || c == '"' {
			// skip quoted
			continue
		}
		if c == 'w' || c == 'e' {
			prevOK := i == 0 || !isAlpha(script[i-1])
			nextOK := i == len(script)-1 || !isAlpha(script[i+1])
			if prevOK && nextOK {
				return true
			}
		}
	}
	return false
}

func isAlpha(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// treeHasDangerousFlag detects tree flags that write to file: -o / --output-file.
func treeHasDangerousFlag(tokens []string) bool {
	for _, t := range tokens {
		if matchesFlag(t, "-o", "--output-file") {
			return true
		}
	}
	return false
}

// yqHasDangerousFlag detects yq flags: -i / --inplace / --in-place
// (including abbreviations).
func yqHasDangerousFlag(tokens []string) bool {
	for _, t := range tokens {
		if matchesFlag(t, "-i", "--inplace") || matchesFlag(t, "", "--in-place") {
			return true
		}
	}
	return false
}

// tailHasFollowFlag detects tail -f / --follow.
func tailHasFollowFlag(tokens []string) bool {
	for _, t := range tokens {
		if t == "-f" || t == "--follow" || strings.HasPrefix(t, "-f") && strings.Contains(t, "f") {
			return true
		}
	}
	return false
}

// matchesFlag reports whether token is, or is an unambiguous GNU-style
// abbreviation of, a dangerous flag. short (e.g. "-i") matches exactly or as
// a bundled-value prefix (e.g. "-i.bak"); long (e.g. "--in-place") matches
// exactly or via any "--"-prefixed abbreviation getopt_long would accept
// ("--in", "--i", ...), including a trailing "=value" form. This closes the
// gap where e.g. `sed --i file` (a valid abbreviation of `sed --in-place`)
// slipped past a detector that only checked for the literal "-i" spelling.
func matchesFlag(token, short, long string) bool {
	if short != "" && strings.HasPrefix(token, short) {
		return true
	}
	if long == "" || !strings.HasPrefix(token, "--") || len(token) < 3 {
		return false
	}
	name := token
	if eq := strings.IndexByte(token, '='); eq >= 0 {
		name = token[:eq]
	}
	return strings.HasPrefix(long, name)
}

// skipGroupTokens drops any leading tokens that are "{" or "}" — pure
// grouping syntax, not a real command name — so the caller's tokens[0] is
// the actual command a "{ rm -rf x; }" brace group really invokes (see
// guard.Segments' doc comment for why braces need this while parens don't).
//
// Skips nothing at all unless groupWellFormed is true — the caller has
// already verified the overall command has both a "{"-leading segment and
// a matching bare "}" segment somewhere (from the same guard.Segments
// call). Without that check, an unmatched "{" or "}" (malformed/incomplete
// input — a real shell would reject it as a syntax error and never
// execute anything) would let whatever follows it be treated as a clean,
// standalone command, e.g. the unbalanced "{ CAt" or "} CAt" resolving to
// a bare, trusted-looking "cat" and passing the SAFE list (found by
// fuzzing).
//
// Deliberately does NOT skip "(" / ")" at all, under any condition: by the
// time a segment reaches this point, Segments' group-flattening has
// already unwrapped every well-formed "(...)" group — a leading "("
// surviving to here means it never closed, with no sibling-segment check
// able to rescue it (parens are depth-tracked, so an unbalanced "("
// swallows the rest of the string into one segment rather than leaving a
// separate closing ")" segment to look for).
func skipGroupTokens(tokens []string, groupWellFormed bool) []string {
	if !groupWellFormed {
		return tokens
	}
	i := 0
	for i < len(tokens) && (tokens[i] == "{" || tokens[i] == "}") {
		i++
	}
	return tokens[i:]
}

// standardBinDirs are the well-known system binary directories a normal,
// unprivileged user can't write into. Only used by safeListCommandName —
// see its doc comment for why this matters.
var standardBinDirs = map[string]bool{
	"/bin": true, "/usr/bin": true, "/usr/local/bin": true,
	"/sbin": true, "/usr/sbin": true, "/usr/local/sbin": true,
	"/opt/homebrew/bin": true, "/opt/homebrew/sbin": true,
}

// safeListCommandName returns the command name eligible to match the SAFE
// list for token, or "" if token isn't eligible at all. Deliberately
// stricter than normalizeToken's general path-prefix stripping (which is
// fine — even desirable — for the "flag this as dangerous" direction, where
// being over-inclusive just means one extra human confirmation): granting
// SAFE-list trust is the one place in this whole guard where a
// misjudgment means a command runs with *zero* review at all, so it must
// never trust a mere basename match.
//
// A bare token with no "/" (e.g. "cat") is genuinely resolved via $PATH, so
// on a normal system it really is the trusted system tool of that name.
// But a slash-QUALIFIED path — relative ("./cat", "some/dir/cat") or
// absolute outside the standard system directories — bypasses $PATH
// entirely and just execs whatever file sits at that exact location. Any
// caller (an attacker, or a manipulated agent) able to write anywhere at
// all — the project's own working directory, /tmp, a subfolder under
// $HOME — could otherwise plant a file literally named "cat"/"ls"/"git"
// containing anything, and have it silently auto-approved as though it
// were the real, trusted tool merely because its path *ends* in that name.
// This also closes a more mundane correctness bug fuzzing found: a
// malformed/incomplete fragment like "(0000/Cd" was normalizing (via
// filepath.Base of the whole leftover string) to a bare "cd" and matching
// the SAFE list, even though it isn't a real command invocation at all.
//
// A relative path, or an absolute path outside standardBinDirs, might
// genuinely be the trusted tool — this guard just has no way to tell that
// apart from a lookalike, so it never silently trusts it. Such a token is
// checked byte-for-byte against the SAFE list instead (which will simply
// never match), falling through to LLM/human review — fails closed, not
// open.
func safeListCommandName(token string) string {
	t := stripEmbeddedQuotes(strings.TrimSpace(token))
	if !strings.Contains(t, "/") {
		return strings.ToLower(t)
	}
	if standardBinDirs[filepath.Dir(t)] {
		return strings.ToLower(filepath.Base(t))
	}
	return ""
}

// normalizeToken trims whitespace, lowercases the command name, and strips a
// leading path prefix (e.g. /usr/bin/git → git).
func normalizeToken(token string) string {
	t := strings.TrimSpace(token)
	t = stripEmbeddedQuotes(t)
	// Strip path prefix: keep the base name of the command.
	if strings.Contains(t, "/") {
		t = filepath.Base(t)
	}
	t = strings.ToLower(t)
	return t
}

// stripEmbeddedQuotes removes quote *delimiters* (not their contents) from a
// single word — the same "quote removal" step a real shell performs before
// treating adjacent quoted/unquoted fragments as one word. tokenize()
// preserves quote characters verbatim in its output (they matter for
// splitting, not for meaning), so without this step a command name spliced
// across empty quotes to dodge a literal-string match — r”m, r"m", 'r'm,
// all real bash for "rm" — normalizes to a string like "r”m" that matches
// neither the SAFE list nor any dangerous/destructive token, silently
// falling through to non-deterministic LLM judgment instead of the
// deterministic block/escalation an unobfuscated "rm" gets. This only
// undoes quoting within one already-tokenized word; it is not a shell
// parser and doesn't attempt variable expansion, command substitution, or
// cross-token effects — those are refused outright elsewhere (see
// hasCommandSubstitution) rather than interpreted.
func stripEmbeddedQuotes(token string) string {
	var b strings.Builder
	i, n := 0, len(token)
	for i < n {
		c := token[i]
		switch c {
		case '\'', '"':
			quote := c
			i++
			for i < n && token[i] != quote {
				b.WriteByte(token[i])
				i++
			}
			if i < n {
				i++ // skip the closing quote
			}
		case '\\':
			// Defensive: tokenize() already drops an unquoted backslash at
			// the top level, so none should remain here in practice — kept
			// idempotent in case a caller ever normalizes a raw token
			// directly instead of one that already went through tokenize().
			if i+1 < n {
				b.WriteByte(token[i+1])
				i += 2
			} else {
				i++
			}
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}
