package main

import (
	"encoding/json"
	"os"
	"sync"
)

// childApprovalBroker serializes stdin reads for bash approval in child mode.
// Approval requests are queued FIFO; each waits indefinitely for a response.
// The parent (which owns the human prompt) has no timeout, so neither do we;
// stdin EOF or a killed process is the only way out.
// approvalReply is a parent's answer to one approval_request: whether it was
// allowed, and (when denied) an optional human-supplied reason.
type approvalReply struct {
	Approved bool
	Reason   string
}

type childApprovalBroker struct {
	mu       sync.Mutex
	serialMu sync.Mutex
	queue    []chan approvalReply
	started  bool

	// onExpedite is invoked when the parent sends an "expedite" message. Set
	// once before start(); read only from the reader goroutine.
	onExpedite func()
}

// emitAndWait sends one approval request to the parent and blocks for its
// response. serialMu keeps a single approval outstanding, so the FIFO queue
// never holds more than one waiter — otherwise concurrent bash approvals in a
// turn could be paired with each other's responses.
//
// register() runs BEFORE writeChildEvent, not after: if the write happened
// first, a reply that lands before the waiter is queued would find an empty
// queue and get silently dropped (reader goroutine's `if len(b.queue) == 0`
// case), leaving this call blocked on a channel nobody will ever send to.
func (b *childApprovalBroker) emitAndWait(req map[string]interface{}) (bool, string) {
	b.serialMu.Lock()
	defer b.serialMu.Unlock()
	respCh := b.register()
	writeChildEvent(req)
	r := <-respCh
	return r.Approved, r.Reason
}

func (b *childApprovalBroker) start() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.started {
		return
	}
	b.started = true
	go func() {
		scanner := bufioNewScanner(os.Stdin)
		for scanner.Scan() {
			var msg struct {
				Type     string `json:"type"`
				Approved bool   `json:"approved"`
				Reason   string `json:"reason"`
			}
			if json.Unmarshal(scanner.Bytes(), &msg) != nil {
				continue
			}
			if msg.Type == "expedite" {
				if b.onExpedite != nil {
					b.onExpedite()
				}
				continue
			}
			if msg.Type != "approval_response" {
				continue
			}
			b.mu.Lock()
			if len(b.queue) == 0 {
				b.mu.Unlock()
				continue
			}
			ch := b.queue[0]
			b.queue = b.queue[1:]
			b.mu.Unlock()
			ch <- approvalReply{Approved: msg.Approved, Reason: msg.Reason}
		}
		b.denyAllWaiters()
	}()
}

func (b *childApprovalBroker) denyAllWaiters() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.queue {
		select {
		case ch <- approvalReply{}:
		default:
		}
	}
	b.queue = nil
}

// register starts the stdin reader (if not already running) and queues a
// reply channel for the next approval_response, before any request that
// would trigger one has been sent. The caller blocks on the returned channel;
// a reply is delivered by the reader goroutine in start(), or by
// denyAllWaiters() on stdin EOF (parent gone).
func (b *childApprovalBroker) register() chan approvalReply {
	b.start()
	respCh := make(chan approvalReply, 1)
	b.mu.Lock()
	b.queue = append(b.queue, respCh)
	b.mu.Unlock()
	return respCh
}

// wait registers a waiter and blocks until its response arrives. Exists
// alongside register()/emitAndWait for tests that exercise the queue/reply
// path directly, without going through a real request write.
func (b *childApprovalBroker) wait() approvalReply {
	return <-b.register()
}