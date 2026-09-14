// Package telegram implements orchestrator.Frontend over the Telegram Bot
// API: one supergroup with Topics, one topic per instance, long-polling
// getUpdates. See docs/orchestrator-plan.md Step 25.
package telegram

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mq37/poisson/internal/orchestrator"
)

// generalTopicPollTimeout is the long-poll duration passed to every
// getUpdates call.
const generalTopicPollTimeout = 50

// pollBackoffMax bounds the exponential backoff a dropped long-poll
// connection retries with — a Tailscale flap or carrier NAT timeout is a
// normal, expected event, never treated as fatal on its own.
const pollBackoffMax = 30 * time.Second

// Frontend implements orchestrator.Frontend over one Telegram supergroup.
type Frontend struct {
	client         *Client
	chatID         int64
	allowedUserIDs map[string]bool
	offsetPath     string

	offset int64 // next getUpdates call's offset: last processed update_id + 1
}

// NewFrontend returns a Frontend for chatID, restricted to allowedUserIDs
// (numeric Telegram user ids as strings). stateRoot is where the getUpdates
// offset is persisted (survives a restart — see Step 25's edge case on
// offset-after-enqueue ordering).
func NewFrontend(client *Client, chatID int64, allowedUserIDs []string, stateRoot string) *Frontend {
	allowed := make(map[string]bool, len(allowedUserIDs))
	for _, id := range allowedUserIDs {
		allowed[id] = true
	}
	f := &Frontend{
		client:         client,
		chatID:         chatID,
		allowedUserIDs: allowed,
		offsetPath:     filepath.Join(stateRoot, "telegram_offset"),
	}
	f.offset = f.loadOffset()
	return f
}

func (f *Frontend) loadOffset() int64 {
	data, err := os.ReadFile(f.offsetPath)
	if err != nil {
		return 0 // first run, or file genuinely missing — start from zero
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		log.Printf("orchestrator/telegram: corrupt offset file %s, starting from 0: %v", f.offsetPath, err)
		return 0
	}
	return n
}

// advanceOffset persists updateID+1 as the next offset. Called ONLY after
// the corresponding Command has been durably handed to Core (see Run) —
// never before — so a crash between receiving an update and actually
// enqueuing it replays that update on restart instead of silently losing
// it.
func (f *Frontend) advanceOffset(updateID int64) {
	f.offset = updateID + 1
	if err := orchestrator.WriteFileAtomic(f.offsetPath, []byte(strconv.FormatInt(f.offset, 10)), 0o600); err != nil {
		log.Printf("orchestrator/telegram: failed to persist offset: %v", err)
	}
}

// Run long-polls getUpdates and emits a Command per recognized message onto
// out, until ctx is cancelled or a fatal (409 Conflict — a second poller
// already running with this token) error occurs.
func (f *Frontend) Run(ctx context.Context, out chan<- orchestrator.Command) error {
	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		updates, err := f.client.GetUpdates(ctx, f.offset, generalTopicPollTimeout)
		if err != nil {
			if errors.Is(err, ErrConflict) {
				return fmt.Errorf("telegram: %w", err)
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Printf("orchestrator/telegram: getUpdates failed, retrying in %s: %v", backoff, err)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return ctx.Err()
			}
			if backoff *= 2; backoff > pollBackoffMax {
				backoff = pollBackoffMax
			}
			continue
		}
		backoff = time.Second

		for _, u := range updates {
			if !f.handleUpdate(ctx, u, out) {
				return ctx.Err()
			}
		}
	}
}

// handleUpdate processes one update, returning false only when the caller
// should stop entirely (ctx cancelled while trying to enqueue).
func (f *Frontend) handleUpdate(ctx context.Context, u Update, out chan<- orchestrator.Command) bool {
	// Edited messages are ignored entirely — never re-process a message
	// just because the user edited it afterward.
	msg := u.Message
	if msg == nil {
		f.advanceOffset(u.UpdateID)
		return true
	}

	if msg.Chat.ID != f.chatID {
		f.advanceOffset(u.UpdateID)
		return true
	}
	// An unauthorized sender is ignored entirely, silently — no reply at
	// all, since an error reply is itself a "yes, there's a bot here"
	// oracle to a potential attacker (docs/orchestrator-plan.md Step 22).
	if !f.allowedUserIDs[strconv.FormatInt(msg.From.ID, 10)] {
		f.advanceOffset(u.UpdateID)
		return true
	}

	if strings.TrimSpace(msg.Text) == "" {
		if _, err := f.client.SendMessage(ctx, f.chatID, msg.MessageThreadID, "text only, please"); err != nil {
			log.Printf("orchestrator/telegram: failed to reply to a non-text message: %v", err)
		}
		f.advanceOffset(u.UpdateID)
		return true
	}

	cmd := parseCommand(msg, f.chatID)
	select {
	case out <- cmd:
		// Persisted only after the command is durably enqueued — a crash
		// between receiving this update and actually handing it to Core
		// replays it on restart instead of losing it.
		f.advanceOffset(u.UpdateID)
		return true
	case <-ctx.Done():
		return false
	}
}

// commandVerbs maps a recognized "/word" (already lowercased, with any
// "@BotName" suffix stripped) to its CommandKind.
var commandVerbs = map[string]orchestrator.CommandKind{
	"/new":      orchestrator.CmdNew, // alias for /new-box
	"/new-box":  orchestrator.CmdNewBox,
	"/new-host": orchestrator.CmdNewHost,
	"/list":     orchestrator.CmdList,
	"/status":   orchestrator.CmdStatus,
	"/model":    orchestrator.CmdModel,
	"/suspend":  orchestrator.CmdSuspend,
	"/resume":   orchestrator.CmdResume,
	"/kill":     orchestrator.CmdKill,
	"/approve":  orchestrator.CmdApprove,
	"/deny":     orchestrator.CmdDeny,
	"/cancel":   orchestrator.CmdCancel,
	"/help":     orchestrator.CmdHelp,
}

// parseCommand turns one Telegram message into an orchestrator.Command.
// Messages in the group's General topic (no message_thread_id at all, 0)
// naturally route to Core's instance-less commands (/new, /list) since
// ChannelKey.Topic is "0" there and no instance is ever registered under
// it. An unrecognized "/word" falls through as a plain CmdMessage (passed
// straight to the agent as text) rather than an error — deliberately
// permissive, since a typo'd command is far more likely than a user
// actually wanting literal text starting with "/".
func parseCommand(msg *Message, chatID int64) orchestrator.Command {
	key := orchestrator.ChannelKey{
		Frontend: "telegram",
		Chat:     strconv.FormatInt(chatID, 10),
		Topic:    strconv.Itoa(msg.MessageThreadID),
	}
	text := strings.TrimSpace(msg.Text)
	cmd := orchestrator.Command{
		Key:    key,
		Text:   text,
		UserID: strconv.FormatInt(msg.From.ID, 10),
		At:     time.Unix(msg.Date, 0),
	}

	fields := strings.Fields(text)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		cmd.Kind = orchestrator.CmdMessage
		return cmd
	}

	verb := strings.ToLower(fields[0])
	if at := strings.IndexByte(verb, '@'); at >= 0 {
		verb = verb[:at] // "/new@LePoissonBot" -> "/new"
	}
	kind, ok := commandVerbs[verb]
	if !ok {
		cmd.Kind = orchestrator.CmdMessage
		return cmd
	}
	cmd.Kind = kind
	cmd.Args = fields[1:]
	return cmd
}

// CreateChannel opens a new forum topic titled title.
func (f *Frontend) CreateChannel(ctx context.Context, title string) (orchestrator.ChannelKey, error) {
	threadID, err := f.client.CreateForumTopic(ctx, f.chatID, title)
	if err != nil {
		return orchestrator.ChannelKey{}, err
	}
	return orchestrator.ChannelKey{
		Frontend: "telegram",
		Chat:     strconv.FormatInt(f.chatID, 10),
		Topic:    strconv.Itoa(threadID),
	}, nil
}

// CloseChannel closes (archives) an existing forum topic.
func (f *Frontend) CloseChannel(ctx context.Context, key orchestrator.ChannelKey) error {
	threadID, err := strconv.Atoi(key.Topic)
	if err != nil {
		return fmt.Errorf("telegram: invalid topic id %q: %w", key.Topic, err)
	}
	return f.client.CloseForumTopic(ctx, f.chatID, threadID)
}

// Send delivers msg to key, returning the first chunk's message_id (see
// Client.SendMessage) usable with Edit.
func (f *Frontend) Send(ctx context.Context, key orchestrator.ChannelKey, msg orchestrator.Message) (string, error) {
	threadID, _ := strconv.Atoi(key.Topic) // 0 (General) on any parse failure
	id, err := f.client.SendMessage(ctx, f.chatID, threadID, renderMessage(msg))
	if err != nil {
		return "", err
	}
	return strconv.Itoa(id), nil
}

// Edit updates a previously sent message's text.
func (f *Frontend) Edit(ctx context.Context, key orchestrator.ChannelKey, msgID, text string) error {
	id, err := strconv.Atoi(msgID)
	if err != nil {
		return fmt.Errorf("telegram: invalid message id %q: %w", msgID, err)
	}
	return f.client.EditMessageText(ctx, f.chatID, id, text)
}

// renderMessage adds a plain-text kind prefix — no Markdown/HTML (see
// client.go's own doc comment on why parse_mode is never used).
func renderMessage(msg orchestrator.Message) string {
	switch msg.Kind {
	case orchestrator.MsgError:
		return "⚠️ " + msg.Text
	case orchestrator.MsgLifecycle:
		return "ℹ️ " + msg.Text
	default:
		return msg.Text
	}
}

var _ orchestrator.Frontend = (*Frontend)(nil)
