package subagent

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// nopWriteCloser adapts a bytes.Buffer to io.WriteCloser for AttachChild's
// stdin param — no real process involved.
type nopWriteCloser struct{ *bytes.Buffer }

func (nopWriteCloser) Close() error { return nil }

// TestAttachChild_ReadsCannedEvents feeds a canned in-memory JSON-lines
// reader through an attached ChildProcess and checks ReadEvent decodes each
// event type in sequence — the plan Step 11 verify criterion, exercising
// the framing without a real systemd-run process anywhere.
func TestAttachChild_ReadsCannedEvents(t *testing.T) {
	stdout := strings.NewReader(
		`{"type":"text","text":"hello"}` + "\n" +
			`{"type":"tool","tool":"read"}` + "\n" +
			`{"type":"done","success":true}` + "\n",
	)
	c := AttachChild(nopWriteCloser{&bytes.Buffer{}}, stdout)

	want := []struct {
		typ  string
		text string
		tool string
	}{
		{typ: "text", text: "hello"},
		{typ: "tool", tool: "read"},
		{typ: "done"},
	}
	for i, w := range want {
		ev, err := c.ReadEvent()
		if err != nil {
			t.Fatalf("event %d: ReadEvent: %v", i, err)
		}
		if ev.Type != w.typ || ev.Text != w.text || ev.Tool != w.tool {
			t.Errorf("event %d = %+v, want type=%q text=%q tool=%q", i, ev, w.typ, w.text, w.tool)
		}
	}
	if _, err := c.ReadEvent(); err != io.EOF {
		t.Errorf("expected EOF after canned events, got %v", err)
	}
}

// TestAttachChild_SendApprovalWritesToStdin checks the stdin side of an
// attached child also works — a caller can write an approval_response the
// same way it would to a Spawn'd child.
func TestAttachChild_SendApprovalWritesToStdin(t *testing.T) {
	var buf bytes.Buffer
	c := AttachChild(nopWriteCloser{&buf}, strings.NewReader(""))
	if err := c.SendApprovalSafe(true, ""); err != nil {
		t.Fatalf("SendApprovalSafe: %v", err)
	}
	if !strings.Contains(buf.String(), `"approved":true`) {
		t.Errorf("stdin = %q, want approval_response with approved:true", buf.String())
	}
}

// TestAttachChild_KillWaitReapAreNilSafe is the Step 11 edge case: an
// attached child has c.cmd == nil, so Kill/Wait/Reap must not panic —
// they're no-ops, since process lifecycle belongs to the caller, not to
// ChildProcess.
func TestAttachChild_KillWaitReapAreNilSafe(t *testing.T) {
	c := AttachChild(nopWriteCloser{&bytes.Buffer{}}, strings.NewReader(""))
	if err := c.Kill(); err != nil {
		t.Errorf("Kill() = %v, want nil", err)
	}
	if err := c.Wait(); err != nil {
		t.Errorf("Wait() = %v, want nil", err)
	}
	c.Reap() // must not panic
}
