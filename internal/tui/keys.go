package tui

import "unicode/utf8"

// KeyKind classifies a single decoded terminal key event.
type KeyKind int

const (
	KeyUnknown KeyKind = iota
	KeyRune
	KeyEnter
	KeyShiftEnter
	KeyBackspace
	KeyDelete
	KeyInsert
	KeyTab
	KeyEscape
	KeyArrowUp
	KeyArrowDown
	KeyArrowLeft
	KeyArrowRight
	KeyPageUp
	KeyPageDown
	KeyHome
	KeyEnd
	KeyShiftArrowUp
	KeyShiftArrowDown
	KeyShiftArrowLeft
	KeyShiftArrowRight
	KeyCtrl
	KeyPaste
)

// Key is one normalized keyboard event after kitty/CSI decoding.
type Key struct {
	Kind KeyKind
	Rune rune
	Text string // bracketed paste payload
	Byte byte   // C0 control for KeyCtrl
}

// Decoder buffers stdin chunks and emits complete Key events. Incomplete CSI
// sequences stay in the buffer so '[' from "\x1b[" never leaks as text.
type Decoder struct {
	pending  []byte
	pasting  bool
	pasteBuf []byte
}

// Push appends raw terminal bytes and returns any fully decoded keys.
func (d *Decoder) Push(data []byte) []Key {
	if len(data) == 0 {
		return nil
	}
	d.pending = append(d.pending, data...)
	var keys []Key
	for {
		k, n := d.parseOne()
		if n == 0 {
			break
		}
		d.pending = d.pending[n:]
		if k.Kind != KeyUnknown {
			keys = append(keys, k)
		}
	}
	// Caps Lock → Esc and similar remaps often send a lone 0x1b with no
	// follow-up byte. Flush it once this read chunk is fully parsed.
	if len(d.pending) == 1 && d.pending[0] == 27 {
		d.pending = nil
		keys = append(keys, Key{Kind: KeyEscape})
	}
	return keys
}

func (d *Decoder) parseOne() (Key, int) {
	if d.pasting {
		return d.parsePasteChunk()
	}
	if len(d.pending) == 0 {
		return Key{}, 0
	}

	if d.pending[0] != 27 {
		return d.parsePlain()
	}

	n := escSeqLen(d.pending)
	if n == 0 {
		return Key{}, 0
	}
	seq := d.pending[:n]

	if hasPrefix(seq, pasteStartV2) {
		rest := seq[len(pasteStartV2):]
		if idx := indexOf(rest, pasteEndV2); idx >= 0 {
			text := string(rest[:idx])
			return Key{Kind: KeyPaste, Text: text}, len(pasteStartV2) + idx + len(pasteEndV2)
		}
		d.pasting = true
		d.pasteBuf = append(d.pasteBuf[:0], rest...)
		return Key{}, n
	}

	if code, mods, event, final, kn := parseKittyKey(seq); kn > 0 && (final == 'u' || final == '~') {
		if event == 3 {
			// Remapped Esc (e.g. Caps Lock → Esc) may only deliver a release event.
			if code == 27 || code == kittyKeyEscape {
				return Key{Kind: KeyEscape}, kn
			}
			return Key{}, kn // other key releases — consume, do not dispatch
		}
		if key, ok := keyFromKitty(code, mods); ok {
			return key, kn
		}
		return Key{Kind: KeyUnknown}, kn
	}

	if key, ok := keyFromSS3(seq); ok {
		return key, n
	}

	if key, ok := keyFromCSI(seq); ok {
		return key, n
	}

	if n == 1 {
		return Key{Kind: KeyEscape}, 1
	}
	if n == 2 && seq[1] != '[' && seq[1] != 'O' {
		if seq[1] == '\r' || seq[1] == 13 {
			return Key{Kind: KeyEnter}, 2
		}
		if seq[1] == 127 || seq[1] == 8 {
			return Key{Kind: KeyBackspace}, 2
		}
		// Lone ESC followed by a normal key: emit Escape only so the next byte
		// is decoded separately (Alt+key stays distinct from a bare Esc).
		return Key{Kind: KeyEscape}, 1
	}

	return Key{Kind: KeyUnknown}, n
}

func (d *Decoder) parsePasteChunk() (Key, int) {
	if idx := indexOf(d.pending, pasteEndV2); idx >= 0 {
		text := string(append(d.pasteBuf, d.pending[:idx]...))
		d.pasting = false
		d.pasteBuf = nil
		consumed := idx + len(pasteEndV2)
		return Key{Kind: KeyPaste, Text: text}, consumed
	}
	d.pasteBuf = append(d.pasteBuf, d.pending...)
	return Key{}, len(d.pending)
}

func (d *Decoder) parsePlain() (Key, int) {
	b := d.pending[0]
	switch b {
	case '\r':
		return Key{Kind: KeyEnter}, 1
	case '\n':
		return Key{Kind: KeyShiftEnter}, 1
	case '\t':
		return Key{Kind: KeyTab}, 1
	case 8, 127:
		return Key{Kind: KeyBackspace}, 1
	}
	if b < 32 {
		return Key{Kind: KeyCtrl, Byte: b}, 1
	}
	r, size := utf8.DecodeRune(d.pending)
	if r == utf8.RuneError && size <= 1 {
		return Key{Kind: KeyUnknown}, 1
	}
	return Key{Kind: KeyRune, Rune: r}, size
}

// escSeqLen returns the byte length of a complete ESC/CSI/SS3 sequence, or 0
// if more input is needed.
func escSeqLen(data []byte) int {
	if len(data) == 0 || data[0] != 27 {
		return 0
	}
	if len(data) == 1 {
		return 0
	}
	switch data[1] {
	case 'O':
		if len(data) < 3 {
			return 0
		}
		return 3
	case '[':
		for i := 2; i < len(data); i++ {
			if data[i] >= 0x40 && data[i] <= 0x7e {
				return i + 1
			}
		}
		return 0
	default:
		return 2
	}
}

func keyFromKitty(code, mods int) (Key, bool) {
	m := mods - 1
	if m < 0 {
		m = 0
	}
	shift := m&1 != 0
	ctrl := m&4 != 0

	switch code {
	case kittyKeyUp, kittyKeyKPUp:
		if shift {
			return Key{Kind: KeyShiftArrowUp}, true
		}
		return Key{Kind: KeyArrowUp}, true
	case kittyKeyDown, kittyKeyKPDown:
		if shift {
			return Key{Kind: KeyShiftArrowDown}, true
		}
		return Key{Kind: KeyArrowDown}, true
	case 57350: // LEFT
		if shift {
			return Key{Kind: KeyShiftArrowLeft}, true
		}
		return Key{Kind: KeyArrowLeft}, true
	case 57351: // RIGHT
		if shift {
			return Key{Kind: KeyShiftArrowRight}, true
		}
		return Key{Kind: KeyArrowRight}, true
	case kittyKeyPageUp, kittyKeyKPPageUp:
		return Key{Kind: KeyPageUp}, true
	case kittyKeyPageDown, kittyKeyKPPageDn:
		return Key{Kind: KeyPageDown}, true
	case 57356, 57423: // HOME
		return Key{Kind: KeyHome}, true
	case 57357, 57424: // END
		return Key{Kind: KeyEnd}, true
	case kittyKeyDelete:
		return Key{Kind: KeyDelete}, true
	case kittyKeyInsert:
		return Key{Kind: KeyInsert}, true
	case 27, kittyKeyEscape:
		return Key{Kind: KeyEscape}, true
	case kittyKeyEnter, 13:
		if shift {
			return Key{Kind: KeyShiftEnter}, true
		}
		return Key{Kind: KeyEnter}, true
	case kittyKeyTab, 9:
		return Key{Kind: KeyTab}, true
	case kittyKeyBackspace, 8, 127:
		return Key{Kind: KeyBackspace}, true
	}
	if ctrl && ((code >= 'a' && code <= 'z') || (code >= 'A' && code <= 'Z')) {
		return Key{Kind: KeyCtrl, Byte: byte(code) & 0x1f}, true
	}
	if !ctrl && code >= 32 && code != 127 && code < 57344 {
		return Key{Kind: KeyRune, Rune: rune(code)}, true
	}
	return Key{}, false
}

func keyFromSS3(seq []byte) (Key, bool) {
	if len(seq) != 3 || seq[0] != 27 || seq[1] != 'O' {
		return Key{}, false
	}
	switch seq[2] {
	case 'A':
		return Key{Kind: KeyArrowUp}, true
	case 'B':
		return Key{Kind: KeyArrowDown}, true
	case 'C':
		return Key{Kind: KeyArrowRight}, true
	case 'D':
		return Key{Kind: KeyArrowLeft}, true
	case 'H':
		return Key{Kind: KeyHome}, true
	case 'F':
		return Key{Kind: KeyEnd}, true
	}
	return Key{}, false
}

func keyFromCSI(seq []byte) (Key, bool) {
	if len(seq) < 3 || seq[0] != 27 || seq[1] != '[' {
		return Key{}, false
	}
	final := seq[len(seq)-1]
	switch final {
	case 'A', 'B', 'C', 'D', 'H', 'F':
		if key, ok := keyFromCSIArrow(seq, final); ok {
			return key, true
		}
	}
	if indexOf(seq, []byte("\x1b[1;2D")) >= 0 || indexOf(seq, []byte("\x1b[1;4D")) >= 0 {
		return Key{Kind: KeyShiftArrowLeft}, true
	}
	if indexOf(seq, []byte("\x1b[1;2C")) >= 0 || indexOf(seq, []byte("\x1b[1;4C")) >= 0 {
		return Key{Kind: KeyShiftArrowRight}, true
	}
	if indexOf(seq, []byte("\x1b[H")) >= 0 || indexOf(seq, []byte("\x1b[1;1H")) >= 0 {
		return Key{Kind: KeyHome}, true
	}
	if indexOf(seq, []byte("\x1b[F")) >= 0 || indexOf(seq, []byte("\x1b[1;1F")) >= 0 {
		return Key{Kind: KeyEnd}, true
	}
	if indexOf(seq, []byte("\x1b[5~")) >= 0 || indexOf(seq, []byte("\x1b[5;")) >= 0 {
		return Key{Kind: KeyPageUp}, true
	}
	if indexOf(seq, []byte("\x1b[6~")) >= 0 || indexOf(seq, []byte("\x1b[6;")) >= 0 {
		return Key{Kind: KeyPageDown}, true
	}
	if indexOf(seq, []byte("\x1b[3~")) >= 0 {
		return Key{Kind: KeyDelete}, true
	}
	if indexOf(seq, []byte("\x1b[2~")) >= 0 {
		return Key{Kind: KeyInsert}, true
	}
	return Key{}, false
}

func keyFromCSIArrow(seq []byte, final byte) (Key, bool) {
	shift := csiShiftFromBody(seq[2 : len(seq)-1])
	switch final {
	case 'A':
		if shift {
			return Key{Kind: KeyShiftArrowUp}, true
		}
		return Key{Kind: KeyArrowUp}, true
	case 'B':
		if shift {
			return Key{Kind: KeyShiftArrowDown}, true
		}
		return Key{Kind: KeyArrowDown}, true
	case 'C':
		if shift {
			return Key{Kind: KeyShiftArrowRight}, true
		}
		return Key{Kind: KeyArrowRight}, true
	case 'D':
		if shift {
			return Key{Kind: KeyShiftArrowLeft}, true
		}
		return Key{Kind: KeyArrowLeft}, true
	case 'H':
		return Key{Kind: KeyHome}, true
	case 'F':
		return Key{Kind: KeyEnd}, true
	}
	return Key{}, false
}

func keyApprovalAnswer(k Key) (allowed, ok bool) {
	switch k.Kind {
	case KeyEscape:
		return false, true
	case KeyEnter:
		return true, true
	case KeyRune:
		switch k.Rune {
		case 'a', 'A', 'y', 'Y':
			return true, true
		case 'd', 'D', 'n', 'N':
			return false, true
		}
	}
	return false, false
}

func (k Key) isCtrlC() bool { return k.Kind == KeyCtrl && k.Byte == 3 }

func (k Key) isEnter() bool { return k.Kind == KeyEnter }

func (k Key) isNavUp() bool {
	return k.Kind == KeyArrowUp || k.Kind == KeyShiftArrowUp
}

func (k Key) isNavDown() bool {
	return k.Kind == KeyArrowDown || k.Kind == KeyShiftArrowDown
}

func scrollDeltaForKey(k Key, viewHeight int) (delta int, ok bool) {
	if viewHeight < 1 {
		viewHeight = 1
	}
	switch k.Kind {
	case KeyPageUp:
		return viewHeight, true
	case KeyPageDown:
		return -viewHeight, true
	case KeyShiftArrowUp:
		return 3, true
	case KeyShiftArrowDown:
		return -3, true
	}
	return 0, false
}

// decodeKittyKeys translates kitty CSI-u sequences into legacy bytes. New code
// should use Decoder.Push instead; this remains for tests and mouse-scroll paths.
func decodeKittyKeys(data []byte) []byte {
	var d Decoder
	keys := d.Push(data)
	if len(keys) == 0 && len(d.pending) > 0 {
		return append([]byte{}, d.pending...)
	}
	return keysToLegacyBytes(keys)
}

func keysToLegacyBytes(keys []Key) []byte {
	var out []byte
	for _, k := range keys {
		out = append(out, keyToLegacyBytes(k)...)
	}
	return out
}

func keyToLegacyBytes(k Key) []byte {
	switch k.Kind {
	case KeyEnter:
		return []byte{'\r'}
	case KeyShiftEnter:
		return []byte{'\n'}
	case KeyBackspace:
		return []byte{127}
	case KeyTab:
		return []byte{9}
	case KeyEscape:
		return []byte{27}
	case KeyArrowUp:
		return []byte{27, '[', 'A'}
	case KeyArrowDown:
		return []byte{27, '[', 'B'}
	case KeyArrowLeft:
		return []byte{27, '[', 'D'}
	case KeyArrowRight:
		return []byte{27, '[', 'C'}
	case KeyPageUp:
		return []byte{27, '[', '5', '~'}
	case KeyPageDown:
		return []byte{27, '[', '6', '~'}
	case KeyShiftArrowUp:
		return []byte{27, '[', '1', ';', '2', 'A'}
	case KeyShiftArrowDown:
		return []byte{27, '[', '1', ';', '2', 'B'}
	case KeyShiftArrowLeft:
		return []byte{27, '[', '1', ';', '2', 'D'}
	case KeyShiftArrowRight:
		return []byte{27, '[', '1', ';', '2', 'C'}
	case KeyDelete:
		return []byte{27, '[', '3', '~'}
	case KeyInsert:
		return []byte{27, '[', '2', '~'}
	case KeyHome:
		return []byte{27, '[', 'H'}
	case KeyEnd:
		return []byte{27, '[', 'F'}
	case KeyCtrl:
		return []byte{k.Byte}
	case KeyRune:
		return []byte(string(k.Rune))
	case KeyPaste:
		return append(append([]byte{}, pasteStartV2...), append([]byte(k.Text), pasteEndV2...)...)
	}
	return nil
}