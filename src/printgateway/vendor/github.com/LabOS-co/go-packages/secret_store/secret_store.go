package secret_store

type SecretStoreDetails struct {
	URL      string
	UserName string
	Password string
	Token    string
}

type LeaseInfo struct {
	LeaseID       string
	LeaseDuration int
}

type SecretStoreSecret struct {
	LeaseInfo
	Data map[string]any
}

type DatabaseCredentials struct {
	LeaseInfo
	Username string
	Password string
}

type SecretStoreClient interface {
	GetSecret(string) (*SecretStoreSecret, error)
	GetSecretValue(string) (map[string]any, error)
	RenewLease(string, int) (*SecretStoreSecret, error)
	GetSecretsList(string) (*SecretStoreSecret, error)
	GetSecretsListValue(string) (map[string]any, error)
	GetDatabaseCredentials(string) (*DatabaseCredentials, error)
	RenewDatabaseCredentialsLease(string, int) (*DatabaseCredentials, error)
}
