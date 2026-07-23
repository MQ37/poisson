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

// touchesSensitivePath reports whether any token references a sensitive
// path, either directly or through a symlink that resolves into one. workdir
// (may be "") resolves relative tokens for symlink lookup only — the plain
// literal-path checks below are workdir-independent, matching prior
// behavior exactly when workdir is empty.
func touchesSensitivePath(tokens []string, workdir string) bool {
	for _, t := range tokens {
		orig := t
		t = strings.Trim(t, "'\"")
		if pathTextIsSensitive(t) {
			return true
		}
		expanded := t
		// Check expanded home-dir form.
		if strings.HasPrefix(orig, "~") {
			expanded = os.ExpandEnv(strings.Replace(orig, "~", "$HOME", 1))
			if pathTextIsSensitive(expanded) {
				return true
			}
		}
		// Resolve symlinks: a token that looks harmless by name (including a
		// bare relative filename with no "/", e.g. "notes.txt") can still
		// point at a sensitive file. Only flags are skipped — everything
		// else gets a cheap stat attempt; a token that isn't really a path
		// (a grep pattern, a hostname, ...) just fails fast (ENOENT) and
		// falls through unchanged, same as the literal check already does.
		if expanded == "" || strings.HasPrefix(expanded, "-") {
			continue
		}
		candidate := expanded
		if !filepath.IsAbs(candidate) && workdir != "" {
			candidate = filepath.Join(workdir, candidate)
		}
		if resolved := ResolveSymlinkTarget(candidate); resolved != candidate {
			if pathTextIsSensitive(resolved) {
				return true
			}
		}
	}
	return false
}

// pathTextIsSensitive checks a path string (already quote-trimmed and
// tilde-expanded by the caller) against the basename/directory-pattern
// tables, without touching the filesystem.
func pathTextIsSensitive(t string) bool {
	base := filepath.Base(t)
	if sensitiveExactBasenames[base] {
		return true
	}
	if sshPrivKeyRe.MatchString(base) {
		return true
	}
	for _, pat := range sensitiveDirPatterns {
		if strings.Contains(t, pat) {
			return true
		}
	}
	return false
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
	base := filepath.Base(path)
	switch {
	case sensitiveExactBasenames[base]:
		return "sensitive file: " + base
	case sshPrivKeyRe.MatchString(base):
		return "SSH private key: " + base
	case base == ".env" || strings.HasPrefix(base, ".env."):
		return "environment secrets file: " + base
	}
	slashed := filepath.ToSlash(path)
	for _, pat := range sensitiveDirPatterns {
		if strings.Contains(slashed, pat) {
			return "sensitive path: " + pat
		}
	}
	return ""
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

// ghApiIsMutating detects gh api calls that are not GET (mutations).
// gh api with --method POST/PUT/PATCH/DELETE is mutating.
func ghApiIsMutating(tokens []string) bool {
	for i, t := range tokens {
		if t == "--method" || t == "-X" {
			if i+1 < len(tokens) {
				m := strings.ToUpper(tokens[i+1])
				switch m {
				case "POST", "PUT", "PATCH", "DELETE":
					return true
				}
			}
		}
	}
	return false
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
	// branch with delete/move flags.
	if sub == "branch" {
		for _, t := range tokens[2:] {
			if t == "-d" || t == "-D" || t == "--delete" || t == "-m" || t == "--move" {
				return true
			}
		}
	}
	// tag with delete flag.
	if sub == "tag" {
		for _, t := range tokens[2:] {
			if t == "-d" || t == "--delete" {
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

// normalizeToken trims whitespace, lowercases the command name, and strips a
// leading path prefix (e.g. /usr/bin/git → git).
func normalizeToken(token string) string {
	t := strings.TrimSpace(token)
	t = strings.Trim(t, "'\"")
	// Strip path prefix: keep the base name of the command.
	if strings.Contains(t, "/") {
		t = filepath.Base(t)
	}
	t = strings.ToLower(t)
	return t
}
