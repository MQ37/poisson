package orchestrator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mq37/poisson/internal/auth"
)

// WriteFileAtomic writes data to path via a sibling .tmp file + rename, so a
// crash mid-write never leaves path itself truncated — the same pattern
// SaveMeta and auth.Save both use.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// GenerateInstanceSecrets writes an instance's secrets/auth.json (mode
// 0600, containing only spec.AuthorizedProviders' entries copied from
// hostAuth — never the full host store) and config.toml (pinning
// provider.default and that provider's model to spec.Provider/spec.Model).
// Both are later bind-mounted read-only into the instance by nspawn's unit
// (see docs/orchestrator-plan.md Step 12) — this function has no knowledge
// of nspawn or bind mounts, it only writes state-directory files, which is
// what makes it independently testable.
//
// 🚨 An instance has full root and can trivially read its own bind-mounted
// auth.json — treat every credential copied here as compromised the moment
// you'd trust the instance's agent less than yourself (see
// docs/orchestrator-plan.md §5). Never bind-mounts (or is even passed) the
// host's own ~/.poisson/auth.json directly; hostAuth must be an explicit,
// separately-loaded copy so this stays testable with a synthetic store and
// so the scoping below is the only path credentials take into an instance.
//
// CreateInstanceLayout must have already been called for spec.Name — this
// function only writes files inside a layout it assumes exists.
func GenerateInstanceSecrets(stateRoot string, spec InstanceSpec, hostAuth auth.AuthStore) error {
	scoped := make(auth.AuthStore, len(spec.AuthorizedProviders))
	for _, p := range spec.AuthorizedProviders {
		if entry, ok := hostAuth[p]; ok {
			scoped[p] = entry
		}
	}
	authData, err := json.MarshalIndent(scoped, "", "  ")
	if err != nil {
		return fmt.Errorf("encode instance auth store: %w", err)
	}
	if err := WriteFileAtomic(AuthJSONPath(stateRoot, spec.Name), authData, 0o600); err != nil {
		return fmt.Errorf("write instance auth.json: %w", err)
	}

	configToml := fmt.Sprintf("[provider]\ndefault = %q\n\n[%s]\nmodel = %q\n", spec.Provider, spec.Provider, spec.Model)
	if err := WriteFileAtomic(ConfigTomlPath(stateRoot, spec.Name), []byte(configToml), 0o600); err != nil {
		return fmt.Errorf("write instance config.toml: %w", err)
	}
	return nil
}

// ShredSecrets overwrites every file under an instance's secrets directory
// with zeros before removing it — called by Destroy so a plain
// directory-entry removal doesn't leave credential bytes recoverable from
// the underlying disk blocks. Best-effort and known-imperfect on
// copy-on-write or wear-leveled storage (the overwrite may land on a
// different physical block than the original write) — still strictly more
// than a bare rm -rf costs nothing to attempt.
//
// A missing secrets directory (already removed, or Create never got far
// enough to write one) is success, not an error — matching Destroy's own
// idempotence contract.
func ShredSecrets(stateRoot, name string) error {
	dir := SecretsDir(stateRoot, name)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read secrets dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		f, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			continue
		}
		_, _ = f.WriteAt(make([]byte, info.Size()), 0)
		_ = f.Sync()
		_ = f.Close()
	}
	return os.RemoveAll(dir)
}
