// Package secrets resolves the Print Gateway's secrets from HashiCorp Vault
// when configured, falling back to the environment on any failure.
package secrets

import (
	"fmt"
	"net"
	"strconv"
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

// logServerPath/logServerKey sit alongside the print token at the same
// Vault path — both are this service's own config, not a shared convention
// with another labOS component the way the print token is.
const (
	logServerPath = "config/print_gateway"
	logServerKey  = "log-server"
)

// vaultClient builds the secret_store client shared by every resolver in
// this package (ResolveToken, ResolveLogServer, and any future one), so the
// userpass-decrypt-then-authenticate logic lives in exactly one place
// instead of drifting between call sites. Each caller gets its own fresh
// login — a startup-only cost, not a per-request one — rather than this
// package threading a single client through main.go, which would couple
// independent resolvers to a shared calling convention.
func vaultClient(cfg config.Config, logger logs.Logger, meta *logs.LogMetaData) (secret_store.SecretStoreClient, error) {
	password := cfg.SecretStorePassword
	if password != "" {
		// SECRET_STORE_PASSWORD is expected encrypted, matching
		// go-packages/settings.go's getSecretStoreSettings — a plaintext
		// value here would authenticate with ciphertext and fail.
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

	client, err := vaultClient(cfg, logger, meta)
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

// ResolveLogServer resolves the logstash address (host, port) main.go
// hands to logger.SetLogstashLogger. Unlike ResolveToken, this is never
// fatal: logstash shipping is an optional capability, and every failure —
// Vault not configured, Vault tried and failed, a malformed value from
// either source, or nothing configured anywhere — just means the service
// stays on console-only logging, logged once so the degradation is visible.
//
// Returns ("", 0, "") when nothing usable was found. source names which
// input won ("vault" or "env") when host is non-empty.
func ResolveLogServer(cfg config.Config, logger logs.Logger, meta *logs.LogMetaData) (host string, port int, source string) {
	if cfg.SecretStoreURL != "" {
		client, err := vaultClient(cfg, logger, meta)
		if err != nil {
			logger.LogError(fmt.Sprintf("vault client init failed: %v; log server falls back to %s",
				err, config.LogServerEnv), meta)
		} else {
			path := vaultPath(cfg.LabosEnv, logServerPath)
			value, err := secret_store.GetSecretString(client, path, logServerKey)
			if err != nil {
				// LogInfo, not LogError: unlike the print token, log-server is
				// optional, and the overwhelmingly common reason this fails is
				// simply that nobody set the key — not a fault worth an ERROR
				// line on every single startup. logs.Logger has no LogWarn.
				logger.LogInfo(fmt.Sprintf("vault read %s (key %s) unavailable: %v; log server falls back to %s",
					path, logServerKey, err, config.LogServerEnv), meta)
			} else if h, p, perr := parseHostPort(value); perr != nil {
				logger.LogError(fmt.Sprintf("vault %s (key %s) is not a valid host:port: %v; log server falls back to %s",
					path, logServerKey, perr, config.LogServerEnv), meta)
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
			config.LogServerEnv, cfg.LogServer, err), meta)
		return "", 0, ""
	}
	return h, p, "env"
}

// parseHostPort parses a "host:port" value shared by both the Vault and env
// forms of the log server address. net.SplitHostPort rejects a missing
// port outright rather than silently defaulting it — logs.LogsSettings'
// own defaultPort (514) is a convenience for callers that never set Port at
// all, not a license to accept an address with no port here.
//
// Trimmed for the same reason ResolveToken trims: a trailing newline or
// space from a unit file's Environment= would otherwise land in the port
// ("514\n" fails Atoi) or the host and disable shipping over whitespace.
//
// An empty host is rejected explicitly: SplitHostPort(":514") succeeds with
// host == "" — a plausible "any interface" typo — which the caller's
// `host != ""` check then drops with no log line at all, and (from the
// Vault branch) without ever trying the env fallback. Every other bad value
// here is loud; this one has to be too.
func parseHostPort(raw string) (host string, port int, err error) {
	h, p, err := net.SplitHostPort(strings.TrimSpace(raw))
	if err != nil {
		return "", 0, err
	}
	if h == "" {
		return "", 0, fmt.Errorf("missing host in address %q", raw)
	}
	n, err := strconv.Atoi(p)
	if err != nil || n <= 0 || n > 65535 {
		return "", 0, fmt.Errorf("invalid port %q", p)
	}
	// SplitHostPort strips the brackets off an IPv6 literal, but
	// logs.SetLogstashLogger rejoins the address with a plain
	// fmt.Sprintf("%s:%d", ...) (logstash_logger.go) — "::1" + ":514"
	// becomes "::1:514", which net.Dial rejects with "too many colons in
	// address" (verified live). Put the brackets back so the value we
	// return is the one that function can actually dial.
	if strings.Contains(h, ":") {
		h = "[" + h + "]"
	}
	return h, n, nil
}
