package testutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTempDirUnderTmp(t *testing.T) {
	dir := TempDir(t)
	if !strings.HasPrefix(dir, tmpRoot+string(os.PathSeparator)) {
		t.Fatalf("TempDir = %q, want under %q", dir, tmpRoot)
	}
	path := filepath.Join(dir, "probe.txt")
	if err := os.WriteFile(path, []byte("ok"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestTempHomeUnderTmp(t *testing.T) {
	home := TempHome(t)
	if os.Getenv("HOME") != home {
		t.Fatalf("HOME = %q, want %q", os.Getenv("HOME"), home)
	}
	if !strings.HasPrefix(home, tmpRoot+string(os.PathSeparator)) {
		t.Fatalf("TempHome = %q, want under %q", home, tmpRoot)
	}
}