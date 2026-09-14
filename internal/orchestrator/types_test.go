package orchestrator

import "testing"

// TestChannelKeyUsableAsMapKey is the Step 16 verify criterion: ChannelKey
// must be usable directly as a map key with no issue (only comparable
// string fields, no slices/maps inside it).
func TestChannelKeyUsableAsMapKey(t *testing.T) {
	m := map[ChannelKey]int{
		{Frontend: "telegram", Chat: "-100", Topic: "42"}: 1,
		{Frontend: "telegram", Chat: "-100", Topic: "77"}: 2,
	}
	if m[ChannelKey{Frontend: "telegram", Chat: "-100", Topic: "42"}] != 1 {
		t.Error("ChannelKey did not round-trip as a map key")
	}
}

func TestStatusString(t *testing.T) {
	cases := map[Status]string{
		StatusIdle: "idle", StatusQueued: "queued", StatusRunning: "running",
		StatusAwaitingApproval: "awaiting_approval", StatusDead: "dead",
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Errorf("Status(%d).String() = %q, want %q", s, got, want)
		}
	}
}

func TestInstanceStatusAndPending(t *testing.T) {
	inst := NewInstance(InstanceMeta{Name: "px-test"}, ChannelKey{Frontend: "fake"}, 8)
	if inst.Status() != StatusIdle {
		t.Errorf("new instance status = %v, want idle", inst.Status())
	}
	inst.setStatus(StatusRunning)
	if inst.Status() != StatusRunning {
		t.Errorf("status = %v, want running", inst.Status())
	}
	if inst.Pending() != nil {
		t.Error("new instance should have no pending approval")
	}
	p := &PendingApproval{Command: "rm -rf /tmp/x", Risk: "high"}
	inst.setPending(p)
	if inst.Pending() != p {
		t.Error("Pending() did not return the set approval")
	}
}
