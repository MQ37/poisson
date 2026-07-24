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

// dangerousTokens is the set of tokens that red-flag a command (SPEC §7.3).
var dangerousTokens = map[string]bool{
	"parallel": true,
	"eval":     true,
	"exec":     true,
	"source":   true,
	"bash":     true,
	"sh":       true,
	"zsh":      true,
	"dash":     true,
	"ksh":      true,
	"fish":     true,
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
// (SPEC §7.5).
var sensitiveExactBasenames = map[string]bool{
	".bash_history":    true,
	".zsh_history":     true,
	".bashrc":          true,
	".zshrc":           true,
	".profile":         true,
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
}

// sensitiveDirPatterns is the set of directory patterns that are sensitive
// (SPEC §7.5).
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
}

// sshPrivKeyRe matches SSH private key filenames.
// ansiEscapeRe matches ANSI escape sequences.
var ansiEscapeRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

var sshPrivKeyRe = regexp.MustCompile(`_(rsa|ecdsa|ed25519|dsa)$`)
