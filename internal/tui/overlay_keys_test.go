package tui

import (
	"testing"
	"time"

	"github.com/mq37/poisson/internal/agent"
)

func TestPickerOverlayArrowKeys(t *testing.T) {
	p := newPickerOverlay("Providers", []pickerItem{
		{id: "anthropic", label: "anthropic"},
		{id: "ollama", label: "ollama"},
	}, "anthropic", nil)
	if p.idx != 0 {
		t.Fatalf("idx=%d", p.idx)
	}
	handled, _, _ := p.feedKey(keyArrowDown())
	if !handled || p.idx != 1 {
		t.Fatalf("down: handled=%v idx=%d", handled, p.idx)
	}
	handled, _, _ = p.feedKey(keyArrowUp())
	if !handled || p.idx != 0 {
		t.Fatalf("up: handled=%v idx=%d", handled, p.idx)
	}
	var d Decoder
	for _, k := range d.Push([]byte{27, '[', '5', '7', '3', '5', '3', 'u'}) {
		handled, _, _ = p.feedKey(k)
		if !handled || p.idx != 1 {
			t.Fatalf("kitty down: handled=%v idx=%d", handled, p.idx)
		}
	}
}

func TestPaletteOverlayArrowKeys(t *testing.T) {
	p := newPaletteOverlay(nil)
	handled, _, _ := p.feedKey(keyArrowDown())
	if !handled || p.idx != 1 {
		t.Fatalf("palette down: handled=%v idx=%d", handled, p.idx)
	}
}

func TestHandleKeyOverlayPreservesChainedPicker(t *testing.T) {
	tui := newTestTUIHelper()
	pal := newPaletteOverlay(func(cmd string) error {
		if cmd == "/providers" {
			tui.activeOverlay = newPickerOverlay("Providers", []pickerItem{
				{id: "ollama", label: "ollama"},
			}, "", nil)
		}
		return nil
	})
	pal.filter = "providers"
	pal.idx = 0
	tui.activeOverlay = pal
	if !tui.handleKeyOverlay(Key{Kind: KeyEnter}) {
		t.Fatal("enter not handled")
	}
	if tui.activeOverlay == nil {
		t.Fatal("provider picker was cleared after palette selection")
	}
	if _, ok := tui.activeOverlay.(*pickerOverlay); !ok {
		t.Fatalf("expected pickerOverlay, got %T", tui.activeOverlay)
	}
}

func TestPaletteQuitPropagates(t *testing.T) {
	tui := newTestTUIHelper()
	pal := newPaletteOverlay(func(cmd string) error {
		if cmd == "/quit" {
			tui.overlayQuit.Store(true)
			return errQuitSentinel
		}
		return nil
	})
	for i, it := range paletteCommands {
		if it.cmd == "/quit" {
			pal.idx = i
			break
		}
	}
	tui.activeOverlay = pal
	if !tui.handleKeyOverlay(Key{Kind: KeyEnter}) {
		t.Fatal("enter not handled")
	}
	if !tui.overlayQuit.Load() {
		t.Fatal("expected overlay quit flag")
	}
}

func TestSearchOverlayAcceptsLetterN(t *testing.T) {
	s := newSearchOverlay(func() []ScreenRow { return nil }, nil, nil)
	handled, _, _ := s.feedKey(Key{Kind: KeyRune, Rune: 'n'})
	if !handled {
		t.Fatal("expected handled")
	}
	if s.query != "n" {
		t.Fatalf("query=%q want n", s.query)
	}
}

func TestSearchOverlayCtrlCDismisses(t *testing.T) {
	s := newSearchOverlay(func() []ScreenRow { return nil }, nil, nil)
	handled, done, cancel := s.feedKey(Key{Kind: KeyCtrl, Byte: 3})
	if !handled || !done || !cancel {
		t.Fatalf("handled=%v done=%v cancel=%v", handled, done, cancel)
	}
}

func TestMouseClickOverlayRowMapping(t *testing.T) {
	tui := newTestTUIHelper()
	tui.headerRows = 2
	tui.scrollRows = 20
	p := newPickerOverlay("Providers", []pickerItem{
		{id: "a", label: "alpha"},
		{id: "b", label: "beta"},
	}, "", nil)
	p.render(tui.scrollRows, tui.cols)
	tui.activeOverlay = p
	scrollStart := tui.headerRows + 1
	targetRow := scrollStart + p.chrome.anchor - 1 + p.chrome.itemLine0
	tui.handleMouseClick(targetRow)
	if p.idx != 0 {
		t.Fatalf("first row click idx=%d want 0", p.idx)
	}
}

func TestApproveBufferedAnswerBeforeReceive(t *testing.T) {
	tui := newTestTUIHelper()
	result := make(chan bool, 1)
	go func() {
		allowed, _ := tui.Approve("rm -rf x", "danger", "", agent.BashRiskHigh)
		result <- allowed
	}()
	deadline := time.Now().Add(500 * time.Millisecond)
	for !tui.approving.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	// Send before Approve blocks on receive (simulates fast key).
	tui.approvalAnswer <- approvalReply{Allowed: true}
	select {
	case got := <-result:
		if !got {
			t.Fatal("expected allow")
		}
	case <-time.After(time.Second):
		t.Fatal("Approve timed out — answer likely dropped")
	}
}
func TestApprovalRoutesToHandler(t *testing.T) {
	cases := []struct {
		name      string
		k         Key
		convFocus bool
		want      bool
	}{
		{"Tab always routes (toggle focus)", Key{Kind: KeyTab}, false, true},
		{"PgUp always routes (scroll convo)", Key{Kind: KeyPageUp}, false, true},
		{"PgDn always routes", Key{Kind: KeyPageDown}, false, true},
		{"Shift+Up always routes", Key{Kind: KeyShiftArrowUp}, false, true},
		{"Up in input focus stays (panel scroll)", Key{Kind: KeyArrowUp}, false, false},
		{"Up in conv focus routes (scroll convo)", Key{Kind: KeyArrowUp}, true, true},
		{"Esc in conv focus routes", Key{Kind: KeyEscape}, true, true},
		{"Esc in input focus stays (deny)", Key{Kind: KeyEscape}, false, false},
		{"answer letter stays (input)", Key{Kind: KeyRune, Rune: 'a'}, false, false},
		{"answer letter stays (conv)", Key{Kind: KeyRune, Rune: 'n'}, true, false},
	}
	for _, c := range cases {
		if got := approvalRoutesToHandler(c.k, c.convFocus, 24); got != c.want {
			t.Errorf("%s: approvalRoutesToHandler = %v, want %v", c.name, got, c.want)
		}
	}
}
