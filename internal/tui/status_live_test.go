package tui

import (
	"strings"
	"testing"
)

// TestStatusShowsModelAndClassifier keeps both spending models visible: the
// conversation's model and the bash-risk classifier, which bills separately
// once per gated command.
func TestStatusShowsModelAndClassifier(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)
	a.SetClassifierModel("tiny-classifier")

	cmdStatus(cmdHost(tui))
	out := testScrollOutput(tui)

	if !strings.Contains(out, a.Model()) {
		t.Errorf("/status missing the session model %q: %q", a.Model(), out)
	}
	if !strings.Contains(out, "tiny-classifier") {
		t.Errorf("/status missing the classifier model: %q", out)
	}
	if !strings.Contains(out, "pinned this session") {
		t.Errorf("/status should say where the classifier came from: %q", out)
	}
}

// TestStatusRunsWhileTurnInFlight covers the complaint that /status was
// unusable exactly when it is most wanted: mid-turn, to check which models are
// burning quota. It only reads state, so it must run instead of being refused
// as "can't queue a / command while busy".
func TestStatusRunsWhileTurnInFlight(t *testing.T) {
	_, a, sessionID := newTestStoreAndAgent(t)
	tui := newTUIWithAgent(a, sessionID)
	tui.status.Thinking = true // a turn is running

	tui.enqueueLocked("/status")
	out := testScrollOutput(tui)

	if strings.Contains(out, "can't queue a / command while busy") {
		t.Fatalf("/status was refused mid-turn: %q", out)
	}
	if !strings.Contains(out, "Session "+sessionID) {
		t.Fatalf("/status produced no report mid-turn: %q", out)
	}
	if len(tui.queued) != 0 {
		t.Fatalf("/status must not be queued as a message, queued = %v", tui.queued)
	}
}
