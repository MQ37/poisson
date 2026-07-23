package guard

import "path/filepath"

// ResolveSymlinkTarget returns path with every symlink in it resolved to
// where it actually points, so a sensitive-path check can't be defeated by
// naming an innocuous file that's secretly a symlink into e.g. ~/.ssh —
// cat/head/tail/ls et al. are only auto-safe because they're read-only, and
// that guarantee is worthless if the "safe" name resolves somewhere else.
//
// If path (or, for a not-yet-existing leaf like a write/touch target, its
// parent directory) can't be resolved, path is returned unchanged — the
// caller then falls back to the plain literal-path check it already had.
func ResolveSymlinkTarget(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	// The full path doesn't exist yet (e.g. a file about to be created by
	// write/touch) — filepath.EvalSymlinks requires every component,
	// including the last, to exist. Resolve as much as we can via the
	// parent directory, which must exist for the operation to succeed
	// anyway; a symlinked parent is exactly the escape this guards against.
	dir, base := filepath.Split(path)
	if dir == "" {
		return path
	}
	if realDir, err := filepath.EvalSymlinks(dir); err == nil {
		return filepath.Join(realDir, base)
	}
	return path
}
