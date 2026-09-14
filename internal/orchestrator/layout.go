package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
)

// InstanceStateDir returns <stateRoot>/instances/<name> — everything this
// package persists about one instance (metadata, generated config/secrets)
// lives under here, distinct from the instance's actual rootfs (which lives
// under /var/lib/machines/<name>, owned by internal/orchestrator/nspawn).
func InstanceStateDir(stateRoot, name string) string {
	return filepath.Join(stateRoot, "instances", name)
}

// SecretsDir returns <stateRoot>/instances/<name>/secrets.
func SecretsDir(stateRoot, name string) string {
	return filepath.Join(InstanceStateDir(stateRoot, name), "secrets")
}

// WorkDir returns <stateRoot>/instances/<name>/work — bind-mounted into the
// instance as /work.
func WorkDir(stateRoot, name string) string {
	return filepath.Join(InstanceStateDir(stateRoot, name), "work")
}

// ConfigTomlPath returns <stateRoot>/instances/<name>/config.toml.
func ConfigTomlPath(stateRoot, name string) string {
	return filepath.Join(InstanceStateDir(stateRoot, name), "config.toml")
}

// AuthJSONPath returns <stateRoot>/instances/<name>/secrets/auth.json.
func AuthJSONPath(stateRoot, name string) string {
	return filepath.Join(SecretsDir(stateRoot, name), "auth.json")
}

// CreateInstanceLayout creates a brand-new instance's state directory tree:
// instances/<name>/{secrets/, work/} at mode 0700 (secrets/auth.json itself
// is written separately, at 0600 — see GenerateInstanceSecrets).
// instance.json and config.toml are written by their own callers (SaveMeta,
// GenerateInstanceSecrets) once this layout exists.
//
// Refuses (hard error, never silently reuses or overwrites) if the
// instance's state directory already exists — a name collision, or a
// leftover directory from a previous incomplete Create, must surface as an
// error rather than quietly adopting whatever's already there.
func CreateInstanceLayout(stateRoot, name string) error {
	dir := InstanceStateDir(stateRoot, name)
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("instance state directory already exists: %s", dir)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", dir, err)
	}
	if err := os.MkdirAll(SecretsDir(stateRoot, name), 0o700); err != nil {
		return fmt.Errorf("create secrets dir: %w", err)
	}
	if err := os.MkdirAll(WorkDir(stateRoot, name), 0o700); err != nil {
		return fmt.Errorf("create work dir: %w", err)
	}
	return nil
}
