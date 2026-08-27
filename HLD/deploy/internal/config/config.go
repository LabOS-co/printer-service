// Package config loads the Print Gateway's runtime configuration.
package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultAddr is used when no address is given on the command line.
	// Loopback-only: the server is reachable only from the machine it runs
	// on unless an address is passed deliberately.
	DefaultAddr = "127.0.0.1:8090"

	// AuthTokenEnv is the environment variable carrying the shared secret
	// compared against the X-Labos-Print-Token header on every request.
	AuthTokenEnv = "PRINT_GATEWAY_TOKEN"

	// ServiceName identifies this service in logs.LogMetaData.
	ServiceName = "printgateway"
)

// Server timeouts and limits (P0-6). A zero-value *http.Server has none of
// these, which is what let a slow or silent client hold a connection open
// forever with nothing to notice or reclaim it. Each has an env var so an
// operator can tune it without a rebuild; a malformed, non-positive, or
// mutually inconsistent value is a startup error rather than a
// silently-ignored override (see Load and validate).
const (
	DefaultReadHeaderTimeout = 10 * time.Second
	DefaultReadTimeout       = 5 * time.Minute // uploads are large; the one value where too tight breaks real clients

	// DefaultWriteTimeout must dominate DefaultReadTimeout, and by enough to
	// cover the work the handler does after the body is in.
	//
	// net/http does NOT start the write deadline when the response starts. In
	// conn.readRequest (net/http/server.go, Go 1.25) the deadline is armed in
	// a defer that fires as soon as the *headers* are parsed:
	//
	//	if d := c.server.WriteTimeout; d > 0 {
	//		defer func() { c.rwc.SetWriteDeadline(time.Now().Add(d)) }()
	//	}
	//
	// so WriteTimeout is the budget for reading the body, spooling it to
	// disk, downloading file_url/s3_key, running lp, AND writing the
	// response — not just the last of those. At the 1m this originally
	// shipped with, any request slower than a minute was read successfully
	// and submitted to CUPS, and then failed on the response write: the
	// caller saw a reset connection, retried, and the document printed
	// twice.
	//
	// validate enforces WriteTimeout > ReadTimeout + max(FetchTimeout,
	// S3Timeout) + SubmitTimeout — the full chain a JSON/file_url/s3_key
	// request can spend before the response is written — not just
	// WriteTimeout > ReadTimeout; 6m stopped covering that once
	// FetchTimeout/SubmitTimeout became real (5m + 60s + 30s = 6.5m), which
	// is exactly the duplicate-print failure mode described above, just
	// with the fetch/s3+submit time standing in for "any request slower
	// than a minute". max, not sum, of FetchTimeout/S3Timeout: a single
	// request only ever exercises one of file_url/s3_key, so charging both
	// would tighten this budget for every deployment the moment S3Timeout
	// existed, whether or not object storage is even configured.
	DefaultWriteTimeout = 8 * time.Minute

	DefaultIdleTimeout    = 60 * time.Second
	DefaultMaxHeaderBytes = 64 << 10 // 64 KiB

	// DefaultShutdownGrace must exceed max(FetchTimeout,S3Timeout)+SubmitTimeout
	// (validate's other budget check) — 60s+30s at these defaults, so 2m
	// leaves comfortable headroom. validate takes the max of Fetch/S3 rather
	// than their sum specifically so adding S3Timeout didn't need to raise
	// this default (see validate's comment).
	DefaultShutdownGrace = 2 * time.Minute

	ReadHeaderTimeoutEnv = "PRINT_GATEWAY_READ_HEADER_TIMEOUT"
	ReadTimeoutEnv       = "PRINT_GATEWAY_READ_TIMEOUT"
	WriteTimeoutEnv      = "PRINT_GATEWAY_WRITE_TIMEOUT"
	IdleTimeoutEnv       = "PRINT_GATEWAY_IDLE_TIMEOUT"
	MaxHeaderBytesEnv    = "PRINT_GATEWAY_MAX_HEADER_BYTES"
	ShutdownGraceEnv     = "PRINT_GATEWAY_SHUTDOWN_GRACE"
)

// DefaultSubmitTimeout bounds how long a single `lp` invocation may run
// (P0-1). Without it, a wedged CUPS queue hangs the handler goroutine
// forever: defer cleanup() never runs, leaking the goroutine, the lp
// process, the spooled temp file, and the client connection, permanently,
// per request. printgw.Service applies this as a context.WithTimeout around
// the Submit call, and cups.LPSubmitter's exec.CommandContext is what turns
// that expiry into the child process actually being killed.
const DefaultSubmitTimeout = 30 * time.Second

const SubmitTimeoutEnv = "PRINT_GATEWAY_SUBMIT_TIMEOUT"

// Fetch (file_url download) settings — SSRF defense, HLD §11.3 (P0-4).
// FetchTimeout is now wired to real cancellation (printgw.Service.fetch), so
// the ShutdownGrace-vs-budget assertion deferred since DefaultSubmitTimeout
// was added is enforced in validate below.
const (
	DefaultFetchTimeout = 60 * time.Second
	FetchTimeoutEnv     = "PRINT_GATEWAY_FETCH_TIMEOUT"

	// DefaultFetchMaxBytes bounds a downloaded file_url response, independent
	// of any general request-body limit: this is disk written from a
	// caller-influenced remote host, before printgw's own logic ever sees it.
	DefaultFetchMaxBytes int64 = 64 << 20 // 64 MiB
	FetchMaxBytesEnv           = "PRINT_GATEWAY_FETCH_MAX_BYTES"

	// AllowPrivateTargetsEnv lifts the loopback/private/link-local block on
	// file_url. false in any deployment reachable by an untrusted caller;
	// exists at all only because fetch's own tests need to dial
	// httptest.Server. Not a strategy interface — a bool the project's own
	// "design patterns are earned" rule says is the right amount of
	// abstraction for one production value and one test value.
	AllowPrivateTargetsEnv = "PRINT_GATEWAY_ALLOW_PRIVATE_TARGETS"

	// FetchAllowedHostsEnv is the optional host-suffix allowlist — HLD
	// §11.3's "pre-approved sources". Empty (the default) means any public
	// host is fetchable; the loopback/private/link-local block above still
	// applies regardless. Comma-separated, e.g. "s3.example.com,cdn.example.com".
	FetchAllowedHostsEnv = "PRINT_GATEWAY_FETCH_ALLOWED_HOSTS"
)

// S3/MinIO object storage settings, all optional: S3Endpoint == "" means
// object storage is not configured at all, and objstore is never
// constructed (main.go). Unlike the print token, this is never a startup
// failure — multipart upload remains the primary intake path per the HLD's
// own constraint (not every Windows caller has an S3 SDK), so a missing or
// broken S3 config just means the s3_key intake and /files/presign answer
// 503, not that the service refuses to start.
const (
	S3EndpointEnv = "PRINT_GATEWAY_S3_ENDPOINT"
	S3BucketEnv   = "PRINT_GATEWAY_S3_BUCKET"
	S3RegionEnv   = "PRINT_GATEWAY_S3_REGION"

	// S3InsecureEnv disables TLS to the S3/MinIO endpoint — see
	// cloud_storage.CloudStorageSettings.Insecure. false (secure) unless a
	// caller opts in, matching that package's own default.
	S3InsecureEnv = "PRINT_GATEWAY_S3_INSECURE"

	// S3AccessKeyEnv/S3SecretKeyEnv are the env-fallback credential source —
	// secrets.ResolveS3Credentials prefers Vault first, the same
	// Vault-then-env pattern as ResolveToken/ResolveLogServer.
	S3AccessKeyEnv = "PRINT_GATEWAY_S3_ACCESS_KEY"
	S3SecretKeyEnv = "PRINT_GATEWAY_S3_SECRET_KEY"

	DefaultS3Timeout = 60 * time.Second
	S3TimeoutEnv     = "PRINT_GATEWAY_S3_TIMEOUT"

	// DefaultS3MaxBytes bounds an s3_key download the same way
	// DefaultFetchMaxBytes bounds a file_url download (P0-4's sibling risk:
	// an authenticated caller naming a huge key would otherwise spool an
	// unbounded amount of disk).
	DefaultS3MaxBytes int64 = 64 << 20 // 64 MiB
	S3MaxBytesEnv           = "PRINT_GATEWAY_S3_MAX_BYTES"

	// DefaultPresignTTL is both the default and the cap for a
	// /files/presign expiry: a caller-requested ttl longer than this is
	// silently clamped down to it, never rejected outright — a client
	// asking for "as long as possible" is not a caller error.
	DefaultPresignTTL = 15 * time.Minute
	PresignTTLEnv     = "PRINT_GATEWAY_PRESIGN_TTL"
)

// Vault/secret_store connection details, all optional. An empty
// SecretStoreURL means Vault is not configured at all — secrets.ResolveToken
// then resolves purely from the environment, unchanged from before Vault
// support existed. Naming follows the team's existing convention (see
// go-packages/settings) so a Nomad job spec needs no new vocabulary.
const (
	// VaultAddrEnv is Nomad's own injected address variable, read as the
	// base value. SecretStoreURLEnv overrides it when set — matching
	// go-packages/settings.go's getSecretStoreSettings precedence exactly,
	// so a standard Nomad job spec (which sets VAULT_ADDR and nothing else)
	// engages Vault here the same way it does for every other labOS Go
	// service, instead of silently looking "unconfigured".
	VaultAddrEnv      = "VAULT_ADDR"
	SecretStoreURLEnv = "SECRET_STORE_URL"

	VaultTokenEnv          = "VAULT_TOKEN"
	SecretStoreUsernameEnv = "SECRET_STORE_USERNAME"
	// SecretStorePasswordEnv, like go-packages/settings, is expected to hold
	// an encryption.Encrypt-ed value, not plaintext — secrets.ResolveToken
	// decrypts it before use.
	SecretStorePasswordEnv = "SECRET_STORE_PASSWORD"

	// LabosEnvEnv names the path prefix under the Vault mount, e.g.
	// "production" turns the print token's path into
	// "production/config/print_gateway". Empty means no prefix.
	LabosEnvEnv = "LABOS_ENV"
)

const (
	// LogServerEnv is the env-fallback address (host:port) for shipping logs
	// to logstash — secrets.ResolveLogServer prefers Vault, matching
	// ResolveToken's pattern, but logstash is optional: an empty result from
	// both sources just means console-only logging, not a startup failure.
	LogServerEnv = "LOG_SERVER"

	// LogLevelEnv selects the logrus level name (e.g. "debug", "info",
	// "warn", "error"). Not validated here — that would pull logrus into a
	// package that is otherwise stdlib-only; logger.SetLogLevel in main.go
	// is where an invalid value is caught and logged.
	LogLevelEnv     = "PRINT_GATEWAY_LOG_LEVEL"
	DefaultLogLevel = "info"
)

// Config holds the service's runtime configuration.
type Config struct {
	Addr string

	// AuthToken starts as whatever PRINT_GATEWAY_TOKEN holds (possibly
	// empty). main.go overwrites it with secrets.ResolveToken's result before
	// constructing the server — see that function for the Vault-then-env
	// precedence and its deliberate fall-back-on-any-error policy.
	AuthToken string

	// Vault/secret_store connection details. SecretStoreURL == "" means
	// Vault is not configured; the other three fields are then meaningless.
	// Populated from VAULT_ADDR, overridden by SECRET_STORE_URL if set (see
	// VaultAddrEnv).
	SecretStoreURL      string
	VaultToken          string
	SecretStoreUsername string
	SecretStorePassword string
	LabosEnv            string

	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	MaxHeaderBytes    int

	// ShutdownGrace bounds how long Shutdown waits for in-flight requests
	// to finish before main forces an exit.
	ShutdownGrace time.Duration

	// SubmitTimeout bounds a single lp invocation (P0-1).
	SubmitTimeout time.Duration

	// FetchTimeout bounds a single file_url download (P0-4).
	FetchTimeout time.Duration
	// FetchMaxBytes bounds a downloaded file_url response's size.
	FetchMaxBytes int64
	// AllowPrivateTargets lifts the loopback/private/link-local block on
	// file_url. Must stay false in any deployment reachable by an untrusted
	// caller — see AllowPrivateTargetsEnv.
	AllowPrivateTargets bool
	// FetchAllowedHosts is the optional host-suffix allowlist; empty means
	// any public host is fetchable. See FetchAllowedHostsEnv.
	FetchAllowedHosts []string

	// S3Endpoint == "" means object storage is not configured; see the const
	// block above for why that is never a startup failure.
	S3Endpoint string
	S3Bucket   string
	S3Region   string
	S3Insecure bool
	S3Timeout  time.Duration
	S3MaxBytes int64

	// S3AccessKey/S3SecretKey are the raw env-fallback values (see
	// S3AccessKeyEnv/S3SecretKeyEnv) — secrets.ResolveS3Credentials prefers
	// a Vault-resolved pair when Vault is configured, the same pattern as
	// AuthToken/LogServer above.
	S3AccessKey string
	S3SecretKey string

	// PresignTTL is both the default and the cap for /files/presign.
	PresignTTL time.Duration

	// LogServer is the raw, unparsed "host:port" env fallback for logstash
	// shipping (see LogServerEnv). secrets.ResolveLogServer parses it and
	// prefers a Vault-resolved value when Vault is configured.
	LogServer string

	// LogLevel names the logrus level main.go asks the logger for.
	LogLevel string
}

// Load builds Config from argv (an address override, matching the original
// main()'s os.Args[1] convention) and the environment. It fails on a
// present-but-malformed override rather than silently keeping the default,
// naming the offending variable.
func Load(args []string, getenv func(string) string) (Config, error) {
	addr := DefaultAddr
	if len(args) > 1 {
		addr = args[1]
	}

	secretStoreURL := getenv(VaultAddrEnv)
	if v := getenv(SecretStoreURLEnv); v != "" {
		secretStoreURL = v
	}

	logLevel := getenv(LogLevelEnv)
	if logLevel == "" {
		logLevel = DefaultLogLevel
	}

	fetchAllowedHosts, err := splitHostList(getenv(FetchAllowedHostsEnv))
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		Addr:      addr,
		AuthToken: getenv(AuthTokenEnv),

		SecretStoreURL:      secretStoreURL,
		VaultToken:          getenv(VaultTokenEnv),
		SecretStoreUsername: getenv(SecretStoreUsernameEnv),
		SecretStorePassword: getenv(SecretStorePasswordEnv),
		LabosEnv:            getenv(LabosEnvEnv),

		ReadHeaderTimeout: DefaultReadHeaderTimeout,
		ReadTimeout:       DefaultReadTimeout,
		WriteTimeout:      DefaultWriteTimeout,
		IdleTimeout:       DefaultIdleTimeout,
		MaxHeaderBytes:    DefaultMaxHeaderBytes,
		ShutdownGrace:     DefaultShutdownGrace,
		SubmitTimeout:     DefaultSubmitTimeout,

		FetchTimeout:        DefaultFetchTimeout,
		FetchMaxBytes:       DefaultFetchMaxBytes,
		AllowPrivateTargets: false,
		FetchAllowedHosts:   fetchAllowedHosts,

		S3Endpoint:  getenv(S3EndpointEnv),
		S3Bucket:    getenv(S3BucketEnv),
		S3Region:    getenv(S3RegionEnv),
		S3Timeout:   DefaultS3Timeout,
		S3MaxBytes:  DefaultS3MaxBytes,
		S3AccessKey: getenv(S3AccessKeyEnv),
		S3SecretKey: getenv(S3SecretKeyEnv),
		PresignTTL:  DefaultPresignTTL,

		LogServer: getenv(LogServerEnv),
		LogLevel:  logLevel,
	}

	// Table rather than one if-block per value: the repeated form made the
	// env-var/field pairing a copy-paste field, where writing ReadTimeoutEnv
	// into &cfg.WriteTimeout would compile, vet clean, and be invisible in
	// review. Here each pairing appears exactly once, on one line.
	for _, d := range []struct {
		name string
		dst  *time.Duration
	}{
		{ReadHeaderTimeoutEnv, &cfg.ReadHeaderTimeout},
		{ReadTimeoutEnv, &cfg.ReadTimeout},
		{WriteTimeoutEnv, &cfg.WriteTimeout},
		{IdleTimeoutEnv, &cfg.IdleTimeout},
		{ShutdownGraceEnv, &cfg.ShutdownGrace},
		{SubmitTimeoutEnv, &cfg.SubmitTimeout},
		{FetchTimeoutEnv, &cfg.FetchTimeout},
		{S3TimeoutEnv, &cfg.S3Timeout},
		{PresignTTLEnv, &cfg.PresignTTL},
	} {
		v, err := overrideDuration(getenv, d.name, *d.dst)
		if err != nil {
			return Config{}, err
		}
		*d.dst = v
	}

	n, err := overrideBytes(getenv, MaxHeaderBytesEnv, cfg.MaxHeaderBytes)
	if err != nil {
		return Config{}, err
	}
	cfg.MaxHeaderBytes = n

	fn, err := overrideBytes64(getenv, FetchMaxBytesEnv, cfg.FetchMaxBytes)
	if err != nil {
		return Config{}, err
	}
	cfg.FetchMaxBytes = fn

	allowPrivate, err := overrideBool(getenv, AllowPrivateTargetsEnv, cfg.AllowPrivateTargets)
	if err != nil {
		return Config{}, err
	}
	cfg.AllowPrivateTargets = allowPrivate

	s3Insecure, err := overrideBool(getenv, S3InsecureEnv, cfg.S3Insecure)
	if err != nil {
		return Config{}, err
	}
	cfg.S3Insecure = s3Insecure

	s3MaxBytes, err := overrideBytes64(getenv, S3MaxBytesEnv, cfg.S3MaxBytes)
	if err != nil {
		return Config{}, err
	}
	cfg.S3MaxBytes = s3MaxBytes

	if err := validate(cfg); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// validate enforces the relationships *between* values. Each one is
// individually plausible and only the combination is wrong, which is
// exactly the class of mistake nothing downstream reports: net/http simply
// applies whichever deadline expires first, leaving an operator to work out
// from a reset connection why a request that was clearly inside the read
// budget died anyway.
func validate(cfg Config) error {
	// A header deadline that outlives the whole-request deadline can never
	// be the one that fires, so setting it is a no-op the operator will
	// believe took effect.
	if cfg.ReadHeaderTimeout > cfg.ReadTimeout {
		return fmt.Errorf("%s (%s) must not exceed %s (%s)",
			ReadHeaderTimeoutEnv, cfg.ReadHeaderTimeout, ReadTimeoutEnv, cfg.ReadTimeout)
	}

	// See DefaultWriteTimeout: the write deadline is armed at header-parse
	// time, so it has to cover the body read, any file_url/s3_key download,
	// and lp submission, not just the response write. If it does not, a
	// slow request is accepted, fetched, and printed, and then fails on the
	// response write — the caller sees failure for a job that succeeded,
	// and retries it, printing the document twice. A single request only
	// ever exercises ONE of file_url/s3_key (print_handler.go rejects a
	// request naming both), so the budget takes their max, not their sum:
	// summing would charge every deployment for S3Timeout even when
	// PRINT_GATEWAY_S3_ENDPOINT is unset (S3Timeout still has a default),
	// which could turn a WriteTimeout/ShutdownGrace pinned before object
	// storage existed into a startup failure on upgrade alone — the
	// opposite of "S3 is additive and never fatal".
	fetchOrS3 := max(cfg.FetchTimeout, cfg.S3Timeout)
	if writeBudget := cfg.ReadTimeout + fetchOrS3 + cfg.SubmitTimeout; cfg.WriteTimeout <= writeBudget {
		return fmt.Errorf("%s (%s) must exceed %s+max(%s,%s)+%s (%s): the write deadline is armed when request headers are parsed, so it must cover reading the body, downloading file_url/s3_key, and running lp, as well as sending the response",
			WriteTimeoutEnv, cfg.WriteTimeout, ReadTimeoutEnv, FetchTimeoutEnv, S3TimeoutEnv, SubmitTimeoutEnv, writeBudget)
	}

	// Deferred since DefaultSubmitTimeout was added: FetchTimeout is now
	// wired to real cancellation (printgw.Service.fetch), so a request that
	// blocks for the full fetch-then-submit budget must still fit inside
	// the shutdown grace period, or a SIGTERM during that request truncates
	// the print it exists to let finish. Same max-not-sum reasoning as
	// writeBudget above applies now that s3_key downloads are real too.
	if budget := fetchOrS3 + cfg.SubmitTimeout; cfg.ShutdownGrace <= budget {
		return fmt.Errorf("%s (%s) must exceed max(%s,%s)+%s (%s): a request already using the full fetch/s3+submit budget must still fit inside the shutdown grace period",
			ShutdownGraceEnv, cfg.ShutdownGrace, FetchTimeoutEnv, S3TimeoutEnv, SubmitTimeoutEnv, budget)
	}

	return nil
}

func overrideDuration(getenv func(string) string, name string, def time.Duration) (time.Duration, error) {
	raw := getenv(name)
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid duration %q: %w", name, raw, err)
	}
	// A non-positive duration is not a tuning choice. net/http guards every
	// timeout with `if d > 0`, so "0" or "-5s" — a plausible typo — does not
	// mean "very short", it means *no timeout at all*, silently restoring the
	// exact exposure these values exist to close. Reject it the same way a
	// non-positive byte size is rejected, rather than inheriting net/http's
	// semantics as an undocumented escape hatch.
	if d <= 0 {
		return 0, fmt.Errorf("%s: %q must be positive; net/http reads a non-positive timeout as no timeout at all", name, raw)
	}
	return d, nil
}

func overrideBytes(getenv func(string) string, name string, def int) (int, error) {
	raw := getenv(name)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s: invalid byte size %q, want a positive integer number of bytes", name, raw)
	}
	return n, nil
}

// overrideBytes64 is overrideBytes for a field too large for a plain int on
// a 32-bit build (FetchMaxBytes) — same validation, same error shape.
func overrideBytes64(getenv func(string) string, name string, def int64) (int64, error) {
	raw := getenv(name)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s: invalid byte size %q, want a positive integer number of bytes", name, raw)
	}
	return n, nil
}

func overrideBool(getenv func(string) string, name string, def bool) (bool, error) {
	raw := getenv(name)
	if raw == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s: invalid boolean %q: %w", name, raw, err)
	}
	return b, nil
}

// splitHostList parses the comma-separated FetchAllowedHostsEnv value.
// Empty entries (from "a,,b" or leading/trailing commas) are dropped rather
// than rejected — a stray comma should not be a startup failure for a
// setting whose empty value ("no allowlist") is itself a valid, meaningful
// choice. A malformed entry, though, fails startup by the same rule every
// other override in this file follows: fetch.hostAllowed does plain suffix
// matching, so a scheme/port/userinfo/path fragment or a leading "." left
// in an entry would silently never match anything, and every file_url
// fetch would then 403 with no hint why.
func splitHostList(raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	var hosts []string
	for _, h := range strings.Split(raw, ",") {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		if strings.ContainsAny(h, "/:@") || strings.HasPrefix(h, ".") || strings.HasSuffix(h, ".") {
			return nil, fmt.Errorf("%s: invalid host entry %q, want a bare hostname (e.g. \"s3.example.com\"), not a URL", FetchAllowedHostsEnv, h)
		}
		hosts = append(hosts, h)
	}
	return hosts, nil
}
