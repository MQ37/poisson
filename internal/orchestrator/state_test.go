package orchestrator

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func mustLayout(t *testing.T, stateRoot, name string) {
	t.Helper()
	if err := CreateInstanceLayout(stateRoot, name); err != nil {
		t.Fatalf("CreateInstanceLayout: %v", err)
	}
}

func TestSaveLoadMeta_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	mustLayout(t, dir, "px-alpha")

	want := InstanceMeta{
		Name: "px-alpha", SessionID: "s-1", Model: "anthropic/claude-sonnet-5",
		ChatID: "-100", TopicID: "42", CreatedAt: time.Now().Truncate(time.Second),
		DesiredState: DesiredRunning,
	}
	if err := SaveMeta(dir, want); err != nil {
		t.Fatalf("SaveMeta: %v", err)
	}
	got, err := LoadMeta(dir, "px-alpha")
	if err != nil {
		t.Fatalf("LoadMeta: %v", err)
	}
	// time.Time's == compares the Location pointer too, which a JSON
	// round-trip never preserves (UnmarshalJSON parses back a fixed-offset
	// Location, not the original *time.Local) even when the wall-clock
	// value is identical — Equal is the only correct comparison. Checked
	// separately, then zeroed so the rest of the struct can still use a
	// plain ==.
	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, want.CreatedAt)
	}
	got.CreatedAt, want.CreatedAt = time.Time{}, time.Time{}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// TestLoadMeta_CorruptFileReturnsError checks a truncated/corrupt
// instance.json comes back as an error, not a zero-value success or a
// panic.
func TestLoadMeta_CorruptFileReturnsError(t *testing.T) {
	dir := t.TempDir()
	mustLayout(t, dir, "px-broken")
	path := metaPath(dir, "px-broken")
	if err := os.WriteFile(path, []byte("{not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadMeta(dir, "px-broken"); err == nil {
		t.Fatal("expected an error for corrupt instance.json, got nil")
	}
}

// TestSaveMeta_CrashMidWriteLeavesOldFileIntact simulates a crash between
// the tmp-write and the rename (Step 14's specific verify criterion): a
// valid instance.json exists; a new write's tmp file is created but never
// renamed into place (standing in for a kill between those two steps).
// LoadMeta must still return the OLD, untouched value.
func TestSaveMeta_CrashMidWriteLeavesOldFileIntact(t *testing.T) {
	dir := t.TempDir()
	mustLayout(t, dir, "px-crash")

	original := InstanceMeta{Name: "px-crash", SessionID: "s-original", DesiredState: DesiredRunning}
	if err := SaveMeta(dir, original); err != nil {
		t.Fatalf("SaveMeta: %v", err)
	}

	// Simulate the write-half of a second SaveMeta without ever renaming —
	// exactly the state a kill between os.WriteFile and os.Rename leaves.
	path := metaPath(dir, "px-crash")
	if err := os.WriteFile(path+".tmp", []byte(`{"Name":"px-crash","SessionID":"s-crashed-mid-write"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := LoadMeta(dir, "px-crash")
	if err != nil {
		t.Fatalf("LoadMeta after simulated crash: %v", err)
	}
	if got != original {
		t.Errorf("got %+v after simulated crash, want untouched original %+v", got, original)
	}
}

func TestScanInstances_EmptyStateRootIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	metas, issues, err := ScanInstances(dir)
	if err != nil {
		t.Fatalf("ScanInstances on empty state root: %v", err)
	}
	if len(metas) != 0 || len(issues) != 0 {
		t.Errorf("metas=%v issues=%v, want both empty", metas, issues)
	}
}

// TestScanInstances_SkipsCorruptButReturnsGood checks one corrupt instance
// directory doesn't take down the whole scan — the good instance is still
// returned, and the corrupt one is reported as an issue, not silently
// dropped or fatal.
func TestScanInstances_SkipsCorruptButReturnsGood(t *testing.T) {
	dir := t.TempDir()
	mustLayout(t, dir, "px-good")
	if err := SaveMeta(dir, InstanceMeta{Name: "px-good", SessionID: "s-good"}); err != nil {
		t.Fatal(err)
	}
	mustLayout(t, dir, "px-bad")
	if err := os.WriteFile(metaPath(dir, "px-bad"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	metas, issues, err := ScanInstances(dir)
	if err != nil {
		t.Fatalf("ScanInstances: %v", err)
	}
	if len(metas) != 1 || metas[0].Name != "px-good" {
		t.Errorf("metas = %+v, want exactly px-good", metas)
	}
	if len(issues) != 1 || issues[0].Name != "px-bad" {
		t.Errorf("issues = %+v, want exactly px-bad", issues)
	}
}

func TestCreateInstanceLayout_RefusesCollision(t *testing.T) {
	dir := t.TempDir()
	mustLayout(t, dir, "px-dup")
	if err := CreateInstanceLayout(dir, "px-dup"); err == nil {
		t.Fatal("expected an error creating an already-existing instance layout, got nil")
	}
}

func TestCreateInstanceLayout_CreatesExpectedDirs(t *testing.T) {
	dir := t.TempDir()
	mustLayout(t, dir, "px-layout")
	for _, p := range []string{
		SecretsDir(dir, "px-layout"),
		WorkDir(dir, "px-layout"),
	} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if !info.IsDir() {
			t.Errorf("%s is not a directory", p)
		}
	}
	// config.toml/instance.json paths resolve under the instance dir, not
	// created by CreateInstanceLayout itself (written by their own callers).
	if got, want := ConfigTomlPath(dir, "px-layout"), filepath.Join(dir, "instances", "px-layout", "config.toml"); got != want {
		t.Errorf("ConfigTomlPath = %q, want %q", got, want)
	}
}
