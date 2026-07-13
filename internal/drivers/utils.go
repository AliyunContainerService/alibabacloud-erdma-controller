package drivers

import (
	"fmt"
	"os/exec"
)

const (
	defaultErdmaInstallerVersion = "latest"
)

func getInstallScript(compat bool, installerVersion string) string {
	// Best-effort: move the installer's parent process out of the (ephemeral)
	// container cgroup so a long-running install survives pod teardown.
	//   - cgroup v1 (cpu controller dir present): write the pid to the cpu and
	//     memory `tasks` files, as before.
	//   - cgroup v2: a task may only live in a leaf cgroup, so create a
	//     dedicated leaf and write the pid to its cgroup.procs. Writing the
	//     unified-hierarchy root is avoided.
	// hostExec enters the host cgroup namespace (nsenter -C) so these writes
	// are permitted; every write is still guarded with `|| true` and the block
	// chained with `;` so a failure can never abort the install.
	script := `ERDMA_PPID=$(awk '/PPid:/{print $2}' /proc/self/status); if [ -d /sys/fs/cgroup/cpu/ ]; then echo $ERDMA_PPID > /sys/fs/cgroup/cpu/tasks 2>/dev/null || true; echo $ERDMA_PPID > /sys/fs/cgroup/memory/tasks 2>/dev/null || true; else mkdir -p /sys/fs/cgroup/erdma-installer 2>/dev/null || true; echo $ERDMA_PPID > /sys/fs/cgroup/erdma-installer/cgroup.procs 2>/dev/null || true; fi; cd /tmp && rm -f erdma_installer-1.4.6.tar.gz &&
wget 'http://mirrors.cloud.aliyuncs.com/erdma/erdma_installer-%s.tar.gz' -O erdma_installer.tar.gz && tar -xzvf erdma_installer.tar.gz && cd erdma_installer &&
if command -v yum >/dev/null 2>&1; then yum install -y kernel-devel-$(uname -r) gcc-c++ dkms cmake; elif command -v dnf >/dev/null 2>&1; then dnf install -y kernel-devel-$(uname -r) gcc-c++ dkms cmake; elif command -v apt >/dev/null 2>&1; then apt update && apt install -y debhelper autotools-dev dkms libnl-3-dev libnl-route-3-dev cmake; else echo 'no supported package manager (yum/dnf/apt) found' >&2; exit 1; fi &&
ERDMA_CM_NO_BOUND_IF=1 %s ./install.sh --batch`
	if installerVersion == "" {
		installerVersion = defaultErdmaInstallerVersion
	}
	if compat {
		return fmt.Sprintf(script, installerVersion, "ERDMA_FORCE_MAD_ENABLE=1")
	}
	return fmt.Sprintf(script, installerVersion, "")
}

func containerOSDriverInstall(compat bool) error {
	driverLog.Info("install driver in container os", "compat", compat)
	containerOSScript := `yum install -y kernel-modules-$(uname -r)`
	output, err := exec.Command("/usr/bin/bash", "-c", containerOSScript).CombinedOutput()
	if err != nil {
		return fmt.Errorf("exec error: %v, output: %s", err, string(output))
	}
	return nil
}
