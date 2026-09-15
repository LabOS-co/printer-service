// Package secrets resolves the Print Gateway's secrets from HashiCorp Vault
// when configured, falling back to the environment on any failure.
package secrets

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/LabOS-co/go-packages/encryption"
	"github.com/LabOS-co/go-packages/logs"
	"github.com/LabOS-co/go-packages/secret_store"

	"printgateway/internal/config"
)

// errSecretNotFound marks a definite "the store answered, and the secret
// genuinely isn't there" - the only condition ResolveToken/ResolveLogServer/
// ResolveS3Credentials treat as "fall back to env", as opposed to a
// transport/auth/malformed-response error, which means the store is broken
// rather than merely empty.
var errSecretNotFound = errors.New("secret not found")

// getSecretString resolves a single string value at key within the secret at
// path, unwrapping secret_store's KV v2 response shape: VaultClient.
// GetSecretValue returns a secret's fields nested one level under "data"
// (the sibling "metadata" key, carrying version/created_time info, is not
// part of what this returns) - this is the one place that unwrap happens
// rather than every caller having to know about it. A present "data" key
// whose value is nil (Vault's shape for a soft-deleted secret version) is
// treated as a miss, not a malformed response.
func getSecretString(client secret_store.SecretStoreClient, path, key string) (string, error) {
	raw, err := client.GetSecretValue(path)
	if err != nil {
		return "", err
	}
	if raw == nil {
		return "", fmt.Errorf("path %s: %w", path, errSecretNotFound)
	}

	fields := raw
	if wrapped, hasDataKey := raw["data"]; hasDataKey {
		if wrapped == nil {
			return "", fmt.Errorf("path %s: %w", path, errSecretNotFound)
		}
		f, ok := wrapped.(map[string]any)
		if !ok {
			return "", fmt.Errorf("path %s: unexpected secret store response shape", path)
		}
		fields = f
	}

	value, ok := fields[key]
	if !ok {
		return "", fmt.Errorf("key %q at path %s: %w", key, path, errSecretNotFound)
	}
	str, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("key %q at path %s is not a string", key, path)
	}
	return str, nil
}

// printTokenPath/printTokenKey mirror the path/key the labOS side already reads
// via gSecretManager, so a Vault-backed deployment needs no new convention there.
const (
	printTokenPath = "config/print_gateway"
	printTokenKey  = "auth-token"
)

// logServerPath/logServerKey sit alongside the print token at the same Vault path.
const (
	logServerPath = "config/print_gateway"
	logServerKey  = "log-server"
)

// s3*Path/Key sit alongside the print token and log server at the same Vault path.
const (
	s3AccessKeyPath = "config/print_gateway"
	s3AccessKeyKey  = "s3-access-key"
	s3SecretKeyPath = "config/print_gateway"
	s3SecretKeyKey  = "s3-secret-key"
)

// vaultClient builds the secret_store client shared by every resolver in this
// package. A package var rather than a plain function, so tests can substitute a
// fake secret_store.SecretStoreClient; not safe to swap from concurrently running
// tests (see secrets_test.go).
var vaultClient = defaultVaultClient

func defaultVaultClient(cfg config.Config, logger logs.Logger, meta *logs.LogMetaData) (secret_store.SecretStoreClient, error) {
	password := cfg.SecretStorePassword
	if password != "" {
		// SECRET_STORE_PASSWORD is expected encrypted, matching
		// go-packages/settings.go's getSecretStoreSettings.
		decrypted, err := encryption.Decrypt(password)
		if err != nil {
			return nil, fmt.Errorf("can't decrypt %s: %w", config.SecretStorePasswordEnv, err)
		}
		password = decrypted
	}

	return secret_store.Vault(&secret_store.SecretStoreDetails{
		URL:      cfg.SecretStoreURL,
		Token:    cfg.VaultToken,
		UserName: cfg.SecretStoreUsername,
		Password: password,
	}, logger, meta)
}

// ResolveToken resolves the shared print token (X-Labos-Print-Token). If Vault is
// not configured, it returns cfg.AuthToken; if that is empty too, it errors rather
// than starting up to answer 503 to every request forever.
//
// If Vault is configured, it falls back to cfg.AuthToken on ANY failure (client
// construction, an unreachable server, a malformed response, or a genuinely
// missing secret) rather than only on a definite miss — a misconfigured or down
// Vault must degrade this prototype to env, not take the service down. Every
// fallback is logged (never the token value).
//
// Returns (token, source, nil) on success, where source names which input won
// ("vault", "env", or "env (vault fallback)"). Returns an error only when no
// source produced a usable token.
func ResolveToken(cfg config.Config, logger logs.Logger, meta *logs.LogMetaData) (token, source string, err error) {
	if cfg.SecretStoreURL == "" {
		if strings.TrimSpace(cfg.AuthToken) == "" {
			return "", "", fmt.Errorf("print token unavailable: vault is not configured and %s is not set", config.AuthTokenEnv)
		}
		return cfg.AuthToken, "env", nil
	}

	client, err := vaultClient(cfg, logger, meta)
	if err != nil {
		logger.LogError(fmt.Sprintf("vault client init failed: %v; falling back to %s", err, config.AuthTokenEnv), meta)
		return fallbackToken(cfg)
	}

	path := vaultPath(cfg.LabosEnv, printTokenPath)
	value, err := getSecretString(client, path, printTokenKey)
	if err != nil {
		logger.LogError(fmt.Sprintf("vault read %s (key %s) failed: %v; falling back to %s",
			path, printTokenKey, err, config.AuthTokenEnv), meta)
		return fallbackToken(cfg)
	}

	// A present-but-blank Vault value is a successful read, not an error — must not
	// "resolve" into an empty token that then 503s every request.
	if strings.TrimSpace(value) == "" {
		logger.LogError(fmt.Sprintf("vault %s (key %s) is empty; falling back to %s",
			path, printTokenKey, config.AuthTokenEnv), meta)
		return fallbackToken(cfg)
	}

	// Trimmed on return (unlike cfg.AuthToken, kept untrimmed since it's compared
	// against on every request exactly as an operator set it).
	return strings.TrimSpace(value), "vault", nil
}

// fallbackToken is ResolveToken's env fallback, shared by both failure sites above.
func fallbackToken(cfg config.Config) (string, string, error) {
	if strings.TrimSpace(cfg.AuthToken) == "" {
		return "", "", fmt.Errorf("print token unavailable: vault failed and %s is not set", config.AuthTokenEnv)
	}
	return cfg.AuthToken, "env (vault fallback)", nil
}

// vaultPath prefixes path with labosEnv (<LABOS_ENV>/<path>, matching the
// go-packages/settings convention). An empty labosEnv leaves path unprefixed.
func vaultPath(labosEnv, path string) string {
	labosEnv = strings.Trim(labosEnv, "/")
	if labosEnv == "" {
		return path
	}
	return labosEnv + "/" + path
}

// ResolveLogServer resolves the logstash address (host, port) main.go hands to
// logger.SetLogstashLogger. Never fatal: any failure just leaves the service on
// console-only logging, logged once.
//
// Returns ("", 0, "") when nothing usable was found. source names which input won
// ("vault" or "env") when host is non-empty.
func ResolveLogServer(cfg config.Config, logger logs.Logger, meta *logs.LogMetaData) (host string, port int, source string) {
	if cfg.SecretStoreURL != "" {
		client, err := vaultClient(cfg, logger, meta)
		if err != nil {
			logger.LogError(fmt.Sprintf("vault client init failed: %v; log server falls back to %s",
				err, cfg.Source(config.LogServerEnv)), meta)
		} else {
			path := vaultPath(cfg.LabosEnv, logServerPath)
			value, err := getSecretString(client, path, logServerKey)
			if err != nil {
				// LogInfo, not LogError: unlike the print token, this key is optional and
				// usually just unset — not worth an ERROR line on every startup.
				logger.LogInfo(fmt.Sprintf("vault read %s (key %s) unavailable: %v; log server falls back to %s",
					path, logServerKey, err, cfg.Source(config.LogServerEnv)), meta)
			} else if h, p, perr := parseHostPort(value); perr != nil {
				logger.LogError(fmt.Sprintf("vault %s (key %s) is not a valid host:port: %v; log server falls back to %s",
					path, logServerKey, perr, cfg.Source(config.LogServerEnv)), meta)
			} else {
				return h, p, "vault"
			}
		}
	}

	if cfg.LogServer == "" {
		return "", 0, ""
	}
	h, p, err := parseHostPort(cfg.LogServer)
	if err != nil {
		logger.LogError(fmt.Sprintf("%s: invalid host:port %q: %v; logstash shipping disabled",
			cfg.Source(config.LogServerEnv), cfg.LogServer, err), meta)
		return "", 0, ""
	}
	return h, p, "env"
}

// parseHostPort parses a "host:port" value shared by both the Vault and env forms
// of the log server address.
func parseHostPort(raw string) (host string, port int, err error) {
	h, p, err := net.SplitHostPort(strings.TrimSpace(raw))
	if err != nil {
		return "", 0, err
	}
	if h == "" {
		// SplitHostPort accepts ":514" with host == "" (a plausible "any interface"
		// typo); reject explicitly rather than let the caller's `host != ""` check
		// silently drop it with no log line.
		return "", 0, fmt.Errorf("missing host in address %q", raw)
	}
	n, err := strconv.Atoi(p)
	if err != nil || n <= 0 || n > 65535 {
		return "", 0, fmt.Errorf("invalid port %q", p)
	}
	// Re-bracket an IPv6 literal: logs.SetLogstashLogger rejoins host+port with a
	// plain "%s:%d", and an unbracketed "::1" there becomes "::1:514", which
	// net.Dial rejects (verified live).
	if strings.Contains(h, ":") {
		h = "[" + h + "]"
	}
	return h, n, nil
}

// ResolveS3Credentials resolves the S3/MinIO access key and secret key. Never
// fatal: a missing or broken source just means main.go skips constructing objstore
// and the s3_key/presign endpoints answer 503.
//
// Returns ("", "", "") when neither Vault nor the environment produced both values.
func ResolveS3Credentials(cfg config.Config, logger logs.Logger, meta *logs.LogMetaData) (accessKey, secretKey, source string) {
	if cfg.SecretStoreURL != "" {
		client, err := vaultClient(cfg, logger, meta)
		if err != nil {
			logger.LogError(fmt.Sprintf("vault client init failed: %v; S3 credentials fall back to %s/%s",
				err, cfg.Source(config.S3AccessKeyEnv), cfg.Source(config.S3SecretKeyEnv)), meta)
		} else {
			ak, akErr := getSecretString(client, vaultPath(cfg.LabosEnv, s3AccessKeyPath), s3AccessKeyKey)
			sk, skErr := getSecretString(client, vaultPath(cfg.LabosEnv, s3SecretKeyPath), s3SecretKeyKey)
			switch {
			case akErr != nil:
				logger.LogInfo(fmt.Sprintf("vault read of S3 access key unavailable: %v; S3 credentials fall back to %s/%s",
					akErr, cfg.Source(config.S3AccessKeyEnv), cfg.Source(config.S3SecretKeyEnv)), meta)
			case skErr != nil:
				logger.LogInfo(fmt.Sprintf("vault read of S3 secret key unavailable: %v; S3 credentials fall back to %s/%s",
					skErr, cfg.Source(config.S3AccessKeyEnv), cfg.Source(config.S3SecretKeyEnv)), meta)
			case strings.TrimSpace(ak) == "" || strings.TrimSpace(sk) == "":
				logger.LogError("vault S3 access/secret key is empty; S3 credentials fall back to "+
					cfg.Source(config.S3AccessKeyEnv)+"/"+cfg.Source(config.S3SecretKeyEnv), meta)
			default:
				return strings.TrimSpace(ak), strings.TrimSpace(sk), "vault"
			}
		}
	}

	accessKey = strings.TrimSpace(cfg.S3AccessKey)
	secretKey = strings.TrimSpace(cfg.S3SecretKey)
	if accessKey == "" || secretKey == "" {
		return "", "", ""
	}
	// Reported separately as "file+env" when only one of the two came from the
	// config file, since that mid-migration/mid-rotation state needs to be named
	// rather than collapsed into either single label.
	akFromFile := cfg.Source(config.S3AccessKeyEnv) != config.S3AccessKeyEnv
	skFromFile := cfg.Source(config.S3SecretKeyEnv) != config.S3SecretKeyEnv
	switch {
	case akFromFile && skFromFile:
		source = "file"
	case akFromFile || skFromFile:
		source = "file+env"
	default:
		source = "env"
	}
	return accessKey, secretKey, source
}
