package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// NewSessionID returns a short, collision-resistant session ID such as
// "s-a3f9c1d2". Timestamp-based IDs were unusable because they share their
// high-order digits, so any short display prefix looked identical.
func NewSessionID() string {
	return "s-" + randomHex(4)
}

// NewSubagentID returns a short subagent session ID such as "sub-a3f9c1d2".
func NewSubagentID() string {
	return "sub-" + randomHex(4)
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(b)
}

// Session represents a row in the sessions table.
type Session struct {
	ID                string
	IsSubagent        bool
	Title             *string
	CompactionSummary *string
	CreatedAt         int64
	UpdatedAt         int64
	Cwd               string
	Provider          string
	Model             string
	// CompactedSeq is the highest api_calls.seq recorded at the time of the last
	// compaction. A real api_call with Seq <= CompactedSeq predates the
	// compaction, so its usage describes the pre-compaction (larger) prompt, not
	// the active context. Zero means never compacted.
	CompactedSeq int
}

// ErrNotFound is returned when a single-row lookup yields no rows.
var ErrNotFound = errors.New("store: not found")

// CreateSession inserts a new session row. CreatedAt and UpdatedAt are
// populated with the current time if they are zero.
func (s *Store) CreateSession(sess *Session) error {
	now := time.Now().Unix()
	if sess.CreatedAt == 0 {
		sess.CreatedAt = now
	}
	if sess.UpdatedAt == 0 {
		sess.UpdatedAt = now
	}
	isSub := 0
	if sess.IsSubagent {
		isSub = 1
	}
	_, err := s.db.Exec(
		`INSERT INTO sessions
		 (id, is_subagent, title, compaction_summary,
		  created_at, updated_at, cwd, provider, model)
		 VALUES (?,?,?,?,?,?,?,?,?)`,
		sess.ID, isSub, sess.Title,
		sess.CompactionSummary, sess.CreatedAt, sess.UpdatedAt,
		sess.Cwd, sess.Provider, sess.Model,
	)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

// MessageCountsBySession returns the number of non-deleted messages per
// session id — one query, so the session picker can show counts for every
// session without loading each conversation. Includes compacted messages:
// compaction only flags a message, it never deletes it, so a session that
// was compacted as its last action (nothing sent since) still has a real
// message count, not zero.
func (s *Store) MessageCountsBySession() (map[string]int, error) {
	rows, err := s.db.Query(
		`SELECT session_id, COUNT(*) FROM messages
		 WHERE deleted_at IS NULL GROUP BY session_id`)
	if err != nil {
		return nil, fmt.Errorf("count messages: %w", err)
	}
	defer rows.Close()
	out := make(map[string]int)
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, fmt.Errorf("scan message count: %w", err)
		}
		out[id] = n
	}
	return out, rows.Err()
}

// DeleteSession permanently removes a session and every row that references it
// (messages, FTS index entries, api_calls, compactions), in one transaction.
// Foreign keys are enforced with no cascade, so children must go first.
// Irreversible.
func (s *Store) DeleteSession(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin delete: %w", err)
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM messages_fts WHERE session_id = ?`,
		`DELETE FROM messages WHERE session_id = ?`,
		`DELETE FROM api_calls WHERE session_id = ?`,
		`DELETE FROM compactions WHERE session_id = ?`,
		`DELETE FROM sessions WHERE id = ?`,
	} {
		if _, err := tx.Exec(q, id); err != nil {
			return fmt.Errorf("delete session %s: %w", id, err)
		}
	}
	return tx.Commit()
}

// GetSession returns the session with the given id, or ErrNotFound.
func (s *Store) GetSession(id string) (*Session, error) {
	row := s.db.QueryRow(
		`SELECT id, is_subagent, title,
		        compaction_summary, created_at, updated_at, cwd, provider, model, compacted_seq
		 FROM sessions WHERE id = ?`, id)
	sess, err := scanSession(row)
	if err != nil {
		return nil, err
	}
	return sess, nil
}

// ListSessions returns recently-used sessions first (updated_at desc), paginated
// by limit/offset.
// ListSessions returns sessions newest-first. limit < 0 returns all sessions
// (offset ignored); limit == 0 defaults to 50; limit > 0 paginates.
func (s *Store) ListSessions(limit, offset int) ([]Session, error) {
	const base = `SELECT id, is_subagent, title,
	        compaction_summary, created_at, updated_at, cwd, provider, model, compacted_seq
	 FROM sessions ORDER BY updated_at DESC, created_at DESC, id DESC`
	var rows *sql.Rows
	var err error
	if limit < 0 {
		rows, err = s.db.Query(base)
	} else {
		if limit == 0 {
			limit = 50
		}
		rows, err = s.db.Query(base+` LIMIT ? OFFSET ?`, limit, offset)
	}
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sess)
	}
	return out, rows.Err()
}

// UpdateSession updates the mutable fields of a session (title, cwd,
// provider, model, updated_at). ID is used as the key.
func (s *Store) UpdateSession(sess *Session) error {
	sess.UpdatedAt = time.Now().Unix()
	_, err := s.db.Exec(
		`UPDATE sessions SET title = ?, cwd = ?, provider = ?, model = ?,
		                     updated_at = ? WHERE id = ?`,
		sess.Title, sess.Cwd, sess.Provider, sess.Model, sess.UpdatedAt, sess.ID)
	if err != nil {
		return fmt.Errorf("update session: %w", err)
	}
	return nil
}

// ApplyCompaction atomically stores the summary and marks messages compacted.
func (s *Store) ApplyCompaction(sessionID string, upToSeq int, summary string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin compaction tx: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().Unix()
	// Watermark the current max api_calls.seq so the context estimator can tell
	// that any earlier real usage predates this compaction and is now stale.
	if _, err := tx.Exec(
		`UPDATE sessions SET compaction_summary = ?, updated_at = ?,
		        compacted_seq = (SELECT COALESCE(MAX(seq), 0) FROM api_calls WHERE session_id = ?)
		 WHERE id = ?`,
		summary, now, sessionID, sessionID); err != nil {
		return fmt.Errorf("set compaction summary: %w", err)
	}
	if _, err := tx.Exec(
		`UPDATE messages SET compacted = 1
		 WHERE session_id = ? AND seq <= ? AND deleted_at IS NULL AND compacted = 0`,
		sessionID, upToSeq); err != nil {
		return fmt.Errorf("mark compacted: %w", err)
	}
	if _, err := tx.Exec(`
		DELETE FROM messages_fts
		WHERE message_id IN (
			SELECT id FROM messages
			WHERE session_id = ? AND seq <= ? AND compacted = 1
		)`, sessionID, upToSeq); err != nil {
		return fmt.Errorf("purge fts compacted: %w", err)
	}
	return tx.Commit()
}

// MessageIDAtSeq returns the message id at the given seq in a session.
// SetSessionTitle sets the display title for a session.
func (s *Store) SetSessionTitle(id, title string) error {
	title = strings.TrimSpace(title)
	var titleVal interface{}
	if title != "" {
		titleVal = title
	}
	_, err := s.db.Exec(
		`UPDATE sessions SET title = ?, updated_at = ? WHERE id = ?`,
		titleVal, time.Now().Unix(), id)
	if err != nil {
		return fmt.Errorf("set session title: %w", err)
	}
	return nil
}

// scanner abstracts *sql.Row and *sql.Rows for shared scan logic.
type scanner interface {
	Scan(dest ...any) error
}

func scanSession(sc scanner) (*Session, error) {
	var sess Session
	var title, compactionSummary sql.NullString
	var isSubagent int
	err := sc.Scan(
		&sess.ID, &isSubagent, &title,
		&compactionSummary, &sess.CreatedAt, &sess.UpdatedAt,
		&sess.Cwd, &sess.Provider, &sess.Model, &sess.CompactedSeq,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scan session: %w", err)
	}
	if title.Valid {
		v := title.String
		sess.Title = &v
	}
	if compactionSummary.Valid {
		v := compactionSummary.String
		sess.CompactionSummary = &v
	}
	sess.IsSubagent = isSubagent != 0
	return &sess, nil
}
