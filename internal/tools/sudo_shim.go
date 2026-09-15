package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// sudoShim is Tier 2 of the sudo-over-ssh fix: guard.RequiresSudoPassword's
// text scan can never see a sudo call hidden behind an alias, a Makefile
// recipe, a nested script, or — the bug this closes — a heredoc-delimited
// remote command sent over ssh (`ssh host <<'EOF'` / `sudo ...` / `EOF`
// used to expose the remote "sudo ..." line as if it were a fresh local
// segment; see guard.Segments' heredoc handling for that half of the fix).
//
// Instead of trying to predict from text whether sudo will run locally,
// this makes a passworded sudo path available to the WHOLE process tree
// one bash call spawns, unconditionally, and only actually prompts a human
// if some real LOCAL sudo process it intercepts genuinely blocks on a
// password. Two pieces, both scoped to exactly one BashTool.Execute call:
//
//   - a "sudo" executable placed at the front of PATH that forwards to the
//     real binary with "-A -k" always added — any shell that resolves the
//     bare word "sudo" via $PATH hits this, no matter how deeply nested;
//   - SUDO_ASKPASS pointing at a relay script (`px --internal-sudo-askpass
//     <socket>`, see cmd/px's runSudoAskpassRelay) that only ever runs when
//     a real "sudo -A" is actually blocked on a password — it connects to
//     the socket below, blocks, and prints back whatever password this
//     shim's own listener hands it after asking the human via the same
//     sudoPasswordFn prompt Tier 1 already uses.
//
// A REMOTE sudo run over ssh never sees any of this: ssh does not forward
// the local PATH or SUDO_ASKPASS to the shell it starts on the far end, so
// the shim is simply never reached — no text parsing decides that, the
// process boundary does.
//
// Never engaged when guard.RequiresSudoPassword already recognized the
// command textually — BashTool.Execute's existing Tier 1 path handles that
// case directly, including one this shim structurally cannot: an
// absolute-path "/usr/bin/sudo" invocation bypasses $PATH resolution
// entirely and would never reach this shim regardless of it being set up.
// Silently unavailable (newSudoShim returns an error, never fatal to the
// bash call) if there's no local "sudo" binary at all, or px's own
// executable path can't be resolved — the command just runs without this
// extra safety net, exactly as it always has for the shapes this adds
// coverage for.
type sudoShim struct {
	dir         string
	askpassSh   string
	listener    net.Listener
	ask         SudoPasswordFn
	command     string
	description string
	workdir     string

	// mu guards cached: the first local sudo call to actually block on a
	// password prompts the human and stores the answer here; every later
	// one in the SAME shim (i.e. same BashTool.Execute call/script) reuses
	// it instead of prompting again — see password's own doc comment.
	mu     sync.Mutex
	cached []byte
}

// newSudoShim resolves the real sudo binary and this process's own
// executable, writes both shim scripts, and starts listening — the
// returned shim is live (already accepting connections) the moment this
// returns successfully. ctx bounds both the listener's lifetime and any
// prompt it triggers: the same context the bash call's own exec timeout
// already cancels, so a command that times out doesn't leave a prompt
// dangling forever.
func newSudoShim(ctx context.Context, ask SudoPasswordFn, command, description, workdir string) (*sudoShim, error) {
	realSudo, err := exec.LookPath("sudo")
	if err != nil {
		return nil, fmt.Errorf("no local sudo binary: %w", err)
	}

	dir, err := os.MkdirTemp("", "poisson-sudoshim-")
	if err != nil {
		return nil, fmt.Errorf("sudo shim tmpdir: %w", err)
	}

	shimSudo := "#!/bin/sh\nexec " + shellSingleQuote(realSudo) + " -A -k \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "sudo"), []byte(shimSudo), 0o700); err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("sudo shim script: %w", err)
	}

	// Kept short and random (not "askpass.sock") for the same reason
	// sudo_askpass.go's randomFileName avoids an obviously secret-shaped
	// name, and short specifically because AF_UNIX socket paths have a
	// hard OS length limit an ordinary file doesn't.
	sockPath := filepath.Join(dir, randomHex(8)+".sock")
	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("sudo shim socket: %w", err)
	}
	_ = os.Chmod(sockPath, 0o700)

	relayScript, err := buildAskpassRelayScript(sockPath)
	if err != nil {
		listener.Close()
		os.RemoveAll(dir)
		return nil, fmt.Errorf("resolve askpass relay: %w", err)
	}
	askpassSh := filepath.Join(dir, "askpass.sh")
	if err := os.WriteFile(askpassSh, []byte(relayScript), 0o700); err != nil {
		listener.Close()
		os.RemoveAll(dir)
		return nil, fmt.Errorf("sudo shim relay script: %w", err)
	}

	s := &sudoShim{
		dir: dir, askpassSh: askpassSh, listener: listener,
		ask: ask, command: command, description: description, workdir: workdir,
	}
	go s.serve(ctx)
	return s, nil
}

// serve accepts connections until the listener is closed (see close) — one
// per local sudo invocation that actually needs a password. The shim
// always adds "-k" to the real sudo call so each one re-checks the
// password against PAM (see injectSudoAskpass's doc comment on why -k is
// deliberate), but password below answers the human only once per shim —
// a script invoking sudo N times prompts once, not N times.
func (s *sudoShim) serve(ctx context.Context) {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return // listener closed — this bash call is done
		}
		go s.handle(ctx, conn)
	}
}

// handle answers one relay connection with password, then writes it back —
// a cancel (password's ok == false) writes nothing, so the relay's read
// hits EOF with an empty password and sudo treats that as a failed/
// cancelled auth attempt, exactly like Tier 1's own cancel path already
// does.
func (s *sudoShim) handle(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	password, ok := s.password(ctx)
	if !ok {
		return
	}
	conn.Write(password)
	conn.Write([]byte("\n"))
}

// password returns this shim's sudo password, asking the human (via ask,
// same overlay UI as Tier 1) at most once for the shim's whole lifetime —
// one BashTool.Execute call. Concurrent callers block on mu behind the
// first prompt rather than opening a second one; every caller after that
// first prompt resolves just reads the cached answer. Cleared and zeroed
// by close, never persisted past this one shim/command.
func (s *sudoShim) password(ctx context.Context) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached != nil {
		return s.cached, true
	}
	password, ok := s.ask(ctx, s.command, s.description, s.workdir)
	if !ok {
		return nil, false
	}
	s.cached = password
	return s.cached, true
}

// buildEnv returns the child process's full environment with PATH (the
// shim directory prepended) and SUDO_ASKPASS overridden. Any existing
// value for either is dropped first so there's exactly one of each in the
// result — a duplicate entry risks resolving to whichever one the child's
// own libc getenv happens to prefer, not necessarily the last one appended.
func (s *sudoShim) buildEnv() []string {
	base := os.Environ()
	env := make([]string, 0, len(base)+2)
	for _, kv := range base {
		if strings.HasPrefix(kv, "PATH=") || strings.HasPrefix(kv, "SUDO_ASKPASS=") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "PATH="+s.dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	env = append(env, "SUDO_ASKPASS="+s.askpassSh)
	return env
}

// close stops accepting new connections, zeroes any cached password, and
// removes every file this shim created. Safe to call exactly once, via
// defer, regardless of whether anything ever actually connected.
func (s *sudoShim) close() {
	s.listener.Close()
	s.mu.Lock()
	for i := range s.cached {
		s.cached[i] = 0 // best-effort zero, same caveat as sudoAskpassHelper's own doc comment
	}
	s.cached = nil
	s.mu.Unlock()
	if err := os.RemoveAll(s.dir); err != nil {
		log.Printf("sudo shim cleanup: %v", err)
	}
}

// buildAskpassRelayScript returns the SUDO_ASKPASS script content for one
// call — normally a relay to this process's own binary (see cmd/px's
// --internal-sudo-askpass dispatch, which only a real px binary
// implements). Overridable in tests, whose compiled binary has no such
// dispatch wired in (same "Overridable in tests" pattern as
// output.go's toolSpillDir).
var buildAskpassRelayScript = func(sockPath string) (string, error) {
	pxExe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve own executable: %w", err)
	}
	return "#!/bin/sh\nexec " + shellSingleQuote(pxExe) + " --internal-sudo-askpass " + shellSingleQuote(sockPath) + "\n", nil
}

// randomHex returns a random hex string nBytes long. Used only for the
// socket filename here — crypto/rand failing is exceptional; fall back to
// a fixed name rather than error the whole shim over it.
func randomHex(nBytes int) string {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "sock"
	}
	return hex.EncodeToString(b)
}
