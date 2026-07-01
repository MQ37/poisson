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
type childApprovalBroker struct {
	mu       sync.Mutex
	serialMu sync.Mutex
	queue    []chan bool
	started  bool
}

// emitAndWait sends one approval request to the parent and blocks for its
// response. serialMu keeps a single approval outstanding, so the FIFO queue
// never holds more than one waiter — otherwise concurrent bash approvals in a
// turn could be paired with each other's responses.
func (b *childApprovalBroker) emitAndWait(req map[string]interface{}) bool {
	b.serialMu.Lock()
	defer b.serialMu.Unlock()
	writeChildEvent(req)
	return b.wait()
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
			var resp struct {
				Type     string `json:"type"`
				Approved bool   `json:"approved"`
			}
			if json.Unmarshal(scanner.Bytes(), &resp) != nil || resp.Type != "approval_response" {
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
			ch <- resp.Approved
		}
		b.denyAllWaiters()
	}()
}

func (b *childApprovalBroker) denyAllWaiters() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.queue {
		select {
		case ch <- false:
		default:
		}
	}
	b.queue = nil
}

// wait blocks until the next approval_response is received for this request,
// or the stdin reader hits EOF (parent gone) and denies all waiters.
func (b *childApprovalBroker) wait() bool {
	b.start()
	respCh := make(chan bool, 1)
	b.mu.Lock()
	b.queue = append(b.queue, respCh)
	b.mu.Unlock()
	return <-respCh
}