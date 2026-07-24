package tools

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// bashSticky is the in-process shell continuity for one BashTool instance
// (one agent session). Subagents build their own registry → own BashTool →
// isolated sticky. Nothing here is written to SQLite; process exit drops it.
//
// Concurrent bash tool_uses on the same instance are serialized so two
// writers cannot race the snapshot.
type bashSticky struct {
	mu  sync.Mutex
	cwd string   // absolute; empty → fall back to BashTool.cwd (session root)
	env []string // KEY=VAL entries for cmd.Env; nil → os.Environ()
}

// lock serializes a full bash Execute against this sticky state. Callers
// read/write s.cwd and s.env only while holding the lock.
func (s *bashSticky) lock() { s.mu.Lock() }
func (s *bashSticky) unlock() { s.mu.Unlock() }

// stickyStartDir picks the directory for this call: explicit workdir wins
// (resolved against sessionRoot), else sticky cwd, else sessionRoot.
func stickyStartDir(sessionRoot, stickyCwd, workdir string) string {
	if workdir != "" {
		if filepath.IsAbs(workdir) {
			return workdir
		}
		base := sessionRoot
		if base == "" {
			base = "."
		}
		return filepath.Join(base, workdir)
	}
	if stickyCwd != "" {
		return stickyCwd
	}
	return sessionRoot
}

// envForCmd returns the environment slice for exec.Cmd. A nil sticky env
// means "inherit the process environment".
func envForCmd(stickyEnv []string) []string {
	if stickyEnv == nil {
		return os.Environ()
	}
	return append([]string(nil), stickyEnv...)
}

// parseEnvNull decodes `env -0` output into KEY=VAL entries.
func parseEnvNull(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	if data[len(data)-1] == 0 {
		data = data[:len(data)-1]
	}
	parts := bytes.Split(data, []byte{0})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if len(p) == 0 {
			continue
		}
		s := string(p)
		if !strings.Contains(s, "=") {
			continue
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// readStickyDump reads the two files written by the bash wrapper after the
// user command: cwdFile is a single line (pwd), envFile is env -0.
func readStickyDump(cwdFile, envFile string) (cwd string, env []string, err error) {
	raw, err := os.ReadFile(cwdFile)
	if err != nil {
		return "", nil, fmt.Errorf("read sticky cwd: %w", err)
	}
	cwd = strings.TrimSpace(string(raw))
	if cwd == "" {
		return "", nil, fmt.Errorf("sticky cwd dump empty")
	}
	if !filepath.IsAbs(cwd) {
		return "", nil, fmt.Errorf("sticky cwd not absolute: %q", cwd)
	}
	envRaw, err := os.ReadFile(envFile)
	if err != nil {
		return "", nil, fmt.Errorf("read sticky env: %w", err)
	}
	env = parseEnvNull(envRaw)
	if env == nil {
		return "", nil, fmt.Errorf("sticky env dump empty")
	}
	return cwd, env, nil
}

// bashSingleQuote returns a bash single-quoted string literal for s.
func bashSingleQuote(s string) string {
	return `'` + strings.ReplaceAll(s, `'`, `'\''`) + `'`
}

// wrapBashForSticky builds a bash -c script that runs the user command in the
// *same* shell (via eval — a nested bash -c would discard cd/export), then
// writes pwd + env -0 into the given paths so the parent can update sticky
// state without scraping stdout/stderr.
//
// Exit status of the script is the user command's exit status.
//
// cmdFile is a path whose contents are the raw user command (written by the
// caller). Reading from a file avoids stuffing arbitrary user bytes into the
// outer -c string beyond a quoted path.
func wrapBashForSticky(cmdFile, cwdFile, envFile string) string {
	// set +e so a failing user command still reaches the dump lines.
	// eval runs in this shell so cd/export persist until we dump.
	return fmt.Sprintf(
		`set +e
eval "$(cat -- %s)"
__poisson_ec=$?
pwd >%s 2>/dev/null
env -0 >%s 2>/dev/null
exit $__poisson_ec
`,
		bashSingleQuote(cmdFile),
		bashSingleQuote(cwdFile),
		bashSingleQuote(envFile),
	)
}
