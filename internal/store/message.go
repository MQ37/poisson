package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Message represents a row in the messages table.
type Message struct {
	ID        string
	SessionID string
	Seq       int
	Role      string // user | assistant | tool
	Content   string // JSON array of content blocks
	DeletedAt *int64
	Compacted bool
	APICallID *string
	CreatedAt int64
}

// newUUID generates a v4 UUID using crypto/rand (no external library).
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	// Set version (4) and variant (10xx) bits per RFC 4122.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	s := hex.EncodeToString(b[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", s[0:8], s[8:12], s[12:16], s[16:20], s[20:32])
}

// extractTextFromContent parses the content JSON array and concatenates
// the text of all text-type content blocks. Non-text blocks (tool_use,
// tool_result) contribute nothing to the FTS index. If the content is not
// a JSON array, the raw string is returned as a fallback.
func extractTextFromContent(content string) string {
	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &blocks); err != nil {
		// Not a JSON array; index the raw content as-is.
		return content
	}
	var parts []string
	for _, blk := range blocks {
		var typ string
		if raw, ok := blk["type"]; ok {
			_ = json.Unmarshal(raw, &typ)
		}
		if typ != "text" {
			continue
		}
		if raw, ok := blk["text"]; ok {
			var t string
			if err := json.Unmarshal(raw, &t); err == nil {
				parts = append(parts, t)
			}
		}
	}
	return strings.Join(parts, "\n")
}

// nextSeq returns the next message sequence number for a session
// (max(seq) + 1, or 1 if there are no rows yet).
func (s *Store) nextSeq(sessionID string) (int, error) {
	var ms int
	row := s.db.QueryRow(`SELECT COALESCE(MAX(seq), 0) FROM messages WHERE session_id = ?`, sessionID)
	if err := row.Scan(&ms); err != nil {
		return 0, fmt.Errorf("next seq: %w", err)
	}
	return ms + 1, nil
}

// AppendMessage inserts a new message, generating an ID and seq if they are
// zero, and indexes the extracted text into the messages_fts FTS5 table.
func (s *Store) AppendMessage(msg *Message) error {
	if msg.ID == "" {
		msg.ID = newUUID()
	}
	if msg.Seq == 0 {
		seq, err := s.nextSeq(msg.SessionID)
		if err != nil {
			return err
		}
		msg.Seq = seq
	}
	if msg.CreatedAt == 0 {
		msg.CreatedAt = time.Now().Unix()
	}
	compacted := 0
	if msg.Compacted {
		compacted = 1
	}

	// Insert message + FTS index + session touch atomically so a failure
	// between steps can't leave the message invisible to /search.
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin append tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		`INSERT INTO messages
		 (id, session_id, seq, role, content, deleted_at, compacted, api_call_id, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?)`,
		msg.ID, msg.SessionID, msg.Seq, msg.Role, msg.Content,
		msg.DeletedAt, compacted, msg.APICallID, msg.CreatedAt); err != nil {
		return fmt.Errorf("append message: %w", err)
	}

	text := extractTextFromContent(msg.Content)
	if ftsEligible(msg.Role, text) {
		if _, err := tx.Exec(
			`INSERT INTO messages_fts (session_id, message_id, role, content_text)
			 VALUES (?,?,?,?)`,
			msg.SessionID, msg.ID, msg.Role, text); err != nil {
			return fmt.Errorf("index fts: %w", err)
		}
	}

	if _, err := tx.Exec(`UPDATE sessions SET updated_at = ? WHERE id = ?`,
		time.Now().Unix(), msg.SessionID); err != nil {
		return fmt.Errorf("touch session: %w", err)
	}

	return tx.Commit()
}

// GetMessages returns only the active messages for a session
// (deleted_at IS NULL AND compacted = 0), ordered by seq ascending.
func (s *Store) GetMessages(sessionID string) ([]Message, error) {
	rows, err := s.db.Query(
		`SELECT id, session_id, seq, role, content, deleted_at, compacted,
		        api_call_id, created_at
		 FROM messages
		 WHERE session_id = ? AND deleted_at IS NULL AND compacted = 0
		 ORDER BY seq ASC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("get messages: %w", err)
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// UpdateMessageContent overwrites a single message's content column in
// place. Used only by compaction-time stale-tool-result pruning: unlike
// AppendMessage/ApplyCompaction (which never rewrite history — only append
// or soft-flag it), this genuinely mutates a still-active row. Safe to do
// only when the caller already knows the request's cached prefix is about
// to be rewritten anyway (compaction always changes the summary system
// block + active message set), so this never causes an *extra* prompt-cache
// invalidation beyond what compaction already forces. The FTS index (built
// from the ORIGINAL content at AppendMessage time) is deliberately left
// untouched, so /search still recalls the full pre-prune text.
func (s *Store) UpdateMessageContent(id, content string) error {
	_, err := s.db.Exec(`UPDATE messages SET content = ? WHERE id = ?`, content, id)
	if err != nil {
		return fmt.Errorf("update message content: %w", err)
	}
	return nil
}

// GetAllMessages returns every non-deleted message for a session, active AND
// compacted, ordered by seq ascending. Compaction only sets compacted = 1 —
// it never deletes rows — so the full conversation is always still here.
// Unlike GetMessages (what the agent actually sends the model), this is for
// display: resuming a session should be able to show the complete history,
// not just what's left after the most recent compaction.
func (s *Store) GetAllMessages(sessionID string) ([]Message, error) {
	rows, err := s.db.Query(
		`SELECT id, session_id, seq, role, content, deleted_at, compacted,
		        api_call_id, created_at
		 FROM messages
		 WHERE session_id = ? AND deleted_at IS NULL
		 ORDER BY seq ASC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("get all messages: %w", err)
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// scanMessage scans a message row from either *sql.Row or *sql.Rows.
func scanMessage(sc scanner) (*Message, error) {
	var m Message
	var deletedAt sql.NullInt64
	var apiCallID sql.NullString
	var compacted int
	err := sc.Scan(
		&m.ID, &m.SessionID, &m.Seq, &m.Role, &m.Content,
		&deletedAt, &compacted, &apiCallID, &m.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("scan message: %w", err)
	}
	if deletedAt.Valid {
		v := deletedAt.Int64
		m.DeletedAt = &v
	}
	if apiCallID.Valid {
		v := apiCallID.String
		m.APICallID = &v
	}
	m.Compacted = compacted != 0
	return &m, nil
}
