package sandbox

import (
	"os"
	"strconv"
	"strings"
)

// sandboxMemoryFraction caps a single sandbox container at this fraction of
// the host's total physical memory. Containers share the host kernel, so an
// unbounded sandbox (fork bomb, runaway build, memory-hungry dependency) can
// otherwise OOM the whole host, not just itself — see docs/sandbox-plan.md's
// "Resource limits" section.
const sandboxMemoryFraction = 0.20

// sandboxMemoryLimit returns the byte count for podman create's --memory
// flag (sandboxMemoryFraction of total system memory), and false if total
// memory can't be determined. Create then skips --memory entirely rather
// than failing sandbox creation over a missing resource limit.
func sandboxMemoryLimit() (bytes string, ok bool) {
	total, ok := totalMemoryBytes()
	if !ok || total == 0 {
		return "", false
	}
	limit := uint64(float64(total) * sandboxMemoryFraction)
	if limit == 0 {
		return "", false
	}
	return strconv.FormatUint(limit, 10), true
}

// totalMemoryBytes reads MemTotal from /proc/meminfo (kB, the kernel's own
// documented unit for that field) — Linux only, matching every other place
// this package already assumes a real Linux podman host (bootstrapScript's
// apt-get, the rootless-userns model). No cross-platform fallback: a host
// where this file doesn't exist just gets no --memory limit, not a crash.
func totalMemoryBytes() (uint64, bool) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "MemTotal:" {
			continue
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, false
		}
		return kb * 1024, true
	}
	return 0, false
}
