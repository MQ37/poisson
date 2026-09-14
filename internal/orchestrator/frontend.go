package orchestrator

import "context"

// MessageKind tags Message's rendering hint — plain text vs. a
// lifecycle/status notice vs. an error — so a frontend can style them
// differently (e.g. Telegram's emoji prefixes) without Core needing to
// know anything transport-specific about how that's done.
type MessageKind int

const (
	MsgText MessageKind = iota
	MsgLifecycle
	MsgError
)

// Message is what Core sends to a channel — plain text plus an optional
// kind tag. No Telegram-shaped (or any transport-shaped) fields.
type Message struct {
	Text string
	Kind MessageKind
}

// Frontend is one transport. internal/orchestrator/telegram is the first
// implementation (M4); Discord/HTTP/web are later implementations of this
// exact interface and nothing else — adding one is one new file plus one
// line in a `--frontend` switch, no Core or Runtime change (see
// docs/orchestrator-plan.md §2.1's requirement-7 contract).
type Frontend interface {
	// Run starts listening for commands (e.g. Telegram's long-poll
	// getUpdates loop), emitting each onto out, until ctx is cancelled.
	Run(ctx context.Context, out chan<- Command) error
	// CreateChannel opens a new routing target (e.g. a Telegram forum
	// topic) titled title and returns its key.
	CreateChannel(ctx context.Context, title string) (ChannelKey, error)
	// CloseChannel closes/archives an existing channel. Best-effort: a
	// failure here must never block whatever destroyed the underlying
	// instance.
	CloseChannel(ctx context.Context, key ChannelKey) error
	// Send delivers msg to key and returns an opaque message id usable with
	// Edit later (e.g. Telegram's message_id) — "" if the frontend has no
	// concept of addressable messages at all (Edit against "" is always a
	// no-op error; callers fall back to a fresh Send).
	Send(ctx context.Context, key ChannelKey, msg Message) (msgID string, err error)
	// Edit updates a previously sent message's text. Explicitly
	// best-effort — not every frontend can support it, and even one that
	// can may fail for an individual message (e.g. already too old to
	// edit); callers are expected to fall back to a plain Send on error,
	// never treat an Edit failure as fatal.
	Edit(ctx context.Context, key ChannelKey, msgID, text string) error
}
