package guard

import "regexp"

// Safe command prefixes (from SPEC §7.2).
// A command is auto-allowed if its normalized first token (plus subcommand
// for commands that have subcommands in the list) prefix-matches one of these.
var SAFE = []string{
	// git subcommands
	"git status",
	"git diff",
	"git log",
	"git show",
	"git branch",
	"git remote",
	"git stash list",
	"git stash show",
	"git rev-parse",
	"git describe",
	"git shortlog",
	"git blame",
	"git ls-files",
	"git ls-tree",
	"git tag",
	"git grep",
	"git worktree list",
	// file utilities
	"cat",
	"head",
	"tail",
	"wc",
	"file",
	"stat",
	// checksums
	"md5sum",
	"sha256sum",
	"sha1sum",
	"sha512sum",
	"diff",
	"cmp",
	"od",
	"xxd",
	// filesystem
	"mkdir",
	"touch",
	// search
	"grep",
	"rg",
	"find",
	"which",
	"whereis",
	"locate",
	"type",
	// listing
	"ls",
	"tree",
	"pwd",
	// navigation
	"cd",
	"pushd",
	"popd",
	"dirs",
	"du",
	"df",
	"realpath",
	"readlink",
	// path utilities
	"dirname",
	"basename",
	// package managers (read-only)
	"npm list",
	"npm view",
	"npm outdated",
	"npm explain",
	"pnpm list",
	"pnpm view",
	"pnpm outdated",
	"yarn list",
	"yarn info",
	"yarn outdated",
	// data tools
	"jq",
	"yq",
	"sed",
	// github cli (read-only)
	"gh pr list",
	"gh pr view",
	"gh pr diff",
	"gh issue list",
	"gh issue view",
	"gh pr checks",
	"gh api",
	"gh repo view",
	// system info
	"uname",
	"date",
	"whoami",
	"id",
	"hostname",
	"uptime",
	"ps",
	// misc
	"echo",
}

// shellInterpreterNames lists shell binaries: flagged as dangerousTokens
// below, and reused as-is by guard.go's shellInterpreters (which looks one
// level into a "sh -c '...'"-style wrapped command). Single source — these
// used to be two hand-maintained copies that could silently drift apart.
var shellInterpreterNames = []string{"bash", "sh", "zsh", "dash", "ksh", "fish"}

// dangerousTokens is the set of tokens that red-flag a command (SPEC §7.3).
var dangerousTokens = stringSet(shellInterpreterNames, map[string]bool{
	"parallel": true,
	"eval":     true,
	"exec":     true,
	"source":   true,
	"python":   true,
	"python2":  true,
	"python3":  true,
	"node":     true,
	"nodejs":   true,
	"ruby":     true,
	"perl":     true,
	"php":      true,
	"lua":      true,
	"curl":     true,
	"wget":     true,
	"nc":       true,
	"netcat":   true,
	"ncat":     true,
	"socat":    true,
	"openssl":  true,
	"su":       true,
	"doas":     true,
	"chmod":    true,
	"chown":    true,
	"mv":       true,
	"cp":       true,
	"ln":       true,
	"umask":    true,
	"unshare":  true,
	"nsenter":  true,
	"chroot":   true,
	"base64":   true,
	"uuencode": true,
	"uudecode": true,
})

// stringSet builds a map[string]bool from names, merged into extra (extra is
// mutated and returned — callers pass a fresh literal, never a shared map).
func stringSet(names []string, extra map[string]bool) map[string]bool {
	for _, n := range names {
		extra[n] = true
	}
	return extra
}

// destructiveCommands is the set of commands that always require approval
// (SPEC §7.4).
var destructiveCommands = map[string]bool{
	"rm":       true,
	"rmdir":    true,
	"dd":       true,
	"shred":    true,
	"mkfs":     true,
	"mke2fs":   true,
	"fdisk":    true,
	"parted":   true,
	"sfdisk":   true,
	"cfdisk":   true,
	"wipefs":   true,
	"truncate": true,
	"unlink":   true,
}

// sensitiveExactBasenames is the set of basenames that are always sensitive
// (SPEC §7.5), even as a bare relative token with no directory component —
// their name alone is enough (private keys, shell history, dotenv files).
var sensitiveExactBasenames = map[string]bool{
	".bash_history":    true,
	".zsh_history":     true,
	".bashrc":          true,
	".zshrc":           true,
	".profile":         true,
	".bash_profile":    true,
	".npmrc":           true,
	".yarnrc":          true,
	".git-credentials": true,
	".netrc":           true,
	".env":             true,
	".env.local":       true,
	".env.production":  true,
	".env.development": true,
	"authorized_keys":  true,
	"known_hosts":      true,
	"id_rsa":           true,
	"id_ecdsa":         true,
	"id_ed25519":       true,
	"id_dsa":           true,
	// Common secret-looking basenames outside the exact ".env*" set.
	"secrets.env":                          true,
	"secret.env":                           true,
	"secrets.yml":                          true,
	"secrets.yaml":                         true,
	"secrets.json":                         true,
	"serviceaccount.json":                  true,
	"application_default_credentials.json": true,
}

// contextSensitiveBasenames are only sensitive when the resolved path sits
// inside a sensitive directory (or the token itself carries a sensitive
// dir component). Bare `cat credentials` in a normal project workdir is
// fine; `cd ~/.aws && cat credentials` / workdir=~/.aws is not. Keeping
// these out of sensitiveExactBasenames avoids flagging every `echo
// credentials` / project file coincidentally named "token".
var contextSensitiveBasenames = map[string]bool{
	"credentials":      true,
	"credentials.db":   true,
	"auth.json":        true,
	"passwd":           true,
	"shadow":           true,
	"sudoers":          true,
	"token":            true,
	"access-tokens.db": true,
	"hosts.yml":        true,
	"config":           true, // ~/.ssh/config, ~/.kube/config
}

// sensitiveDirPatterns is the set of directory patterns that are sensitive
// (SPEC §7.5). Matched as substrings against a slash-normalized path.
var sensitiveDirPatterns = []string{
	"/.ssh/",
	"/.aws/",
	"/.config/gcloud/",
	"/.config/gh/",
	"/.gnupg/",
	"/etc/passwd",
	"/etc/shadow",
	"/etc/sudoers",
	"/.docker/",
	"/.kube/",
	"/.poisson/", // px's own config, OAuth tokens (auth.json), and session DB
	"/proc/self/environ",
	"/proc/1/environ",
	"/var/run/secrets/",
	"/run/secrets/",
}

// sshPrivKeyRe matches SSH private key filenames, including FIDO/sk variants
// (id_ed25519_sk, id_ecdsa_sk) whose trailing "_sk" the older `$` anchor missed.
// ansiEscapeRe matches ANSI escape sequences.
var ansiEscapeRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

var sshPrivKeyRe = regexp.MustCompile(`_(rsa|ecdsa|ed25519|dsa)(_sk)?$`)
