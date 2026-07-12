package guard

import (
	"strings"
	"unicode/utf8"
)

// HighlightSpan is a contiguous run of command text with uniform danger styling.
type HighlightSpan struct {
	Text   string
	Danger bool
}

// HighlightSpans splits a bash command into safe/danger spans using the same
// rules as pi's bash-guard highlightBashCommand (per-token vs whole-segment).
func HighlightSpans(command string) []HighlightSpan {
	if command == "" {
		return []HighlightSpan{{Text: "", Danger: false}}
	}

	runes := []rune(command)
	danger := make([]bool, len(runes))
	inSegment := make([]bool, len(runes))

	pos := 0
	for _, seg := range Segments(command) {
		idx := strings.Index(command[pos:], seg)
		if idx < 0 {
			break
		}
		startByte := pos + idx
		endByte := startByte + len(seg)
		startRune := utf8.RuneCountInString(command[:startByte])
		endRune := utf8.RuneCountInString(command[:endByte])
		for i := startRune; i < endRune && i < len(runes); i++ {
			inSegment[i] = true
		}

		if segmentDangerousPattern(seg) {
			for i := startRune; i < endRune && i < len(runes); i++ {
				danger[i] = true
			}
		} else {
			tokens := tokenize(seg)
			if isDestructiveOrEscalated(tokens) {
				for i := startRune; i < endRune && i < len(runes); i++ {
					danger[i] = true
				}
			} else {
				tokenPos := startByte
				for _, rawTok := range tokens {
					tokIdx := strings.Index(seg, rawTok)
					if tokIdx < 0 {
						continue
					}
					absTokByte := startByte + tokIdx
					absTokEndByte := absTokByte + len(rawTok)
					if classifyTokenDanger(rawTok, normalizeToken(rawTok)) != "" {
						tokStart := utf8.RuneCountInString(command[:absTokByte])
						tokEnd := utf8.RuneCountInString(command[:absTokEndByte])
						for i := tokStart; i < tokEnd && i < len(runes); i++ {
							danger[i] = true
						}
					}
					tokenPos = absTokEndByte
					_ = tokenPos
				}
			}
		}
		pos = endByte
	}

	var spans []HighlightSpan
	i := 0
	for i < len(runes) {
		effective := inSegment[i] && danger[i]
		j := i + 1
		for j < len(runes) {
			next := inSegment[j] && danger[j]
			if next != effective {
				break
			}
			j++
		}
		spans = append(spans, HighlightSpan{
			Text:   string(runes[i:j]),
			Danger: effective,
		})
		i = j
	}
	if len(spans) == 0 {
		return []HighlightSpan{{Text: command, Danger: false}}
	}
	return spans
}

func classifyTokenDanger(token, normalized string) string {
	if normalized == "sudo" || normalized == "pkexec" {
		return "sudo"
	}
	if normalized == "xargs" {
		return "xargs"
	}
	if dangerousTokens[normalized] {
		return "dangerous-token"
	}
	if strings.HasPrefix(normalized, ".env") || strings.Contains(normalized, "/.env") {
		return "dotenv"
	}
	if normalized == "env" || normalized == "printenv" {
		return "env-leak"
	}
	if touchesSensitivePath([]string{normalized}) {
		return "sensitive-path"
	}
	return ""
}

// segmentDangerousPattern matches bash-guard segment-level pattern checks used
// for highlight (redirects, pipes-to-shell, command substitution).
func segmentDangerousPattern(seg string) bool {
	return hasDangerousPatterns(seg) || hasCommandSubstitution(seg)
}

func isDestructiveOrEscalated(tokens []string) bool {
	if len(tokens) == 0 {
		return false
	}
	norm := make([]string, len(tokens))
	for i, t := range tokens {
		norm[i] = normalizeToken(t)
	}
	stem := norm[0]
	if destructiveCommands[stem] {
		return true
	}
	switch stem {
	case "find":
		return findHasDangerousFlag(norm)
	case "gh":
		if len(norm) >= 2 && norm[1] == "api" {
			return ghApiIsMutating(norm)
		}
	case "git":
		if gitSubIsMutating(norm) || gitHasOutputFlag(norm) {
			return true
		}
	case "rg":
		return rgHasDangerousFlag(norm)
	case "sed":
		return sedHasDangerousFlag(norm) || sedScriptIsDangerous(norm)
	case "tree":
		return treeHasDangerousFlag(norm)
	case "yq":
		return yqHasDangerousFlag(norm)
	case "tail":
		return tailHasFollowFlag(norm)
	}
	return false
}
