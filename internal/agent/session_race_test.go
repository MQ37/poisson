package agent

import (
	"sync"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/config"
	"github.com/mq37/poisson/internal/provider"
	"github.com/mq37/poisson/internal/store"
	"github.com/mq37/poisson/internal/testutil"
)

// TestRecordSubagentUsageBillsSpawningSession is the regression guard for
// billing a finished async subagent job's cost to whatever session it was
// SPAWNED under, not whatever session happens to be live by the time it
// finishes — an async job deliberately outlives its spawning turn, so by
// completion time the user may have /new'd or /resume'd elsewhere.
func TestRecordSubagentUsageBillsSpawningSession(t *testing.T) {
	dir := testutil.TempDir(t)
	st, err := store.Open(dir + "/test.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	const spawningSession, liveSession = "session-a-spawned-job", "session-b-now-live"
	for _, id := range []string{spawningSession, liveSession} {
		if err := st.CreateSession(&store.Session{
			ID: id, Cwd: ".", Provider: "fake", Model: "test-model", CreatedAt: time.Now().Unix(),
		}); err != nil {
			t.Fatalf("create session %s: %v", id, err)
		}
	}

	a := newTestAgentForSession(st, spawningSession)
	a.config = config.DefaultConfig()
	a.config.Pricing["fake"] = map[string]config.Pricing{
		"test-model": {InputPerMTok: 1.0, OutputPerMTok: 2.0},
	}
	// The user switches away from the spawning session before the job's
	// completion arrives — exactly what happens after a /new or /resume.
	a.SwitchSession(liveSession)

	usage := &provider.Usage{InputTokens: 500, OutputTokens: 300}
	if _, err := a.RecordSubagentUsage(spawningSession, "fake", "test-model", usage, 0); err != nil {
		t.Fatalf("RecordSubagentUsage: %v", err)
	}

	spawnCost, err := st.GetSessionCost(spawningSession)
	if err != nil {
		t.Fatalf("GetSessionCost(spawning): %v", err)
	}
	if spawnCost <= 0 {
		t.Fatalf("spawning session cost = %v, want positive (the job's cost belongs here)", spawnCost)
	}
	liveCost, err := st.GetSessionCost(liveSession)
	if err != nil {
		t.Fatalf("GetSessionCost(live): %v", err)
	}
	if liveCost != 0 {
		t.Fatalf("live session cost = %v, want 0 (job's cost must not leak to whatever session is live now)", liveCost)
	}
}

// TestSessionIDAndToolStatsRaceFree runs SwitchSession, SessionID, and the
// tool-call counters concurrently under -race: SwitchSession simulates /new
// and /resume from the input goroutine, SessionID/RecordSubagentUsage
// simulate an async job's completion goroutine reading/billing the session
// long after its own turn ended, and SessionToolStats simulates the TUI's
// render goroutine polling every ~33ms. Before making sessionIDStore an
// atomic.Pointer and the counters atomic.Int64, this reliably tripped the
// race detector.
func TestSessionIDAndToolStatsRaceFree(t *testing.T) {
	dir := testutil.TempDir(t)
	st, err := store.Open(dir + "/test.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	for _, id := range []string{"race-a", "race-b"} {
		if err := st.CreateSession(&store.Session{ID: id, Cwd: ".", Provider: "fake", Model: "m", CreatedAt: time.Now().Unix()}); err != nil {
			t.Fatalf("create session %s: %v", id, err)
		}
	}

	a := newTestAgentForSession(st, "race-a")

	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(3)

	go func() { // input goroutine: /new, /resume back and forth
		defer wg.Done()
		toggle := false
		for {
			select {
			case <-stop:
				return
			default:
				if toggle {
					a.SwitchSession("race-a")
				} else {
					a.SwitchSession("race-b")
				}
				toggle = !toggle
			}
		}
	}()

	go func() { // turn-loop goroutine: increments the counters directly
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				a.sessionToolCalls.Add(1)
				a.sessionToolErrors.Add(1)
			}
		}
	}()

	go func() { // render goroutine: reads SessionID + counters
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = a.SessionID()
				_, _ = a.SessionToolStats()
			}
		}
	}()

	time.Sleep(200 * time.Millisecond)
	close(stop)
	wg.Wait()
}
