package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mq37/poisson/internal/guard"
)

// maxRenderTagLines caps how many lines a single <render> widget shows, so a
// malformed or huge range can't produce an unbounded box.
const maxRenderTagLines = 500

// gitShowTimeout bounds a <render ref="..."> citation's `git show` call —
// layout runs on the paint hot path, so a slow/huge/cold repo must not stall
// the whole TUI. A timeout renders as an error box instead of hanging.
const gitShowTimeout = 2 * time.Second

// gitRevParseTimeout bounds the repo-root lookup (gitRepoRelativePath) that
// precedes a git show call — kept shorter than gitShowTimeout since it's
// pure local metadata (no object data to read), so the worst case for a
// full <render ref="..."> citation stays a bounded few seconds, not double
// gitShowTimeout.
const gitRevParseTimeout = 1 * time.Second

// renderTagOuterRe matches a <render .../> tag that occupies its entire
// (already trimmed) line, same discipline as a ``` fence delimiter — a tag
// sharing a line with other text is left as literal text instead of
// expanding into a widget. This is deliberate: the widget renders full
// width on its own line, so mid-sentence use would visually split the
// sentence around it.
var renderTagOuterRe = regexp.MustCompile(`^<render\s+(.*?)\s*/>$`)

// renderAttrRe matches one key="value" or key=value attribute inside a
// <render> tag's body — quotes optional, so both `from="10"` and `from=0`
// (the two shapes seen in the wild) parse the same way.
var renderAttrRe = regexp.MustCompile(`(\w+)=("[^"]*"|[^\s/]+)`)

// parseRenderTag reports whether trimmed line is a complete <render> tag,
// returning its attributes. file is required; ref/from/to are optional
// (ref="" means "read straight from disk", from/to==0 means "unset" — see
// sliceLines for the resulting defaults). Attribute order doesn't matter.
func parseRenderTag(line string) (file, ref string, from, to int, ok bool) {
	m := renderTagOuterRe.FindStringSubmatch(line)
	if m == nil {
		return "", "", 0, 0, false
	}
	attrs := map[string]string{}
	for _, am := range renderAttrRe.FindAllStringSubmatch(m[1], -1) {
		attrs[am[1]] = strings.Trim(am[2], `"`)
	}
	file = attrs["file"]
	if file == "" {
		return "", "", 0, 0, false
	}
	ref = attrs["ref"]
	from, _ = strconv.Atoi(attrs["from"])
	to, _ = strconv.Atoi(attrs["to"])
	return file, ref, from, to, true
}

// renderFileWidget turns one <render> citation into a bordered code box —
// same shape as a fenced code block, but the content is read fresh (disk,
// or a git ref via `git show`) rather than anything already in the model's
// context, so showing a snippet to the human costs no output tokens. Errors
// (missing file, bad ref, sensitive path, timeout) render as a one-line
// message inside the box instead of failing the whole message.
func renderFileWidget(file, ref string, from, to, width int, prefix string) []string {
	var body string
	var effFrom, effTo int
	var err error
	if ref != "" {
		body, effFrom, effTo, err = readGitLineRange(ref, file, from, to)
	} else {
		body, effFrom, effTo, err = readFileLineRange(file, from, to)
	}
	title := file
	if ref != "" {
		title = ref + ":" + file
	}
	if err != nil {
		return renderCodeBlock(title, "", "render error: "+err.Error(), width, prefix)
	}
	if effTo > 0 {
		title = fmt.Sprintf("%s:%d-%d", title, effFrom, effTo)
	}
	return renderCodeBlock(title, langFromExt(file), body, width, prefix)
}

// readFileLineRange reads path fresh off disk and slices it to [from, to].
// path resolves relative to the process's cwd, same convention as read/write
// (see readGitLineRange's doc comment) — a relative path silently pointing
// at the wrong file (e.g. left over from a bash workdir override) is a
// non-obvious bug to spot, so a missing-file error names the absolute path
// that was actually tried instead of leaving the reader to guess.
func readFileLineRange(path string, from, to int) (body string, effFrom, effTo int, err error) {
	if reason := guard.SensitivePathReason(path); reason != "" {
		return "", 0, 0, fmt.Errorf("blocked: %s", reason)
	}
	data, err := readAtFile(path)
	if err != nil {
		if !filepath.IsAbs(path) {
			if abs, absErr := filepath.Abs(path); absErr == nil {
				return "", 0, 0, fmt.Errorf("%w (resolved to %s)", err, abs)
			}
		}
		return "", 0, 0, err
	}
	return sliceLines(string(data), from, to)
}

// readGitLineRange reads path's content at git ref via `git show ref:path`.
// path resolves the same implicit-cwd way as a disk read (relative to the
// process's own working directory) — but the git command itself always runs
// inside path's own repository, with a path relative to that repository's
// root, regardless of what the process's cwd happens to be. Needed because
// px's session cwd is routinely a multi-project index directory sitting one
// level above several independent repos (this repo's own dev workflow — see
// AGENTS.md), not a git repo itself; running `git show` there fails "not a
// git repository" for every single citation no matter how correct path is.
func readGitLineRange(ref, path string, from, to int) (body string, effFrom, effTo int, err error) {
	repoRoot, relPath, err := gitRepoRelativePath(path)
	if err != nil {
		return "", 0, 0, err
	}
	return readGitLineRangeIn(repoRoot, ref, relPath, from, to)
}

// gitRepoRelativePath resolves path (implicit process-cwd, same convention
// as a disk read) to an absolute path, then finds the git repository
// containing it via `git rev-parse --show-toplevel` and returns that
// repository's root plus path's location relative to it.
func gitRepoRelativePath(path string) (repoRoot, relPath string, err error) {
	path, err = resolveUnderWorkspaceRepo(path)
	if err != nil {
		return "", "", err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitRevParseTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", filepath.Dir(abs), "rev-parse", "--show-toplevel").Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", "", fmt.Errorf("git rev-parse timed out after %s", gitRevParseTimeout)
		}
		// Real cause first: rev-parse fails "not a git repository" for a
		// wrong directory, but also for e.g. dubious-ownership — reporting
		// git's own message instead of a fixed guess avoids lying about
		// which one it is. Same "(resolved to %s)" naming as the disk-read
		// miss below: a relative path is routinely left over from a citation
		// made inside a different repo (bash workdir override, subagent
		// cwd), and the resolved absolute path makes that mismatch obvious
		// on sight instead of requiring the reader to re-derive it.
		reason := "is not inside a git repository"
		if ee, ok := err.(*exec.ExitError); ok {
			if msg := firstLine(string(ee.Stderr)); msg != "" {
				reason = msg
			}
		}
		if !filepath.IsAbs(path) {
			return "", "", fmt.Errorf("%s: %s (resolved to %s)", path, reason, abs)
		}
		return "", "", fmt.Errorf("%s: %s", path, reason)
	}
	repoRoot = strings.TrimSpace(string(out))
	rel, err := filepath.Rel(repoRoot, abs)
	if err != nil {
		return "", "", err
	}
	return repoRoot, filepath.ToSlash(rel), nil
}

// resolveUnderWorkspaceRepo adjusts a relative <render ref="..."> path when
// it doesn't exist directly under the process cwd but is found, unambiguously,
// one or two directories below it — the shape of a multi-repo workspace (see
// AGENTS.md: px's own dev cwd sits one level above many independent repos).
// This recovers the routine case where ref/path were copied from a citation
// made inside one of those repos (bash workdir override, subagent cwd) and
// reused verbatim against the outer session's cwd — exactly the mismatch
// gitRepoRelativePath's error names but previously left the reader to fix
// by hand. A direct hit or zero candidates return path unchanged, so the
// existing "not inside a git repository" error (with its resolved-path
// hint) still fires for a genuinely wrong or nonexistent path. Multiple
// candidates return an error instead of guessing — showing the wrong
// repo's file silently would be worse than the original error.
func resolveUnderWorkspaceRepo(path string) (string, error) {
	if filepath.IsAbs(path) {
		return path, nil
	}
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	var matches []string
	for _, depth := range []string{"*", filepath.Join("*", "*")} {
		found, _ := filepath.Glob(filepath.Join(depth, path))
		matches = append(matches, found...)
	}
	switch len(matches) {
	case 0:
		return path, nil
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("%s is ambiguous, found under: %s", path, strings.Join(matches, ", "))
	}
}

// readGitLineRangeIn is readGitLineRange with an explicit repo directory
// (dir == "" means the process's own cwd) — split out so tests can point it
// at a throwaway fixture repo instead of depending on process cwd. argv-only
// invocation (never a shell string), so ref/path carry no injection risk
// regardless of content — git itself rejects anything that isn't a valid
// revision.
func readGitLineRangeIn(dir, ref, path string, from, to int) (body string, effFrom, effTo int, err error) {
	if reason := guard.SensitivePathReason(path); reason != "" {
		return "", 0, 0, fmt.Errorf("blocked: %s", reason)
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitShowTimeout)
	defer cancel()
	// --end-of-options: ref is model-controlled text. Without it, a ref
	// starting with "-" (e.g. "--output=/some/path") is parsed by git as a
	// flag, not a revision — argv-only exec.Command already blocks shell
	// injection, but not this: git itself would still act on the flag (a
	// real reproduced arbitrary-file-write via "git show --output=...").
	// Plain "--" doesn't work here — it changes how git parses "ref:path".
	cmd := exec.CommandContext(ctx, "git", "show", "--end-of-options", ref+":"+path)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", 0, 0, fmt.Errorf("git show timed out after %s", gitShowTimeout)
		}
		if ee, ok := err.(*exec.ExitError); ok {
			return "", 0, 0, fmt.Errorf("git show %s:%s: %s", ref, path, firstLine(string(ee.Stderr)))
		}
		return "", 0, 0, err
	}
	return sliceLines(string(out), from, to)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// sliceLines extracts 1-indexed lines [from, to] (inclusive) from content,
// clamped to content's actual length and to maxRenderTagLines. from<=0
// defaults to 1; to<=0 (or a range wider than the cap) defaults to
// from+maxRenderTagLines-1. Numbered like the read tool's own output
// ("N: text"), so a cited range visually matches what the model already
// saw when it picked the range.
func sliceLines(content string, from, to int) (body string, effFrom, effTo int, err error) {
	if len(content) == 0 {
		return "(empty file)", 0, 0, nil
	}
	if strings.IndexByte(content, 0) >= 0 {
		return "", 0, 0, fmt.Errorf("binary file not supported")
	}
	if !utf8.ValidString(content) {
		return "", 0, 0, fmt.Errorf("non-UTF-8 file not supported")
	}
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	if from <= 0 {
		from = 1
	}
	if to <= 0 || to-from+1 > maxRenderTagLines {
		to = from + maxRenderTagLines - 1
	}
	if from > len(lines) {
		return "", 0, 0, fmt.Errorf("from=%d beyond end of content (%d lines)", from, len(lines))
	}
	if to > len(lines) {
		to = len(lines)
	}
	var b strings.Builder
	for i := from; i <= to; i++ {
		fmt.Fprintf(&b, "%d: %s\n", i, lines[i-1])
	}
	return b.String(), from, to, nil
}

// langFromExt maps a file extension to the highlight/lang key
// layoutRichMarkdown already recognizes (see langKeywords) — "" falls back
// to the same plain-yellow rendering an unknown fenced-code language gets.
func langFromExt(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".js", ".jsx", ".mjs":
		return "javascript"
	case ".ts", ".tsx":
		return "typescript"
	case ".sh", ".bash", ".zsh":
		return "bash"
	case ".yml", ".yaml":
		return "yaml"
	case ".json":
		return "json"
	case ".md":
		return "markdown"
	default:
		return ""
	}
}
