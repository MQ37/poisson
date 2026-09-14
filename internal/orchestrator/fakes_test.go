package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/subagent"
)

func TestFakeFrontend_InjectAndRun(t *testing.T) {
	ff := NewFakeFrontend()
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan Command, 4)
	done := make(chan struct{})
	go func() {
		ff.Run(ctx, out)
		close(done)
	}()

	// Give Run a moment to capture out before injecting (a real race in a
	// production goroutine handoff, not just this test).
	time.Sleep(10 * time.Millisecond)
	ff.Inject(Command{Kind: CmdMessage, Text: "hi"})

	select {
	case cmd := <-out:
		if cmd.Text != "hi" {
			t.Errorf("got %+v, want text hi", cmd)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for injected command")
	}

	cancel()
	<-done
}

func TestFakeFrontend_SendRecordsAndErrorPath(t *testing.T) {
	ff := NewFakeFrontend()
	key := ChannelKey{Frontend: "fake", Chat: "c", Topic: "1"}
	if _, err := ff.Send(context.Background(), key, Message{Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	sent := ff.Sent()
	if len(sent) != 1 || sent[0].Msg.Text != "hello" {
		t.Errorf("Sent() = %+v", sent)
	}

	ff.SendErr = context.Canceled
	if _, err := ff.Send(context.Background(), key, Message{Text: "x"}); err == nil {
		t.Error("expected SendErr to be returned")
	}
}

func TestFakeRuntime_CreateStartListDestroy(t *testing.T) {
	rt := NewFakeRuntime()
	ctx := context.Background()
	if err := rt.Create(ctx, InstanceSpec{Name: "px-alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := rt.Create(ctx, InstanceSpec{Name: "px-alpha"}); err == nil {
		t.Error("expected an error creating a duplicate instance")
	}
	if err := rt.Start(ctx, "px-alpha"); err != nil {
		t.Fatal(err)
	}
	infos, err := rt.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].Name != "px-alpha" || infos[0].State != "running" {
		t.Errorf("List() = %+v", infos)
	}
	if err := rt.Destroy(ctx, "px-alpha"); err != nil {
		t.Fatal(err)
	}
	infos, _ = rt.List(ctx)
	if len(infos) != 0 {
		t.Errorf("List() after Destroy = %+v, want empty", infos)
	}
	// Idempotent.
	if err := rt.Destroy(ctx, "px-alpha"); err != nil {
		t.Errorf("second Destroy = %v, want nil (idempotent)", err)
	}
}

func TestFakeRuntime_ScriptedTurnEvents(t *testing.T) {
	rt := NewFakeRuntime()
	ctx := context.Background()
	rt.ScriptTurn("px-alpha", TurnScript{Events: []subagent.ChildEvent{
		{Type: "text", Text: "hi"},
		{Type: "done", Success: true},
	}})

	turn, err := rt.StartTurn(ctx, "px-alpha", TurnSpec{SessionID: "s-1", Message: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	ev, err := turn.ReadEvent()
	if err != nil || ev.Type != "text" || ev.Text != "hi" {
		t.Fatalf("event 0 = %+v, err %v", ev, err)
	}
	ev, err = turn.ReadEvent()
	if err != nil || ev.Type != "done" || !ev.Success {
		t.Fatalf("event 1 = %+v, err %v", ev, err)
	}
}

// TestFakeRuntime_HangThenStopTurnUnblocks is the concrete mechanism a hung
// turn + forcible stop is verified through: ReadEvent blocks until StopTurn
// closes the pipe, simulating an externally killed stuck process.
func TestFakeRuntime_HangThenStopTurnUnblocks(t *testing.T) {
	rt := NewFakeRuntime()
	ctx := context.Background()
	rt.ScriptTurn("px-alpha", TurnScript{Hang: true})

	turn, err := rt.StartTurn(ctx, "px-alpha", TurnSpec{SessionID: "s-1", Message: "hi"})
	if err != nil {
		t.Fatal(err)
	}

	readDone := make(chan error, 1)
	go func() {
		_, err := turn.ReadEvent()
		readDone <- err
	}()

	select {
	case <-readDone:
		t.Fatal("ReadEvent returned before StopTurn — hang script did not actually hang")
	case <-time.After(50 * time.Millisecond):
	}

	if err := rt.StopTurn(ctx, turn.UnitName); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-readDone:
		if err == nil {
			t.Error("expected EOF-shaped error after StopTurn, got nil")
		}
	case <-time.After(time.Second):
		t.Fatal("ReadEvent still blocked after StopTurn")
	}
}

func TestFakeRuntime_StdinWrittenCapturesApprovalResponse(t *testing.T) {
	rt := NewFakeRuntime()
	ctx := context.Background()
	turn, err := rt.StartTurn(ctx, "px-alpha", TurnSpec{SessionID: "s-1", Message: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if err := turn.SendApprovalSafe(true, ""); err != nil {
		t.Fatal(err)
	}
	got := rt.StdinWritten("px-alpha")
	if got == "" {
		t.Fatal("StdinWritten returned empty, want the approval_response JSON")
	}
}
