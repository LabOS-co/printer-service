// Package secrets resolves the Print Gateway's secrets from HashiCorp Vault
// when configured, falling back to the environment on any failure.
package secrets

import (
	"fmt"
	"strings"

	"github.com/LabOS-co/go-packages/encryption"
	"github.com/LabOS-co/go-packages/logs"
	"github.com/LabOS-co/go-packages/secret_store"

	"printgateway/internal/config"
)

// printTokenPath/printTokenKey mirror the path/key the labOS side already
// reads via gSecretManager (see README.md's Access control section), so a
// Vault-backed deployment needs no new convention on that side.
const (
	printTokenPath = "config/print_gateway"
	printTokenKey  = "auth-token"
)

// ResolveToken resolves the shared print token (X-Labos-Print-Token). If
// cfg.SecretStoreURL is empty, Vault is not configured at all: this returns
// cfg.AuthToken (what config.Load already read from PRINT_GATEWAY_TOKEN)
// unchanged — with one exception since F2: if that is empty too, no source
// produced a token, and this is an error rather than a "successful" empty
// resolution, so the process refuses to start instead of coming up and
// answering 503 to every request forever.
//
// If Vault is configured, ResolveToken falls back to cfg.AuthToken on ANY
// failure — client construction (bad or missing credentials), an
// unreachable server, a malformed response, or the secret genuinely not
// being there — not only on secret_store.ErrSecretNotFound. That is
// deliberately broader than secret_store.GetSecretStringWithFallback, which
// only falls back on a definite miss and returns any other error as-is (by
// design: it must not mask a real outage with a possibly-stale value). Here
// the product decision is the opposite: a misconfigured or down Vault must
// degrade this prototype to env, not take the whole service down. Every
// fallback is logged loudly (never the token value) so the degradation is
// visible in the log even though it is not fatal.
//
// The first read this performs also serves as Vault's "is it actually
// reachable" startup probe: Vault(...) with a token does no I/O by itself
// (see secret_store.Vault), so construction succeeding proves nothing on its
// own.
//
// Returns (token, source, nil) on success, where source names which input
// won ("vault", "env", or "env (vault fallback)") for the caller to log.
// Returns an error whenever no source produced a token — whether Vault
// wasn't configured at all, or Vault was tried and failed — and the
// environment was also empty, naming what was tried.
func ResolveToken(cfg config.Config, logger logs.Logger, meta *logs.LogMetaData) (token, source string, err error) {
	if cfg.SecretStoreURL == "" {
		// Trimmed for the emptiness test only — a whitespace-only
		// PRINT_GATEWAY_TOKEN (e.g. a trailing newline from a unit file's
		// Environment=) must not "resolve" into a token nobody set on
		// purpose. The untrimmed value is still what's returned and compared
		// against on every request, so a legitimate token with meaningful
		// leading/trailing space (unlikely, but not this function's call) is
		// preserved exactly.
		if strings.TrimSpace(cfg.AuthToken) == "" {
			return "", "", fmt.Errorf("print token unavailable: vault is not configured and %s is not set", config.AuthTokenEnv)
		}
		return cfg.AuthToken, "env", nil
	}

	password := cfg.SecretStorePassword
	if password != "" {
		// SECRET_STORE_PASSWORD is expected encrypted, matching
		// go-packages/settings.go's getSecretStoreSettings — a plaintext
		// value here would authenticate with ciphertext and fail, which
		// fail-open then silently converts into "resolved from env".
		decrypted, decErr := encryption.Decrypt(password)
		if decErr != nil {
			logger.LogError(fmt.Sprintf("vault client init failed: can't decrypt %s: %v; falling back to %s",
				config.SecretStorePasswordEnv, decErr, config.AuthTokenEnv), meta)
			return fallbackToken(cfg)
		}
		password = decrypted
	}

	client, err := secret_store.Vault(&secret_store.SecretStoreDetails{
		URL:      cfg.SecretStoreURL,
		Token:    cfg.VaultToken,
		UserName: cfg.SecretStoreUsername,
		Password: password,
	}, logger, meta)
	if err != nil {
		logger.LogError(fmt.Sprintf("vault client init failed: %v; falling back to %s", err, config.AuthTokenEnv), meta)
		return fallbackToken(cfg)
	}

	path := vaultPath(cfg.LabosEnv, printTokenPath)
	value, err := secret_store.GetSecretString(client, path, printTokenKey)
	if err != nil {
		logger.LogError(fmt.Sprintf("vault read %s (key %s) failed: %v; falling back to %s",
			path, printTokenKey, err, config.AuthTokenEnv), meta)
		return fallbackToken(cfg)
	}

	// GetSecretString has no emptiness check — a present-but-blank value
	// (an unset key, a botched `vault kv put`, a rotation that cleared it)
	// reports as success. Treating that as a "resolved" token would set
	// cfg.AuthToken to "", which requireToken then answers 503 to every
	// request — a total outage disguised as a successful startup log line.
	// Trimming catches whitespace-only values the same way.
	if strings.TrimSpace(value) == "" {
		logger.LogError(fmt.Sprintf("vault %s (key %s) is empty; falling back to %s",
			path, printTokenKey, config.AuthTokenEnv), meta)
		return fallbackToken(cfg)
	}

	return value, "vault", nil
}

// fallbackToken is ResolveToken's env fallback, shared by both failure
// sites above. Refuses to resolve only when the environment is empty too —
// the one case where neither configured source produced a usable token.
func fallbackToken(cfg config.Config) (string, string, error) {
	if strings.TrimSpace(cfg.AuthToken) == "" {
		return "", "", fmt.Errorf("print token unavailable: vault failed and %s is not set", config.AuthTokenEnv)
	}
	return cfg.AuthToken, "env (vault fallback)", nil
}

// vaultPath prefixes path with labosEnv, matching the go-packages/settings
// convention (<LABOS_ENV>/<path> relative to the KV mount). An empty
// labosEnv leaves path unprefixed. Trims stray slashes off labosEnv first —
// LABOS_ENV=staging/ would otherwise double the separator into
// "staging//config/print_gateway", a path Vault reports as a plain miss
// with nothing distinguishing it from a real one.
func vaultPath(labosEnv, path string) string {
	labosEnv = strings.Trim(labosEnv, "/")
	if labosEnv == "" {
		return path
	}
	return labosEnv + "/" + path
}
