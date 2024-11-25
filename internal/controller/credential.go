package controller

import (
	"fmt"
	"github.com/alibabacloud-go/endpoint-util/service"
	"github.com/alibabacloud-go/tea/tea"

	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/config"
	"github.com/aliyun/credentials-go/credentials"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
)

var credentialLogger = ctrl.Log.WithName("credential")

func getCredential() (credentials.Credential, error) {
	stsEndpoint, err := service.GetEndpointRules(tea.String("sts"), tea.String(config.GetConfig().Region), tea.String("regional"), tea.String("vpc"), nil)
	if err != nil {
		return nil, err
	}
	credType := config.GetCredential().Type
	switch credType {
	case "", "access_key":
		credentialLogger.Info("using access_key credential")
		return credentials.NewCredential(&credentials.Config{
			AccessKeyId:     ptr.To(string(config.GetCredential().AccessKeyID)),
			AccessKeySecret: ptr.To(string(config.GetCredential().AccessKeySecret)),
			Type:            ptr.To("access_key"),
			STSEndpoint:     stsEndpoint,
		})
	case "oidc_role_arn":
		credentialLogger.Info("using oidc_role_arn credential")
		return credentials.NewCredential(new(credentials.Config).SetType("oidc_role_arn").SetSTSEndpoint(*stsEndpoint))
	default:
		return nil, fmt.Errorf("unsupported credential type: %s", credType)
	}
}
