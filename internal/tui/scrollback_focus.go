package tui

// userBlockIndices returns block indices for every user prompt in order.
func (s *scrollback) userBlockIndices() []int {
	var out []int
	for i := range s.blocks {
		if s.blocks[i].kind == blockUser {
			out = append(out, i)
		}
	}
	return out
}

func (s *scrollback) hasUserBlocks() bool {
	for i := range s.blocks {
		if s.blocks[i].kind == blockUser {
			return true
		}
	}
	return false
}

func (s *scrollback) blockGlobalStart(blockIdx, width int) int {
	_, cumulative := s.layoutAll(width)
	if blockIdx < 0 || blockIdx >= len(cumulative)-1 {
		return 0
	}
	return cumulative[blockIdx]
}

// scrollBlockToTop positions blockIdx in the viewport. skipTopRows leaves room
// above the block (e.g. conv-focus pinned prompt).
func (s *scrollback) scrollBlockToTop(blockIdx, viewHeight, width, skipTopRows int) {
	if viewHeight < 1 {
		viewHeight = 1
	}
	if skipTopRows < 0 {
		skipTopRows = 0
	}
	if skipTopRows >= viewHeight {
		skipTopRows = viewHeight - 1
	}
	wrapped, _ := s.layoutAll(width)
	if len(wrapped) == 0 {
		s.scrollOffset = 0
		return
	}
	target := s.blockGlobalStart(blockIdx, width)
	max := len(wrapped) - viewHeight
	if max < 0 {
		max = 0
	}
	off := len(wrapped) - viewHeight - target + skipTopRows
	if off < 0 {
		off = 0
	}
	if off > max {
		off = max
	}
	s.scrollOffset = off
}

// userPromptText returns plain text for a user block index.
func (s *scrollback) userPromptText(blockIdx int) string {
	if blockIdx < 0 || blockIdx >= len(s.blocks) {
		return ""
	}
	b := &s.blocks[blockIdx]
	if b.kind != blockUser {
		return ""
	}
	return stripANSI(b.raw)
}