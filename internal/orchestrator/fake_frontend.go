package orchestrator

import (
	"context"
	"fmt"
	"sync"
)

// SentMessage records one FakeFrontend.Send call, for test assertions.
type SentMessage struct {
	Key ChannelKey
	Msg Message
}

// EditCall records one FakeFrontend.Edit call.
type EditCall struct {
	Key   ChannelKey
	MsgID string
	Text  string
}

// FakeFrontend is an in-memory Frontend test double — no real transport,
// following the exact pattern internal/sandbox/fake_driver.go establishes
// for Driver: pre-programmed/observable behavior, and a way to inject
// Commands directly instead of driving them through any real protocol.
type FakeFrontend struct {
	mu    sync.Mutex
	out   chan<- Command // captured by Run; Inject sends onto this
	sent  []SentMessage
	edits []EditCall

	closedChannels []ChannelKey
	nextTopicID    int

	// SendErr, when set, makes every Send fail with this error.
	SendErr error
	// EditErr, when set, makes every Edit fail — exercising a caller's
	// fallback-to-Send path.
	EditErr error
}

// NewFakeFrontend returns an empty FakeFrontend.
func NewFakeFrontend() *FakeFrontend {
	return &FakeFrontend{}
}

// Run captures out (so Inject can use it) and blocks until ctx is
// cancelled, matching a real Frontend's Run contract.
func (f *FakeFrontend) Run(ctx context.Context, out chan<- Command) error {
	f.mu.Lock()
	f.out = out
	f.mu.Unlock()
	<-ctx.Done()
	return ctx.Err()
}

// Inject pushes cmd directly onto the channel Run captured — the mechanism
// a Core test uses to simulate an incoming Telegram message with zero real
// transport involved. Must be called only after Run has actually started
// (typically Run is launched in its own goroutine first); panics with a
// clear message rather than silently doing nothing against a nil channel.
func (f *FakeFrontend) Inject(cmd Command) {
	f.mu.Lock()
	out := f.out
	f.mu.Unlock()
	if out == nil {
		panic("FakeFrontend.Inject called before Run started")
	}
	out <- cmd
}

// Ready reports whether Run has started and captured its output channel —
// tests should wait for this before calling Inject, since Inject panics if
// called too early.
func (f *FakeFrontend) Ready() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.out != nil
}

func (f *FakeFrontend) CreateChannel(_ context.Context, title string) (ChannelKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextTopicID++
	return ChannelKey{Frontend: "fake", Chat: "fake-chat", Topic: fmt.Sprintf("%d", f.nextTopicID)}, nil
}

func (f *FakeFrontend) CloseChannel(_ context.Context, key ChannelKey) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closedChannels = append(f.closedChannels, key)
	return nil
}

func (f *FakeFrontend) Send(_ context.Context, key ChannelKey, msg Message) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.SendErr != nil {
		return "", f.SendErr
	}
	f.sent = append(f.sent, SentMessage{Key: key, Msg: msg})
	return fmt.Sprintf("%d", len(f.sent)), nil
}

func (f *FakeFrontend) Edit(_ context.Context, key ChannelKey, msgID, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.EditErr != nil {
		return f.EditErr
	}
	f.edits = append(f.edits, EditCall{Key: key, MsgID: msgID, Text: text})
	return nil
}

// Sent returns every message sent so far, in order.
func (f *FakeFrontend) Sent() []SentMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]SentMessage(nil), f.sent...)
}

// Edits returns every Edit call so far, in order.
func (f *FakeFrontend) Edits() []EditCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]EditCall(nil), f.edits...)
}

// ClosedChannels returns every key CloseChannel was called with, in order.
func (f *FakeFrontend) ClosedChannels() []ChannelKey {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ChannelKey(nil), f.closedChannels...)
}

var _ Frontend = (*FakeFrontend)(nil)
