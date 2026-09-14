package orchestrator

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

// DesiredState is what an instance's owner (the user, via Telegram command)
// last asked for — distinct from its actual runtime state, which Runtime.List
// reports independently and may disagree with this after a crash or a host
// reboot (that disagreement is exactly what M3's restart reconciliation,
// Step 21, resolves).
type DesiredState string

const (
	DesiredRunning   DesiredState = "running"
	DesiredSuspended DesiredState = "suspended"
)

// InstanceMeta is one instance's persistent record: everything needed to
// route Telegram messages to it, recreate its turn context, and reconcile
// its desired vs. actual state after a restart. Persisted as
// instances/<name>/instance.json.
type InstanceMeta struct {
	Name      string
	SessionID string
	Model     string // "provider/model"
	Frontend  string // which Frontend owns ChatID/TopicID (e.g. "telegram") — lets reconcile.go rebuild the exact ChannelKey without hardcoding a transport
	ChatID    string
	TopicID   string
	CreatedAt time.Time

	DesiredState DesiredState
	// PendingDestroy, once set, means a /kill was requested but may not
	// have finished (e.g. the orchestrator crashed mid-Destroy). A fresh
	// startup that finds it set must finish the destroy rather than leaving
	// the instance half-removed.
	PendingDestroy bool
	// RepoURL is an optional git repo this instance was created to work in
	// (empty for a general-purpose instance).
	RepoURL string
	// Kind is "box" (nspawn container, yolo turns) or "host" (runs directly
	// on the orchestrator host, real approvals, never yolo). Empty means
	// "box" -- metadata written before this field existed has no value here,
	// and an orchestrator upgrade must not silently change an already-
	// running instance's behavior. Always read through IsBoxKind, never
	// compare to "box"/"" directly, so this compatibility shim lives in
	// exactly one place.
	Kind string
}

const (
	KindBox  = "box"
	KindHost = "host"
)

// IsBoxKind reports whether kind means a box (nspawn) instance, treating the
// empty string (pre-Kind-field metadata) the same as "box" -- see
// InstanceMeta.Kind's doc comment.
func IsBoxKind(kind string) bool {
	return kind == "" || kind == KindBox
}

// metaPath returns the path SaveMeta/LoadMeta read and write.
func metaPath(stateRoot, name string) string {
	return filepath.Join(InstanceStateDir(stateRoot, name), "instance.json")
}

// SaveMeta writes m to its instance.json, atomically: marshal, write to a
// sibling .tmp file, then rename over the target. A crash between those two
// steps leaves the previous, still-valid instance.json completely untouched
// — readers never observe a truncated or half-written file, only the old
// complete one or the new one.
func SaveMeta(stateRoot string, m InstanceMeta) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode instance meta: %w", err)
	}
	if err := WriteFileAtomic(metaPath(stateRoot, m.Name), data, 0o600); err != nil {
		return fmt.Errorf("write instance meta: %w", err)
	}
	return nil
}

// LoadMeta reads and parses one instance's instance.json.
func LoadMeta(stateRoot, name string) (InstanceMeta, error) {
	data, err := os.ReadFile(metaPath(stateRoot, name))
	if err != nil {
		return InstanceMeta{}, err
	}
	var m InstanceMeta
	if err := json.Unmarshal(data, &m); err != nil {
		return InstanceMeta{}, fmt.Errorf("parse instance meta for %s: %w", name, err)
	}
	return m, nil
}

// ScanIssue records one instance directory ScanInstances could not load —
// e.g. a corrupt/truncated instance.json from a crash mid-write. The
// instance is skipped, never treated as a reason to touch its rootfs: a
// metadata parse failure is a metadata bug, not evidence the instance
// itself should be destroyed.
type ScanIssue struct {
	Name string
	Err  error
}

// ScanInstances enumerates every instance directory under stateRoot's
// instances/ subdirectory and loads each one's metadata. A missing
// instances/ directory (first run, nothing created yet) is not an error —
// it just means zero instances. Each instance directory that fails to parse
// is reported in issues (logged loudly by the caller, typically) rather
// than failing the whole scan or being silently dropped.
func ScanInstances(stateRoot string) (metas []InstanceMeta, issues []ScanIssue, err error) {
	root := filepath.Join(stateRoot, "instances")
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("scan instances dir: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		m, err := LoadMeta(stateRoot, name)
		if err != nil {
			issues = append(issues, ScanIssue{Name: name, Err: err})
			log.Printf("orchestrator: skipping instance %q, could not load metadata: %v", name, err)
			continue
		}
		metas = append(metas, m)
	}
	return metas, issues, nil
}
