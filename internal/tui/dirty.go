package tui

import "sync"

// dirtyTracker records which screen regions need repainting on the next frame.
// A full repaint takes precedence over partial row updates.
type dirtyTracker struct {
	mu      sync.Mutex
	full    bool
	scroll  map[int]struct{} // 0-based row indices within the scrollback region
	input   bool
	status  bool
	overlay bool
	cursor  bool
}

func newDirtyTracker() dirtyTracker {
	return dirtyTracker{scroll: make(map[int]struct{})}
}

func (d *dirtyTracker) markFull() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.full = true
	d.scroll = make(map[int]struct{})
	d.input = true
	d.status = true
	d.overlay = true
	d.cursor = true
}

func (d *dirtyTracker) markScrollAll(height int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.full {
		return
	}
	for i := 0; i < height; i++ {
		d.scroll[i] = struct{}{}
	}
	d.cursor = true
}

func (d *dirtyTracker) markScrollRows(rows ...int) {
	if len(rows) == 0 {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.full {
		return
	}
	for _, r := range rows {
		if r >= 0 {
			d.scroll[r] = struct{}{}
		}
	}
	d.cursor = true
}

func (d *dirtyTracker) markInput() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.full {
		return
	}
	d.input = true
	d.overlay = true
	d.cursor = true
}

func (d *dirtyTracker) markStatus() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.full {
		return
	}
	d.status = true
}

func (d *dirtyTracker) markOverlay() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.full {
		return
	}
	d.overlay = true
	d.cursor = true
}

func (d *dirtyTracker) markCursor() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.full {
		return
	}
	d.cursor = true
}

// dirtySnapshot is a point-in-time view consumed by the render goroutine.
type dirtySnapshot struct {
	full    bool
	scroll  []int
	input   bool
	status  bool
	overlay bool
	cursor  bool
}

func (s dirtySnapshot) any() bool {
	return s.full || len(s.scroll) > 0 || s.input || s.status || s.overlay || s.cursor
}

// consume returns pending dirty flags and clears the tracker.
func (d *dirtyTracker) consume() dirtySnapshot {
	d.mu.Lock()
	defer d.mu.Unlock()
	var snap dirtySnapshot
	snap.full = d.full
	if len(d.scroll) > 0 {
		snap.scroll = make([]int, 0, len(d.scroll))
		for r := range d.scroll {
			snap.scroll = append(snap.scroll, r)
		}
	}
	snap.input = d.input
	snap.status = d.status
	snap.overlay = d.overlay
	snap.cursor = d.cursor

	d.full = false
	d.scroll = make(map[int]struct{})
	d.input = false
	d.status = false
	d.overlay = false
	d.cursor = false
	return snap
}

// mergeScrollRows coalesces scroll row indices for tests.
func mergeScrollRows(rows []int) []int {
	if len(rows) == 0 {
		return nil
	}
	m := make(map[int]struct{}, len(rows))
	for _, r := range rows {
		m[r] = struct{}{}
	}
	out := make([]int, 0, len(m))
	for r := range m {
		out = append(out, r)
	}
	return out
}