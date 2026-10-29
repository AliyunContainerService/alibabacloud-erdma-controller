package drivers

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSMCSetupKernelSupport(t *testing.T) {
	for _, config := range []string{"# CONFIG_SMC is not set", "CONFIG_SMC=m"} {
		t.Run(config, func(t *testing.T) {
			dir := t.TempDir()
			configPath := filepath.Join(dir, "config")
			if err := os.WriteFile(configPath, []byte(config+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			// Run the decision script without touching the host's kernel/modules.
			script := strings.NewReplacer(
				"/sys/module/smc", fmt.Sprintf("%q", filepath.Join(dir, "smc")),
				"/boot/config-$(uname -r)", fmt.Sprintf("%q", configPath),
				"/proc/config.gz", fmt.Sprintf("%q", filepath.Join(dir, "missing.gz")),
			).Replace(smcSetupScript)
			output, err := exec.Command("bash", "-c", "set -e\nmodinfo() { return 1; }\nmodprobe() { return 7; }\n"+script).CombinedOutput()
			if config == "# CONFIG_SMC is not set" {
				if err != nil {
					t.Fatalf("unsupported kernel must skip SMC: %v, output=%s", err, output)
				}
			} else if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 7 {
				t.Fatalf("must preserve modprobe failure: %v, output=%s", err, output)
			}
		})
	}
}

// getDriverSetupScript encodes the per-profile install/activation decisions.
// We assert those decisions directly on the generated script rather than
// simulating the shell with mocks.
func TestDriverSetupScriptProfileLogic(t *testing.T) {
	for _, tc := range []struct {
		profile    string
		compatMode string
		wantSMC    bool
	}{
		{profile: defaultDriver, compatMode: "N", wantSMC: true},
		{profile: compatDriver, compatMode: "Y", wantSMC: true},
		{profile: defaultGPUDriver, compatMode: "Y", wantSMC: false},
	} {
		t.Run(tc.profile, func(t *testing.T) {
			script := getDriverSetupScript(tc.profile, "latest", false)
			if !strings.Contains(script, "modprobe erdma compat_mode="+tc.compatMode) {
				t.Errorf("profile %s: expected compat_mode=%s", tc.profile, tc.compatMode)
			}
			if got := strings.Contains(script, "modprobe smc\n"); got != tc.wantSMC {
				t.Errorf("profile %s: SMC load=%v, want %v", tc.profile, got, tc.wantSMC)
			}
		})
	}
}

// Lifsea cannot run the official installer (its OS forbids nsenter and installing
// the full stack) and cannot migrate the installer into a host cgroup. It only
// pulls the matching kernel-modules package, reusing an already-present module.
func TestLifseaSetupSkipsInstallerAndCgroupMigration(t *testing.T) {
	script := getDriverSetupScript(defaultDriver, "latest", true)
	if strings.Contains(script, "wget ") {
		t.Error("lifsea must not download the official installer")
	}
	if !strings.Contains(script, "yum install -y kernel-modules-$(uname -r)") {
		t.Error("lifsea must install the kernel-modules package")
	}
	if strings.Contains(script, "cgroup.procs") || strings.Contains(script, "/sys/fs/cgroup/") {
		t.Error("lifsea must not perform host-cgroup migration")
	}
}
