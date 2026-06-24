package guard

import "testing"

func TestIsAllSafe_SafeCommands(t *testing.T) {
	safe := []string{
		"git status",
		"git diff HEAD",
		"git log --oneline -5",
		"git show HEAD",
		"git branch -a",
		"git remote -v",
		"ls -la",
		"ls",
		"cat README.md",
		"cat /etc/hostname",
		"head -n 10 file.txt",
		"tail -n 20 file.txt",
		"wc -l *.go",
		"echo hello world",
		"pwd",
		"mkdir -p build/tmp",
		"touch file.txt",
		"grep -r foo .",
		"rg 'TODO' .",
		"find . -name '*.go'",
		"which go",
		"tree -L 2",
		"uname -a",
		"date",
		"whoami",
		"id",
		"hostname",
		"uptime",
		"du -sh .",
		"df -h",
		"realpath ./foo",
		"readlink -f link",
		"dirname /a/b/c",
		"basename /a/b/c.txt",
		"md5sum file",
		"sha256sum file",
		"diff a b",
		"cmp a b",
		"od -x file",
		"xxd file",
		"jq '.' file.json",
		"yq '.' file.yaml",
		"npm list",
		"npm view react version",
		"gh pr list",
		"gh pr view 123",
		"gh issue list",
		"gh api repos/foo",
		"git stash list",
		"git rev-parse HEAD",
		"git describe --tags",
		"git blame file.go",
		"git ls-files",
	}
	for _, cmd := range safe {
		if !IsAllSafe(cmd) {
			t.Errorf("expected safe: %q", cmd)
		}
	}
}

func TestIsAllSafe_UnsafeCommands(t *testing.T) {
	unsafe := []string{
		"rm -rf /",
		"rm -rf node_modules",
		"rmdir dir",
		"dd if=/dev/zero of=/dev/sda",
		"shred file",
		"mkfs.ext4 /dev/sda",
		"curl http://example.com",
		"wget http://example.com",
		"eval 'rm -rf /'",
		"exec bash",
		"source script.sh",
		"bash script.sh",
		"sh -c 'rm -rf /'",
		"python -c 'import os; os.system(\"rm -rf /\")'",
		"node script.js",
		"ruby script.rb",
		"perl -e 'system(\"rm\")'",
		"nc localhost 80",
		"netcat localhost 80",
		"openssl s_client -connect host:443",
		"chmod 777 file",
		"chown root file",
		"mv file /elsewhere",
		"cp secret /tmp",
		"ln -s /etc/passwd link",
		"base64 /etc/shadow",
		"truncate -s 0 file",
		"unlink file",
		"parted /dev/sda",
	}
	for _, cmd := range unsafe {
		if IsAllSafe(cmd) {
			t.Errorf("expected unsafe: %q", cmd)
		}
	}
}

func TestIsAllSafe_EdgeCases(t *testing.T) {
	// Semicolons: safe + unsafe → unsafe.
	if IsAllSafe("git status; rm -rf /") {
		t.Error("expected unsafe for compound command with semicolon")
	}
	// Pipe into safe command is fine.
	if !IsAllSafe("git status | head -5") {
		t.Error("expected safe for pipe into head")
	}
	// Pipe into dangerous shell.
	if IsAllSafe("ls | bash") {
		t.Error("expected unsafe for pipe into bash")
	}
	if IsAllSafe("echo foo | sh") {
		t.Error("expected unsafe for pipe into sh")
	}
	// ANSI escape sequences.
	if IsAllSafe("echo \x1b[31mred\x1b[0m") {
		t.Error("expected unsafe for ANSI escape")
	}
	// Redirect to file.
	if IsAllSafe("ls > file.txt") {
		t.Error("expected unsafe for redirect")
	}
	if IsAllSafe("echo foo >> file.txt") {
		t.Error("expected unsafe for append redirect")
	}
	// Sensitive paths.
	if IsAllSafe("cat ~/.ssh/id_rsa") {
		t.Error("expected unsafe for ssh private key")
	}
	if IsAllSafe("cat /etc/passwd") {
		t.Error("expected unsafe for /etc/passwd")
	}
	if IsAllSafe("cat /.ssh/config") {
		t.Error("expected unsafe for /.ssh/")
	}
	if IsAllSafe("cat ~/.aws/credentials") {
		t.Error("expected unsafe for /.aws/")
	}
	// .env files.
	if IsAllSafe("cat .env") {
		t.Error("expected unsafe for .env")
	}
	if IsAllSafe("cat .env.local") {
		t.Error("expected unsafe for .env.local")
	}
	// .bash_history.
	if IsAllSafe("cat ~/.bash_history") {
		t.Error("expected unsafe for .bash_history")
	}
	// Git mutating subcommands.
	if IsAllSafe("git push origin main") {
		t.Error("expected unsafe for git push")
	}
	if IsAllSafe("git commit -m 'msg'") {
		t.Error("expected unsafe for git commit")
	}
	if IsAllSafe("git reset --hard") {
		t.Error("expected unsafe for git reset")
	}
	if IsAllSafe("git clean -fd") {
		t.Error("expected unsafe for git clean")
	}
	// gh api mutating.
	if IsAllSafe("gh api --method POST /repos") {
		t.Error("expected unsafe for gh api POST")
	}
	// sed in-place.
	if IsAllSafe("sed -i 's/foo/bar/' file") {
		t.Error("expected unsafe for sed -i")
	}
	// yq in-place.
	if IsAllSafe("yq -i '.' file.yaml") {
		t.Error("expected unsafe for yq -i")
	}
	// tail -f.
	if IsAllSafe("tail -f file.txt") {
		t.Error("expected unsafe for tail -f")
	}
	// find -exec.
	if IsAllSafe("find . -exec rm {} \\;") {
		t.Error("expected unsafe for find -exec")
	}
	if IsAllSafe("find . -name '*.tmp' -delete") {
		t.Error("expected unsafe for find -delete")
	}
}

func TestIsAllSafe_Sandbox(t *testing.T) {
	t.Setenv("POISSON_SANDBOX", "1")
	if !IsAllSafe("rm -rf /") {
		t.Error("expected safe in sandbox mode")
	}
}

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
