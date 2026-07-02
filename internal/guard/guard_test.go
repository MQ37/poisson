package guard

import "testing"

func TestClassify_Reason(t *testing.T) {
	safe, reason := Classify("rm -rf /")
	if safe {
		t.Error("expected unsafe")
	}
	if reason == "" {
		t.Error("expected non-empty reason")
	}
	safe, _ = Classify("git status")
	if !safe {
		t.Error("expected safe")
	}
}

func TestSegments(t *testing.T) {
	tests := []struct {
		cmd  string
		want []string
	}{
		{"ls -la", []string{"ls -la"}},
		{"git status; ls -la", []string{"git status", "ls -la"}},
		{"a && b", []string{"a", "b"}},
		{"a || b", []string{"a", "b"}},
		{"a | b", []string{"a", "b"}},
		{"echo 'hello; world'", []string{"echo 'hello; world'"}},
		{`echo "a | b"`, []string{`echo "a | b"`}},
		{"a; b; c", []string{"a", "b", "c"}},
	}
	for _, tc := range tests {
		got := Segments(tc.cmd)
		if len(got) != len(tc.want) {
			t.Errorf("Segments(%q) = %v, want %v", tc.cmd, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("Segments(%q)[%d] = %q, want %q", tc.cmd, i, got[i], tc.want[i])
			}
		}
	}
}

func TestSegments_Parentheses(t *testing.T) {
	// Parens at top level split; inside parens don't split.
	got := Segments("echo a; (echo b; echo c)")
	if len(got) != 2 {
		t.Errorf("expected 2 segments, got %d: %v", len(got), got)
	}
}

func TestNormalizeToken(t *testing.T) {
	tests := []struct {
		in, out string
	}{
		{"git", "git"},
		{"GIT", "git"},
		{"/usr/bin/git", "git"},
		{"'git'", "git"},
		{`"git"`, "git"},
	}
	for _, tc := range tests {
		got := normalizeToken(tc.in)
		if got != tc.out {
			t.Errorf("normalizeToken(%q) = %q, want %q", tc.in, got, tc.out)
		}
	}
}

func TestContainsAnsiEscape(t *testing.T) {
	if !containsAnsiEscape("echo \x1b[31mred\x1b[0m") {
		t.Error("expected true for ANSI escape")
	}
	if containsAnsiEscape("echo hello") {
		t.Error("expected false for plain text")
	}
}

func TestTouchesSensitivePath(t *testing.T) {
	tests := []string{
		"~/.ssh/id_rsa",
		"~/.ssh/config",
		"~/.aws/credentials",
		"/etc/passwd",
		"~/.bash_history",
		"~/.npmrc",
		"id_ed25519",
	}
	for _, p := range tests {
		if !touchesSensitivePath([]string{p}) {
			t.Errorf("expected sensitive: %s", p)
		}
	}
	if touchesSensitivePath([]string{"README.md"}) {
		t.Error("expected README.md to not be sensitive")
	}
}
