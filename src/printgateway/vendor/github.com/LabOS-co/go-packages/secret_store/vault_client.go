package secret_store

import (
	"fmt"

	"github.com/LabOS-co/go-packages/logs"

	vault "github.com/hashicorp/vault/api"
)

type VaultClient struct {
	*vault.Client
	Logger   logs.Logger
	MetaData *logs.LogMetaData
}

const (
	loginPath                    = "auth/userpass/login/%s"
	password                     = "password"
	secretStoreRootPath          = "kv-v2/data"
	secretStoreMetaDataPath      = "kv-v2/metadata"
	secretStoreDbCredentialsPath = "database/creds"
)

func Vault(details *SecretStoreDetails, logger logs.Logger, logMetaData *logs.LogMetaData) (SecretStoreClient, error) {
	if details == nil || details.URL == "" {
		return nil, fmt.Errorf("missing secret store details")
	}
	if details.Token == "" && (details.UserName == "" || details.Password == "") {
		return nil, fmt.Errorf("missing secret store credentials")
	}

	logger.LogInfo(fmt.Sprintf("Initializing Vault client (%s)", details.URL), logMetaData)
	config := vault.DefaultConfig()
	config.Address = details.URL
	client, err := vault.NewClient(config)
	if err != nil {
		return nil, err
	}

	if details.Token == "" {
		loginData := map[string]any{
			password: details.Password,
		}

		path := fmt.Sprintf(loginPath, details.UserName)
		resp, err := client.Logical().Write(path, loginData)
		if err != nil {
			return nil, err
		}

		if resp.Auth == nil || resp.Auth.ClientToken == "" {
			return nil, fmt.Errorf("authentication failed")
		}

		// Override the token with the one obtained from Vault
		details.Token = resp.Auth.ClientToken
	}

	client.SetToken(details.Token)

	return &VaultClient{
		Client:   client,
		Logger:   logger,
		MetaData: logMetaData,
	}, nil
}

func (v *VaultClient) GetSecret(path string) (*SecretStoreSecret, error) {
	secretStorePath := fmt.Sprintf("%s/%s", secretStoreRootPath, path)
	v.logInfo(fmt.Sprintf("Getting secret from path %s", secretStorePath))
	kvSecret, err := v.Logical().Read(secretStorePath)

	if err != nil {
		return nil, err
	}
	if kvSecret == nil {
		return nil, nil
	}

	return getSecretData(kvSecret), nil
}

func (v *VaultClient) GetSecretValue(path string) (map[string]any, error) {
	kvSecret, err := v.GetSecret(path)
	if err != nil {
		return nil, err
	}
	if kvSecret == nil {
		return nil, nil
	}

	return kvSecret.Data, nil
}

func (v *VaultClient) GetSecretsList(path string) (*SecretStoreSecret, error) {
	secretStorePath := fmt.Sprintf("%s/%s", secretStoreMetaDataPath, path)
	v.logInfo(fmt.Sprintf("Getting Pages from path %s", secretStorePath))

	secret, err := v.Logical().List(secretStorePath)
	if err != nil {
		return nil, err
	}
	if secret == nil {
		return nil, fmt.Errorf("path %s not found", secretStorePath)
	}

	return getSecretData(secret), nil
}

func (v *VaultClient) GetSecretsListValue(path string) (map[string]any, error) {
	secret, err := v.GetSecretsList(path)
	if err != nil {
		return nil, err
	}
	if secret == nil {
		return nil, nil
	}

	return secret.Data, nil
}

func (v *VaultClient) RenewLease(leaseId string, leaseDuration int) (*SecretStoreSecret, error) {
	secret, err := v.Client.Sys().Renew(leaseId, leaseDuration)
	if err != nil {
		return nil, fmt.Errorf("can't renew lease: %s", err)
	}

	return getSecretData(secret), nil
}

func (v *VaultClient) GetDatabaseCredentials(role string) (*DatabaseCredentials, error) {
	secretStorePath := fmt.Sprintf("%s/%s", secretStoreDbCredentialsPath, role)
	v.logInfo(fmt.Sprintf("Getting secret from path %s", secretStorePath))
	kvSecret, err := v.Logical().Read(secretStorePath)

	if err != nil {
		return nil, err
	}
	if kvSecret == nil {
		return nil, nil
	}

	return getDatabaseCredentials(kvSecret.Data, kvSecret.LeaseID, kvSecret.LeaseDuration), nil
}

func (v *VaultClient) RenewDatabaseCredentialsLease(leaseId string, leaseDuration int) (*DatabaseCredentials, error) {
	secret, err := v.RenewLease(leaseId, leaseDuration)
	if err != nil {
		return nil, fmt.Errorf("can't renew lease: %s", err)
	}

	return getDatabaseCredentials(secret.Data, secret.LeaseID, secret.LeaseDuration), nil
}

func (v *VaultClient) logInfo(message string) {
	v.Logger.LogInfo(message, v.MetaData)
}

func getSecretData(secret *vault.Secret) *SecretStoreSecret {
	secretData := SecretStoreSecret{Data: secret.Data}
	secretData.LeaseID = secret.LeaseID
	secretData.LeaseDuration = secret.LeaseDuration

	return &secretData
}

func getDatabaseCredentials(secretData map[string]interface{}, leaseID string, leaseDuration int) *DatabaseCredentials {
	credentials := DatabaseCredentials{
		Username: secretData["username"].(string),
		Password: secretData["password"].(string),
	}
	credentials.LeaseID = leaseID
	credentials.LeaseDuration = leaseDuration

	return &credentials
}
