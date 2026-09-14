package guard

import "testing"

func TestRequiresSudoPassword(t *testing.T) {
	cases := []struct {
		command string
		want    bool
	}{
		{"sudo apt-get update", true},
		{"sudo -u root whoami", true},
		{"pkexec ls /root", false}, // no SUDO_ASKPASS/-A equivalent — see doc comment
		{"FOO=bar sudo whoami", true},
		{"echo hi | sudo tee /etc/foo", true},
		{"ls -la && sudo reboot", true},
		{"echo sudo", false},
		{"ls -la", false},
		{"pseudo-command --flag", false},
		{"echo 'sudo is not run here'", false},
		{"SUDO_ASKPASS=/x echo hi", false},

		// A remote sudo run over ssh is never a LOCAL sudo invocation —
		// this host bash tool has no password to give it (and shouldn't
		// try: the remote machine has its own credential boundary).
		{"ssh host 'sudo apt update'", false},
		{`ssh host "sudo apt update"`, false},
		{"ssh host sudo apt update", false},
		// Regression: a heredoc-delimited remote script used to have its
		// body split line-by-line, exposing "sudo apt update" as if it
		// were its own fresh top-level LOCAL segment — see
		// guard.Segments' heredoc handling.
		{"ssh host <<'EOF'\nsudo apt update\nEOF", false},
		{"ssh host <<EOF\nsudo systemctl restart nginx\nEOF", false},
		{"ssh host <<-EOF\n\tsudo apt update\nEOF", false},
		// A genuinely local sudo call preceded by an unrelated heredoc
		// must still be caught — heredoc-awareness must not blind the
		// scanner to a real local sudo elsewhere in the same command.
		{"cat <<'EOF'\nnot a secret\nEOF\nsudo whoami", true},
	}
	for _, c := range cases {
		if got := RequiresSudoPassword(c.command); got != c.want {
			t.Errorf("RequiresSudoPassword(%q) = %v, want %v", c.command, got, c.want)
		}
	}
}
