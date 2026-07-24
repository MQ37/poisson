package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/testutil"
)

func TestEdit_ReplaceAll(t *testing.T) {
	dir := testutil.TempDir(t)
	w := NewWriteTool(dir, true, nil)
	e := NewEditTool(dir, true, nil)
	w.Execute(context.Background(), mustJSON(t, map[string]string{
		"path": "f.txt", "content": "foo\nbar foo\nfoo\n",
	}))

	res, _ := e.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"path":       "f.txt",
		"oldText":    "foo",
		"newText":    "baz",
		"replaceAll": true,
	}))
	if res.Error != "" {
		t.Fatalf("edit error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "3 replacement(s)") {
		t.Errorf("content = %q, want 3 replacements", res.Content)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "f.txt"))
	if string(got) != "baz\nbar baz\nbaz\n" {
		t.Fatalf("file = %q", got)
	}
}

func TestEdit_ReplaceAllFalseStillRequiresUnique(t *testing.T) {
	dir := testutil.TempDir(t)
	w := NewWriteTool(dir, true, nil)
	e := NewEditTool(dir, true, nil)
	w.Execute(context.Background(), mustJSON(t, map[string]string{"path": "f.txt", "content": "foo\nfoo\n"}))

	res, _ := e.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"path": "f.txt", "oldText": "foo", "newText": "bar",
	}))
	if res.Error == "" || !strings.Contains(res.Error, "not unique") {
		t.Fatalf("error = %q, want not unique", res.Error)
	}
	if !strings.Contains(res.Error, "replaceAll") {
		t.Errorf("error should mention replaceAll, got %q", res.Error)
	}
}

func TestEdit_CRLFRoundTrip(t *testing.T) {
	dir := testutil.TempDir(t)
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("alpha\r\nbeta\r\ngamma\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := NewEditTool(dir, true, nil)
	res, _ := e.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"path": "f.txt", "oldText": "beta", "newText": "BETA",
	}))
	if res.Error != "" {
		t.Fatalf("edit error: %s", res.Error)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "alpha\r\nBETA\r\ngamma\r\n" {
		t.Fatalf("CRLF not preserved: %q", got)
	}
}

func TestEdit_SuccessIncludesLineAndSnippet(t *testing.T) {
	dir := testutil.TempDir(t)
	w := NewWriteTool(dir, true, nil)
	e := NewEditTool(dir, true, nil)
	w.Execute(context.Background(), mustJSON(t, map[string]string{
		"path": "f.txt", "content": "one\ntwo\nthree\nfour\n",
	}))
	res, _ := e.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"path": "f.txt", "oldText": "three", "newText": "THREE",
	}))
	if res.Error != "" {
		t.Fatalf("edit error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "first @ line 3") {
		t.Errorf("missing line number: %q", res.Content)
	}
	if !strings.Contains(res.Content, "3: THREE") {
		t.Errorf("missing snippet: %q", res.Content)
	}
}

func TestEdit_IdenticalOldNewRejected(t *testing.T) {
	dir := testutil.TempDir(t)
	w := NewWriteTool(dir, true, nil)
	e := NewEditTool(dir, true, nil)
	w.Execute(context.Background(), mustJSON(t, map[string]string{"path": "f.txt", "content": "x\n"}))
	res, _ := e.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"path": "f.txt", "oldText": "x", "newText": "x",
	}))
	if res.Error == "" || !strings.Contains(res.Error, "identical") {
		t.Fatalf("error = %q, want identical", res.Error)
	}
}

// Deleted space next to punctuation used to miss the whitespace hint because
// strings.Fields merged tokens (Foo(){ vs Foo() + {). stripWS must catch it.
func TestEdit_MissingHintsDeletedSpaceNearPunct(t *testing.T) {
	dir := testutil.TempDir(t)
	w := NewWriteTool(dir, true, nil)
	e := NewEditTool(dir, true, nil)
	w.Execute(context.Background(), mustJSON(t, map[string]string{
		"path": "f.txt", "content": "func Foo() {\n\treturn 1\n}\n",
	}))
	res, _ := e.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"path": "f.txt", "oldText": "func Foo(){", "newText": "func Foo() {",
	}))
	if res.Error == "" {
		t.Fatal("expected miss")
	}
	if !strings.Contains(res.Error, "whitespace-only mismatch at line 1") {
		t.Fatalf("error = %q, want whitespace-only mismatch", res.Error)
	}
}

// replaceAll across a huge file must not dump the whole result as "snippet".
// Regression: window used to grow with newText line count → EOF on mass replace.
func TestEdit_SuccessSnippetCappedOnMassReplaceAll(t *testing.T) {
	dir := testutil.TempDir(t)
	var body strings.Builder
	const n = 5000
	for i := 0; i < n; i++ {
		fmt.Fprintf(&body, "line %05d foo end\n", i)
	}
	path := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(path, []byte(body.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	e := NewEditTool(dir, true, nil)
	res, _ := e.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"path":       "big.txt",
		"oldText":    "foo",
		"newText":    "bar",
		"replaceAll": true,
	}))
	if res.Error != "" {
		t.Fatalf("edit error: %s", res.Error)
	}
	if !strings.Contains(res.Content, fmt.Sprintf("%d replacement(s)", n)) {
		prefix := res.Content
		if len(prefix) > 200 {
			prefix = prefix[:200]
		}
		t.Fatalf("want %d replacements in %q", n, prefix)
	}
	if len(res.Content) > 4*1024 {
		t.Fatalf("success content too large (%d bytes) — snippet not capped", len(res.Content))
	}
	if strings.Contains(res.Content, "5000:") || strings.Contains(res.Content, "4999:") {
		t.Fatalf("snippet reached EOF of file: %q", res.Content)
	}
	if !strings.Contains(res.Content, "1: ") {
		t.Fatalf("missing first-line snippet: %q", res.Content)
	}
}

// Single enormous line must still cap — old Split-based snippet emitted one
// gigantic "1: <whole file>" line.
func TestEdit_SuccessSnippetCappedOnGiantLine(t *testing.T) {
	dir := testutil.TempDir(t)
	payload := "START foo " + strings.Repeat("x", 80*1024) + " END\n"
	if err := os.WriteFile(filepath.Join(dir, "fat.txt"), []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	e := NewEditTool(dir, true, nil)
	res, _ := e.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"path": "fat.txt", "oldText": "foo", "newText": "bar",
	}))
	if res.Error != "" {
		t.Fatalf("edit error: %s", res.Error)
	}
	if len(res.Content) > 4*1024 {
		t.Fatalf("success content too large (%d bytes)", len(res.Content))
	}
	if !strings.Contains(res.Content, "…") {
		t.Fatalf("expected per-line ellipsis on giant line, got %q", res.Content)
	}
}

// Near-miss identifier (baz vs bar) must surface a nearest-line hint, not only
// the generic re-read footer.
func TestEdit_MissingHintsFuzzyNearMiss(t *testing.T) {
	dir := testutil.TempDir(t)
	w := NewWriteTool(dir, true, nil)
	e := NewEditTool(dir, true, nil)
	w.Execute(context.Background(), mustJSON(t, map[string]string{
		"path": "f.txt", "content": "alpha\nbar\ngamma\n",
	}))
	res, _ := e.Execute(context.Background(), mustJSON(t, map[string]interface{}{
		"path": "f.txt", "oldText": "baz", "newText": "qux",
	}))
	if res.Error == "" {
		t.Fatal("expected miss")
	}
	if !strings.Contains(res.Error, "nearest match: line 2") {
		t.Fatalf("error = %q, want nearest match at line 2", res.Error)
	}
	if !strings.Contains(res.Error, "bar") {
		t.Fatalf("error = %q, want to show the bar line", res.Error)
	}
}
