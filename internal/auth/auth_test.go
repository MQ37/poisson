package auth

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/mq37/poisson/internal/testutil"
)

func TestLoadEmpty(t *testing.T) {
	testutil.TempHome(t)

	store, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(store) != 0 {
		t.Errorf("expected empty store, got %d entries", len(store))
	}
}

func TestSaveAndLoad(t *testing.T) {
	tmpHome := testutil.TempHome(t)

	store := AuthStore{
		"anthropic": {Type: "oauth", Access: "tok123", Refresh: "ref456", Expires: 9999999999999},
		"ollama":    {Type: "none"},
	}
	if err := Save(store); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Verify file permissions.
	path := filepath.Join(tmpHome, ".poisson", "auth.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %o, want 0600", info.Mode().Perm())
	}

	// Load and verify.
	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded["anthropic"].Access != "tok123" {
		t.Errorf("access = %q, want tok123", loaded["anthropic"].Access)
	}
	if loaded["anthropic"].Refresh != "ref456" {
		t.Errorf("refresh = %q, want ref456", loaded["anthropic"].Refresh)
	}
	if loaded["ollama"].Type != "none" {
		t.Errorf("ollama type = %q, want 'none'", loaded["ollama"].Type)
	}
}

// TestSaveAtomicNoTempFileLeftBehind pins Save's atomic-write fix
// (temp file + rename): a normal Save must leave only the final auth.json,
// never a stray sibling .tmp file.
func TestSaveAtomicNoTempFileLeftBehind(t *testing.T) {
	tmpHome := testutil.TempHome(t)
	if err := Save(AuthStore{"anthropic": {Type: "oauth", Access: "a"}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	path := filepath.Join(tmpHome, ".poisson", "auth.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected final file to exist: %v", err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("expected temp file to be renamed away, got err=%v", err)
	}
}

// TestUpdateEntryConcurrentDifferentProvidersNoLostUpdate is the regression
// guard for the cross-process auth.json lost-update race: before UpdateEntry
// existed, every refresh path did Load-mutate-Save on its own stale
// in-memory snapshot of the WHOLE map (see anthropic.go/openai.go's
// refresh paths), so two concurrent refreshes of DIFFERENT providers (e.g.
// a parent px and a subagent child process) could clobber each other —
// whichever saved last silently overwrote the other's provider entry,
// since its own stale snapshot never had it. UpdateEntry closes this by
// re-reading fresh under a file lock immediately before each write.
func TestUpdateEntryConcurrentDifferentProvidersNoLostUpdate(t *testing.T) {
	testutil.TempHome(t)

	providers := []string{"anthropic", "openai", "xai"}
	var wg sync.WaitGroup
	for i, p := range providers {
		wg.Add(1)
		go func(p string, i int) {
			defer wg.Done()
			if err := UpdateEntry(p, AuthEntry{Type: "oauth", Access: fmt.Sprintf("tok-%d", i)}); err != nil {
				t.Errorf("UpdateEntry(%s): %v", p, err)
			}
		}(p, i)
	}
	wg.Wait()

	store, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(store) != len(providers) {
		t.Fatalf("got %d entries, want %d — lost update: %+v", len(store), len(providers), store)
	}
	for i, p := range providers {
		want := fmt.Sprintf("tok-%d", i)
		if store[p].Access != want {
			t.Errorf("%s access = %q, want %q", p, store[p].Access, want)
		}
	}
}

// TestDeleteEntryRemovesOnlyTargetProvider pins DeleteEntry's read-fresh-
// under-lock behavior: it must remove exactly the named provider and leave
// every other entry (concurrently written or not) untouched.
func TestDeleteEntryRemovesOnlyTargetProvider(t *testing.T) {
	testutil.TempHome(t)
	if err := Save(AuthStore{
		"anthropic": {Type: "oauth", Access: "a"},
		"openai":    {Type: "oauth", Access: "o"},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := DeleteEntry("anthropic"); err != nil {
		t.Fatalf("DeleteEntry: %v", err)
	}
	store, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := store["anthropic"]; ok {
		t.Error("anthropic entry should be gone")
	}
	if store["openai"].Access != "o" {
		t.Error("openai entry should survive")
	}
}

func TestIsOAuth(t *testing.T) {
	store := AuthStore{
		"anthropic": {Type: "oauth", Access: "tok"},
		"ollama":    {Type: "none"},
	}
	if !IsOAuth(store, "anthropic") {
		t.Error("anthropic should be OAuth")
	}
	if IsOAuth(store, "ollama") {
		t.Error("ollama should not be OAuth")
	}
	if IsOAuth(store, "nonexistent") {
		t.Error("nonexistent should not be OAuth")
	}
}

func TestGetAPIKey(t *testing.T) {
	store := AuthStore{
		"anthropic": {Type: "api_key", Key: "sk-ant-123"},
		"ollama":    {Type: "none"},
	}
	if got := GetAPIKey(store, "anthropic"); got != "sk-ant-123" {
		t.Errorf("anthropic key = %q, want sk-ant-123", got)
	}
	if got := GetAPIKey(store, "ollama"); got != "" {
		t.Errorf("ollama key = %q, want empty", got)
	}
}

func TestIsExpired(t *testing.T) {
	// Expired token.
	expired := AuthEntry{Type: "oauth", Expires: 1000}
	if !IsExpired(expired, 0) {
		t.Error("token with expires=1000 should be expired")
	}

	// Not expired.
	future := AuthEntry{Type: "oauth", Expires: 999999999999999}
	if IsExpired(future, 5*60*1000) {
		t.Error("token far in future should not be expired")
	}

	// Non-OAuth never expires.
	apiKey := AuthEntry{Type: "api_key", Key: "sk-test"}
	if IsExpired(apiKey, 0) {
		t.Error("api_key should never be expired")
	}
}
