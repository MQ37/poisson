// Package sandbox manages podman-backed sandbox containers for the bash
// tool's per-call sandboxId routing (see poisson/docs/sandbox-plan.md).
// Driver is the mechanical container-lifecycle interface; Manager adds
// session-scoped ownership tracking on top of it. File tools
// (read/write/edit/grep/glob) never call into this package at all — a
// sandbox's workspace is just a plain host directory handed back to the
// agent, per the plan doc.
package sandbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// CreateOpts configures a new sandbox container.
type CreateOpts struct {
	// Image is the container image, e.g. "ubuntu:26.04". Empty means the
	// driver's own default.
	Image string
	// Name is the agent-chosen sandbox name (e.g. "api-testing-2"), used as-is
	// by a caller building a create_sandbox request. Every Driver resolves it
	// through ResolveSandboxName before use — sanitized and prefixed
	// "px-sandbox-", or a random fallback name when empty — and returns that
	// resolved string as Create's id. The container's own name *is* the id: no
	// separate opaque handle exists, so "know the exact name" is the whole
	// access model (see Manager.EnableDiscovery / docs/sandbox-plan.md's
	// "Crash recovery" section).
	Name string
	// SessionID, when non-empty, is recorded as a poisson.session label on the
	// container — set by Manager.Create from its own sessionID, never by the
	// caller directly. Purely informational (shown by list_sandboxes); access
	// control never depends on it.
	SessionID string
	// HostPath is bind-mounted into the container as its /workspace — the
	// same path later handed to the agent for read/write/edit/grep/glob
	// calls against absolute paths under it.
	HostPath string
	// Mounts are additional bind mounts beyond the base workspace (e.g. a
	// second project directory, credentials). The caller is responsible for
	// getting these human-approved before they ever reach here — this
	// package auto-approves nothing.
	Mounts []Mount
	// Env is extra KEY=VALUE entries injected into the container.
	Env []string
}

// sandboxNamePrefix marks every container this package creates, real or
// fake — the label a List call filters on is the authoritative marker;
// this prefix is a human-readable convenience (podman ps output, avoiding
// collisions with unrelated containers on the host) on top of that.
const sandboxNamePrefix = "px-sandbox-"

// ResolveSandboxName turns an agent-supplied (possibly empty, possibly
// messy) name into the actual container name a Driver creates: sanitized
// and prefixed when requested is non-empty, or a random fallback name
// otherwise. Both podmanDriver and FakeDriver call this themselves at the
// top of Create — naming policy lives once, here, not duplicated per
// driver — so Create's returned id is always a real, filterable, agent-
// legible name, never an opaque hash.
func ResolveSandboxName(requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		suffix, err := randomHex(4)
		if err != nil {
			return "", fmt.Errorf("generate sandbox name: %w", err)
		}
		return sandboxNamePrefix + suffix, nil
	}
	return sandboxNamePrefix + sanitizeSandboxName(requested), nil
}

// sanitizeSandboxName keeps only characters podman/docker container names
// actually allow the way this package uses them — lowercase alnum plus
// -/_ — mapping everything else to '-' rather than rejecting the request
// outright: a messy agent-supplied name (spaces, punctuation) still becomes
// something usable instead of a hard error.
func sanitizeSandboxName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "sandbox"
	}
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// Info describes one live sandbox container as discovered directly from
// podman/the driver — independent of any single Manager's in-memory map.
// This is what makes a sandbox survive a poisson crash: podman itself (not
// this process's memory) is the record of what exists. See Driver.List and
// Manager.EnableDiscovery.
type Info struct {
	// ID is the container's name — the same string a caller passes back as
	// sandboxId.
	ID string
	// HostPath is the host-side source of the /workspace bind mount, when
	// discoverable (empty if the container has no such mount).
	HostPath string
	// SessionID is the poisson.session label value ("" if never set).
	SessionID string
	CreatedAt time.Time
	// Running reports whether the container is currently alive. list_sandboxes
	// shows it; Manager's discovery-attach refuses to adopt a non-running one.
	Running bool
}

// Mount is one additional bind mount beyond CreateOpts.HostPath/workspace.
// JSON tags let the create_sandbox tool unmarshal its input's "mounts"
// array directly into a []Mount.
type Mount struct {
	HostPath      string `json:"hostPath"`
	ContainerPath string `json:"containerPath"`
	ReadOnly      bool   `json:"readOnly"`
}

// Driver is the mechanical container-lifecycle operations a sandbox needs.
// podmanDriver shells out to the real podman CLI (see podman_driver.go);
// FakeDriver is an in-memory test double requiring no podman install — see
// fake_driver.go.
type Driver interface {
	// Create starts a new container per opts and returns its id.
	Create(ctx context.Context, opts CreateOpts) (id string, err error)
	// Exec runs cmd inside container id, in workdir (a container-side path;
	// empty means the container's own default), bounded by timeout.
	Exec(ctx context.Context, id, cmd, workdir string, timeout time.Duration) (stdout, stderr string, exitCode int, err error)
	// Inspect reports whether container id is still alive.
	Inspect(ctx context.Context, id string) (alive bool, err error)
	// Kill stops and removes container id.
	Kill(ctx context.Context, id string) error
	// List reports every live sandbox container this driver's backend knows
	// about, regardless of which Manager (or process) created it — the
	// mechanism behind cross-session discovery and crash recovery. Filtered
	// to containers this package itself created (the poisson.sandbox label),
	// never arbitrary host containers.
	List(ctx context.Context) ([]Info, error)
}
