// Package auth manages OAuth tokens and API keys stored in ~/.poisson/auth.json.
package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// AuthEntry represents credentials for a single provider.
type AuthEntry struct {
	Type    string `json:"type"`    // "oauth" | "api_key" | "none"
	Access  string `json:"access"`  // OAuth access token
	Refresh string `json:"refresh"` // OAuth refresh token
	Expires int64  `json:"expires"` // Unix millis when access token expires
	Key     string `json:"key"`     // API key (for type == "api_key")
}

// AuthStore maps provider names to their credentials.
type AuthStore map[string]AuthEntry

// AuthPath returns the path to ~/.poisson/auth.json.
func AuthPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".poisson", "auth.json"), nil
}

// Load reads ~/.poisson/auth.json and returns the AuthStore.
// Returns an empty store if the file doesn't exist.
func Load() (AuthStore, error) {
	path, err := AuthPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return AuthStore{}, nil
		}
		return nil, err
	}
	var store AuthStore
	if err := json.Unmarshal(data, &store); err != nil {
		return nil, err
	}
	return store, nil
}

// Save writes the AuthStore to ~/.poisson/auth.json with 0600 permissions,
// atomically (write to a sibling temp file, then rename over the target).
// A crash/kill mid-write can therefore never leave auth.json truncated or
// empty — readers always see either the old complete file or the new one.
func Save(store AuthStore) error {
	path, err := AuthPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// withLock runs fn while holding an exclusive OS file lock (flock) on
// auth.json's sidecar lock file, serializing the load-modify-save cycle
// across every process that touches auth.json: the parent px, any subagent
// child process it spawns, or a concurrent `px login`/`px logout`. Without
// this, two processes each Load() the whole map, mutate a different
// provider's entry, and Save() the whole map back — whichever saves last
// silently clobbers the other's fresh token with its own stale copy of it.
func withLock(fn func() error) error {
	path, err := AuthPath()
	if err != nil {
		return err
	}
	lockPath := path + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock auth store: %w", err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}

// UpdateEntry atomically sets a single provider's entry: under withLock, it
// re-reads the store fresh from disk (not the caller's possibly-stale
// in-memory copy), sets the entry, and saves. This is the safe way for a
// token refresh to persist its result — see withLock's doc for why a plain
// Save(wholeMapSnapshot) races other processes.
func UpdateEntry(provider string, entry AuthEntry) error {
	return withLock(func() error {
		store, err := Load()
		if err != nil {
			return err
		}
		store[provider] = entry
		return Save(store)
	})
}

// DeleteEntry atomically removes a provider's entry, under the same lock as
// UpdateEntry (see its docs).
func DeleteEntry(provider string) error {
	return withLock(func() error {
		store, err := Load()
		if err != nil {
			return err
		}
		delete(store, provider)
		return Save(store)
	})
}

// IsOAuth returns true if the given provider has OAuth credentials.
func IsOAuth(store AuthStore, provider string) bool {
	entry, ok := store[provider]
	return ok && entry.Type == "oauth"
}

// GetAPIKey returns the API key for the given provider, or "".
func GetAPIKey(store AuthStore, provider string) string {
	entry, ok := store[provider]
	if !ok {
		return ""
	}
	if entry.Type == "api_key" {
		return entry.Key
	}
	return ""
}

// IsExpired returns true if the OAuth token has expired (or will within the
// given skew in milliseconds).
func IsExpired(entry AuthEntry, skewMs int64) bool {
	if entry.Type != "oauth" {
		return false
	}
	now := nowMillis()
	return entry.Expires <= now+skewMs
}

// nowMillis returns the current time in Unix milliseconds.
func nowMillis() int64 {
	return time.Now().UnixMilli()
}
