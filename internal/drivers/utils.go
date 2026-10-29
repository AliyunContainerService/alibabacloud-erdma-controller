package drivers

import (
	"fmt"
	"strings"
)

const (
	// DefaultErdmaInstallerVersion selects the package for first-time installation.
	// It is not a desired version reconciled against already installed drivers.
	DefaultErdmaInstallerVersion = "latest"

	// Preserve the existing ordinary-OS reuse check; Lifsea only uses modinfo.
	installedDriverCheck = `test -f /bin/eadm && modinfo erdma >/dev/null 2>&1`
)

func getInstallScript(compat bool, installerVersion string) string {
	script := `set -e
workdir=$(mktemp -d /tmp/erdma-installer.XXXXXX)
trap 'rm -rf -- "$workdir"' EXIT
cd "$workdir"
wget --timeout=30 --tries=3 %s -O erdma_installer.tar.gz
tar -xzvf erdma_installer.tar.gz
cd erdma_installer
kernel_version=$(uname -r)
kernel_build=/lib/modules/$kernel_version/build
kernel_package=""
prepare_rpm_os() {
    if [ ! -r /etc/os-release ]; then return; fi
    host_os_id=$(. /etc/os-release; echo "$ID")
    case "$host_os_id" in
        rocky)
            # Rocky's base repositories do not provide DKMS. Use the official
            # EPEL release package from Extras; keep package signature checks.
            if ! rpm -q dkms >/dev/null 2>&1 && ! rpm -q epel-release >/dev/null 2>&1; then
                $pkg_manager install -y epel-release
            fi
            ;;
    esac
}
if command -v yum >/dev/null 2>&1; then
    pkg_manager=yum
    prepare_rpm_os
    if [ ! -d "$kernel_build" ]; then kernel_package="kernel-devel-$kernel_version"; fi
    yum install -y $kernel_package gcc-c++ dkms cmake
elif command -v dnf >/dev/null 2>&1; then
    pkg_manager=dnf
    prepare_rpm_os
    if [ ! -d "$kernel_build" ]; then kernel_package="kernel-devel-$kernel_version"; fi
    dnf install -y $kernel_package gcc-c++ dkms cmake
elif command -v apt >/dev/null 2>&1; then
    pkg_manager=apt
    if [ ! -d "$kernel_build" ]; then kernel_package="linux-headers-$kernel_version"; fi
    apt update
    apt install -y $kernel_package debhelper autotools-dev zlib1g-dev dkms libnl-3-dev libnl-route-3-dev cmake
else
    echo 'no supported package manager (yum/dnf/apt) found' >&2
    exit 1
fi
# New installers determine additional dependencies from the OS, architecture,
# and whether their components need a source build. Older packages retain the
# baseline dependency installation above.
check_supported_os() {
    if echo "$1" | grep -Eiq '(^|[^[:alnum:]_])unsupported([^[:alnum:]_]|$)'; then
        echo 'official eRDMA installer reported an unsupported OS; refusing to continue' >&2
        exit 1
    fi
}
run_check() {
    check_status=0
    check_output=$(bash ./install.sh --check-only 2>&1) || check_status=$?
    echo "$check_output"
    check_supported_os "$check_output"
}
if bash ./install.sh --help | grep -q -- '--check-only'; then
    run_check
    packages=$(echo "$check_output" | sed -n 's/.*to be installed: *//p')
    if [ -n "$packages" ]; then
        $pkg_manager install -y $packages
        run_check
    fi
    if [ "$check_status" -ne 0 ]; then exit "$check_status"; fi
fi
install_status=0
ERDMA_CM_NO_BOUND_IF=1 %s ./install.sh --batch > installer-output.log 2>&1 || install_status=$?
cat installer-output.log
check_supported_os "$(cat installer-output.log)"
if [ "$install_status" -ne 0 ]; then exit "$install_status"; fi
if ! { %s; }; then
    echo 'eRDMA installation incomplete: check the installer logs' >&2
    exit 1
fi`
	if installerVersion == "" {
		installerVersion = DefaultErdmaInstallerVersion
	}
	installerURL := "'" + strings.ReplaceAll("http://mirrors.cloud.aliyuncs.com/erdma/erdma_installer-"+installerVersion+".tar.gz", "'", "'\"'\"'") + "'"
	if compat {
		return fmt.Sprintf(script, installerURL, "ERDMA_FORCE_MAD_ENABLE=1", installedDriverCheck)
	}
	return fmt.Sprintf(script, installerURL, "", installedDriverCheck)
}

func erdmaModuleScript(compatMode string) string {
	return fmt.Sprintf(`set -e
cm=/sys/module/erdma/parameters/compat_mode
if [ -d /sys/module/erdma ] && { [ ! -f "$cm" ] || [ "%[1]s" != "$(cat "$cm")" ]; }; then
    rmmod erdma || { echo 'cannot unload erdma; release active users before activating the installed module or switching mode' >&2; exit 1; }
fi
modprobe erdma compat_mode=%[1]s
if [ ! -f "$cm" ] || [ "$(cat "$cm")" != "%[1]s" ]; then
    echo 'erdma compat_mode verification failed (expected %[1]s)' >&2
    exit 1
fi
`, compatMode)
}

// Best-effort migration of the installation subshell out of the Pod cgroup.
// Failure must not prevent installation, matching the original behavior.
const detachInstallerScript = `installer_pid=$BASHPID
trap "" HUP
if [ -d /sys/fs/cgroup/cpu/ ]; then
    echo "$installer_pid" > /sys/fs/cgroup/cpu/tasks 2>/dev/null || true
    echo "$installer_pid" > /sys/fs/cgroup/memory/tasks 2>/dev/null || true
else
    mkdir -p /sys/fs/cgroup/erdma-installer 2>/dev/null || true
    echo "$installer_pid" > /sys/fs/cgroup/erdma-installer/cgroup.procs 2>/dev/null || true
fi
`

// Only an explicit kernel configuration can prove SMC was not compiled in.
const smcSetupScript = `stage='SMC configuration'
if [ -d /sys/module/smc ]; then
    echo 'erdma-controller: SMC already available'
elif modinfo smc >/dev/null 2>&1; then
    modprobe smc
elif grep -qx '# CONFIG_SMC is not set' /boot/config-$(uname -r) 2>/dev/null ||
     zgrep -qx '# CONFIG_SMC is not set' /proc/config.gz 2>/dev/null; then
    echo 'erdma-controller: kernel built without SMC; skip SMC-R configuration'
else
    # Missing module metadata is not proof that the kernel lacks SMC.
    # Preserve the modprobe error for broken or incomplete installations.
    modprobe smc
fi
`

// getDriverSetupScript keeps our install and module configuration operations
// under one lock. External installers do not participate in this protocol.
func getDriverSetupScript(profile, version string, containerOS bool) string {
	mode := "N"
	if profile != defaultDriver {
		mode = "Y"
	}
	check := installedDriverCheck
	install := getInstallScript(mode == "Y", version)
	detach := detachInstallerScript
	lock := "/run/lock/erdma-controller-install.lock"
	if containerOS {
		// Lifsea runs with the host userspace but its OS forbids nsenter, so the
		// installer cannot be migrated into a host cgroup; skip that step. Lifsea
		// also cannot run the official installer, so reuse detection is just the
		// module presence and installation only pulls the matching kernel-modules
		// package (no eadm/DKMS). If the module is already present, it is reused.
		detach = ""
		check = "modinfo erdma >/dev/null 2>&1"
		install = "set -e\nyum install -y kernel-modules-$(uname -r)\nmodinfo erdma >/dev/null"
		// /lib/modules is shared with the host, unlike the agent's /run.
		lock = "/lib/modules/.erdma-controller-install.lock"
	}
	script := fmt.Sprintf(`set -e
stage='setup lock'
trap 'rc=$?; if [ "$rc" -ne 0 ] && [ "$BASH_SUBSHELL" = 0 ]; then if [ -n "${install_log:-}" ]; then cat "$install_log" >&2 || :; fi; echo "erdma-controller: $stage failed (exit $rc)" >&2; fi; exit "$rc"' EXIT
mkdir -p "$(dirname '%s')"
exec 9>'%s'
flock -w 1800 9 || { echo 'timed out waiting for erdma-controller setup lock' >&2; exit 1; }
if ( %s ); then
    echo 'erdma-controller: reuse installed eRDMA driver; skip package installation'
else
    stage='driver installation'
    echo 'Installing missing eRDMA driver'
    # Use a regular host file for ALL installation output. A vanished Pod must
    # not give package managers SIGPIPE through the agent's output pipe.
    install_log="%s.log"
    umask 077
    (
%s
%s
    ) > "$install_log" 2>&1
    cat "$install_log"
    echo 'erdma-controller: eRDMA driver installation completed'
fi
stage='module configuration'
%s
echo 'erdma-controller: module configuration verified'
`, lock, lock, check, lock, detach, install, erdmaModuleScript(mode))
	if mode == "Y" {
		script += `if command -v nvidia-smi >/dev/null 2>&1; then
    if modinfo nvidia_peermem >/dev/null 2>&1; then
        if modprobe nvidia_peermem; then
            echo 'erdma-controller: nvidia-peermem loaded for GPUDirect RDMA'
        else
            echo 'erdma-controller: nvidia-peermem load failed (ignored)' >&2
        fi
    else
        echo 'erdma-controller: nvidia-peermem module not present, skip loading'
    fi
fi
`
	}
	if profile != defaultGPUDriver {
		script += smcSetupScript
	}
	return script
}
