package tui

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"poisson/internal/agent"
)

const maxAtFileBytes = 512 << 10 // 512 KiB

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

func readAtFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}

	data := make([]byte, info.Size())
	if _, err := io.ReadFull(f, data); err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return data, nil
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return nil, fmt.Errorf("binary file not supported")
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("non-UTF-8 file not supported")
	}
	return data, nil
}

// listAtDir returns a one-level (non-recursive) listing of dir's entries,
// sorted by name, with subdirectories suffixed by "/". Only the directory's own
// entries are listed — nothing nested.
func listAtDir(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, "\n"), nil
}

// fenceFor returns a ``` fence long enough that it can't collide with any
// fence already present in body (escalating backtick count as needed).
func fenceFor(body string) string {
	fence := "```"
	for strings.Contains(body, fence) {
		fence += "`"
	}
	return fence
}

// stripFence removes a leading/trailing fence line from s, if s has the exact
// shape expandAtFilesSegments produces for a FileRef segment (a fence line,
// body, matching fence line). Used to recover the display-friendly body from
// text that's also the literal, unmodified content sent to the model —
// avoiding a second copy of the same content just for display. Returns s
// unchanged if the shape doesn't match (e.g. a session stored before this
// existed, or the content legitimately isn't fenced).
func stripFence(s string) string {
	lines := strings.Split(s, "\n")
	if len(lines) < 2 {
		return s
	}
	first := lines[0]
	last := lines[len(lines)-1]
	if first == "" || first != last || !strings.HasPrefix(first, "```") {
		return s
	}
	return strings.Join(lines[1:len(lines)-1], "\n")
}

// expandAtFilesSegments splits input into text segments, inlining each
// @path reference's file/directory contents as its own segment (FileRef set
// to the source path) instead of splicing it into one flat string. Plain
// typed text — including any @token that isn't a real file/dir, e.g. "@link"
// in a pasted JSDoc comment or an @handle — stays as ordinary segments with
// no FileRef. Concatenating every segment's Text in order reproduces exactly
// what the model is sent; the segment boundaries exist purely so the TUI can
// render a file segment as a collapsible card instead of dumping it inline.
func expandAtFilesSegments(input string) ([]agent.TextSegment, error) {
	var firstErr error
	var segs []agent.TextSegment
	appendPlain := func(s string) {
		if s != "" {
			segs = append(segs, agent.TextSegment{Text: s})
		}
	}
	last := 0
	for _, loc := range atFileRe.FindAllStringIndex(input, -1) {
		start, end := loc[0], loc[1]
		appendPlain(input[last:start])
		last = end
		match := input[start:end]
		path := match[1:]
		info, statErr := os.Stat(path)
		if statErr != nil {
			// Not a real file/dir — leave it as plain text instead of erroring the
			// send.
			appendPlain(match)
			continue
		}
		// A directory expands to a one-level listing (subdirs suffixed "/")
		// instead of erroring; a file expands to its fenced contents.
		if info.IsDir() {
			listing, err := listAtDir(path)
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("read @%s: %w", path, err)
				}
				appendPlain(match)
				continue
			}
			fence := fenceFor(listing)
			segs = append(segs, agent.TextSegment{
				Text:    fmt.Sprintf("%s\n%s (directory):\n%s\n%s", fence, path, listing, fence),
				FileRef: path,
			})
			continue
		}
		if info.Size() > maxAtFileBytes {
			// Don't dump an oversized file into the message (blows the context
			// window) and don't error the send either — tell the model the file
			// exists and how big it is, and let it read the file itself: a tool
			// call with offset/limit for arbitrary text, or a format-aware parse
			// (e.g. streaming/incremental JSON) for structured data.
			note := fmt.Sprintf(
				"%s is %d bytes, too large to inline (max %d). Read it yourself in chunks (e.g. offset/limit) or parse it smartly for its format (e.g. stream/parse JSON) instead of loading it whole.",
				path, info.Size(), maxAtFileBytes,
			)
			segs = append(segs, agent.TextSegment{Text: note, FileRef: path})
			continue
		}
		data, err := readAtFile(path)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("read @%s: %w", path, err)
			}
			appendPlain(match)
			continue
		}
		fence := fenceFor(string(data))
		segs = append(segs, agent.TextSegment{
			Text:    fence + "\n" + string(data) + "\n" + fence,
			FileRef: path,
		})
	}
	appendPlain(input[last:])
	if firstErr != nil {
		return nil, firstErr
	}
	return segs, nil
}

// segmentsText concatenates every segment's Text — exactly the flat string
// that used to be sent to the model before segments existed.
func segmentsText(segs []agent.TextSegment) string {
	var b strings.Builder
	for _, s := range segs {
		b.WriteString(s.Text)
	}
	return b.String()
}
