package tui

import "testing"

func TestParseMouseClick(t *testing.T) {
	events := parseMouseEvents([]byte("\x1b[<0;12;5M"))
	if len(events) != 1 {
		t.Fatalf("events = %d", len(events))
	}
	ev := events[0]
	if ev.Button != 0 || ev.Col != 12 || ev.Row != 5 || !ev.Press {
		t.Fatalf("ev = %+v", ev)
	}
}

func TestParseMouseReleaseIgnored(t *testing.T) {
	events := parseMouseEvents([]byte("\x1b[<0;12;5m"))
	if len(events) != 1 || events[0].Press {
		t.Fatalf("release: %+v", events)
	}
}

func TestParseMouseWheel(t *testing.T) {
	cases := []struct {
		seq  string
		btn  int
		want int
	}{
		{"\x1b[<64;10;20M", 64, 3},
		{"\x1b[<65;10;20M", 65, -3},
	}
	for _, tc := range cases {
		events := parseMouseEvents([]byte(tc.seq))
		if len(events) != 1 {
			t.Fatalf("seq %q: events %d", tc.seq, len(events))
		}
		d, ok := mouseWheelDelta(events[0].Button)
		if !ok || d != tc.want {
			t.Fatalf("seq %q: delta %d %v", tc.seq, d, ok)
		}
	}
}

func TestDataIsOnlyMouse(t *testing.T) {
	if !dataIsOnlyMouse([]byte("\x1b[<64;1;1M")) {
		t.Fatal("wheel only")
	}
	if dataIsOnlyMouse([]byte("\x1b[<0;1;1Mx")) {
		t.Fatal("mixed should be false (trailing garbage)")
	}
	// Regression: a leading non-mouse byte (a real keystroke) must not be
	// silently treated as part of "only mouse" just because a valid mouse
	// sequence follows it in the same chunk — the caller in lifecycle.go's
	// input loop drops the whole chunk without feeding it to the key decoder
	// whenever this returns true.
	if dataIsOnlyMouse([]byte("x\x1b[<64;1;1M")) {
		t.Fatal("mixed should be false (leading keystroke before a mouse sequence)")
	}
}

// TestParseMouseWheelIgnoresLeadingKeystroke is the end-to-end regression:
// a keystroke coalesced into the same read() chunk as a wheel event must not
// be recognized as a pure wheel scroll (which would make lifecycle.go's input
// loop discard it instead of pushing it to the key decoder).
func TestParseMouseWheelIgnoresLeadingKeystroke(t *testing.T) {
	if _, ok := parseMouseWheel([]byte("x\x1b[<65;10;5M")); ok {
		t.Fatal("parseMouseWheel treated a chunk with a leading keystroke as a pure wheel event")
	}
}

// TestAdvancePastMouseDoesNotScanAhead: a broken/incomplete sequence at
// position 0 must not make advancePastMouse skip ahead to a later valid one.
func TestAdvancePastMouseDoesNotScanAhead(t *testing.T) {
	if adv := advancePastMouse([]byte("x\x1b[<64;1;1M")); adv != 0 {
		t.Fatalf("advancePastMouse = %d, want 0 (no match at position 0)", adv)
	}
	if adv := advancePastMouse([]byte("\x1b[<64;1;1M")); adv != len("\x1b[<64;1;1M") {
		t.Fatalf("advancePastMouse = %d, want %d (whole sequence consumed)", adv, len("\x1b[<64;1;1M"))
	}
}
func TestParseMouseEventsMotionBit(t *testing.T) {
	events := parseMouseEvents([]byte("\x1b[<32;12;5M"))
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if !ev.Motion {
		t.Fatal("expected Motion=true for btn 32")
	}
	if ev.Button != 0 {
		t.Fatalf("expected motion bit stripped from Button, got %d", ev.Button)
	}
	if ev.Col != 12 || ev.Row != 5 {
		t.Fatalf("Col/Row = %d/%d, want 12/5", ev.Col, ev.Row)
	}
}

func TestParseMouseEventsPlainClickNoMotion(t *testing.T) {
	events := parseMouseEvents([]byte("\x1b[<0;3;2M"))
	if events[0].Motion {
		t.Fatal("plain click must not report Motion")
	}
}
