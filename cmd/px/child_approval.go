package main

import (
	"encoding/json"
	"os"
	"sync"
)

// childApprovalBroker serializes stdin reads for bash approval in child mode.
// Approval requests are queued FIFO; each waits indefinitely for a response.
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
				Approved bool `json:"approved"`
			}
			if json.Unmarshal(scanner.Bytes(), &resp) != nil {
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
	}()
}

// wait blocks until the next approval_response is received for this request.
func (b *childApprovalBroker) wait() bool {
	b.start()
	respCh := make(chan bool, 1)
	b.mu.Lock()
	b.queue = append(b.queue, respCh)
	b.mu.Unlock()
	return <-respCh
}