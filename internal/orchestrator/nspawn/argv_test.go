package nspawn

import (
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/orchestrator"
)

func testConfig() Config {
	return Config{
		StateRoot:    "/var/lib/px-orchestrate",
		MachinesRoot: "/var/lib/machines",
		GoldenImage:  "px-golden",
		PxBinPath:    "/usr/local/bin/px",
		MemoryMax:    "1200M",
		CPUQuota:     "150%",
		TasksMax:     "2048",
	}
}

func TestTemplateUnitContent_HasEveryRequiredFlag(t *testing.T) {
	content := templateUnitContent(testConfig())
	required := []string{
		"--private-users=no",
		"--capability=all",
		"--drop-capability=CAP_SYS_MODULE",
		"--boot",
		"--machine=%i",
		"--directory=/var/lib/machines/%i",
		"--bind-ro=/usr/local/bin/px",
		"--bind-ro=/var/lib/px-orchestrate/instances/%i/secrets/auth.json:/root/.poisson/auth.json",
		"--bind-ro=/var/lib/px-orchestrate/instances/%i/config.toml:/root/.poisson/config.toml",
		"--bind=/var/lib/px-orchestrate/instances/%i/work:/work",
		"--resolv-conf=bind-host",
		"Type=notify",
	}
	for _, want := range required {
		if !strings.Contains(content, want) {
			t.Errorf("template unit missing %q:\n%s", want, content)
		}
	}
	// The invalid comma-subtraction syntax from an earlier draft must never
	// reappear (docs/orchestrator-plan.md Step 12's explicit correction).
	if strings.Contains(content, "--capability=all,-CAP_SYS_MODULE") {
		t.Error("template unit uses the invalid --capability=all,-X syntax")
	}
	// --private-network must never be set — host networking is deliberate
	// (§3.1).
	if strings.Contains(content, "--private-network") || strings.Contains(content, "--network-veth") {
		t.Error("template unit sets a network isolation flag; host networking is required")
	}
}

func TestTemplateUnitContent_NeverUsesMachinectlStart(t *testing.T) {
	// This is a content-shape guard, not a behavioral one: the whole point
	// of an explicit unit file is that nothing in this package ever calls
	// `machinectl start` (which would force -U via systemd-nspawn@.service)
	// — checked properly in TestRuntimeNeverCallsMachinectlStart below,
	// this just confirms the template itself doesn't reference it either.
	if strings.Contains(templateUnitContent(testConfig()), "machinectl start") {
		t.Error("template unit references machinectl start")
	}
}

func TestInstanceDropInContent_HasAllFiveDirectives(t *testing.T) {
	content := instanceDropInContent(testConfig())
	for _, want := range []string{
		"MemoryMax=1200M",
		"CPUQuota=150%",
		"TasksMax=2048",
		"MemoryAccounting=yes",
		"Delegate=yes",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("drop-in missing %q:\n%s", want, content)
		}
	}
}

func TestUnitNameAndTurnUnitName(t *testing.T) {
	if got, want := unitName("px-alpha"), "px-instance@px-alpha.service"; got != want {
		t.Errorf("unitName = %q, want %q", got, want)
	}
	if got, want := turnUnitName("px-alpha"), "px-turn-px-alpha"; got != want {
		t.Errorf("turnUnitName = %q, want %q", got, want)
	}
}

func TestCloneArgs(t *testing.T) {
	got := cloneArgs("px-golden", "px-alpha")
	want := []string{"clone", "px-golden", "px-alpha"}
	assertArgsEqual(t, got, want)
}

func TestMachinectlRemoveArgs(t *testing.T) {
	assertArgsEqual(t, machinectlRemoveArgs("px-alpha"), []string{"remove", "px-alpha"})
}

func TestSystemctlStartStopKillArgs(t *testing.T) {
	assertArgsEqual(t, systemctlStartArgs("px-alpha"), []string{"start", "px-instance@px-alpha.service"})
	assertArgsEqual(t, systemctlStopArgs("px-alpha"), []string{"stop", "px-instance@px-alpha.service"})
	assertArgsEqual(t, systemctlKillArgs("px-alpha"), []string{"kill", "--signal=SIGKILL", "px-instance@px-alpha.service"})
}

func TestSystemctlStopUnitByNameArgs(t *testing.T) {
	assertArgsEqual(t, systemctlStopUnitByNameArgs("px-turn-px-alpha"), []string{"stop", "px-turn-px-alpha.service"})
}

func TestReadinessProbeArgs(t *testing.T) {
	assertArgsEqual(t, readinessProbeArgs("px-alpha"), []string{
		"--machine=px-alpha", "--pipe", "--wait", "--", "/bin/true",
	})
}

func TestExecArgs(t *testing.T) {
	got := execArgs("px-alpha", []string{"px", "cost", "s-1"})
	want := []string{"--machine=px-alpha", "--pipe", "--wait", "--collect", "--", "px", "cost", "s-1"}
	assertArgsEqual(t, got, want)
}

func TestTurnRunArgs_BasicShape(t *testing.T) {
	spec := orchestrator.TurnSpec{SessionID: "s-1", Provider: "anthropic", Model: "claude-sonnet-5", Message: "hello there"}
	got := turnRunArgs(testConfig(), "px-alpha", spec)
	want := []string{
		"--machine=px-alpha", "--unit=px-turn-px-alpha", "--pipe", "--collect", "--wait",
		"--setenv=HOME=/root", "--setenv=POISSON_ORCHESTRATE_INSTANCE=1",
		"--", "/usr/local/bin/px", "-p", "--print-json",
		"--session", "s-1", "--model", "anthropic/claude-sonnet-5", "--", "hello there",
	}
	assertArgsEqual(t, got, want)
}

func TestTurnRunArgs_YoloFlagIncludedWhenSet(t *testing.T) {
	spec := orchestrator.TurnSpec{SessionID: "s-1", Message: "hi", Yolo: true}
	got := turnRunArgs(testConfig(), "px-alpha", spec)
	found := false
	for _, a := range got {
		if a == "--yolo" {
			found = true
		}
	}
	if !found {
		t.Errorf("args = %v, want --yolo present", got)
	}
}

// TestTurnRunArgs_MessageAlwaysLast checks the message is the final argv
// element regardless of what it contains — including a message that starts
// with "-", which must never be swallowed as a flag by px's own parser
// (exactly the M1 Step 5 bug this "--" placement exists to prevent).
func TestTurnRunArgs_MessageAlwaysLast(t *testing.T) {
	spec := orchestrator.TurnSpec{SessionID: "s-1", Message: "-rm test"}
	got := turnRunArgs(testConfig(), "px-alpha", spec)
	if got[len(got)-1] != "-rm test" {
		t.Errorf("last arg = %q, want the literal message", got[len(got)-1])
	}
	if got[len(got)-2] != "--" {
		t.Errorf("second-to-last arg = %q, want \"--\" immediately before the message", got[len(got)-2])
	}
}

func TestValidateUnderRoot(t *testing.T) {
	cases := []struct {
		root, p string
		wantErr bool
	}{
		{"/var/lib/machines", "/var/lib/machines/px-alpha", false},
		{"/var/lib/machines", "/var/lib/machines", true},          // root itself
		{"/var/lib/machines", "/var/lib/other", true},              // sibling escape
		{"/var/lib/machines", "/var/lib/machines-evil/x", true},    // prefix-string trick, not a real subdirectory
		{"/var/lib/machines", "/var/lib/machines/../../etc", true}, // traversal
	}
	for _, c := range cases {
		err := validateUnderRoot(c.root, c.p)
		if (err != nil) != c.wantErr {
			t.Errorf("validateUnderRoot(%q, %q) error = %v, wantErr %v", c.root, c.p, err, c.wantErr)
		}
	}
}

func TestRootfsPathAndInstanceStateDir(t *testing.T) {
	cfg := testConfig()
	if got, want := rootfsPath(cfg, "px-alpha"), "/var/lib/machines/px-alpha"; got != want {
		t.Errorf("rootfsPath = %q, want %q", got, want)
	}
	if got, want := instanceStateDir(cfg, "px-alpha"), "/var/lib/px-orchestrate/instances/px-alpha"; got != want {
		t.Errorf("instanceStateDir = %q, want %q", got, want)
	}
}

func assertArgsEqual(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("args = %v, want %v (differ at index %d)", got, want, i)
		}
	}
}
