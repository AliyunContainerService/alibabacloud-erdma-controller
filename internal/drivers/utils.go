package drivers

import (
	"fmt"
	"os/exec"
)

func getInstallScript(compat bool) string {
	script := `if [ -d /sys/fs/cgroup/cpu/ ]; then cat /proc/self/status | awk '/PPid:/{print $2}' > /sys/fs/cgroup/cpu/tasks && cat /proc/self/status | awk '/PPid:/{print $2}' > /sys/fs/cgroup/memory/tasks; else 
cat /proc/self/status | awk '/PPid:/{print $2}' > /sys/fs/cgroup/cgroup.procs; fi && cd /tmp && rm -f erdma_installer-1.4.6.tar.gz &&
wget 'http://mirrors.cloud.aliyuncs.com/erdma/erdma_installer-1.4.6.tar.gz' && tar -xzvf erdma_installer-1.4.6.tar.gz && cd erdma_installer && 
(type yum && yum install -y kernel-devel-$(uname -r) gcc-c++ dkms cmake) || (apt update && apt install -y debhelper autotools-dev dkms libnl-3-dev libnl-route-3-dev cmake) &&
ERDMA_CM_NO_BOUND_IF=1 %s ./install.sh --batch`
	if compat {
		return fmt.Sprintf(script, "ERDMA_FORCE_MAD_ENABLE=1")
	}
	return fmt.Sprintf(script, "")
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
