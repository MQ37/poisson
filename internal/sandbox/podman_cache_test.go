package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakePodmanScript writes a fake `podman` binary to a temp dir that logs
// every invocation's args (one line per call) to logPath and answers each
// subcommand deterministically enough to drive Create() through its real
// logic: "image exists" honors $FAKE_PODMAN_IMAGE_EXISTS (0 = yes, 1 = no,
// matching podman's own real exit codes), "exec" (bootstrap) prints a
// resolved username on stdout, everything else just succeeds. Returns the
// fake binary's path.
func fakePodmanScript(t *testing.T, logPath string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake podman script assumes a POSIX shell")
	}
	script := `#!/bin/sh
echo "$@" >> "` + logPath + `"
case "$1 $2" in
  "image exists") exit "${FAKE_PODMAN_IMAGE_EXISTS:-1}" ;;
esac
if [ "$1" = "exec" ]; then
  echo "poisson"
fi
exit 0
`
	bin := filepath.Join(t.TempDir(), "podman")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake podman: %v", err)
	}
	return bin
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

// TestCreate_CachesBootstrappedImageOnFirstUse: with no cache tag present
// yet, Create must bootstrap the real requested image (paying whatever cost
// that has — the apt-get/network hit this whole cache exists to amortize)
// and then commit the freshly bootstrapped container to the cache tag, so a
// later Create for the same image can skip straight to it.
func TestCreate_CachesBootstrappedImageOnFirstUse(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "calls.log")
	origBin := podmanBin
	podmanBin = fakePodmanScript(t, logPath)
	t.Cleanup(func() { podmanBin = origBin })
	t.Setenv("FAKE_PODMAN_IMAGE_EXISTS", "1") // "no" — cache tag doesn't exist

	d := &podmanDriver{execUser: make(map[string]string)}
	id, err := d.Create(context.Background(), CreateOpts{Image: "ubuntu:26.04", Name: "cache-test-1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	log := readLog(t, logPath)
	wantTag := "localhost/poisson-sandbox-cache:ubuntu-26.04"
	if !strings.Contains(log, "image exists "+wantTag) {
		t.Errorf("did not check the cache tag before creating; log:\n%s", log)
	}
	if !strings.Contains(log, "ubuntu:26.04 sleep infinity") {
		t.Errorf("create did not use the real requested image on a cache miss; log:\n%s", log)
	}
	if !strings.Contains(log, "commit "+id+" "+wantTag) {
		t.Errorf("did not commit the freshly bootstrapped container to the cache tag; log:\n%s", log)
	}
}

// TestCreate_ReusesCachedImageSkipsRecommit: with the cache tag already
// present, Create must build the container FROM the cache tag (not the
// original image) and must not commit again — there's nothing new to cache.
func TestCreate_ReusesCachedImageSkipsRecommit(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "calls.log")
	origBin := podmanBin
	podmanBin = fakePodmanScript(t, logPath)
	t.Cleanup(func() { podmanBin = origBin })
	t.Setenv("FAKE_PODMAN_IMAGE_EXISTS", "0") // "yes" — cache tag already exists

	d := &podmanDriver{execUser: make(map[string]string)}
	if _, err := d.Create(context.Background(), CreateOpts{Image: "ubuntu:26.04", Name: "cache-test-2"}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	log := readLog(t, logPath)
	wantTag := "localhost/poisson-sandbox-cache:ubuntu-26.04"
	if strings.Contains(log, "ubuntu:26.04 sleep infinity") {
		t.Errorf("create used the real image instead of the cache tag; log:\n%s", log)
	}
	if !strings.Contains(log, wantTag+" sleep infinity") {
		t.Errorf("create did not use the cache tag as the container image; log:\n%s", log)
	}
	if strings.Contains(log, "commit ") {
		t.Errorf("re-committed an already-cached image; log:\n%s", log)
	}
}

func TestCacheTagFor(t *testing.T) {
	cases := map[string]string{
		"ubuntu:26.04":                   "localhost/poisson-sandbox-cache:ubuntu-26.04",
		"docker.io/library/ubuntu:26.04": "localhost/poisson-sandbox-cache:docker.io-library-ubuntu-26.04",
	}
	for in, want := range cases {
		if got := cacheTagFor(in); got != want {
			t.Errorf("cacheTagFor(%q) = %q, want %q", in, got, want)
		}
	}
}
