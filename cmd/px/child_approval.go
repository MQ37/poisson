package main

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

const childApprovalTimeout = 30 * time.Second

// childApprovalBroker serializes stdin reads for bash approval in child mode.
// Approval requests are queued FIFO; each waits up to childApprovalTimeout.
type childApprovalBroker struct {
	mu      sync.Mutex
	queue   []chan bool
	started bool
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

// wait blocks until the next approval_response is received for this request.
func (b *childApprovalBroker) wait() bool {
	b.start()
	respCh := make(chan bool, 1)
	b.mu.Lock()
	b.queue = append(b.queue, respCh)
	b.mu.Unlock()

	timer := time.NewTimer(childApprovalTimeout)
	defer timer.Stop()

	select {
	case approved := <-respCh:
		return approved
	case <-timer.C:
		b.removeWaiter(respCh)
		return false
	}
}

func (b *childApprovalBroker) removeWaiter(ch chan bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, w := range b.queue {
		if w == ch {
			b.queue = append(b.queue[:i], b.queue[i+1:]...)
			return
		}
	}
}