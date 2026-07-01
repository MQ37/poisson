package tui

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"regexp"
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

func expandAtFiles(input string) (string, error) {
	var firstErr error
	result := atFileRe.ReplaceAllStringFunc(input, func(match string) string {
		path := match[1:]
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