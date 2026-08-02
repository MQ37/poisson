package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/subagent"
)

// TestSubagentGlobalConcurrencyCapAcrossNestedBatches reproduces the gap
// scouting found: batchMaxConcurrent (8) and agent.maxConcurrentToolCalls
// (8) each cap fan-out WITHIN their own call, but nothing previously capped
// the combined total across several independent batch calls running at
// once — e.g. several of an agent round's own 8 top-level tool_use slots
// each being their own `batch` call, each spawning its own 8-wide subagent
// fan-out (8x8=64 potential concurrent child processes). This spawns 3
// concurrent batch calls of 8 subagents each (24 total spawns) against REAL
// child processes and measures the actual peak concurrently-running count
// via marker files, asserting it never exceeds maxConcurrentSubagents (8)
// process-wide — not just within any single batch.
func TestSubagentGlobalConcurrencyCapAcrossNestedBatches(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	scriptDir := t.TempDir()
	markerDir := t.TempDir()
	scriptPath := scriptDir + "/fake-child-marker.sh"
	script := fmt.Sprintf(`#!/bin/sh
marker="%s/running-$$"
touch "$marker"
sleep 0.3
rm -f "$marker"
printf '{"type":"done","success":true,"turns":1,"contextTokens":10,"contextWindow":200000}\n'
`, markerDir)
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake child script: %v", err)
	}
	restore := subagent.SetLookupExecutableForTest(scriptPath)
	defer restore()

	tool := NewSubagentTool(".", alwaysApproveSubagent)
	tool.SetRuntime(
		func() string { return "anthropic" },
		func() string { return "claude-opus-5" },
		func() string { return "" },
	)
	reg := NewRegistry()
	reg.Register(tool)
	bt := NewBatchTool(reg)
	reg.Register(bt)

	const perBatch = batchMaxConcurrent // 8
	const nBatches = 3                  // 24 total spawns, well over maxConcurrentSubagents (8)
	calls := make([]map[string]interface{}, perBatch)
	for i := range calls {
		calls[i] = map[string]interface{}{"tool": "subagent", "input": map[string]string{"task": "scout"}}
	}
	body := mustJSON(t, map[string]interface{}{"calls": calls})

	// Poll the marker directory concurrently with the batches to find the
	// real peak simultaneously-running child processes across the WHOLE
	// test, not just within one batch call.
	var peak int32
	stopPolling := make(chan struct{})
	pollDone := make(chan struct{})
	go func() {
		defer close(pollDone)
		for {
			select {
			case <-stopPolling:
				return
			default:
			}
			entries, _ := os.ReadDir(markerDir)
			if n := int32(len(entries)); n > atomic.LoadInt32(&peak) {
				atomic.StoreInt32(&peak, n)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	var wg sync.WaitGroup
	wg.Add(nBatches)
	for i := 0; i < nBatches; i++ {
		go func() {
			defer wg.Done()
			bt.Execute(context.Background(), body)
		}()
	}
	wg.Wait()
	close(stopPolling)
	<-pollDone

	if got := atomic.LoadInt32(&peak); got > maxConcurrentSubagents {
		t.Errorf("peak concurrent subagent child processes = %d, want <= %d (maxConcurrentSubagents) — %d batches x %d each must be capped globally, not just per-batch", got, maxConcurrentSubagents, nBatches, perBatch)
	}
	if got := atomic.LoadInt32(&peak); got == 0 {
		t.Fatalf("peak concurrent subagent child processes = 0 — marker-file measurement itself is broken, not a real pass")
	}
}
