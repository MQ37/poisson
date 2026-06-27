// Package testutil provides helpers shared across poisson tests.
package testutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const tmpRoot = "/tmp"

// TempDir returns a unique directory under /tmp for test file I/O.
func TempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(tmpRoot, "poisson-*")
	if err != nil {
		t.Fatalf("testutil.TempDir: %v", err)
	}
	assertUnderTmp(t, dir)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// TempHome sets HOME to a unique directory under /tmp and returns it.
func TempHome(t *testing.T) string {
	t.Helper()
	home := TempDir(t)
	t.Setenv("HOME", home)
	return home
}

func assertUnderTmp(t *testing.T, dir string) {
	t.Helper()
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("testutil: abs path: %v", err)
	}
	root, err := filepath.Abs(tmpRoot)
	if err != nil {
		t.Fatalf("testutil: abs /tmp: %v", err)
	}
	prefix := root + string(os.PathSeparator)
	if abs != root && !strings.HasPrefix(abs, prefix) {
		t.Fatalf("testutil: path %q is not under %q", abs, root)
	}
}