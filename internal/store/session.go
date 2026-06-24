package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
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
	ParentID          *string
	ForkPoint         *string
	IsSubagent        bool
	Title             *string
	CompactionSummary *string
	CreatedAt         int64
	UpdatedAt         int64
	Cwd               string
	Provider          string
	Model             string
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
		 (id, parent_id, fork_point, is_subagent, title, compaction_summary,
		  created_at, updated_at, cwd, provider, model)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		sess.ID, sess.ParentID, sess.ForkPoint, isSub, sess.Title,
		sess.CompactionSummary, sess.CreatedAt, sess.UpdatedAt,
		sess.Cwd, sess.Provider, sess.Model,
	)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

// GetSession returns the session with the given id, or ErrNotFound.
func (s *Store) GetSession(id string) (*Session, error) {
	row := s.db.QueryRow(
		`SELECT id, parent_id, fork_point, is_subagent, title,
		        compaction_summary, created_at, updated_at, cwd, provider, model
		 FROM sessions WHERE id = ?`, id)
	sess, err := scanSession(row)
	if err != nil {
		return nil, err
	}
	return sess, nil
}

// ListSessions returns sessions newest-first (by created_at desc, then id),
// paginated by limit/offset.
func (s *Store) ListSessions(limit, offset int) ([]Session, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(
		`SELECT id, parent_id, fork_point, is_subagent, title,
		        compaction_summary, created_at, updated_at, cwd, provider, model
		 FROM sessions ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`,
		limit, offset)
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

// SetCompactionSummary stores the compaction summary on a session and
// bumps updated_at.
func (s *Store) SetCompactionSummary(id, summary string) error {
	_, err := s.db.Exec(
		`UPDATE sessions SET compaction_summary = ?, updated_at = ? WHERE id = ?`,
		summary, time.Now().Unix(), id)
	if err != nil {
		return fmt.Errorf("set compaction summary: %w", err)
	}
	return nil
}

// ClearCompactionSummary nulls out the compaction summary on a session.
func (s *Store) ClearCompactionSummary(id string) error {
	_, err := s.db.Exec(
		`UPDATE sessions SET compaction_summary = NULL, updated_at = ? WHERE id = ?`,
		time.Now().Unix(), id)
	if err != nil {
		return fmt.Errorf("clear compaction summary: %w", err)
	}
	return nil
}

// scanner abstracts *sql.Row and *sql.Rows for shared scan logic.
type scanner interface {
	Scan(dest ...any) error
}

func scanSession(sc scanner) (*Session, error) {
	var sess Session
	var parentID, forkPoint, title, compactionSummary sql.NullString
	var isSubagent int
	err := sc.Scan(
		&sess.ID, &parentID, &forkPoint, &isSubagent, &title,
		&compactionSummary, &sess.CreatedAt, &sess.UpdatedAt,
		&sess.Cwd, &sess.Provider, &sess.Model,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scan session: %w", err)
	}
	if parentID.Valid {
		v := parentID.String
		sess.ParentID = &v
	}
	if forkPoint.Valid {
		v := forkPoint.String
		sess.ForkPoint = &v
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
