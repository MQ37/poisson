package tui

import (
	"strings"
	"unicode"

	"poisson/internal/guard"
)

const boxListMaxInner = 72

// resolveApprovalPurpose returns the purpose line for approval modals.
func resolveApprovalPurpose(command, description string) string {
	if strings.TrimSpace(description) != "" {
		return description
	}
	if _, reason := guard.Classify(command); reason != "" {
		return reason
	}
	return "(no description provided)"
}

func appendOverlayFilter(filter *string, data []byte) bool {
	for _, r := range string(decodeKittyKeys(data)) {
		if r == '\r' || r == '\n' || r == '\t' {
			continue
		}
		if unicode.IsPrint(r) {
			*filter += string(r)
			return true
		}
	}
	return false
}

func trimOverlayFilter(filter *string) bool {
	runes := []rune(*filter)
	if len(runes) == 0 {
		return false
	}
	*filter = string(runes[:len(runes)-1])
	return true
}