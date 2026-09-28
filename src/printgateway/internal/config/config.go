// Package config loads the Print Gateway's runtime configuration.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultPort is used when neither PortAliasEnv nor PortEnv is set.
	DefaultPort = 8090
	// PortEnv names the TCP port to listen on (under Nomad, the dynamically allocated port,
	// injected as PORT). Env-only, not file-settable, so the bind port stays auditable.
	PortEnv = "PORT"
	// PortAliasEnv overrides PortEnv when set: PORT is commonly pre-set in a shell or base
	// image, so PRINT_GATEWAY_PORT lets an operator pin this service's port deliberately.
	PortAliasEnv = "PRINT_GATEWAY_PORT"

	// DefaultBindHost binds every interface, since Consul/Traefik must reach the dynamically
	// allocated Nomad port from outside the allocating host's own network namespace.
	DefaultBindHost = "0.0.0.0"
	// BindHostEnv overrides the bind host — set to "127.0.0.1" to restore loopback-only listening
	// for a manual local run. Env-only for the same reason as PortEnv.
	BindHostEnv = "PRINT_GATEWAY_BIND_HOST"

	// AuthTokenEnv carries the shared secret compared against the X-Labos-Print-Token header.
	AuthTokenEnv = "PRINT_GATEWAY_TOKEN"

	// RequireAuthEnv toggles whether a print token is required at all. Defaults to false (off) —
	// this deployment doesn't issue tokens to callers yet — but stays a first-class switch so
	// turning it on later is a config change, not a code change.
	RequireAuthEnv     = "PRINT_GATEWAY_REQUIRE_AUTH"
	DefaultRequireAuth = false

	// ServiceName identifies this service in logs.LogMetaData.
	ServiceName = "printgateway"

	// ConfigPathEnv names the optional JSON config file. Discovery is explicit-only: no default
	// path, no probing.
	ConfigPathEnv = "PRINT_GATEWAY_CONFIG"
)

// Server timeouts and limits, each with an env var for tuning without a rebuild. A malformed,
// non-positive, or mutually inconsistent value is a startup error (see Load and validate).
const (
	DefaultReadHeaderTimeout = 10 * time.Second
	DefaultReadTimeout       = 5 * time.Minute

	// DefaultWriteTimeout must exceed ReadTimeout+max(FetchTimeout,S3Timeout)+SubmitTimeout: net/http
	// arms the write deadline at header-parse time, so too low a value lets a slow request get
	// read, printed, and only then fail on the write — causing a retry and a duplicate print.
	DefaultWriteTimeout = 8 * time.Minute

	DefaultIdleTimeout    = 60 * time.Second
	DefaultMaxHeaderBytes = 64 << 10 // 64 KiB

	// DefaultMaxUploadBytes bounds a multipart/form-data body via http.MaxBytesReader.
	DefaultMaxUploadBytes int64 = 64 << 20 // 64 MiB
	MaxUploadBytesEnv           = "PRINT_GATEWAY_MAX_UPLOAD_BYTES"

	// DefaultMultipartMemoryBytes is ParseMultipartForm's in-memory threshold — deliberately not
	// configurable or tied to MaxUploadBytes; see mime/multipart's own memory/disk tradeoff.
	DefaultMultipartMemoryBytes int64 = 1 << 20 // 1 MiB

	// DefaultMaxJSONBytes bounds every non-multipart request body (JSON print-by-reference, /files/presign).
	DefaultMaxJSONBytes int64 = 8 << 10 // 8 KiB
	MaxJSONBytesEnv           = "PRINT_GATEWAY_MAX_JSON_BYTES"

	// DefaultShutdownGrace must exceed max(FetchTimeout,S3Timeout)+SubmitTimeout (see validate).
	DefaultShutdownGrace = 2 * time.Minute

	ReadHeaderTimeoutEnv = "PRINT_GATEWAY_READ_HEADER_TIMEOUT"
	ReadTimeoutEnv       = "PRINT_GATEWAY_READ_TIMEOUT"
	WriteTimeoutEnv      = "PRINT_GATEWAY_WRITE_TIMEOUT"
	IdleTimeoutEnv       = "PRINT_GATEWAY_IDLE_TIMEOUT"
	MaxHeaderBytesEnv    = "PRINT_GATEWAY_MAX_HEADER_BYTES"
	ShutdownGraceEnv     = "PRINT_GATEWAY_SHUTDOWN_GRACE"
)

// DefaultSubmitTimeout bounds a single `lp` invocation; without it a wedged CUPS queue hangs the
// handler goroutine (and its temp file, process, and client connection) forever.
const DefaultSubmitTimeout = 30 * time.Second

const SubmitTimeoutEnv = "PRINT_GATEWAY_SUBMIT_TIMEOUT"

// Fetch (file_url download) settings — SSRF defense.
const (
	DefaultFetchTimeout = 60 * time.Second
	FetchTimeoutEnv     = "PRINT_GATEWAY_FETCH_TIMEOUT"

	// DefaultFetchMaxBytes bounds a downloaded file_url response, independent of any request-body limit.
	DefaultFetchMaxBytes int64 = 64 << 20 // 64 MiB
	FetchMaxBytesEnv           = "PRINT_GATEWAY_FETCH_MAX_BYTES"

	// AllowPrivateTargetsEnv lifts the loopback/private/link-local block on file_url; must stay
	// false in any deployment reachable by an untrusted caller.
	AllowPrivateTargetsEnv = "PRINT_GATEWAY_ALLOW_PRIVATE_TARGETS"

	// FetchAllowedHostsEnv is the optional comma-separated host-suffix allowlist; empty allows any
	// public host (the private-target block above still applies regardless).
	FetchAllowedHostsEnv = "PRINT_GATEWAY_FETCH_ALLOWED_HOSTS"
)

// S3/MinIO object storage settings, all optional: an empty S3Endpoint means object storage isn't
// configured, and a broken config degrades s3_key/presign to 503 instead of failing startup.
const (
	S3EndpointEnv = "PRINT_GATEWAY_S3_ENDPOINT"
	S3BucketEnv   = "PRINT_GATEWAY_S3_BUCKET"
	S3RegionEnv   = "PRINT_GATEWAY_S3_REGION"

	S3InsecureEnv = "PRINT_GATEWAY_S3_INSECURE"

	// S3AccessKeyEnv/S3SecretKeyEnv are the env/file-fallback credentials; Vault is preferred first.
	S3AccessKeyEnv = "PRINT_GATEWAY_S3_ACCESS_KEY"
	S3SecretKeyEnv = "PRINT_GATEWAY_S3_SECRET_KEY"

	DefaultS3Timeout = 60 * time.Second
	S3TimeoutEnv     = "PRINT_GATEWAY_S3_TIMEOUT"

	// DefaultS3MaxBytes bounds an s3_key download the same way DefaultFetchMaxBytes bounds file_url.
	DefaultS3MaxBytes int64 = 64 << 20 // 64 MiB
	S3MaxBytesEnv           = "PRINT_GATEWAY_S3_MAX_BYTES"

	// DefaultPresignTTL is both the default and the cap: a longer caller-requested ttl is clamped.
	DefaultPresignTTL = 15 * time.Minute
	PresignTTLEnv     = "PRINT_GATEWAY_PRESIGN_TTL"
)

// Vault/secret_store connection details, all optional; an empty SecretStoreURL means Vault isn't
// configured and secrets resolve purely from the environment.
const (
	// VaultAddrEnv is Nomad's injected address variable; SecretStoreURLEnv overrides it when set,
	// matching go-packages/settings' own precedence.
	VaultAddrEnv      = "VAULT_ADDR"
	SecretStoreURLEnv = "SECRET_STORE_URL"

	VaultTokenEnv          = "VAULT_TOKEN"
	SecretStoreUsernameEnv = "SECRET_STORE_USERNAME"
	// SecretStorePasswordEnv holds an encryption.Encrypt-ed value, not plaintext.
	SecretStorePasswordEnv = "SECRET_STORE_PASSWORD"

	// LabosEnvEnv names the path prefix under the Vault mount (e.g. "production"); empty means no prefix.
	LabosEnvEnv = "LABOS_ENV"
)

const (
	// LogServerEnv is the env fallback "host:port" for shipping logs to logstash; empty means console-only.
	LogServerEnv = "LOG_SERVER"

	// LogLevelEnv selects the logrus level name; not validated here to keep this package stdlib-only
	// — main.go's logger.SetLogLevel catches an invalid value.
	LogLevelEnv     = "PRINT_GATEWAY_LOG_LEVEL"
	DefaultLogLevel = "info"
)

// Config holds the service's runtime configuration.
type Config struct {
	// Port is the TCP port the HTTP server listens on.
	Port int
	// BindHost is the host the HTTP server listens on. See Addr for the combined "host:port" form.
	BindHost string

	// AuthToken starts as PRINT_GATEWAY_TOKEN; main.go overwrites it with secrets.ResolveToken's result.
	AuthToken string

	// RequireAuth gates whether requireToken enforces the print token, and whether an unresolvable
	// token is fatal at startup. false means /print and /files/presign accept any request unauthenticated.
	RequireAuth bool

	// Vault/secret_store connection details; SecretStoreURL == "" means the other three are unused.
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

	// MaxUploadBytes/MaxJSONBytes bound an inbound request body by Content-Type (see the maxBytes middleware).
	MaxUploadBytes int64
	MaxJSONBytes   int64

	// ShutdownGrace bounds how long Shutdown waits for in-flight requests before main forces an exit.
	ShutdownGrace time.Duration

	// SubmitTimeout bounds a single lp invocation.
	SubmitTimeout time.Duration

	// FetchTimeout bounds a single file_url download.
	FetchTimeout time.Duration
	// FetchMaxBytes bounds a downloaded file_url response's size.
	FetchMaxBytes int64
	// AllowPrivateTargets lifts the loopback/private/link-local block on file_url; must stay false
	// in any deployment reachable by an untrusted caller.
	AllowPrivateTargets bool
	// FetchAllowedHosts is the optional host-suffix allowlist; empty means any public host is fetchable.
	FetchAllowedHosts []string

	// S3Endpoint == "" means object storage isn't configured.
	S3Endpoint string
	S3Bucket   string
	S3Region   string
	S3Insecure bool
	S3Timeout  time.Duration
	S3MaxBytes int64

	// S3AccessKey/S3SecretKey are the raw env-or-file-fallback values; secrets.ResolveS3Credentials
	// prefers a Vault-resolved pair when Vault is configured.
	S3AccessKey string
	S3SecretKey string

	// PresignTTL is both the default and the cap for /files/presign.
	PresignTTL time.Duration

	// LogServer is the raw, unparsed "host:port" env fallback for logstash shipping.
	LogServer string

	// LogLevel names the logrus level main.go asks the logger for.
	LogLevel string

	// sources records, per env-var name, where that setting came from; populated only when a
	// config file supplies it. Unexported: Config is copied by value into several packages
	// that would otherwise alias this map through an exported field.
	sources map[string]string

	// ConfigFilePath is the path a config file was actually read from, or "" if none was.
	ConfigFilePath string
}

// Addr is the "<BindHost>:<Port>" address the HTTP server listens on.
func (c Config) Addr() string {
	// JoinHostPort, not Sprintf: an IPv6 literal ("::", "::1") must be bracketed, or net.Listen
	// rejects it with "too many colons in address".
	return net.JoinHostPort(c.BindHost, strconv.Itoa(c.Port))
}

// Source returns the label describing how the setting named by the given env var was supplied,
// falling back to the bare env var name when no config file set it.
func (c Config) Source(name string) string {
	// An empty stored label is treated the same as absent, so it can never render as a blank name.
	if v := c.sources[name]; v != "" {
		return v
	}
	return name
}

// FileSourcedKeys returns the env-var names of every setting a config file supplied, sorted for a
// stable startup log line. Returns nil when no file was read.
func (c Config) FileSourcedKeys() []string {
	if len(c.sources) == 0 {
		return nil
	}
	keys := make([]string, 0, len(c.sources))
	for k := range c.sources {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// fileConfig mirrors the JSON config document (printservice.config.json); every scalar is a
// pointer so nil means "not present" (JSON null decodes the same as an absent key).
// Fetch.AllowedHosts and the Limits fields deviate: see their own field comments.
type fileConfig struct {
	Log          fileLog          `json:"resource/log"`
	FileStorage  fileFileStorage  `json:"resource/file_storage"`
	Printgateway filePrintgateway `json:"resource/printgateway"`
}

// fileLog is the shared logstash-shipping destination, hence its own top-level "resource/log"
// block rather than living under "resource/printgateway". Host and Port are separate fields
// (matching the reference convention) where Config.LogServer is a single "host:port" string —
// mergeFileConfig combines them.
type fileLog struct {
	Host *string `json:"host"`
	Port *string `json:"port"`
}

// fileFileStorage is the shared S3/MinIO connection — host and credentials only. Bucket/region
// live under "resource/printgateway.objectStore" instead.
type fileFileStorage struct {
	// Host: an explicit "" disables object storage from the file, suppressing PRINT_GATEWAY_S3_ENDPOINT.
	Host *string `json:"host"`
	// S3User/S3Password: unlike every other secret this package knows about, these two ARE valid in
	// the file — for a deployment model where the file itself is rendered from Vault at process
	// start. Precedence is unaffected: secrets.ResolveS3Credentials still tries Vault first.
	S3User     *string `json:"s3-user"`
	S3Password *string `json:"s3-password"`
}

// filePrintgateway is everything specific to running this service, the "resource/printgateway"
// counterpart to the reference convention's "resource/controlplane".
type filePrintgateway struct {
	// Addr only still exists here to turn a leftover "addr" key from before Port/BindHost went
	// env-only into an actionable error instead of a generic "unknown field" one — see
	// mergeFileConfig. It is never read into Config.
	Addr *string `json:"addr"`
	// LogLevel: unlike every other file-sourced string, an explicit "" here is a startup error, not
	// a suppression — see mergeFileConfig.
	LogLevel    *string         `json:"logLevel"`
	Timeouts    fileTimeouts    `json:"timeouts"`
	Limits      fileLimits      `json:"limits"`
	Fetch       fileFetch       `json:"fetch"`
	ObjectStore fileObjectStore `json:"objectStore"`
}

type fileTimeouts struct {
	ReadHeader    *string `json:"readHeader"`
	Read          *string `json:"read"`
	Write         *string `json:"write"`
	Idle          *string `json:"idle"`
	ShutdownGrace *string `json:"shutdownGrace"`
	Submit        *string `json:"submit"`
}

type fileLimits struct {
	MaxHeaderBytes *int64 `json:"maxHeaderBytes"`
	MaxUploadBytes *int64 `json:"maxUploadBytes"`
	MaxJSONBytes   *int64 `json:"maxJsonBytes"`
}

type fileFetch struct {
	Timeout  *string `json:"timeout"`
	MaxBytes *int64  `json:"maxBytes"`
	// AllowedHosts: nil means not mentioned (env, if any, still applies); a non-nil pointer to an
	// empty slice means the file explicitly says "no allowlist", suppressing FetchAllowedHostsEnv.
	AllowedHosts *[]string `json:"allowedHosts"`
}

// fileObjectStore is this service's own use of the shared S3 resource — which bucket/region, and
// every printgateway-specific S3 setting. Endpoint and credentials live in fileFileStorage instead.
type fileObjectStore struct {
	// Bucket/Region: an explicit "" is meaningful (a deliberately empty region, or clearing a
	// bucket override) and suppresses the corresponding env var.
	Bucket     *string `json:"bucket"`
	Region     *string `json:"region"`
	Insecure   *bool   `json:"insecure"`
	Timeout    *string `json:"timeout"`
	MaxBytes   *int64  `json:"maxBytes"`
	PresignTTL *string `json:"presignTtl"`
}

// decodeFileConfig decodes exactly one JSON object from data into a fileConfig, rejecting an
// unknown field and any trailing content. A duplicate *group* key (e.g. two "timeouts" objects)
// merges; only a duplicate *leaf* key is ordinary last-wins encoding/json behavior.
func decodeFileConfig(data []byte) (fileConfig, error) {
	// Checked explicitly so an empty/whitespace-only file (e.g. an unmounted bind mount) gets a
	// diagnosable message instead of a bare "EOF" from the decoder.
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return fileConfig{}, errors.New("file is empty")
	}
	// A top-level `null` would otherwise decode to a zero-value fileConfig exactly like `{}` does.
	if string(trimmed) == "null" {
		return fileConfig{}, errors.New("file is a JSON null, not an object")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var fc fileConfig
	if err := dec.Decode(&fc); err != nil {
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			// jsonNoun keeps the message in the JSON vocabulary the operator typed, never a Go type name.
			if typeErr.Field == "" {
				return fileConfig{}, fmt.Errorf("must be a JSON object, got %s", typeErr.Value)
			}
			return fileConfig{}, fmt.Errorf("field %q must be %s, got %s", typeErr.Field, jsonNoun(typeErr.Type), typeErr.Value)
		}
		return fileConfig{}, err
	}
	// A second concatenated JSON value would otherwise be silently discarded.
	var extra json.RawMessage
	switch err := dec.Decode(&extra); {
	case errors.Is(err, io.EOF):
		return fc, nil
	case err == nil:
		return fileConfig{}, errors.New("must contain exactly one JSON value")
	default:
		return fileConfig{}, err
	}
}

// jsonNoun names an UnmarshalTypeError's expected Go type in the JSON vocabulary of the config
// document, never a Go type name.
func jsonNoun(t reflect.Type) string {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		return "an object"
	case reflect.Slice:
		return "an array"
	case reflect.Bool:
		return "a boolean"
	case reflect.String:
		return "a string"
	default:
		return "an integer"
	}
}

// fileBytesInt validates and narrows a file-sourced byte-size value (JSON has no distinct integer
// widths, so every Limits field decodes as int64) into MaxHeaderBytes's plain int.
func fileBytesInt(n int64, src string) (int, error) {
	// Must be checked before the int64->int narrowing, or an oversized value silently wraps.
	if n > math.MaxInt {
		return 0, fmt.Errorf("%s: invalid byte size %d, want a positive integer number of bytes", src, n)
	}
	return validateBytes(int(n), src)
}

// loadConfigFile reads and decodes the file named by ConfigPathEnv. A missing, unreadable, or
// malformed file are all treated as the same startup error, naming both ConfigPathEnv and the path given.
func loadConfigFile(path string, readFile func(string) ([]byte, error)) (fileConfig, error) {
	data, err := readFile(path)
	if err != nil {
		return fileConfig{}, fmt.Errorf("%s: cannot read %q: %w", ConfigPathEnv, path, err)
	}
	fc, err := decodeFileConfig(data)
	if err != nil {
		return fileConfig{}, fmt.Errorf("%s: %s: %w", ConfigPathEnv, path, err)
	}
	return fc, nil
}

// mergeFileConfig overlays fc onto cfg and returns the sources map (env-var name to
// "<path>:<jsonPath>") for every setting the file supplied; file beats env unconditionally.
// Port/BindHost stay env-only, and Addr exists only to reject the legacy key.
func mergeFileConfig(cfg *Config, fc fileConfig, path string) (map[string]string, error) {
	label := func(jsonPath string) string { return path + ":" + jsonPath }
	sources := make(map[string]string)
	pg := fc.Printgateway

	if pg.Addr != nil {
		return nil, fmt.Errorf("%s: resource/printgateway.addr is no longer supported; set %s and, if needed, %s instead",
			label("resource/printgateway.addr"), PortEnv, BindHostEnv)
	}

	if pg.LogLevel != nil {
		// Unlike every other file-sourced string below, "" is an error here, not a suppression: it
		// is meaningless to logger.SetLogLevel.
		if *pg.LogLevel == "" {
			return nil, fmt.Errorf("%s: must not be empty", label("resource/printgateway.logLevel"))
		}
		cfg.LogLevel = *pg.LogLevel
		sources[LogLevelEnv] = label("resource/printgateway.logLevel")
	}

	durationRows := []struct {
		env      string
		jsonPath string
		raw      *string
		dst      *time.Duration
	}{
		{ReadHeaderTimeoutEnv, "resource/printgateway.timeouts.readHeader", pg.Timeouts.ReadHeader, &cfg.ReadHeaderTimeout},
		{ReadTimeoutEnv, "resource/printgateway.timeouts.read", pg.Timeouts.Read, &cfg.ReadTimeout},
		{WriteTimeoutEnv, "resource/printgateway.timeouts.write", pg.Timeouts.Write, &cfg.WriteTimeout},
		{IdleTimeoutEnv, "resource/printgateway.timeouts.idle", pg.Timeouts.Idle, &cfg.IdleTimeout},
		{ShutdownGraceEnv, "resource/printgateway.timeouts.shutdownGrace", pg.Timeouts.ShutdownGrace, &cfg.ShutdownGrace},
		{SubmitTimeoutEnv, "resource/printgateway.timeouts.submit", pg.Timeouts.Submit, &cfg.SubmitTimeout},
		{FetchTimeoutEnv, "resource/printgateway.fetch.timeout", pg.Fetch.Timeout, &cfg.FetchTimeout},
		{S3TimeoutEnv, "resource/printgateway.objectStore.timeout", pg.ObjectStore.Timeout, &cfg.S3Timeout},
		{PresignTTLEnv, "resource/printgateway.objectStore.presignTtl", pg.ObjectStore.PresignTTL, &cfg.PresignTTL},
	}
	for _, r := range durationRows {
		if r.raw == nil {
			continue
		}
		d, err := parseDuration(*r.raw, label(r.jsonPath))
		if err != nil {
			return nil, err
		}
		*r.dst = d
		sources[r.env] = label(r.jsonPath)
	}

	bytes64Rows := []struct {
		env      string
		jsonPath string
		raw      *int64
		dst      *int64
	}{
		{MaxUploadBytesEnv, "resource/printgateway.limits.maxUploadBytes", pg.Limits.MaxUploadBytes, &cfg.MaxUploadBytes},
		{MaxJSONBytesEnv, "resource/printgateway.limits.maxJsonBytes", pg.Limits.MaxJSONBytes, &cfg.MaxJSONBytes},
		{FetchMaxBytesEnv, "resource/printgateway.fetch.maxBytes", pg.Fetch.MaxBytes, &cfg.FetchMaxBytes},
		{S3MaxBytesEnv, "resource/printgateway.objectStore.maxBytes", pg.ObjectStore.MaxBytes, &cfg.S3MaxBytes},
	}
	for _, r := range bytes64Rows {
		if r.raw == nil {
			continue
		}
		n, err := validateBytes(*r.raw, label(r.jsonPath))
		if err != nil {
			return nil, err
		}
		*r.dst = n
		sources[r.env] = label(r.jsonPath)
	}

	if pg.Limits.MaxHeaderBytes != nil {
		n, err := fileBytesInt(*pg.Limits.MaxHeaderBytes, label("resource/printgateway.limits.maxHeaderBytes"))
		if err != nil {
			return nil, err
		}
		cfg.MaxHeaderBytes = n
		sources[MaxHeaderBytesEnv] = label("resource/printgateway.limits.maxHeaderBytes")
	}

	if pg.ObjectStore.Insecure != nil {
		cfg.S3Insecure = *pg.ObjectStore.Insecure
		sources[S3InsecureEnv] = label("resource/printgateway.objectStore.insecure")
	}

	stringRows := []struct {
		env      string
		jsonPath string
		raw      *string
		dst      *string
	}{
		{S3BucketEnv, "resource/printgateway.objectStore.bucket", pg.ObjectStore.Bucket, &cfg.S3Bucket},
		{S3RegionEnv, "resource/printgateway.objectStore.region", pg.ObjectStore.Region, &cfg.S3Region},
	}
	for _, r := range stringRows {
		if r.raw == nil {
			continue
		}
		*r.dst = *r.raw // "" is a meaningful, deliberate value for both
		sources[r.env] = label(r.jsonPath)
	}

	// resource/file_storage: the shared S3 connection (endpoint + creds), cross-cutting
	// infrastructure rather than this service's own setting.
	if fc.FileStorage.Host != nil {
		cfg.S3Endpoint = *fc.FileStorage.Host // "" suppresses PRINT_GATEWAY_S3_ENDPOINT
		sources[S3EndpointEnv] = label("resource/file_storage.host")
	}
	if fc.FileStorage.S3User != nil {
		cfg.S3AccessKey = *fc.FileStorage.S3User
		sources[S3AccessKeyEnv] = label("resource/file_storage.s3-user")
	}
	if fc.FileStorage.S3Password != nil {
		cfg.S3SecretKey = *fc.FileStorage.S3Password
		sources[S3SecretKeyEnv] = label("resource/file_storage.s3-password")
	}

	// resource/log splits into host/port where Config.LogServer is one "host:port" string, so
	// it's combined here rather than through the stringRows table above. Suppressed only when
	// both sides resolve empty.
	if fc.Log.Host != nil || fc.Log.Port != nil {
		var host, port string
		if fc.Log.Host != nil {
			host = *fc.Log.Host
		}
		if fc.Log.Port != nil {
			port = *fc.Log.Port
		}
		if host == "" && port == "" {
			cfg.LogServer = ""
		} else {
			cfg.LogServer = host + ":" + port
		}
		sources[LogServerEnv] = label("resource/log")
	}

	if fc.Printgateway.Fetch.AllowedHosts != nil {
		hosts, err := normalizeHostList(*fc.Printgateway.Fetch.AllowedHosts, label("resource/printgateway.fetch.allowedHosts"))
		if err != nil {
			return nil, err
		}
		// nil when the file's array is empty: a deliberate "no allowlist" that suppresses FetchAllowedHostsEnv.
		cfg.FetchAllowedHosts = hosts
		sources[FetchAllowedHostsEnv] = label("resource/printgateway.fetch.allowedHosts")
	}

	// allowPrivateTargets deliberately has no row and no field anywhere in fileConfig: putting it
	// in the file would be a total SSRF bypass, so DisallowUnknownFields turns any attempt into a
	// startup error instead of a working feature.

	return sources, nil
}

// Load builds Config from the environment and, when ConfigPathEnv names one, a JSON config file
// that wins over it. Fails on a malformed override from either source, naming the offending
// variable or JSON path.
func Load(getenv func(string) string, readFile func(string) ([]byte, error)) (Config, error) {
	port := DefaultPort
	portRaw, portSrc := getenv(PortEnv), PortEnv
	if v := getenv(PortAliasEnv); v != "" {
		portRaw, portSrc = v, PortAliasEnv
	}
	if portRaw != "" {
		var err error
		if port, err = parsePort(portRaw, portSrc); err != nil {
			return Config{}, err
		}
	}
	bindHost := overrideString(getenv, BindHostEnv, DefaultBindHost)

	secretStoreURL := getenv(VaultAddrEnv)
	if v := getenv(SecretStoreURLEnv); v != "" {
		secretStoreURL = v
	}

	logLevel := overrideString(getenv, LogLevelEnv, DefaultLogLevel)

	fetchAllowedHosts, err := splitHostList(getenv(FetchAllowedHostsEnv))
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		Port:      port,
		BindHost:  bindHost,
		AuthToken: getenv(AuthTokenEnv),

		RequireAuth: DefaultRequireAuth,

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
		MaxUploadBytes:    DefaultMaxUploadBytes,
		MaxJSONBytes:      DefaultMaxJSONBytes,
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

	// A table, not one if-block per value, so each env-var/field pairing appears exactly once —
	// the repeated form let a copy-pasted line target the wrong field silently.
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

	// Order matters: the first malformed env var is the one reported.
	n, err := overrideBytes(getenv, MaxHeaderBytesEnv, cfg.MaxHeaderBytes)
	if err != nil {
		return Config{}, err
	}
	cfg.MaxHeaderBytes = n

	for _, r := range []struct {
		name string
		dst  *int64
	}{
		{FetchMaxBytesEnv, &cfg.FetchMaxBytes},
		{MaxUploadBytesEnv, &cfg.MaxUploadBytes},
		{MaxJSONBytesEnv, &cfg.MaxJSONBytes},
	} {
		v, err := overrideBytes64(getenv, r.name, *r.dst)
		if err != nil {
			return Config{}, err
		}
		*r.dst = v
	}

	for _, r := range []struct {
		name string
		dst  *bool
	}{
		{AllowPrivateTargetsEnv, &cfg.AllowPrivateTargets},
		{S3InsecureEnv, &cfg.S3Insecure},
		{RequireAuthEnv, &cfg.RequireAuth},
	} {
		v, err := overrideBool(getenv, r.name, *r.dst)
		if err != nil {
			return Config{}, err
		}
		*r.dst = v
	}

	s3MaxBytes, err := overrideBytes64(getenv, S3MaxBytesEnv, cfg.S3MaxBytes)
	if err != nil {
		return Config{}, err
	}
	cfg.S3MaxBytes = s3MaxBytes

	// Config file layer: read after every env/default value above is resolved, so mergeFileConfig
	// only has to overlay what the file actually names.
	if path := getenv(ConfigPathEnv); path != "" {
		fc, err := loadConfigFile(path, readFile)
		if err != nil {
			return Config{}, err
		}
		sources, err := mergeFileConfig(&cfg, fc, path)
		if err != nil {
			return Config{}, err
		}
		cfg.ConfigFilePath = path
		cfg.sources = sources
	}

	if err := validate(cfg); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// validate enforces relationships between values that are each plausible alone but wrong in
// combination — a class of mistake net/http never reports; it just applies whichever deadline
// fires first.
func validate(cfg Config) error {
	// A header deadline that outlives the read deadline can never be the one that fires.
	if cfg.ReadHeaderTimeout > cfg.ReadTimeout {
		return fmt.Errorf("%s (%s) must not exceed %s (%s)",
			cfg.Source(ReadHeaderTimeoutEnv), cfg.ReadHeaderTimeout, cfg.Source(ReadTimeoutEnv), cfg.ReadTimeout)
	}

	// The write deadline arms at header-parse time, so it must also cover the body read, download,
	// and lp submission, not just the response write, or a slow request prints and then fails on
	// the write, causing a duplicate print on retry. max, not sum: a request only ever exercises
	// one of FetchTimeout/S3Timeout.
	fetchOrS3 := max(cfg.FetchTimeout, cfg.S3Timeout)
	if writeBudget := cfg.ReadTimeout + fetchOrS3 + cfg.SubmitTimeout; cfg.WriteTimeout <= writeBudget {
		return fmt.Errorf("%s (%s) must exceed %s+max(%s,%s)+%s (%s): the write deadline is armed when request headers are parsed, so it must cover reading the body, downloading file_url/s3_key, and running lp, as well as sending the response",
			cfg.Source(WriteTimeoutEnv), cfg.WriteTimeout, cfg.Source(ReadTimeoutEnv), cfg.Source(FetchTimeoutEnv), cfg.Source(S3TimeoutEnv), cfg.Source(SubmitTimeoutEnv), writeBudget)
	}

	// A request already using the full fetch/s3+submit budget must still fit inside the shutdown
	// grace period, or a SIGTERM during that request truncates the print it exists to let finish.
	if budget := fetchOrS3 + cfg.SubmitTimeout; cfg.ShutdownGrace <= budget {
		return fmt.Errorf("%s (%s) must exceed max(%s,%s)+%s (%s): a request already using the full fetch/s3+submit budget must still fit inside the shutdown grace period",
			cfg.Source(ShutdownGraceEnv), cfg.ShutdownGrace, cfg.Source(FetchTimeoutEnv), cfg.Source(S3TimeoutEnv), cfg.Source(SubmitTimeoutEnv), budget)
	}

	return nil
}

func overrideDuration(getenv func(string) string, name string, def time.Duration) (time.Duration, error) {
	raw := getenv(name)
	if raw == "" {
		return def, nil
	}
	return parseDuration(raw, name)
}

// parseDuration is overrideDuration's parse-and-validate half, split out so a config file can
// share the same validation and error wording. src labels the error.
func parseDuration(raw, src string) (time.Duration, error) {
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid duration %q: %w", src, raw, err)
	}
	// net/http guards every timeout with `if d > 0`, so "0" or a negative value means *no timeout
	// at all*, not "very short" — reject it rather than silently reopening that exposure.
	if d <= 0 {
		return 0, fmt.Errorf("%s: %q must be positive; net/http reads a non-positive timeout as no timeout at all", src, raw)
	}
	return d, nil
}

// parsePort is the parse-and-validate half of a TCP port number, valid range 1-65535. src labels
// the error with whichever env var actually supplied raw.
func parsePort(raw, src string) (int, error) {
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("%s: invalid port %q, want an integer between 1 and 65535", src, raw)
	}
	return n, nil
}

// overrideBytesEnv reads name as a positive byte size via parse, or returns def when unset.
func overrideBytesEnv[N int | int64](getenv func(string) string, name string, def N, parse func(string) (N, error)) (N, error) {
	raw := getenv(name)
	if raw == "" {
		return def, nil
	}
	n, err := parse(raw)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s: invalid byte size %q, want a positive integer number of bytes", name, raw)
	}
	return n, nil
}

func overrideBytes(getenv func(string) string, name string, def int) (int, error) {
	return overrideBytesEnv(getenv, name, def, strconv.Atoi)
}

// overrideBytes64 is overrideBytes for a field too large for a plain int on a 32-bit build.
func overrideBytes64(getenv func(string) string, name string, def int64) (int64, error) {
	return overrideBytesEnv(getenv, name, def, func(raw string) (int64, error) { return strconv.ParseInt(raw, 10, 64) })
}

// validateBytes is the positivity check for an already-parsed byte size (e.g. from the config file).
func validateBytes[N int | int64](n N, src string) (N, error) {
	if n <= 0 {
		return 0, fmt.Errorf("%s: invalid byte size %d, want a positive integer number of bytes", src, n)
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

// overrideString reads name from the environment, falling back to def when unset. Only correct
// where "" is not itself a meaningful configured value — S3Endpoint and S3Region are exceptions
// and keep bespoke handling instead of routing through this helper.
func overrideString(getenv func(string) string, name, def string) string {
	if v := getenv(name); v != "" {
		return v
	}
	return def
}

// splitHostList parses the comma-separated FetchAllowedHostsEnv value. Empty entries (from "a,,b"
// or leading/trailing commas) are dropped rather than rejected.
func splitHostList(raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	return normalizeHostList(strings.Split(raw, ","), FetchAllowedHostsEnv)
}

// normalizeHostList is splitHostList's validate-only half: it rejects anything that is not a
// bare hostname. fetch.hostAllowed matches on a label boundary, so a scheme/port/userinfo/path
// fragment, or a leading/trailing dot, must be rejected here rather than silently producing an
// unexplained 403.
func normalizeHostList(entries []string, src string) ([]string, error) {
	var hosts []string
	for _, h := range entries {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		if strings.ContainsAny(h, "/:@") || strings.HasPrefix(h, ".") || strings.HasSuffix(h, ".") {
			return nil, fmt.Errorf("%s: invalid host entry %q, want a bare hostname (e.g. \"s3.example.com\"), not a URL", src, h)
		}
		hosts = append(hosts, h)
	}
	return hosts, nil
}
