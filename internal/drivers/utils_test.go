package drivers

import (
	"strings"
	"testing"
)

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

// Lifsea must not write to its immutable OS or change OS-managed modules.
func TestLifseaSetupChecksWithoutInstalling(t *testing.T) {
	for _, profile := range []string{defaultDriver, compatDriver, defaultGPUDriver} {
		t.Run(profile, func(t *testing.T) {
			script := getDriverSetupScript(profile, "latest", true)
			if !strings.Contains(script, "modinfo erdma") {
				t.Fatal("Lifsea must check the OS-managed driver")
			}
			for _, forbidden := range []string{"wget", "yum", "flock", "modprobe", "rmmod", "/lib/modules/", "/sys/fs/cgroup/"} {
				if strings.Contains(script, forbidden) {
					t.Errorf("Lifsea setup must not contain %q", forbidden)
				}
			}
		})
	}
}
