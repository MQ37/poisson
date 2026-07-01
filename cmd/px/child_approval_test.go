package main

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// TestChildApprovalBrokerSerializesConcurrent verifies two concurrent approval
// round-trips are paired with the correct responses: emitAndWait keeps a single
// approval outstanding, so the second request is only emitted after the first
// is answered.
func TestChildApprovalBrokerSerializesConcurrent(t *testing.T) {
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdin, oldStdout := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW
	t.Cleanup(func() { os.Stdin, os.Stdout = oldStdin, oldStdout })

	var broker childApprovalBroker
	type res struct {
		id       string
		approved bool
	}
	results := make(chan res, 2)
	emit := func(id string) {
		approved := broker.emitAndWait(map[string]interface{}{"type": "approval_request", "command": id})
		results <- res{id, approved}
	}
	go emit("A")
	go emit("B")

	scanner := bufio.NewScanner(outR)
	readCmd := func() string {
		if !scanner.Scan() {
			t.Fatal("no request emitted")
		}
		var ev struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			t.Fatalf("parse request: %v", err)
		}
		return ev.Command
	}
	respond := func(approved bool) {
		data, _ := json.Marshal(map[string]interface{}{"type": "approval_response", "approved": approved})
		if _, err := inW.Write(append(data, '\n')); err != nil {
			t.Fatalf("write response: %v", err)
		}
	}

	first := readCmd()
	respond(true)
	if r := <-results; r.id != first || !r.approved {
		t.Fatalf("first: emitted %q, got %+v", first, r)
	}

	second := readCmd()
	respond(false)
	if r := <-results; r.id != second || r.approved {
		t.Fatalf("second: emitted %q, got %+v", second, r)
	}

	if first == second {
		t.Fatalf("both goroutines emitted %q", first)
	}
}

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

func TestChildApprovalBrokerEOFAutoDeny(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = oldStdin })

	var broker childApprovalBroker
	done := make(chan bool, 1)
	go func() {
		done <- broker.wait()
	}()

	time.Sleep(50 * time.Millisecond)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-done:
		if got {
			t.Fatal("expected auto-deny on stdin close")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for EOF auto-deny")
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