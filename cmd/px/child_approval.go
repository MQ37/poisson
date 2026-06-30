package main

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// childApprovalBroker serializes stdin reads for bash approval in child mode.
// A single reader goroutine dispatches lines to the active waiter only.
type childApprovalBroker struct {
	mu      sync.Mutex
	waiter  chan bool
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
				Approved bool `json:"approved"`
			}
			if json.Unmarshal(scanner.Bytes(), &resp) != nil {
				continue
			}
			b.mu.Lock()
			ch := b.waiter
			b.mu.Unlock()
			if ch != nil {
				select {
				case ch <- resp.Approved:
				default:
				}
			}
		}
	}()
}

func (b *childApprovalBroker) wait(timeout time.Duration) bool {
	b.start()
	respCh := make(chan bool, 1)
	b.mu.Lock()
	if b.waiter != nil {
		b.mu.Unlock()
		return false
	}
	b.waiter = respCh
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		if b.waiter == respCh {
			b.waiter = nil
		}
		b.mu.Unlock()
	}()

	select {
	case approved := <-respCh:
		return approved
	case <-time.After(timeout):
		go func() {
			select {
			case <-respCh:
			case <-time.After(5 * time.Second):
			}
		}()
		return false
	}
}