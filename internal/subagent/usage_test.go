package subagent

import (
	"bufio"
	"os/exec"
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/provider"
)

// TestReadEventDecodesUsage verifies ChildEvent.Usage round-trips over the
// wire: present on "tool"/"done" lines that carry it, nil when a line omits
// it (e.g. "text"/"retrying" events never set it).
func TestReadEventDecodesUsage(t *testing.T) {
	lines := strings.Join([]string{
		`{"type":"tool","tool":"read","turns":1,"usage":{"InputTokens":1000,"OutputTokens":50,"CacheReadTokens":10,"CacheWriteTokens":5,"InputTokensUnknown":false}}`,
		`{"type":"text","text":"hello"}`,
		`{"type":"done","success":true,"usage":{"InputTokens":1500,"OutputTokens":80}}`,
	}, "\n") + "\n"

	c := &ChildProcess{cmd: exec.Command("true"), stdout: bufio.NewReader(strings.NewReader(lines))}

	ev, err := c.ReadEvent()
	if err != nil {
		t.Fatalf("ReadEvent (tool): %v", err)
	}
	if ev.Usage == nil {
		t.Fatal("tool event: Usage = nil, want populated")
	}
	want := provider.Usage{InputTokens: 1000, OutputTokens: 50, CacheReadTokens: 10, CacheWriteTokens: 5}
	if *ev.Usage != want {
		t.Fatalf("tool event Usage = %+v, want %+v", *ev.Usage, want)
	}

	ev, err = c.ReadEvent()
	if err != nil {
		t.Fatalf("ReadEvent (text): %v", err)
	}
	if ev.Usage != nil {
		t.Fatalf("text event: Usage = %+v, want nil (never sent on this event type)", ev.Usage)
	}

	ev, err = c.ReadEvent()
	if err != nil {
		t.Fatalf("ReadEvent (done): %v", err)
	}
	if ev.Usage == nil {
		t.Fatal("done event: Usage = nil, want populated")
	}
	want = provider.Usage{InputTokens: 1500, OutputTokens: 80}
	if *ev.Usage != want {
		t.Fatalf("done event Usage = %+v, want %+v", *ev.Usage, want)
	}
}
