package guard

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolveSymlinkTarget_FollowsToRealPath verifies a symlink chain
// resolves to its real target.
func TestResolveSymlinkTarget_FollowsToRealPath(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.txt")
	if err := os.WriteFile(real, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "innocuous.txt")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	got := ResolveSymlinkTarget(link)
	realResolved, _ := filepath.EvalSymlinks(real)
	if got != realResolved {
		t.Errorf("ResolveSymlinkTarget(%q) = %q, want %q", link, got, realResolved)
	}
}

// TestResolveSymlinkTarget_NonexistentLeafUsesParent verifies a not-yet
// existing file (e.g. a write/touch target) still resolves through a
// symlinked parent directory.
func TestResolveSymlinkTarget_NonexistentLeafUsesParent(t *testing.T) {
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real_dir")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(dir, "link_dir")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(linkDir, "new_file.txt") // doesn't exist yet
	got := ResolveSymlinkTarget(target)
	want := filepath.Join(realDir, "new_file.txt")
	wantResolved, _ := filepath.EvalSymlinks(filepath.Dir(want))
	want = filepath.Join(wantResolved, "new_file.txt")
	if got != want {
		t.Errorf("ResolveSymlinkTarget(%q) = %q, want %q", target, got, want)
	}
}

// TestResolveSymlinkTarget_NoSymlinkReturnsCleanPath verifies a plain path
// with no symlink involved resolves to itself (modulo Clean).
func TestResolveSymlinkTarget_NoSymlinkReturnsCleanPath(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "plain.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ResolveSymlinkTarget(f); got != f {
		t.Errorf("ResolveSymlinkTarget(%q) = %q, want unchanged", f, got)
	}
}

// TestResolveSymlinkTarget_GarbagePathReturnsUnchanged verifies a bogus,
// non-path-like string (a regex pattern, not a real file) doesn't error out
// or panic — it just comes back unchanged.
func TestResolveSymlinkTarget_GarbagePathReturnsUnchanged(t *testing.T) {
	garbage := "not\x00a-real/path??$$"
	if got := ResolveSymlinkTarget(garbage); got != garbage {
		t.Errorf("ResolveSymlinkTarget(garbage) = %q, want unchanged", got)
	}
}

// TestSensitivePathReason_FollowsSymlink is the actual attack this closes:
// a file with a harmless name that's secretly a symlink into ~/.ssh must
// still be flagged sensitive via its resolved target.
func TestSensitivePathReason_FollowsSymlink(t *testing.T) {
	dir := t.TempDir()
	sshDir := filepath.Join(dir, "home", ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	realKey := filepath.Join(sshDir, "id_rsa")
	if err := os.WriteFile(realKey, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	notesLink := filepath.Join(dir, "notes.txt")
	if err := os.Symlink(realKey, notesLink); err != nil {
		t.Fatal(err)
	}

	if r := SensitivePathReason(notesLink); r == "" {
		t.Fatal("expected notes.txt (symlinked to id_rsa) to be flagged sensitive")
	}

	// A harmless symlink to a harmless file must not be flagged.
	harmless := filepath.Join(dir, "harmless.txt")
	if err := os.WriteFile(harmless, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	harmlessLink := filepath.Join(dir, "harmless_link.txt")
	if err := os.Symlink(harmless, harmlessLink); err != nil {
		t.Fatal(err)
	}
	if r := SensitivePathReason(harmlessLink); r != "" {
		t.Errorf("expected harmless symlink to not be flagged, got %q", r)
	}
}

// TestTouchesSensitivePath_FollowsSymlinkWithWorkdir verifies the bash-guard
// path (a relative token resolved against workdir) also follows a symlink
// escape — the exact scenario that would otherwise let an auto-safe `cat`
// read a credential file through an innocently-named symlink.
func TestTouchesSensitivePath_FollowsSymlinkWithWorkdir(t *testing.T) {
	dir := t.TempDir()
	awsDir := filepath.Join(dir, "secrets", ".aws")
	if err := os.MkdirAll(awsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	credsFile := filepath.Join(awsDir, "credentials")
	if err := os.WriteFile(credsFile, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A relative, innocuous-looking symlink inside the sandboxed workdir.
	if err := os.Symlink(credsFile, filepath.Join(dir, "config.txt")); err != nil {
		t.Fatal(err)
	}

	if !touchesSensitivePath([]string{"config.txt"}, dir) {
		t.Fatal("expected config.txt (symlinked to ~/.aws/credentials) to be caught via workdir-relative resolution")
	}
	// Without a workdir, resolution falls back to the process cwd (which
	// isn't dir) — the literal name alone isn't sensitive, matching the
	// pre-existing (pre-symlink-check) behavior exactly.
	if touchesSensitivePath([]string{"config.txt"}, "") {
		t.Error("expected config.txt with no workdir context to not resolve into the sandbox dir by accident")
	}
}

// TestClassifyInDir_BlocksSymlinkEscape is the end-to-end version: a plain
// `cat` of a symlinked file must not be auto-safe when the symlink points at
// a sensitive path.
func TestClassifyInDir_BlocksSymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	envDir := filepath.Join(dir, "secrets")
	if err := os.MkdirAll(envDir, 0o700); err != nil {
		t.Fatal(err)
	}
	realEnv := filepath.Join(envDir, ".env")
	if err := os.WriteFile(realEnv, []byte("SECRET=1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realEnv, filepath.Join(dir, "readme.txt")); err != nil {
		t.Fatal(err)
	}

	safe, reason := ClassifyInDir("cat readme.txt", dir)
	if safe {
		t.Fatal("expected cat of a symlink into .env to be unsafe")
	}
	if reason == "" {
		t.Error("expected a non-empty reason")
	}
}
