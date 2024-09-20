package config

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"k8s.io/utils/ptr"

	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/types"
	ctrl "sigs.k8s.io/controller-runtime"
)

const (
	defaultConfigPath     = "/etc/erdma-controller/config.json"
	defaultCredentialPath = "/etc/erdma-controller-credential/credential.json"
	regionIDAddr          = "http://100.100.100.200/latest/meta-data/region-id"
)

var configLog = ctrl.Log.WithName("config")

var (
	cfg        *types.Config
	credential *types.Credentials
)

func getRegion() (string, error) {
	url := regionIDAddr
	resp, err := http.Get(url)
	if err != nil {
		return "", fmt.Errorf("error get url: %s from metaserver. %w", url, err)
	}
	//nolint:errcheck
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("error get url: %s from metaserver, code: %v, %v", url, resp.StatusCode, "Not Found")
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return "", fmt.Errorf("error get url: %s from metaserver, code: %v", url, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	result := strings.Split(string(body), "\n")
	trimResult := strings.Trim(result[0], "/")
	return trimResult, nil
}

func InitConfig(configPath, credentialPath string) error {
	var err error
	cfg, err = parseConfig(configPath)
	if err != nil {
		return err
	}
	credential, err = parseCredential(credentialPath)
	if err != nil {
		return err
	}
	configLog.Info("init config", "config", cfg)
	return nil
}

func GetConfig() *types.Config {
	return cfg
}

func GetCredential() *types.Credentials {
	return credential
}

func parseConfig(configPath string) (*types.Config, error) {
	if configPath == "" {
		configPath = defaultConfigPath
	}
	conf, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %v, %v", configPath, err)
	}
	erdmaConfig := &types.Config{}
	err = json.Unmarshal(conf, erdmaConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal config file: %v, %v", configPath, err)
	}
	if erdmaConfig.EnableWebhook == nil {
		erdmaConfig.EnableWebhook = ptr.To(true)
	}
	if erdmaConfig.EnableDevicePlugin == nil {
		erdmaConfig.EnableDevicePlugin = ptr.To(true)
	}
	if erdmaConfig.Region == "" {
		configLog.Info("region is not set, try to get region from metaserver")
		erdmaConfig.Region, err = getRegion()
		if err != nil {
			return nil, fmt.Errorf("failed to get region from metaserver: %v", err)
		}
		configLog.Info("region id from metaserver", "region", erdmaConfig.Region)
	}
	return erdmaConfig, nil
}

func parseCredential(credentialPath string) (*types.Credentials, error) {
	if credentialPath == "" {
		credentialPath = defaultCredentialPath
	}
	cred, err := os.ReadFile(credentialPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &types.Credentials{}, nil
		}
		return nil, fmt.Errorf("failed to read credential file: %v, %v", credentialPath, err)
	}
	credential := &types.Credentials{}
	err = json.Unmarshal(cred, credential)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal credential file: %v, %v", credentialPath, err)
	}
	return credential, nil
}
