// Package store provides SQLite-backed persistence for Poisson sessions,
// messages, API calls, FTS5 search, and model pricing.
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

// schemaSQL contains all CREATE TABLE IF NOT EXISTS statements from
// SPEC §8.1. Execution is idempotent.
const schemaSQL = `
PRAGMA journal_mode = WAL;
PRAGMA busy_timeout = 5000;

CREATE TABLE IF NOT EXISTS sessions (
    id                  TEXT PRIMARY KEY,
    parent_id           TEXT,
    fork_point          TEXT,
    is_subagent         INTEGER DEFAULT 0,
    title               TEXT,
    compaction_summary  TEXT,
    created_at          INTEGER NOT NULL,
    updated_at          INTEGER NOT NULL,
    cwd                 TEXT NOT NULL,
    provider            TEXT NOT NULL,
    model               TEXT NOT NULL,
    CHECK (
        (parent_id IS NULL AND fork_point IS NULL) OR
        (parent_id IS NOT NULL AND fork_point IS NOT NULL)
    )
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
CREATE INDEX IF NOT EXISTS idx_sessions_parent ON sessions(parent_id);

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
    model               TEXT NOT NULL,
    input_tokens        INTEGER NOT NULL,
    input_tokens_known  INTEGER NOT NULL DEFAULT 1,
    output_tokens       INTEGER NOT NULL,
    cache_read_tokens   INTEGER DEFAULT 0,
    cache_write_tokens  INTEGER DEFAULT 0,
    cost                REAL NOT NULL,
    created_at          INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_api_calls_session ON api_calls(session_id, created_at);

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

CREATE TABLE IF NOT EXISTS model_pricing (
    provider             TEXT NOT NULL,
    model                TEXT NOT NULL,
    input_per_mtok       REAL NOT NULL,
    output_per_mtok      REAL NOT NULL,
    cache_read_per_mtok  REAL DEFAULT 0,
    cache_write_per_mtok REAL DEFAULT 0,
    PRIMARY KEY (provider, model)
);
`

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

	// SQLite performs best with a single connection for writes under WAL.
	// We allow multiple open connections for concurrent readers.
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := ensureAPICallsColumns(db); err != nil {
		db.Close()
		return nil, err
	}

	st := &Store{db: db}
	if err := st.reconcileFTS(); err != nil {
		db.Close()
		return nil, fmt.Errorf("reconcile fts: %w", err)
	}
	return st, nil
}

func ensureAPICallsColumns(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(api_calls)`)
	if err != nil {
		return fmt.Errorf("inspect api_calls schema: %w", err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var dflt any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			return fmt.Errorf("scan api_calls schema: %w", err)
		}
		seen[name] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read api_calls schema: %w", err)
	}
	if !seen["input_tokens_known"] {
		if _, err := db.Exec(`ALTER TABLE api_calls ADD COLUMN input_tokens_known INTEGER NOT NULL DEFAULT 1`); err != nil {
			return fmt.Errorf("migrate api_calls.input_tokens_known: %w", err)
		}
	}
	if _, err := db.Exec(`UPDATE api_calls
		SET input_tokens_known = 0
		WHERE input_tokens = 0 AND output_tokens > 0 AND input_tokens_known = 1`); err != nil {
		return fmt.Errorf("migrate zero-input api_calls: %w", err)
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
