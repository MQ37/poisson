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
	if info.Size() > maxAtFileBytes {
		return nil, fmt.Errorf("file too large (%d bytes, max %d)", info.Size(), maxAtFileBytes)
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

func expandAtFiles(input string) (string, error) {
	var firstErr error
	result := atFileRe.ReplaceAllStringFunc(input, func(match string) string {
		path := match[1:]
		info, statErr := os.Stat(path)
		if statErr != nil {
			// Not a real file/dir — e.g. "@link" in a pasted JSDoc comment, an
			// @handle, etc. Leave it as plain text instead of erroring the send.
			return match
		}
		// A directory expands to a one-level listing (subdirs suffixed "/")
		// instead of erroring; a file expands to its fenced contents.
		if info.IsDir() {
			listing, err := listAtDir(path)
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("read @%s: %w", path, err)
				}
				return match
			}
			fence := "```"
			for strings.Contains(listing, fence) {
				fence += "`"
			}
			return fmt.Sprintf("%s\n%s (directory):\n%s\n%s", fence, path, listing, fence)
		}
		data, err := readAtFile(path)
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