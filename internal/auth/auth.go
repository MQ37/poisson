// Package auth manages OAuth tokens and API keys stored in ~/.poisson/auth.json.
package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
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

// Save writes the AuthStore to ~/.poisson/auth.json with 0600 permissions.
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
	return os.WriteFile(path, data, 0o600)
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
