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
		t.Fatal("mixed should be false")
	}
}