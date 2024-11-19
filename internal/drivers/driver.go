package drivers

import (
	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/types"
	ctrl "sigs.k8s.io/controller-runtime"
)

const (
	defaultGPUDriver = "ofed"
	defaultDriver    = "default"
)

var (
	driverLog = ctrl.Log.WithName("Driver")
)

type ERdmaDriver interface {
	Install() error
	ProbeDevice(eri *types.ERI) (*types.ERdmaDeviceInfo, error)
	Name() string
}

var (
	drivers = make(map[string]ERdmaDriver)
)

func Register(name string, driver ERdmaDriver) {
	drivers[name] = driver
}
func GetDriver(name string) ERdmaDriver {
	if name != "" {
		return drivers[name]
	}
	// pod injected nvidia-smi, prefer ofed driver
	if _, err := hostExec("which nvidia-smi"); err == nil {
		if driver, ok := drivers[defaultGPUDriver]; ok {
			return driver
		}
	} else {
		if driver, ok := drivers[defaultDriver]; ok {
			return driver
		}
	}
	for _, driver := range drivers {
		return driver
	}
	panic("no erdma driver found")
}
