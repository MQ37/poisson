package tui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const fuzzyResultCap = 50

type fuzzyMatch struct {
	text  string
	score int
}

// fuzzyScore returns a subsequence match score, or -1 if query is not a
// subsequence of candidate (case-insensitive).
func fuzzyScore(query, candidate string) int {
	if query == "" {
		return 0
	}
	qr := []rune(strings.ToLower(query))
	cr := []rune(strings.ToLower(candidate))
	qi := 0
	score := 0
	prev := -1
	for ci := 0; ci < len(cr) && qi < len(qr); ci++ {
		if cr[ci] != qr[qi] {
			continue
		}
		score += 10
		if prev >= 0 && ci == prev+1 {
			score += 6
		}
		if ci == 0 || !unicode.IsLetter(cr[ci-1]) {
			score += 4
		}
		if cr[ci] == '/' || cr[ci] == '@' || cr[ci] == '.' || cr[ci] == '_' || cr[ci] == '-' {
			score += 2
		}
		prev = ci
		qi++
	}
	if qi < len(qr) {
		return -1
	}
	score -= utf8.RuneCountInString(candidate) / 8
	return score
}

func rankFuzzy(query string, candidates []string, capN int) []string {
	if capN < 1 {
		capN = fuzzyResultCap
	}
	var matches []fuzzyMatch
	for _, c := range candidates {
		sc := fuzzyScore(query, c)
		if sc < 0 {
			continue
		}
		matches = append(matches, fuzzyMatch{text: c, score: sc})
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		return matches[i].text < matches[j].text
	})
	if len(matches) > capN {
		matches = matches[:capN]
	}
	out := make([]string, len(matches))
	for i, m := range matches {
		out[i] = m.text
	}
	return out
}

// matchSlashFuzzy ranks slash commands against the partial token at cursor.
func matchSlashFuzzy(partial string) []string {
	if !strings.HasPrefix(partial, "/") {
		return nil
	}
	if partial == "/" {
		return append([]string(nil), slashCommands...)
	}
	var prefix []string
	for _, c := range slashCommands {
		if strings.HasPrefix(c, partial) {
			prefix = append(prefix, c)
		}
	}
	if len(prefix) > 0 {
		return prefix
	}
	return rankFuzzy(strings.TrimPrefix(partial, "/"), slashCommands, fuzzyResultCap)
}

// matchAtFileFuzzy returns @-file candidates ranked by fuzzy score.
func matchAtFileFuzzy(partial string, cwd string) (cands []string, truncated bool) {
	if !strings.HasPrefix(partial, "@") {
		return nil, false
	}
	body := strings.TrimPrefix(partial, "@")
	dirPart, prefix := filepath.Split(body)
	dir := cwd
	if dirPart != "" {
		if filepath.IsAbs(dirPart) {
			dir = dirPart
		} else {
			dir = filepath.Join(cwd, dirPart)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, false
	}
	query := prefix
	if query == "" {
		query = strings.TrimPrefix(partial, "@")
	}
	var names, prefixHits []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(query, ".") && !strings.HasPrefix(prefix, ".") {
			continue
		}
		repl := "@" + dirPart + name
		if e.IsDir() {
			repl += "/"
		}
		names = append(names, repl)
		if query == "" || strings.HasPrefix(name, query) {
			prefixHits = append(prefixHits, repl)
		}
	}
	if len(prefixHits) > 0 {
		sort.Strings(prefixHits)
		truncated = len(prefixHits) > fuzzyResultCap
		if truncated {
			prefixHits = prefixHits[:fuzzyResultCap]
		}
		return prefixHits, truncated
	}
	ranked := rankFuzzy(query, names, fuzzyResultCap)
	truncated = len(ranked) >= fuzzyResultCap && len(names) > fuzzyResultCap
	return ranked, truncated
}
