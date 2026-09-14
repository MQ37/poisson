// Package telegram is a minimal Telegram Bot API client — just the methods
// the orchestrator's Telegram frontend needs (plain long-poll updates,
// sending/editing text, forum-topic management), not a general SDK. Modeled
// on internal/mcpclient/client.go's idiom: stdlib net/http only, response
// structs decode only the fields actually read.
//
// Deliberately no parse_mode (Markdown/HTML) support: correctly escaping
// arbitrary LLM-generated text for either format is its own class of bugs.
// Plain text sidesteps all of it — see docs/orchestrator-plan.md Step 24.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	apiBase      = "https://api.telegram.org/bot"
	maxRespBytes = 4 << 20 // 4 MiB: cap worst-case response memory
	maxMsgLen    = 4096    // Telegram's hard per-message character limit
	maxRetries   = 5       // cap on consecutive 429 retries, not unbounded
)

// ErrConflict means a second process is already long-polling with this same
// token (HTTP 409). Unrecoverable by retrying — the caller should exit with
// a clear message rather than fight the other process for updates.
var ErrConflict = errors.New("telegram: 409 conflict (another getUpdates poller is running with this token)")

// Client talks to the Telegram Bot API for a single bot token.
type Client struct {
	token      string
	httpClient *http.Client
}

// NewClient builds a Client for the given bot token. httpClient may be nil
// to use http.DefaultClient; callers that pass GetUpdates a long poll
// timeout must ensure ctx's deadline (if any) exceeds it — this client sets
// its own per-request deadline internally and does not rely on the
// http.Client's own Timeout field.
func NewClient(token string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{token: token, httpClient: httpClient}
}

// User is the subset of Telegram's User object this client reads.
type User struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
}

// Chat is the subset of Telegram's Chat object this client reads.
type Chat struct {
	ID      int64  `json:"id"`
	Type    string `json:"type"`
	Title   string `json:"title"`
	IsForum bool   `json:"is_forum"`
}

// Message is the subset of Telegram's Message object this client reads.
type Message struct {
	MessageID       int    `json:"message_id"`
	From            User   `json:"from"`
	Chat            Chat   `json:"chat"`
	Text            string `json:"text"`
	MessageThreadID int    `json:"message_thread_id"`
	Date            int64  `json:"date"`
}

// Update is one item from getUpdates. EditedMessage is decoded (so callers
// can detect and deliberately ignore edits) but Message is what carries new
// commands.
type Update struct {
	UpdateID      int64    `json:"update_id"`
	Message       *Message `json:"message"`
	EditedMessage *Message `json:"edited_message"`
}

// apiResponse is the {"ok":bool,"result":...} envelope every Bot API method
// replies with, success or failure.
type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Parameters  *struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// GetMe returns the bot's own identity — used as a live connectivity/token
// smoke test.
func (c *Client) GetMe(ctx context.Context) (User, error) {
	var u User
	err := c.call(ctx, "getMe", nil, 15*time.Second, &u)
	return u, err
}

// GetUpdates long-polls for new updates since offset (exclusive), waiting
// up to timeoutSeconds for at least one to arrive. Pass offset as
// last_seen_update_id+1 to acknowledge everything before it.
func (c *Client) GetUpdates(ctx context.Context, offset int64, timeoutSeconds int) ([]Update, error) {
	params := map[string]any{
		"timeout": timeoutSeconds,
	}
	if offset != 0 {
		params["offset"] = offset
	}
	// The client-side deadline must exceed Telegram's own long-poll wait,
	// or every cycle spuriously times out client-side first.
	deadline := time.Duration(timeoutSeconds+20) * time.Second
	var updates []Update
	err := c.call(ctx, "getUpdates", params, deadline, &updates)
	return updates, err
}

// SendMessage sends text to chatID, optionally into a specific forum topic
// (threadID; pass 0 for the General topic / a non-forum chat). Text over
// Telegram's 4096-character limit is chunked on line boundaries into
// multiple messages, never mid-word. Returns the first chunk's message_id
// (0 only if text produced zero chunks, which chunkMessage's own contract
// never does) — a caller that later wants to Edit should only rely on this
// id when it knows the text fits in one chunk (Edit only ever targets
// short, single-chunk status lines in practice; a multi-chunk message's
// later chunks have their own ids this return value doesn't expose).
func (c *Client) SendMessage(ctx context.Context, chatID int64, threadID int, text string) (int, error) {
	var firstID int
	for i, chunk := range chunkMessage(text) {
		params := map[string]any{
			"chat_id": chatID,
			"text":    chunk,
		}
		if threadID != 0 {
			params["message_thread_id"] = threadID
		}
		var result struct {
			MessageID int `json:"message_id"`
		}
		if err := c.call(ctx, "sendMessage", params, 15*time.Second, &result); err != nil {
			return firstID, err
		}
		if i == 0 {
			firstID = result.MessageID
		}
	}
	return firstID, nil
}

// EditMessageText replaces the text of a previously sent message.
func (c *Client) EditMessageText(ctx context.Context, chatID int64, messageID int, text string) error {
	if len(text) > maxMsgLen {
		text = text[:maxMsgLen]
	}
	params := map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
		"text":       text,
	}
	return c.call(ctx, "editMessageText", params, 15*time.Second, nil)
}

// CreateForumTopic creates a new topic in a forum-enabled supergroup and
// returns its thread ID.
func (c *Client) CreateForumTopic(ctx context.Context, chatID int64, name string) (int, error) {
	params := map[string]any{
		"chat_id": chatID,
		"name":    name,
	}
	var result struct {
		MessageThreadID int `json:"message_thread_id"`
	}
	if err := c.call(ctx, "createForumTopic", params, 15*time.Second, &result); err != nil {
		return 0, err
	}
	return result.MessageThreadID, nil
}

// CloseForumTopic closes (but does not delete) a topic.
func (c *Client) CloseForumTopic(ctx context.Context, chatID int64, threadID int) error {
	params := map[string]any{"chat_id": chatID, "message_thread_id": threadID}
	return c.call(ctx, "closeForumTopic", params, 15*time.Second, nil)
}

// DeleteForumTopic deletes a topic and all its messages.
func (c *Client) DeleteForumTopic(ctx context.Context, chatID int64, threadID int) error {
	params := map[string]any{"chat_id": chatID, "message_thread_id": threadID}
	return c.call(ctx, "deleteForumTopic", params, 15*time.Second, nil)
}

// chunkMessage splits text into pieces no longer than maxMsgLen, breaking
// only at line boundaries (falling back to the limit itself for a single
// line that's already longer than that, which is never mid-word for actual
// prose but avoids an infinite loop on pathological input).
func chunkMessage(text string) []string {
	if text == "" {
		return []string{""}
	}
	if len(text) <= maxMsgLen {
		return []string{text}
	}
	var chunks []string
	var cur strings.Builder
	for _, line := range strings.SplitAfter(text, "\n") {
		if cur.Len()+len(line) > maxMsgLen && cur.Len() > 0 {
			chunks = append(chunks, cur.String())
			cur.Reset()
		}
		for len(line) > maxMsgLen {
			chunks = append(chunks, line[:maxMsgLen])
			line = line[maxMsgLen:]
		}
		cur.WriteString(line)
	}
	if cur.Len() > 0 {
		chunks = append(chunks, cur.String())
	}
	return chunks
}

// call performs one Bot API method call, retrying on 429 by sleeping the
// server-specified retry_after, and decodes "result" into out (nil to
// discard it). Error messages never include the request URL — it embeds the
// bot token.
func (c *Client) call(ctx context.Context, method string, params any, timeout time.Duration, out any) error {
	url := apiBase + c.token + "/" + method

	var body []byte
	if params != nil {
		var err error
		body, err = json.Marshal(params)
		if err != nil {
			return fmt.Errorf("telegram %s: encode params: %w", method, err)
		}
	}

	for attempt := 0; ; attempt++ {
		reqCtx, cancel := context.WithTimeout(ctx, timeout)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			cancel()
			return fmt.Errorf("telegram %s: %w", method, err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			cancel()
			return fmt.Errorf("telegram %s: %w", method, err)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxRespBytes))
		resp.Body.Close()
		cancel()
		if err != nil {
			return fmt.Errorf("telegram %s: read response: %w", method, err)
		}

		var api apiResponse
		if err := json.Unmarshal(data, &api); err != nil {
			return fmt.Errorf("telegram %s: decode response: %w", method, err)
		}

		if api.OK {
			if out != nil {
				if err := json.Unmarshal(api.Result, out); err != nil {
					return fmt.Errorf("telegram %s: decode result: %w", method, err)
				}
			}
			return nil
		}

		if api.ErrorCode == http.StatusConflict {
			return ErrConflict
		}
		if api.ErrorCode == http.StatusTooManyRequests && api.Parameters != nil && attempt < maxRetries {
			select {
			case <-time.After(time.Duration(api.Parameters.RetryAfter) * time.Second):
			case <-ctx.Done():
				return fmt.Errorf("telegram %s: %w", method, ctx.Err())
			}
			continue
		}
		return fmt.Errorf("telegram %s: %d %s", method, api.ErrorCode, api.Description)
	}
}
