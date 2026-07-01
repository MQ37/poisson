package main

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestChildApprovalBrokerFIFO(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = oldStdin })

	writeApproval := func(approved bool) {
		t.Helper()
		data, _ := json.Marshal(map[string]interface{}{
			"type":     "approval_response",
			"approved": approved,
		})
		if _, err := w.Write(append(data, '\n')); err != nil {
			t.Fatalf("write approval: %v", err)
		}
	}

	var broker childApprovalBroker
	results := make(chan bool, 2)
	go func() {
		results <- broker.wait()
		results <- broker.wait()
	}()

	waitForQueueLen := func(n int) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for {
			broker.mu.Lock()
			got := len(broker.queue)
			broker.mu.Unlock()
			if got == n {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("timeout waiting for queue len %d (got %d)", n, got)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	waitForQueueLen(1)
	writeApproval(true)
	if got := <-results; !got {
		t.Fatal("first waiter expected approval")
	}

	waitForQueueLen(1)
	writeApproval(false)
	if got := <-results; got {
		t.Fatal("second waiter expected denial")
	}
}

func TestChildApprovalBrokerWaitsIndefinitely(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = oldStdin })

	var broker childApprovalBroker
	start := time.Now()
	done := make(chan bool, 1)
	go func() {
		done <- broker.wait()
	}()

	time.Sleep(150 * time.Millisecond)
	data, _ := json.Marshal(map[string]interface{}{
		"type":     "approval_response",
		"approved": true,
	})
	if _, err := w.Write(append(data, '\n')); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-done:
		if !got {
			t.Fatal("expected approval")
		}
		if time.Since(start) < 100*time.Millisecond {
			t.Fatal("returned too quickly")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
}

func TestChildApprovalBrokerDropsOrphanResponse(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = oldStdin })

	var broker childApprovalBroker
	broker.start()

	data, _ := json.Marshal(map[string]interface{}{
		"type":     "approval_response",
		"approved": true,
	})
	if _, err := w.Write(append(data, '\n')); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)

	done := make(chan bool, 1)
	go func() {
		done <- broker.wait()
	}()

	time.Sleep(50 * time.Millisecond)
	if _, err := w.Write(append(data, '\n')); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-done:
		if !got {
			t.Fatal("expected queued waiter to receive response")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}

}