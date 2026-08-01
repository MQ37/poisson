// Package store provides SQLite-backed persistence for Poisson sessions,
// messages, API calls, FTS5 search, and model pricing. schemaSQL always
// reflects the current, final shape of a fresh database; forward
// compatibility for an existing on-disk database that predates a schema
// change is handled by migrations (see the migrations var below), tracked
// via PRAGMA user_version.
package store

import (
	"database/sql"
	"fmt"

	// Register the modernc.org/sqlite driver (pure-Go, cgo-free).
	_ "modernc.org/sqlite"
)

// Store wraps a *sql.DB connection to the Poisson SQLite database.
type Store struct {
	db *sql.DB
}

// schemaSQL contains all CREATE TABLE IF NOT EXISTS statements for the
// current, final schema. Execution is idempotent: on an existing database
// every statement here is a no-op (SQLite does not validate that an
// existing table's actual columns match an IF NOT EXISTS statement's
// declared ones), so a column added here has no effect on a database that
// predates it — that requires a migration (see migrations below), not an
// edit to this constant.
const schemaSQL = `
PRAGMA journal_mode = WAL;
PRAGMA busy_timeout = 5000;
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS sessions (
    id                  TEXT PRIMARY KEY,
    is_subagent         INTEGER DEFAULT 0,
    title               TEXT,
    compaction_summary  TEXT,
    created_at          INTEGER NOT NULL,
    updated_at          INTEGER NOT NULL,
    cwd                 TEXT NOT NULL,
    provider            TEXT NOT NULL,
    model               TEXT NOT NULL,
    compacted_seq       INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS messages (
    id          TEXT PRIMARY KEY,
    session_id  TEXT NOT NULL REFERENCES sessions(id),
    seq         INTEGER NOT NULL,
    role        TEXT NOT NULL,
    content     TEXT NOT NULL,
    deleted_at  INTEGER,
    compacted   INTEGER DEFAULT 0,
    api_call_id TEXT,
    created_at  INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id, seq);
CREATE UNIQUE INDEX IF NOT EXISTS idx_messages_session_seq_uniq ON messages(session_id, seq);

CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
    session_id UNINDEXED,
    message_id UNINDEXED,
    role UNINDEXED,
    content_text,
    tokenize='unicode61'
);

CREATE TABLE IF NOT EXISTS api_calls (
    id                  TEXT PRIMARY KEY,
    session_id          TEXT NOT NULL REFERENCES sessions(id),
    seq                 INTEGER NOT NULL,
    provider            TEXT NOT NULL DEFAULT '',
    model               TEXT NOT NULL,
    input_tokens        INTEGER NOT NULL,
    input_tokens_known  INTEGER NOT NULL DEFAULT 1,
    output_tokens       INTEGER NOT NULL,
    cache_read_tokens   INTEGER DEFAULT 0,
    cache_write_tokens  INTEGER DEFAULT 0,
    cost                REAL NOT NULL,
    purpose             TEXT NOT NULL DEFAULT 'main',
    is_compaction       INTEGER NOT NULL DEFAULT 0,
    created_at          INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_api_calls_session ON api_calls(session_id, created_at);
CREATE UNIQUE INDEX IF NOT EXISTS idx_api_calls_session_seq_uniq ON api_calls(session_id, seq);

CREATE TABLE IF NOT EXISTS compactions (
    id            TEXT PRIMARY KEY,
    session_id    TEXT NOT NULL REFERENCES sessions(id),
    message_id    TEXT,
    summary       TEXT NOT NULL,
    tokens_before INTEGER,
    tokens_after  INTEGER,
    cost          REAL DEFAULT 0.0,
    created_at    INTEGER NOT NULL
);
`

// migrations are schema changes applied in order, gated by SQLite's PRAGMA
// user_version: migrations[i] takes a database from version i to i+1.
// Append here for a future schema change that an already-shipped database
// needs carried forward (a new/changed column, a data backfill, a new
// constraint) — schemaSQL alone only ever affects a database being created
// from scratch, never an existing one.
//
// Each migration runs inside its own transaction together with its version
// bump (see runMigration): a failure partway through rolls back atomically,
// so a later Open() retries against the exact pre-migration state instead
// of replaying a partially-applied migration against data it no longer
// matches (which could fail differently, or succeed incorrectly, the
// second time).
//
// Empty for now: this project has no released version with an on-disk
// schema older than schemaSQL's current shape to carry forward (previous
// schema changes were folded directly into schemaSQL and applied once,
// by hand, to the sole pre-release database — see git history). The next
// schema change that needs to reach an already-shipped database is
// migrations[0].
var migrations = []func(*sql.Tx) error{}

// migrate reads db's PRAGMA user_version and applies any migrations not yet
// run, bumping the stored version after each one so a later Open resumes
// from wherever it left off.
func migrate(db *sql.DB) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	for version < len(migrations) {
		if err := runMigration(db, migrations[version], version); err != nil {
			return err
		}
		version++
	}
	return nil
}

// runMigration runs one migration step and its version bump inside a
// single transaction, committed together — see the migrations var's doc
// comment for why atomicity matters here.
func runMigration(db *sql.DB, step func(*sql.Tx) error, version int) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin migration %d->%d: %w", version, version+1, err)
	}
	defer tx.Rollback() // no-op once Commit has succeeded below
	if err := step(tx); err != nil {
		return fmt.Errorf("migration %d->%d: %w", version, version+1, err)
	}
	if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, version+1)); err != nil {
		return fmt.Errorf("bump schema version to %d: %w", version+1, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %d->%d: %w", version, version+1, err)
	}
	return nil
}

// Open opens (or creates) the SQLite database at path, sets the WAL
// journal mode and busy_timeout pragmas, and runs idempotent schema
// creation. The returned Store is ready for use.
func Open(path string) (*Store, error) {
	// "_pragma=busy_timeout(5000)" is applied via exec below; we also set
	// pragmas through schemaSQL execution.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// Serialize DB access on a single connection: SQLite serializes writes
	// anyway, and for this local single-user CLI one connection under WAL avoids
	// "database is locked" contention with no meaningful throughput cost.
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate schema: %w", err)
	}
	if err := reconcileAPICalls(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("reconcile api_calls: %w", err)
	}

	st := &Store{db: db}
	if err := st.reconcileFTS(); err != nil {
		db.Close()
		return nil, fmt.Errorf("reconcile fts: %w", err)
	}
	return st, nil
}

// reconcileAPICalls is an ongoing data-hygiene pass (not a one-time schema
// migration — see reconcileFTS for the same pattern), run on every Open.
// A row with input_tokens=0, output_tokens>0, and input_tokens_known=1 is
// self-contradictory: some providers (e.g. minimax via Ollama) don't report
// prompt-token usage at all, and a row recorded before that gap was
// understood/handled at insert time can end up wrongly marked "known" —
// clearing the flag lets ContextTokens' estimator ignore a real-usage
// anchor it can't trust instead of silently under/overestimating context
// from a bogus zero.
func reconcileAPICalls(db *sql.DB) error {
	_, err := db.Exec(`UPDATE api_calls
		SET input_tokens_known = 0
		WHERE input_tokens = 0 AND output_tokens > 0 AND input_tokens_known = 1`)
	if err != nil {
		return fmt.Errorf("mark zero-input api_calls unknown: %w", err)
	}
	return nil
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// DB exposes the underlying *sql.DB for advanced use cases.
func (s *Store) DB() *sql.DB {
	return s.db
}
