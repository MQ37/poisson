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

func TestIsGitCommit(t *testing.T) {
	yes := []string{
		"git commit -m 'fix bug'",
		"git commit --amend",
		"cd /repo && git add -A && git commit -m wip",
		"GIT COMMIT -M X", // tokenize lowercases via normalizeToken
		"/usr/bin/git commit",
		// Global options before the subcommand.
		"git -C /repo commit -m done",
		"git --no-pager commit -m done",
		"git -c user.name=x commit -m done",
		"git -C /repo --no-pager commit",
		// Leading env-assignment prefix.
		"GIT_AUTHOR_NAME=x git commit -m y",
		"FOO=bar BAZ=qux git commit",
		// One level into a shell wrapper.
		`sh -c "git commit -m foo"`,
		`bash -c 'git add -A && git commit -m wip'`,
	}
	for _, cmd := range yes {
		if !IsGitCommit(cmd) {
			t.Errorf("IsGitCommit(%q) = false, want true", cmd)
		}
	}
	no := []string{
		"git status",
		"git commit-msg-lint", // not a separate "commit" token
		"git commit-tree abc123",
		"git log --oneline",
		"echo 'do not run git commit'", // one segment, first token is echo
		"ls -la",
		"make GIT_COMMIT=1 build", // GIT_COMMIT=1 is an arg to make, not an env prefix on git
		"git -C /repo status",
		`sh -c "echo hello"`,
	}
	for _, cmd := range no {
		if IsGitCommit(cmd) {
			t.Errorf("IsGitCommit(%q) = true, want false", cmd)
		}
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
