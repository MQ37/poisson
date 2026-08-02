package tui

import "testing"

// TestKeyApprovalAnswerTable covers every key keyApprovalAnswer maps: Escape
// (deny, handled), Enter (accept, handled), the accept rune set (y/Y/a/A),
// the deny rune set (n/N/d/D), and an unmapped key (ok=false).
func TestKeyApprovalAnswerTable(t *testing.T) {
	cases := []struct {
		name        string
		key         Key
		wantAllowed bool
		wantOK      bool
	}{
		{"escape", Key{Kind: KeyEscape}, false, true},
		{"enter", Key{Kind: KeyEnter}, true, true},
		{"rune y", Key{Kind: KeyRune, Rune: 'y'}, true, true},
		{"rune Y", Key{Kind: KeyRune, Rune: 'Y'}, true, true},
		{"rune a", Key{Kind: KeyRune, Rune: 'a'}, true, true},
		{"rune A", Key{Kind: KeyRune, Rune: 'A'}, true, true},
		{"rune n", Key{Kind: KeyRune, Rune: 'n'}, false, true},
		{"rune N", Key{Kind: KeyRune, Rune: 'N'}, false, true},
		{"rune d", Key{Kind: KeyRune, Rune: 'd'}, false, true},
		{"rune D", Key{Kind: KeyRune, Rune: 'D'}, false, true},
		{"unmapped rune", Key{Kind: KeyRune, Rune: 'x'}, false, false},
		{"unmapped kind", Key{Kind: KeyTab}, false, false},
		{"ctrl key", Key{Kind: KeyCtrl, Byte: 1}, false, false},
	}
	for _, c := range cases {
		allowed, ok := keyApprovalAnswer(c.key)
		if allowed != c.wantAllowed || ok != c.wantOK {
			t.Errorf("%s: keyApprovalAnswer(%+v) = (%v,%v), want (%v,%v)",
				c.name, c.key, allowed, ok, c.wantAllowed, c.wantOK)
		}
	}
}
