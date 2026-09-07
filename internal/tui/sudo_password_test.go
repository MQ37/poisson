package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/agent"
)

// TestFeedSudoPasswordKeyPassthroughWhenNotAsking mirrors
// TestFeedDenyReasonKeyPassthroughWhenNotDenying: feedSudoPasswordKey only
// takes over once the overlay is actually in password mode.
func TestFeedSudoPasswordKeyPassthroughWhenNotAsking(t *testing.T) {
	tui := newTestTUIHelper()
	tui.mu.Lock()
	tui.activeOverlay = newApprovalOverlay("sudo apt-get update", "update", "", agent.ApprovalOriginMain)
	tui.mu.Unlock()

	if tui.feedSudoPasswordKey(Key{Kind: KeyRune, Rune: 'x'}) {
		t.Fatal("expected passthrough (false) outside password mode")
	}
}

// TestFeedSudoPasswordKeyTypesAndSubmits drives the password prompt
// directly: type a password (with a backspace correction), confirm with
// Enter, and assert the exact reply sent on approvalAnswer.
func TestFeedSudoPasswordKeyTypesAndSubmits(t *testing.T) {
	tui := newTestTUIHelper()
	tui.mu.Lock()
	ao := newSudoPasswordOverlay("sudo apt-get update", "update", "", agent.ApprovalOriginMain)
	tui.activeOverlay = ao
	tui.mu.Unlock()

	for _, r := range "hunter3x" {
		if !tui.feedSudoPasswordKey(Key{Kind: KeyRune, Rune: r}) {
			t.Fatalf("expected feedSudoPasswordKey to handle rune %q", r)
		}
	}
	if !tui.feedSudoPasswordKey(Key{Kind: KeyBackspace}) {
		t.Fatal("expected feedSudoPasswordKey to handle backspace")
	}

	tui.mu.Lock()
	if got := ao.passwordText(); got != "hunter3" {
		t.Fatalf("password = %q, want %q", got, "hunter3")
	}
	tui.mu.Unlock()

	if !tui.feedSudoPasswordKey(Key{Kind: KeyEnter}) {
		t.Fatal("expected feedSudoPasswordKey to handle Enter")
	}

	select {
	case reply := <-tui.approvalAnswer:
		if !reply.Allowed {
			t.Fatal("expected submission (Allowed=true)")
		}
		if string(reply.Password) != "hunter3" {
			t.Fatalf("reply.Password = %q, want %q", reply.Password, "hunter3")
		}
	default:
		t.Fatal("expected a reply on approvalAnswer")
	}
}

// TestFeedSudoPasswordKeyEscapeCancels verifies Escape sends a cancel
// (Allowed=false, no password) without needing a second keypress — unlike
// the deny-reason prompt, there is nothing further to type.
func TestFeedSudoPasswordKeyEscapeCancels(t *testing.T) {
	tui := newTestTUIHelper()
	tui.mu.Lock()
	ao := newSudoPasswordOverlay("sudo apt-get update", "update", "", agent.ApprovalOriginMain)
	tui.activeOverlay = ao
	tui.mu.Unlock()

	if !tui.feedSudoPasswordKey(Key{Kind: KeyEscape}) {
		t.Fatal("expected feedSudoPasswordKey to handle Escape")
	}

	select {
	case reply := <-tui.approvalAnswer:
		if reply.Allowed {
			t.Fatal("expected cancel (Allowed=false)")
		}
		if len(reply.Password) != 0 {
			t.Fatalf("expected no password on cancel, got %q", reply.Password)
		}
	default:
		t.Fatal("expected a reply on approvalAnswer")
	}
}

// TestAskSudoPasswordEndToEnd drives the whole path through
// AskSudoPassword(): type a password, confirm with Enter, and check it
// comes back from AskSudoPassword itself — what BashTool's host exec path
// writes into the one-shot askpass file.
func TestAskSudoPasswordEndToEnd(t *testing.T) {
	tui := newTestTUIHelper()
	type outcome struct {
		password []byte
		ok       bool
	}
	result := make(chan outcome, 1)
	go func() {
		pw, ok := tui.AskSudoPassword(context.Background(), "sudo apt-get update", "update packages", "")
		result <- outcome{pw, ok}
	}()

	deadline := time.Now().Add(500 * time.Millisecond)
	for !tui.approving.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !tui.approving.Load() {
		t.Fatal("AskSudoPassword never entered approving state")
	}

	tui.mu.Lock()
	ao, ok := tui.activeOverlay.(*approvalOverlay)
	tui.mu.Unlock()
	if !ok || !ao.passwordMode {
		t.Fatal("expected a password-mode approvalOverlay to be active")
	}

	for _, r := range "hunter2" {
		tui.feedSudoPasswordKey(Key{Kind: KeyRune, Rune: r})
	}
	tui.feedSudoPasswordKey(Key{Kind: KeyEnter})

	select {
	case got := <-result:
		if !got.ok {
			t.Fatal("expected ok=true")
		}
		if string(got.password) != "hunter2" {
			t.Fatalf("password = %q, want %q", got.password, "hunter2")
		}
	case <-time.After(time.Second):
		t.Fatal("AskSudoPassword timed out")
	}

	tui.mu.Lock()
	blocks := tui.scroll.blockCount()
	tui.mu.Unlock()
	if blocks != 0 {
		t.Fatalf("sudo password prompt should not append to scrollback, blocks=%d", blocks)
	}
}

// TestRenderPasswordPanelNeverShowsTypedText verifies the panel only ever
// renders '*' for the typed password, at every length, never the plaintext.
func TestRenderPasswordPanelNeverShowsTypedText(t *testing.T) {
	ao := newSudoPasswordOverlay("sudo apt-get update", "update", "", agent.ApprovalOriginMain)
	ao.passwordEditor.insertText("correct horse battery staple")

	lines := ao.renderPasswordPanel(8, 60)
	for _, l := range lines {
		if strings.Contains(l, "correct horse battery staple") || strings.Contains(l, "horse") {
			t.Fatalf("rendered panel line leaks the typed password: %q", l)
		}
	}
	joined := strings.Join(lines, "")
	if !strings.Contains(joined, strings.Repeat("*", len("correct horse battery staple"))) {
		t.Errorf("expected masked '*' run for the password length, got: %q", joined)
	}
}
