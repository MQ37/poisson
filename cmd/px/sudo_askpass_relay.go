package main

import (
	"io"
	"net"
	"os"
	"time"
)

// sudoAskpassRelayFlag is the hidden arg BashTool's sudo shim (see
// internal/tools/sudo_shim.go) writes into a one-shot askpass script:
// `px --internal-sudo-askpass <socket>`. Real sudo only ever execs this
// when a LOCAL "sudo -A" it's running is actually blocked on a password —
// never for a remote sudo over ssh, which never sees this binary or the
// socket path at all.
const sudoAskpassRelayFlag = "--internal-sudo-askpass"

// runSudoAskpassRelay connects to the parent px process's per-call Unix
// socket, blocks until it sends back a password (the same interactive
// prompt sudoPasswordFn always used, just triggered on demand instead of
// predicted from the command text up front), and prints it to this
// process's own stdout — the only thing a sudo askpass helper is contracted
// to do. Any failure here (can't connect, parent writes nothing because the
// human cancelled, timeout) exits nonzero with no stdout, which sudo
// correctly treats as a failed/cancelled password attempt.
func runSudoAskpassRelay(sockPath string) {
	conn, err := net.DialTimeout("unix", sockPath, 5*time.Second)
	if err != nil {
		os.Exit(1)
	}
	defer conn.Close()
	// Bounds how long a human can take to answer the prompt this
	// connection triggered — generous, since it's a live interactive
	// decision, but must not hang forever if the parent process died
	// mid-prompt without ever closing its end.
	_ = conn.SetDeadline(time.Now().Add(5 * time.Minute))

	password, err := io.ReadAll(conn)
	if err != nil || len(password) == 0 {
		os.Exit(1)
	}
	os.Stdout.Write(password)
}
