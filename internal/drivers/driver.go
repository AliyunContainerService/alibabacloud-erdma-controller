package drivers

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/types"
	ctrl "sigs.k8s.io/controller-runtime"
)

const (
	defaultGPUDriver = "ofed"
	defaultDriver    = "default"
	compatDriver     = "compat"
)

var (
	driverLog = ctrl.Log.WithName("Driver")
)

// ErrERdmaLinkNotFound identifies a delayed RDMA link.
var ErrERdmaLinkNotFound = errors.New("cannot found rdma link")

type ERdmaDriver interface {
	Install() error
	ProbeDevice(eri *types.ERI) (*types.ERdmaDeviceInfo, error)
	Name() string
	SetERdmaInstallerVersion(version string)
}

var (
	drivers = make(map[string]ERdmaDriver)
)

func Register(name string, driver ERdmaDriver) {
	drivers[name] = driver
}

func GetDriver(name, erdmaInstallerVersion string) ERdmaDriver {
	// The version becomes part of a shell command and a package URL.
	if erdmaInstallerVersion != "" && !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`).MatchString(erdmaInstallerVersion) {
		panic(fmt.Sprintf("invalid erdma installer version %q", erdmaInstallerVersion))
	}
	if name != "" {
		driver := drivers[name]
		if driver == nil {
			panic(fmt.Sprintf("no erdma driver named %q found", name))
		}
		driver.SetERdmaInstallerVersion(erdmaInstallerVersion)
		return driver
	}

	// pod injected nvidia-smi, prefer ofed driver
	if _, err := hostExec("which nvidia-smi"); err == nil {
		if driver, ok := drivers[defaultGPUDriver]; ok {
			driver.SetERdmaInstallerVersion(erdmaInstallerVersion)
			return driver
		}
	} else {
		if driver, ok := drivers[defaultDriver]; ok {
			driver.SetERdmaInstallerVersion(erdmaInstallerVersion)
			return driver
		}
	}
	for _, driver := range drivers {
		driver.SetERdmaInstallerVersion(erdmaInstallerVersion)
		return driver
	}
	panic("no erdma driver found")
}
