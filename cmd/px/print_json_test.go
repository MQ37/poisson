package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// --- parseArgs: --print-json / "--" terminator (plan Step 5) ---

func TestParseArgs_PrintJSONFlag(t *testing.T) {
	opts, _, _ := parseArgs([]string{"-p", "--print-json", "--", "hello"})
	if !opts.print || !opts.jsonOut {
		t.Fatalf("opts = %+v, want print=true jsonOut=true", opts)
	}
	if opts.prompt != "hello" {
		t.Errorf("prompt = %q, want %q", opts.prompt, "hello")
	}
}

// TestParseArgs_DoubleDashJoinsVerbatimEvenLeadingDash is the regression
// guard named in Step 5: a message that happens to start with "-" must not
// be swallowed by -p's own flag-shaped guard once "--" has terminated flag
// parsing.
func TestParseArgs_DoubleDashJoinsVerbatimEvenLeadingDash(t *testing.T) {
	opts, _, _ := parseArgs([]string{"-p", "--print-json", "--", "-rm", "test"})
	if opts.prompt != "-rm test" {
		t.Errorf("prompt = %q, want %q", opts.prompt, "-rm test")
	}
}

// TestParseArgs_DoubleDashOnlyFirstTerminates checks a literal "--" inside
// the message body itself is just more text, not a second terminator.
func TestParseArgs_DoubleDashOnlyFirstTerminates(t *testing.T) {
	opts, _, _ := parseArgs([]string{"-p", "--", "before", "--", "after"})
	if opts.prompt != "before -- after" {
		t.Errorf("prompt = %q, want %q", opts.prompt, "before -- after")
	}
}

// TestParseArgs_DoubleDashEmptyYieldsEmptyPrompt checks "--" with nothing
// after it produces an empty prompt (not a panic or a leftover "--" in the
// value) — main() is what turns this into the exit-2 "no prompt" path.
func TestParseArgs_DoubleDashEmptyYieldsEmptyPrompt(t *testing.T) {
	opts, _, _ := parseArgs([]string{"-p", "--print-json", "--"})
	if opts.prompt != "" {
		t.Errorf("prompt = %q, want empty", opts.prompt)
	}
}

// --- printSetupExitCode ---

func TestPrintSetupExitCode(t *testing.T) {
	if code := printSetupExitCode(&printSetupError{code: 2, msg: "x"}); code != 2 {
		t.Errorf("code = %d, want 2", code)
	}
	if code := printSetupExitCode(&printSetupError{code: 1, msg: "x"}); code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
}

// --- main()-level integration, run as a subprocess (no network/credentials
// needed: every case below fails before any provider call happens) ---

// TestPrintJSONRequiresPrint checks --print-json without -p is a clean
// exit-2 user error, not silently ignored (Step 5 edge case).
func TestPrintJSONRequiresPrint(t *testing.T) {
	bin := buildPX(t)
	cmd := exec.Command(bin, "--print-json")
	cmd.Env = isolatedEnv(isolatedHome(t))
	out, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected exit error, got %v (output: %s)", err, out)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2 (output: %s)", exitErr.ExitCode(), out)
	}
	if !strings.Contains(string(out), "--print-json requires -p") {
		t.Errorf("output = %q, want the requires-p message", out)
	}
}

// TestPrintJSONNoPromptNeverReadsStdin checks --print-json with no prompt
// given exits 2 immediately without blocking on stdin (which in this mode is
// reserved for approval responses, not a prompt fallback — Step 8). cmd.Stdin
// is left nil (closed/empty), so a code path that tried to read a prompt
// from it would hang and this test would time out instead of failing fast.
func TestPrintJSONNoPromptNeverReadsStdin(t *testing.T) {
	bin := buildPX(t)
	cmd := exec.Command(bin, "-p", "--print-json")
	cmd.Env = isolatedEnv(isolatedHome(t))
	out, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected exit error, got %v (output: %s)", err, out)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2 (output: %s)", exitErr.ExitCode(), out)
	}
	if !strings.Contains(string(out), "no prompt") {
		t.Errorf("output = %q, want the no-prompt message", out)
	}
}

// TestPrintJSONSetupErrorEmitsEventsAndExitCode drives a buildPrintAgent
// setup failure (unknown provider — no network/credentials needed to reach
// it) all the way through runPrintJSON, checking stdout is a clean JSON
// event stream (error then done, done.success=false) and the exit code
// matches the pre-refactor plain-text behavior (2).
func TestPrintJSONSetupErrorEmitsEventsAndExitCode(t *testing.T) {
	bin := buildPX(t)
	cmd := exec.Command(bin, "-p", "--print-json", "--model", "bogus/nomodel", "--", "hi")
	cmd.Env = isolatedEnv(isolatedHome(t))
	out, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected exit error, got %v (output: %s)", err, out)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code = %d, want 2 (output: %s)", exitErr.ExitCode(), out)
	}

	lines := splitNonEmptyLines(out)
	if len(lines) != 2 {
		t.Fatalf("stdout lines = %d, want 2 (error, done): %q", len(lines), out)
	}
	var errEvent, doneEvent map[string]interface{}
	if err := json.Unmarshal(lines[0], &errEvent); err != nil {
		t.Fatalf("line 0 not valid JSON: %v (%s)", err, lines[0])
	}
	if err := json.Unmarshal(lines[1], &doneEvent); err != nil {
		t.Fatalf("line 1 not valid JSON: %v (%s)", err, lines[1])
	}
	if errEvent["type"] != "error" {
		t.Errorf("line 0 type = %v, want error", errEvent["type"])
	}
	if !strings.Contains(errEvent["error"].(string), "unknown provider") {
		t.Errorf("line 0 error = %v, want unknown-provider message", errEvent["error"])
	}
	if doneEvent["type"] != "done" || doneEvent["success"] != false {
		t.Errorf("line 1 = %+v, want done/success=false", doneEvent)
	}
}

// splitNonEmptyLines returns each non-blank line of out as its own []byte,
// for per-line json.Unmarshal — the print-json wire protocol is exactly one
// JSON object per line (writeChildEvent uses json.Marshal + Println).
func splitNonEmptyLines(out []byte) [][]byte {
	var lines [][]byte
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) > 0 {
			lines = append(lines, append([]byte(nil), line...))
		}
	}
	return lines
}
