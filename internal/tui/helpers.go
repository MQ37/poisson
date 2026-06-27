package tui

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

var atFileRe = regexp.MustCompile(`@([^\s@]+)`)

func commonPrefix(a, b string) string {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return a[:i]
		}
	}
	return a[:n]
}

func expandAtFiles(input string) (string, error) {
	var firstErr error
	result := atFileRe.ReplaceAllStringFunc(input, func(match string) string {
		path := match[1:]
		data, err := os.ReadFile(path)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("read @%s: %w", path, err)
			}
			return match
		}
		fence := "```"
		for strings.Contains(string(data), fence) {
			fence += "`"
		}
		return fence + "\n" + string(data) + "\n" + fence
	})
	if firstErr != nil {
		return "", firstErr
	}
	return result, nil
}

func renderHelp() string {
	return strings.TrimRight(`Slash commands:
  /help        Show this help
  /quit        Exit Poisson
  /clear       Clear scrollback
  /new         Start a new session
  /resume <id> Resume a session
  /sessions    Session picker
  /search <q>  Search across sessions (no args: find in scrollback)
  /fork [seq]  Fork the current session
  /undo        Undo the last turn
  /compact     Compact context now
  /model <m>   Switch provider/model
  /providers   Provider picker
  /effort <l>  Set thinking effort (low|medium|high|xhigh|max)
  /models      Model picker
  /reload      Reload config and skills
  /cost        Show session cost
  /btw <q>     Side question in floating box
  Tab          Focus conversation · Ctrl+F find · Ctrl+P palette`, "\n")
}