package tui

import (
	"strings"
	"unicode"
)

const boxListMaxInner = 72

// resolveApprovalPurpose returns the purpose line for approval modals.
func resolveApprovalPurpose(command, description string) string {
	if strings.TrimSpace(description) != "" {
		return description
	}
	return "(no description provided)"
}

func appendOverlayFilterRune(filter *string, r rune) bool {
	if r == '\r' || r == '\n' || r == '\t' {
		return false
	}
	if !unicode.IsPrint(r) {
		return false
	}
	*filter += string(r)
	return true
}

func trimOverlayFilter(filter *string) bool {
	runes := []rune(*filter)
	if len(runes) == 0 {
		return false
	}
	*filter = string(runes[:len(runes)-1])
	return true
}

func appendOverlayFilterText(filter *string, text string, resetIdx *int) bool {
	changed := false
	for _, r := range text {
		if appendOverlayFilterRune(filter, r) {
			changed = true
		}
	}
	if changed && resetIdx != nil {
		*resetIdx = 0
	}
	return changed
}
