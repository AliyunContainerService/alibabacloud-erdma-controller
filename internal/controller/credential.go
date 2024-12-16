package controller

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/alibabacloud-go/endpoint-util/service"
	"github.com/alibabacloud-go/tea/tea"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/config"
	"github.com/aliyun/credentials-go/credentials"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
)

var credentialLogger = ctrl.Log.WithName("credential")

func getCredential(k8sClient client.Client) (credentials.Credential, error) {
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
	case "ecs_ram_role":
		credentialLogger.Info("using ecs_ram_tole credential")
		return credentials.NewCredential(new(credentials.Config).SetType("ecs_ram_role"))
	case "ram_role_sts":
		credentialLogger.Info("using ram_role_sts credential")
		return getStsCredential(k8sClient), nil
	default:
		return nil, fmt.Errorf("unsupported credential type: %s", credType)
	}
}

func getStsCredential(k8sClient client.Client) credentials.Credential {
	cred := &stsTokenCredential{
		k8sClient:  k8sClient,
		secretNs:   config.GetCredential().StsSecretNS,
		SecretName: config.GetCredential().StsSecretName,
	}
	return cred
}

type stsTokenCredential struct {
	k8sClient            client.Client
	secretNs             string
	SecretName           string
	sessionCredential    *credentials.CredentialModel
	credentialExpiration time.Time
	lastUpdate           time.Time
}

func (s *stsTokenCredential) GetAccessKeyId() (*string, error) {
	cred, err := s.GetCredential()
	if err != nil {
		return nil, err
	}
	return cred.AccessKeyId, nil
}

func (s *stsTokenCredential) GetAccessKeySecret() (*string, error) {
	cred, err := s.GetCredential()
	if err != nil {
		return nil, err
	}
	return cred.AccessKeySecret, nil
}

func (s *stsTokenCredential) GetSecurityToken() (*string, error) {
	cred, err := s.GetCredential()
	if err != nil {
		return nil, err
	}
	return cred.SecurityToken, nil
}

func (s *stsTokenCredential) GetBearerToken() *string {
	return nil
}

func (s *stsTokenCredential) GetType() *string {
	return ptr.To("ram_role_sts")
}

func (s *stsTokenCredential) GetCredential() (*credentials.CredentialModel, error) {
	if s.sessionCredential == nil || s.needUpdateCredential() {
		err := s.updateCredential()
		if err != nil {
			if s.credentialExpiration.After(time.Now()) {
				// credential still valid
			} else {
				return nil, err
			}
		}
	}

	return &credentials.CredentialModel{
		AccessKeyId:     s.sessionCredential.AccessKeyId,
		AccessKeySecret: s.sessionCredential.AccessKeySecret,
		SecurityToken:   s.sessionCredential.SecurityToken,
		Type:            tea.String("ram_role_sts"),
	}, nil
}

type EncryptedCredentialInfo struct {
	AccessKeyID     string `json:"access.key.id"`
	AccessKeySecret string `json:"access.key.secret"`
	SecurityToken   string `json:"security.token"`
	Expiration      string `json:"expiration"`
	Keyring         string `json:"keyring"`
}

func (s *stsTokenCredential) updateCredential() error {
	stsSecret := &corev1.Secret{}
	err := s.k8sClient.Get(context.TODO(), client.ObjectKey{Namespace: s.secretNs, Name: s.SecretName}, stsSecret)
	if err != nil {
		return fmt.Errorf("failed to get sts secret: %s", err)
	}
	var akInfo EncryptedCredentialInfo
	credentialLogger.Info("resolve encrypted credential")
	encodeTokenCfg, ok := stsSecret.Data["addon.token.config"]
	if !ok {
		return fmt.Errorf("sts secret does not contain addon.token.config")
	}

	err = json.Unmarshal(encodeTokenCfg, &akInfo)
	if err != nil {
		return fmt.Errorf("error unmarshal token config: %w", err)
	}
	keyring := []byte(akInfo.Keyring)
	ak, err := decrypt(akInfo.AccessKeyID, keyring)
	if err != nil {
		return fmt.Errorf("failed to decode ak, err: %w", err)
	}
	sk, err := decrypt(akInfo.AccessKeySecret, keyring)
	if err != nil {
		return fmt.Errorf("failed to decode sk, err: %w", err)
	}
	token, err := decrypt(akInfo.SecurityToken, keyring)
	if err != nil {
		return fmt.Errorf("failed to decode token, err: %w", err)
	}
	layout := "2006-01-02T15:04:05Z"
	t, err := time.Parse(layout, akInfo.Expiration)
	if err != nil {
		return fmt.Errorf("failed to parse expiration time, err: %w", err)
	}
	s.credentialExpiration = t
	s.sessionCredential = &credentials.CredentialModel{
		AccessKeyId:     tea.String(string(ak)),
		AccessKeySecret: tea.String(string(sk)),
		SecurityToken:   tea.String(string(token)),
		Type:            tea.String("ram_role_sts"),
	}
	s.lastUpdate = time.Now()
	return nil
}

func pks5UnPadding(origData []byte) []byte {
	length := len(origData)
	unpadding := int(origData[length-1])
	return origData[:(length - unpadding)]
}

func decrypt(s string, keyring []byte) ([]byte, error) {
	cdata, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("failed to decode base64 string, err: %w", err)
	}
	block, err := aes.NewCipher(keyring)
	if err != nil {
		return nil, fmt.Errorf("failed to new cipher, err:%w", err)
	}
	blockSize := block.BlockSize()

	iv := cdata[:blockSize]
	blockMode := cipher.NewCBCDecrypter(block, iv)
	origData := make([]byte, len(cdata)-blockSize)

	blockMode.CryptBlocks(origData, cdata[blockSize:])

	origData = pks5UnPadding(origData)
	return origData, nil
}
func (s *stsTokenCredential) needUpdateCredential() bool {
	if s.sessionCredential == nil {
		return true
	}
	if s.credentialExpiration.Before(time.Now()) {
		return true
	}
	if s.lastUpdate.Before(time.Now().Add(-5 * time.Minute)) {
		return true
	}
	return false
}
