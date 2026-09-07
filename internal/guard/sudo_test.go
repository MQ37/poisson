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
	}
	for _, c := range cases {
		if got := RequiresSudoPassword(c.command); got != c.want {
			t.Errorf("RequiresSudoPassword(%q) = %v, want %v", c.command, got, c.want)
		}
	}
}
