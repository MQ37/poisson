package store

import (
	"path/filepath"
	"testing"

	"github.com/mq37/poisson/internal/testutil"
)

// TestPinMigrationBackwardCompat simulates an on-disk database created
// before sessions.pinned existed (schemaSQL's CREATE TABLE predates it,
// user_version stuck at 0) and checks Open's migration adds the column
// without disturbing existing rows.
func TestPinMigrationBackwardCompat(t *testing.T) {
	dbPath := filepath.Join(testutil.TempDir(t), "pre-pin.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.CreateSession(&Session{ID: "old-sess", Cwd: "/tmp", Provider: "p", Model: "m"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	// Roll the schema back to "before pinned existed": drop the column and
	// rewind user_version so migrate() has to redo the work on next Open.
	if _, err := s.db.Exec(`ALTER TABLE sessions DROP COLUMN pinned`); err != nil {
		t.Fatalf("drop pinned (simulating pre-migration schema): %v", err)
	}
	if _, err := s.db.Exec(`PRAGMA user_version = 0`); err != nil {
		t.Fatalf("reset user_version: %v", err)
	}
	s.db.Close()

	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopen (this is the real regression check): %v", err)
	}
	defer s2.db.Close()

	got, err := s2.GetSession("old-sess")
	if err != nil {
		t.Fatalf("GetSession after migration: %v", err)
	}
	if got.Pinned {
		t.Fatal("migrated column should default to unpinned")
	}
	if err := s2.SetSessionPinned("old-sess", true); err != nil {
		t.Fatalf("SetSessionPinned after migration: %v", err)
	}
	got, _ = s2.GetSession("old-sess")
	if !got.Pinned {
		t.Fatal("pin did not persist after migration")
	}
}
