package guard

// RequiresSudoPassword reports whether command contains a bare sudo
// invocation as some segment's leading command word (past any leading
// env-var assignments) — the shape the host bash tool has no way to
// satisfy without a password prompt (no terminal, no cached credential).
// Used only for the host exec path; the sandbox grants passwordless sudo
// at bootstrap instead (see podman_driver.go), so this never applies there.
//
// pkexec (same "escalation" danger class as sudo for highlighting — see
// classifyTokenDanger) is deliberately NOT matched here: it has no
// SUDO_ASKPASS/-A equivalent at all (PolicyKit's agent talks to a real
// tty/GUI directly, no documented non-interactive override), so there is
// nothing this package's rewrite could do for it. A bare pkexec just fails
// the same way it always did before this feature existed — not worse, just
// unimproved.
func RequiresSudoPassword(command string) bool {
	for _, seg := range Segments(command) {
		if segmentLeadsWithSudo(seg) {
			return true
		}
	}
	return false
}

// segmentLeadsWithSudo reports whether seg's first non-env-assignment token
// normalizes to sudo — restricted to leading position since this drives a
// command rewrite (inject -A -k), not just a display span.
func segmentLeadsWithSudo(seg string) bool {
	for _, tok := range tokenize(seg) {
		if isEnvAssignment(tok) {
			continue
		}
		return normalizeToken(tok) == "sudo"
	}
	return false
}
