package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"poisson/internal/provider"
	"poisson/internal/store"
)

// --- helpers -----------------------------------------------------------

func appendToolUse(t *testing.T, s *store.Store, sid, toolCallID, name string, input interface{}) {
	t.Helper()
	inputJSON, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	blocks := []contentBlockJSON{{Type: "tool_use", ToolCallID: toolCallID, ToolName: name, ToolInput: inputJSON}}
	data, err := json.Marshal(blocks)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessage(&store.Message{SessionID: sid, Role: "assistant", Content: string(data)}); err != nil {
		t.Fatal(err)
	}
}

func appendToolResult(t *testing.T, s *store.Store, sid, toolCallID, result string) {
	t.Helper()
	blocks := []contentBlockJSON{{Type: "tool_result", ToolCallID: toolCallID, ToolResult: result}}
	data, err := json.Marshal(blocks)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessage(&store.Message{SessionID: sid, Role: "tool", Content: string(data)}); err != nil {
		t.Fatal(err)
	}
}

func appendUserText(t *testing.T, s *store.Store, sid, text string) {
	t.Helper()
	blocks := []contentBlockJSON{{Type: "text", Text: text}}
	data, err := json.Marshal(blocks)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessage(&store.Message{SessionID: sid, Role: "user", Content: string(data)}); err != nil {
		t.Fatal(err)
	}
}

// toolResultContent returns the ToolResult text of a message's first block.
func toolResultContent(t *testing.T, m store.Message) string {
	t.Helper()
	var blocks []contentBlockJSON
	if err := json.Unmarshal([]byte(m.Content), &blocks); err != nil {
		t.Fatal(err)
	}
	if len(blocks) == 0 {
		t.Fatal("message has no content blocks")
	}
	return blocks[0].ToolResult
}

// --- direct pruneStaleToolResults tests ---------------------------------

// TestPruneStaleToolResultsEditSupersedesEarlierRead verifies a `read`
// followed by an `edit` of the same file, followed by another `read`, gets
// its FIRST read result pruned (content changed under it) while the second
// read (the current truth) is untouched.
func TestPruneStaleToolResultsEditSupersedesEarlierRead(t *testing.T) {
	s := newTestStore(t)
	sid := newTestSession(t, s, "test-model")
	a := NewAgent(s, newFakeProvider(), nil, newTestConfig(), sid, make(chan OutputEvent, 8), nil)

	big := strings.Repeat("old content line\n", 50) // > pruneMinBytes
	newContent := strings.Repeat("new content line\n", 50)

	appendToolUse(t, s, sid, "c1", "read", map[string]interface{}{"path": "f.go"})
	appendToolResult(t, s, sid, "c1", big)
	appendToolUse(t, s, sid, "c2", "edit", map[string]interface{}{"path": "f.go", "oldText": "x", "newText": "y"})
	appendToolResult(t, s, sid, "c2", "edited f.go")
	appendToolUse(t, s, sid, "c3", "read", map[string]interface{}{"path": "f.go"})
	appendToolResult(t, s, sid, "c3", newContent)

	msgs, err := s.GetMessages(sid)
	if err != nil {
		t.Fatal(err)
	}
	a.pruneStaleToolResults(".", msgs)

	after, err := s.GetMessages(sid)
	if err != nil {
		t.Fatal(err)
	}
	// msgs[1] is the tool_result for c1 (the first read).
	firstResult := toolResultContent(t, after[1])
	if !strings.Contains(firstResult, "superseded") {
		t.Errorf("first read result should be pruned, got: %s", firstResult)
	}
	// msgs[5] is the tool_result for c3 (the second read) — must stay intact.
	secondResult := toolResultContent(t, after[5])
	if secondResult != newContent {
		t.Errorf("second read result must be untouched, got: %s", secondResult)
	}
}

// TestPruneStaleToolResultsEditSupersedesAllPriorDisjointReads verifies an
// edit prunes EVERY still-active earlier read of that path, not just the
// most recent one — two reads of disjoint (non-overlapping) ranges must both
// be caught, since neither covers the other and an older tracking scheme
// that only remembered the latest read would silently miss the first one.
func TestPruneStaleToolResultsEditSupersedesAllPriorDisjointReads(t *testing.T) {
	s := newTestStore(t)
	sid := newTestSession(t, s, "test-model")
	a := NewAgent(s, newFakeProvider(), nil, newTestConfig(), sid, make(chan OutputEvent, 8), nil)

	firstHalf := strings.Repeat("aaaa\n", 200)
	secondHalf := strings.Repeat("bbbb\n", 200)

	appendToolUse(t, s, sid, "c1", "read", map[string]interface{}{"path": "f.go", "offset": 1, "limit": 10})
	appendToolResult(t, s, sid, "c1", firstHalf)
	appendToolUse(t, s, sid, "c2", "read", map[string]interface{}{"path": "f.go", "offset": 500, "limit": 10})
	appendToolResult(t, s, sid, "c2", secondHalf)
	appendToolUse(t, s, sid, "c3", "edit", map[string]interface{}{"path": "f.go", "oldText": "x", "newText": "y"})
	appendToolResult(t, s, sid, "c3", "edited f.go")

	msgs, err := s.GetMessages(sid)
	if err != nil {
		t.Fatal(err)
	}
	a.pruneStaleToolResults(".", msgs)

	after, err := s.GetMessages(sid)
	if err != nil {
		t.Fatal(err)
	}
	if got := toolResultContent(t, after[1]); !strings.Contains(got, "superseded") {
		t.Errorf("first (disjoint-range) read should be pruned by the edit, got: %s", got)
	}
	if got := toolResultContent(t, after[3]); !strings.Contains(got, "superseded") {
		t.Errorf("second (disjoint-range) read should ALSO be pruned by the edit, got: %s", got)
	}
}

// TestPruneStaleToolResultsLaterReadSupersedesEarlier verifies a duplicate
// (unchanged) read of the same range is pruned in favor of the later one.
func TestPruneStaleToolResultsLaterReadSupersedesEarlier(t *testing.T) {
	s := newTestStore(t)
	sid := newTestSession(t, s, "test-model")
	a := NewAgent(s, newFakeProvider(), nil, newTestConfig(), sid, make(chan OutputEvent, 8), nil)

	content := strings.Repeat("same content\n", 50)

	appendToolUse(t, s, sid, "c1", "read", map[string]interface{}{"path": "f.go"})
	appendToolResult(t, s, sid, "c1", content)
	appendToolUse(t, s, sid, "c2", "read", map[string]interface{}{"path": "f.go"})
	appendToolResult(t, s, sid, "c2", content)

	msgs, err := s.GetMessages(sid)
	if err != nil {
		t.Fatal(err)
	}
	a.pruneStaleToolResults(".", msgs)

	after, err := s.GetMessages(sid)
	if err != nil {
		t.Fatal(err)
	}
	if got := toolResultContent(t, after[1]); !strings.Contains(got, "superseded") {
		t.Errorf("first (duplicate) read should be pruned, got: %s", got)
	}
	if got := toolResultContent(t, after[3]); got != content {
		t.Errorf("second read must stay intact, got: %s", got)
	}
}

// TestPruneStaleToolResultsSkipsSmallResults verifies results under
// pruneMinBytes are left alone even when superseded — not worth the churn.
func TestPruneStaleToolResultsSkipsSmallResults(t *testing.T) {
	s := newTestStore(t)
	sid := newTestSession(t, s, "test-model")
	a := NewAgent(s, newFakeProvider(), nil, newTestConfig(), sid, make(chan OutputEvent, 8), nil)

	appendToolUse(t, s, sid, "c1", "read", map[string]interface{}{"path": "f.go"})
	appendToolResult(t, s, sid, "c1", "tiny")
	appendToolUse(t, s, sid, "c2", "read", map[string]interface{}{"path": "f.go"})
	appendToolResult(t, s, sid, "c2", "tiny")

	msgs, err := s.GetMessages(sid)
	if err != nil {
		t.Fatal(err)
	}
	a.pruneStaleToolResults(".", msgs)

	after, err := s.GetMessages(sid)
	if err != nil {
		t.Fatal(err)
	}
	if got := toolResultContent(t, after[1]); got != "tiny" {
		t.Errorf("small result should be left alone, got: %s", got)
	}
}

// TestPruneStaleToolResultsSkipsTruncatedRead verifies a later read that was
// itself truncated does NOT get treated as covering an earlier read — we
// don't actually know it saw everything the earlier one did.
func TestPruneStaleToolResultsSkipsTruncatedRead(t *testing.T) {
	s := newTestStore(t)
	sid := newTestSession(t, s, "test-model")
	a := NewAgent(s, newFakeProvider(), nil, newTestConfig(), sid, make(chan OutputEvent, 8), nil)

	first := strings.Repeat("line\n", 100)
	secondTruncated := strings.Repeat("line\n", 100) + "\n... (output truncated at 2000 lines)\n"

	appendToolUse(t, s, sid, "c1", "read", map[string]interface{}{"path": "f.go"})
	appendToolResult(t, s, sid, "c1", first)
	appendToolUse(t, s, sid, "c2", "read", map[string]interface{}{"path": "f.go"})
	appendToolResult(t, s, sid, "c2", secondTruncated)

	msgs, err := s.GetMessages(sid)
	if err != nil {
		t.Fatal(err)
	}
	a.pruneStaleToolResults(".", msgs)

	after, err := s.GetMessages(sid)
	if err != nil {
		t.Fatal(err)
	}
	if got := toolResultContent(t, after[1]); got != first {
		t.Errorf("earlier read must not be pruned by a truncated later read, got: %s", got)
	}
}

// --- end-to-end: compact() wires pruning into the kept tail ------------

// TestCompactPrunesStaleReadInKeptTail drives the real auto-compaction path
// (keepActiveTail=true) with two user turns: a small first turn (summarized
// away) and a second turn containing read→edit→read of the same file. The
// second turn survives as the active tail; its first (now-stale) read must
// come back pruned.
func TestCompactPrunesStaleReadInKeptTail(t *testing.T) {
	s := newTestStore(t)
	sid := newTestSession(t, s, "test-model")

	appendUserText(t, s, sid, "first turn")
	appendUserText(t, s, sid, "ok")

	appendUserText(t, s, sid, "second turn, read then edit f.go")
	big := strings.Repeat("old content line\n", 50)
	appendToolUse(t, s, sid, "c1", "read", map[string]interface{}{"path": "f.go"})
	appendToolResult(t, s, sid, "c1", big)
	appendToolUse(t, s, sid, "c2", "edit", map[string]interface{}{"path": "f.go", "oldText": "x", "newText": "y"})
	appendToolResult(t, s, sid, "c2", "edited f.go")

	fp := newFakeProvider()
	fp.SetResponses([][]provider.StreamEvent{provider.FakeTextResponse("## Big Picture\nsummary", nil)})
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, make(chan OutputEvent, 8),
		func(context.Context, string, string, string) (bool, string) { return true, "" })

	if err := a.compact(context.Background(), false, true); err != nil {
		t.Fatalf("compact: %v", err)
	}

	msgs, err := s.GetMessages(sid)
	if err != nil {
		t.Fatal(err)
	}
	var sawPruned bool
	for _, m := range msgs {
		if m.Role != "tool" {
			continue
		}
		if strings.Contains(toolResultContent(t, m), "superseded") {
			sawPruned = true
		}
		if m.Content == big {
			t.Fatal("stale read content should not survive compaction unpruned")
		}
	}
	if !sawPruned {
		t.Error("expected the stale read in the kept tail to be pruned by compact()")
	}
}
