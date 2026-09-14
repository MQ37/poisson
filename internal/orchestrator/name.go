package orchestrator

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// instanceNamePrefix marks every instance this package creates — mirrors
// sandbox.ResolveSandboxName's "px-sandbox-" prefix, shortened since nspawn
// machine names are hostnames and must stay well under RFC1123's 64-char
// ceiling with room to spare for unit/rootfs path suffixes.
const instanceNamePrefix = "px-"

// maxBodyLen caps the sanitized portion (after instanceNamePrefix) at 30
// characters, so ResolveInstanceName's output always matches
// ^px-[a-z0-9][a-z0-9-]{0,28}[a-z0-9]$ — the format every nspawn machine
// name, systemd unit name, and rootfs path component built from it assumes.
const maxBodyLen = 30

// ResolveInstanceName turns a requested instance name (e.g. from a Telegram
// /new <name> command — untrusted input) into the sanitized, px-prefixed
// form every downstream consumer (nspawn machine name, systemd unit name,
// rootfs path, Destroy's path-safety check) assumes. Unlike
// sandbox.ResolveSandboxName, underscores are mapped to '-' rather than
// kept: nspawn machine names must be valid RFC1123 hostnames (no '_'),
// whereas podman container names permit it — see
// docs/orchestrator-plan.md §4 reuse decision 9.
//
// requested == "" (or anything that sanitizes down to nothing, e.g. an
// all-punctuation or emoji-only string) falls back to a random
// 4-hex-character name, matching ResolveSandboxName's own fallback shape.
func ResolveInstanceName(requested string) (string, error) {
	body := sanitizeInstanceName(requested)
	if len(body) < 2 {
		// Either genuinely empty input, or sanitization reduced it below
		// the two-character minimum the name format requires (a lone
		// surviving character can't supply both a first and last char) —
		// both cases get the same random fallback.
		suffix, err := randomHex(4)
		if err != nil {
			return "", fmt.Errorf("generate instance name: %w", err)
		}
		return instanceNamePrefix + suffix, nil
	}
	return instanceNamePrefix + body, nil
}

// sanitizeInstanceName keeps only lowercase alnum (mapping every other
// character — including '_', punctuation, emoji, whitespace — to '-'),
// collapses repeated '-' runs down to one, trims leading/trailing '-', and
// caps the result at maxBodyLen characters (re-trimming any '-' truncation
// exposes at the cut point).
func sanitizeInstanceName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	out := strings.Trim(collapseDashes(b.String()), "-")
	if len(out) > maxBodyLen {
		out = strings.Trim(out[:maxBodyLen], "-")
	}
	return out
}

// collapseDashes replaces every run of one or more '-' with a single '-'.
func collapseDashes(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		if r == '-' {
			if prevDash {
				continue
			}
			prevDash = true
		} else {
			prevDash = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

// instanceNameFormat is the exact contract every already-resolved instance
// name satisfies — see ResolveInstanceName's own doc comment for how it
// gets there.
var instanceNameFormat = regexp.MustCompile(`^px-[a-z0-9][a-z0-9-]{0,28}[a-z0-9]$`)

// ValidateInstanceName re-checks that name is already in
// ResolveInstanceName's sanitized form. Used by destructive operations
// (nspawn's Destroy) as a second, independent check before name is used to
// build any rm -rf-reachable path — name is transitively untrusted input
// (it ultimately comes from Telegram message text), so every consumer that
// turns it into a filesystem path treats it that way, not just the one
// place that first sanitized it.
func ValidateInstanceName(name string) error {
	if !instanceNameFormat.MatchString(name) {
		return fmt.Errorf("invalid instance name %q: must match %s", name, instanceNameFormat)
	}
	return nil
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
