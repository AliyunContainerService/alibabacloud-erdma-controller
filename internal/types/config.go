package types

type Config struct {
	Region                    string `json:"region"`
	ManageNonOwnedERIs        bool   `json:"manageNonOwnedENIs"`
	ControllerNamespace       string `json:"controllerNamespace"`
	ControllerName            string `json:"controllerName"`
	ClusterDomain             string `json:"clusterDomain"`
	CertDir                   string `json:"certDir"`
	EnableDevicePlugin        *bool  `json:"enableDevicePlugin"`
	EnableWebhook             *bool  `json:"enableWebhook"`
	EnableInitContainerInject *bool  `json:"enableInitContainerInject"`
}

type Sensitive string
type Credentials struct {
	Type            string    `json:"type"`
	AccessKeyID     Sensitive `json:"accessKeyID"`
	AccessKeySecret Sensitive `json:"accessKeySecret"`
}

func (c Sensitive) String() string {
	return "*******"
}
