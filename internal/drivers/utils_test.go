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
	for _, tc := range []struct {
		name           string
		config         string
		loaded         bool
		modinfoStatus  int
		modprobeStatus int
		wantProbe      bool
		wantError      bool
	}{
		{name: "Rocky kernel without SMC", config: "# CONFIG_SMC is not set\n", modinfoStatus: 1, modprobeStatus: 7},
		{name: "load supported module", config: "CONFIG_SMC=m\n", wantProbe: true},
		{name: "propagate module load failure", modprobeStatus: 7, wantProbe: true, wantError: true},
		{name: "missing module is not unsupported", config: "CONFIG_SMC=m\n", modinfoStatus: 1, modprobeStatus: 7, wantProbe: true, wantError: true},
		{name: "unknown kernel config", modinfoStatus: 1, modprobeStatus: 7, wantProbe: true, wantError: true},
		{name: "already loaded or built in", loaded: true, modinfoStatus: 1, modprobeStatus: 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			config := filepath.Join(dir, "config")
			module := filepath.Join(dir, "smc")
			if tc.config != "" {
				if err := os.WriteFile(config, []byte(tc.config), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.loaded {
				if err := os.Mkdir(module, 0700); err != nil {
					t.Fatal(err)
				}
			}
			// Run the real decision script without loading modules on the test host.
			script := strings.NewReplacer(
				"/sys/module/smc", `"$SMC_TEST_MODULE"`,
				"/boot/config-$(uname -r)", `"$SMC_TEST_CONFIG"`,
				"/proc/config.gz", `"$SMC_TEST_PROC_CONFIG"`,
			).Replace(smcSetupScript)
			functions := fmt.Sprintf("modinfo() { return %d; }\nmodprobe() { echo MODPROBE_CALLED; return %d; }\n", tc.modinfoStatus, tc.modprobeStatus)
			cmd := exec.Command("bash", "-c", "set -e\n"+functions+script)
			cmd.Env = append(os.Environ(), "SMC_TEST_MODULE="+module, "SMC_TEST_CONFIG="+config, "SMC_TEST_PROC_CONFIG="+filepath.Join(dir, "missing.gz"))
			output, err := cmd.CombinedOutput()
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v, wantError=%v, output=%s", err, tc.wantError, output)
			}
			if got := strings.Contains(string(output), "MODPROBE_CALLED"); got != tc.wantProbe {
				t.Fatalf("modprobe called=%v, want=%v, output=%s", got, tc.wantProbe, output)
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
