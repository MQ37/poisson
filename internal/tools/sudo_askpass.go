package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/mq37/poisson/internal/guard"
)

// SudoPasswordFn prompts for the sudo password a host bash command needs
// (see guard.RequiresSudoPassword), called only after that command's own
// risk approval already passed. ok is false on cancel — treated exactly
// like a denied tool call. The returned bytes must never be logged, put in
// a ToolResult, or handed to a provider; BashTool's only use of them is
// newSudoAskpassHelper below, and it zeroes its own copy immediately after
// writing the one-shot askpass file.
type SudoPasswordFn func(ctx context.Context, command, description, workdir string) (password []byte, ok bool)

// sudoLeadRe matches a segment's leading env-assignments (FOO=bar ...)
// followed by a bare sudo invocation. Detection of WHETHER a command needs
// rewriting at all is guard.RequiresSudoPassword's job (exact,
// tokenizer-based, case-insensitive via normalizeToken; pkexec deliberately
// excluded there — see its own doc comment); this regex is a best-effort
// rewrite of whatever matched that shape. A segment guard flagged but this
// doesn't match is left untouched — sudo then still demands a terminal and
// the command errors out, never silently insecure.
var sudoLeadRe = regexp.MustCompile(`(?i)^(\s*(?:[A-Za-z_][A-Za-z0-9_]*=\S*\s+)*)(sudo)\b`)

// injectSudoAskpass rewrites every bare sudo segment in command to pass
// "-A -k": -A so sudo reads SUDO_ASKPASS instead of demanding a controlling
// terminal (host bash execs with none — see BashTool.Execute); -k so it
// actually re-checks the password every single time instead of silently
// reusing a cached credential ticket from an earlier bash call. Found by
// hand: sudo's timestamp ticket is keyed to the session/controlling
// terminal, not the specific child process — px's own tty is that same
// session for every bash call it makes, so two SEPARATE bash-tool calls
// both inherit it. Without -k, a first correct password primes a ticket
// (usually ~15 min) that a SECOND call's password — right OR wrong — never
// actually gets checked against, since sudo just reuses the still-valid
// ticket and skips askpass entirely. -k forces sudo to invalidate that
// ticket up front, so this feature's own "prompt fresh every call" model
// (bash is stateless — see BashTool's own doc comment) is matched by sudo
// actually verifying fresh every call too.
//
// Segment boundaries and byte offsets in the original string are found the
// same way guard.HighlightSpans does: sequential strings.Index against the
// segments guard.Segments already split out. A segment that doesn't
// reappear verbatim (e.g. flattened out of a "(...)" group) is left
// unrewritten rather than guessed at.
func injectSudoAskpass(command string) string {
	var b strings.Builder
	pos := 0
	for _, seg := range guard.Segments(command) {
		idx := strings.Index(command[pos:], seg)
		if idx < 0 {
			continue
		}
		b.WriteString(command[pos : pos+idx])
		if loc := sudoLeadRe.FindStringSubmatchIndex(seg); loc != nil {
			insertAt := loc[5] // end of the sudo match itself
			b.WriteString(seg[:insertAt])
			b.WriteString(" -A -k")
			b.WriteString(seg[insertAt:])
		} else {
			b.WriteString(seg)
		}
		pos += idx + len(seg)
	}
	b.WriteString(command[pos:])
	return b.String()
}

// sudoAskpassHelper is the on-disk material one sudo-needing bash call gets
// turned into: a private 0700 directory holding a 0600 file with the raw
// password bytes and a 0700 script that just cats it — set as SUDO_ASKPASS
// so `sudo -A` reads the file instead of a terminal. Scoped to exactly one
// BashTool.Execute call: created right before cmd.Run(), cleaned up right
// after, never persisted or reused across calls (bash is stateless anyway).
//
// An earlier version passed the password through an anonymous pipe (fd 3,
// via cmd.ExtraFiles) instead of a file, specifically to close off the risk
// documented below. That broke in practice: sudo sanitizes file descriptors
// before exec'ing its own askpass helper on at least some builds/distros
// (closefrom-style hardening), closing fd 3 before the script ever runs —
// "Bad file descriptor" reading it, sudo fails outright. A plain file is
// what virtually every real askpass-style tool uses (git-credential
// helpers, ssh-askpass wrappers, ansible's --ask-become-pass, ...)
// precisely because it works across every sudo build with no fd-inheritance
// assumptions — reverted to it for that reason.
//
// Residual risk, accepted rather than hidden: the password file is
// same-uid-readable for this whole command's runtime, so a SIBLING process
// the SAME approved command spawns (e.g. a background `find /tmp -iname
// '*pw*'`) could in principle read it and echo it back into the tool's own
// stdout before sudo does. Mitigated as far as is practical without
// OS-level per-process isolation (SELinux/AppArmor, out of scope for a
// dependency-tiny CLI): private 0700 dir, 0600 file, a random (not
// guessable/grep-pattern-matching) filename, and a lifetime bounded to
// exactly this one command's run. Not eliminated: a command specifically
// crafted to race for it can still win. That same command already runs
// with the human's full approval as their own user, with equal access to
// SSH keys, cloud credentials, shell history, and everything else in their
// home directory — the sudo password is a marginal addition to an
// already-complete trust boundary, not a new one.
type sudoAskpassHelper struct {
	dir        string
	pwPath     string
	scriptPath string
}

// newSudoAskpassHelper never leaves password's own bytes copied anywhere
// but this one file — no env var (visible to the whole command tree via a
// plain `env`), no argv (visible via ps/proc to any same-user process).
func newSudoAskpassHelper(password []byte) (*sudoAskpassHelper, error) {
	dir, err := os.MkdirTemp("", "poisson-sudo-")
	if err != nil {
		return nil, fmt.Errorf("sudo askpass tmpdir: %w", err)
	}
	h := &sudoAskpassHelper{
		dir:        dir,
		pwPath:     filepath.Join(dir, randomFileName()),
		scriptPath: filepath.Join(dir, "askpass.sh"),
	}
	if err := os.WriteFile(h.pwPath, password, 0o600); err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("sudo askpass password file: %w", err)
	}
	script := "#!/bin/sh\nexec cat " + shellSingleQuote(h.pwPath) + "\n"
	if err := os.WriteFile(h.scriptPath, []byte(script), 0o700); err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("sudo askpass script: %w", err)
	}
	return h, nil
}

// randomFileName avoids an obviously secret-shaped name ("pw", "password")
// that a naive same-uid scanner might specifically grep for — see
// sudoAskpassHelper's own doc comment on why this is a mitigation, not a
// fix, for the residual same-uid race.
func randomFileName() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "pw" // crypto/rand failing is exceptional; fall back rather than error the whole call
	}
	return hex.EncodeToString(b[:])
}

// cleanup zeroes the password file's on-disk bytes before removing the
// whole directory. Best-effort: Go's own string/GC internals mean nothing
// here can guarantee the password never lingered in a memory page, but this
// keeps a stray disk snapshot/backup from ever seeing plaintext past this
// one call, and bounds the file's live window to this command's own run
// (capped by its timeout — default 120s, whatever the caller passed).
func (h *sudoAskpassHelper) cleanup() {
	if info, err := os.Stat(h.pwPath); err == nil {
		_ = os.WriteFile(h.pwPath, make([]byte, info.Size()), 0o600)
	}
	os.RemoveAll(h.dir)
}

func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
