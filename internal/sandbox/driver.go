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
	"time"
)

// CreateOpts configures a new sandbox container.
type CreateOpts struct {
	// Image is the container image, e.g. "ubuntu:26.04". Empty means the
	// driver's own default.
	Image string
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
}
