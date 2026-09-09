package config

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// env builds Load's getenv parameter from a map, so no case touches the real process environment
// and every one can run in parallel.
func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// progName stands in for os.Args[0]: Load reads the address from args[1].
const progName = "printgateway"

// noFiles fails the test if readFile is ever called.
func noFiles(t *testing.T) func(string) ([]byte, error) {
	t.Helper()
	return func(path string) ([]byte, error) {
		t.Fatalf("readFile unexpectedly called with %q", path)
		return nil, nil
	}
}

// mustLoad fails the test if Load errors.
func mustLoad(t *testing.T, args []string, m map[string]string) Config {
	t.Helper()
	cfg, err := Load(args, env(m), noFiles(t))
	if err != nil {
		t.Fatalf("Load(%v, %v) returned an unexpected error: %v", args, m, err)
	}
	return cfg
}

// testConfigPath is the one fixed fake path every file-layer test names via PRINT_GATEWAY_CONFIG.
const testConfigPath = "/etc/printgateway-test.json"

// files is a readFile stand-in backed by a map of path -> raw file content.
func files(m map[string]string) func(string) ([]byte, error) {
	return func(path string) ([]byte, error) {
		if raw, ok := m[path]; ok {
			return []byte(raw), nil
		}
		return nil, fmt.Errorf("no such file: %q", path)
	}
}

// fileWith builds a minimal JSON document with a single value at a dotted path (e.g.
// "timeouts.write" -> {"timeouts":{"write":value}}).
func fileWith(dotted string, value any) string {
	parts := strings.Split(dotted, ".")
	var node any = value
	for i := len(parts) - 1; i > 0; i-- {
		node = map[string]any{parts[i]: node}
	}
	root := map[string]any{parts[0]: node}
	b, err := json.Marshal(root)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// mustLoadFile is mustLoad's equivalent for a case that needs the file layer to actually engage.
func mustLoadFile(t *testing.T, args []string, m map[string]string, fileContent string) Config {
	t.Helper()
	envVars := make(map[string]string, len(m)+1)
	for k, v := range m {
		envVars[k] = v
	}
	envVars[ConfigPathEnv] = testConfigPath
	cfg, err := Load(args, env(envVars), files(map[string]string{testConfigPath: fileContent}))
	if err != nil {
		t.Fatalf("Load with config file content %s returned an unexpected error: %v", fileContent, err)
	}
	return cfg
}

// requireErrContaining asserts err is non-nil and its text mentions every given substring.
func requireErrContaining(t *testing.T, err error, substrs ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error mentioning %v, got nil", substrs)
	}
	for _, s := range substrs {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error %q does not mention %q", err, s)
		}
	}
}

func TestLoadDefaults(t *testing.T) {
	t.Parallel()

	cfg := mustLoad(t, []string{progName}, nil)

	checks := []struct {
		field string
		got   any
		want  any
	}{
		{"Addr", cfg.Addr, DefaultAddr},
		{"ReadHeaderTimeout", cfg.ReadHeaderTimeout, DefaultReadHeaderTimeout},
		{"ReadTimeout", cfg.ReadTimeout, DefaultReadTimeout},
		{"WriteTimeout", cfg.WriteTimeout, DefaultWriteTimeout},
		{"IdleTimeout", cfg.IdleTimeout, DefaultIdleTimeout},
		{"MaxHeaderBytes", cfg.MaxHeaderBytes, DefaultMaxHeaderBytes},
		{"ShutdownGrace", cfg.ShutdownGrace, DefaultShutdownGrace},
		{"SubmitTimeout", cfg.SubmitTimeout, DefaultSubmitTimeout},
		{"FetchTimeout", cfg.FetchTimeout, DefaultFetchTimeout},
		{"FetchMaxBytes", cfg.FetchMaxBytes, DefaultFetchMaxBytes},
		{"AllowPrivateTargets", cfg.AllowPrivateTargets, false},
		{"S3Timeout", cfg.S3Timeout, DefaultS3Timeout},
		{"S3MaxBytes", cfg.S3MaxBytes, DefaultS3MaxBytes},
		{"S3Insecure", cfg.S3Insecure, false},
		{"PresignTTL", cfg.PresignTTL, DefaultPresignTTL},
		{"LogLevel", cfg.LogLevel, DefaultLogLevel},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("default %s = %v (%T), want %v (%T)", c.field, c.got, c.got, c.want, c.want)
		}
	}

	if cfg.FetchAllowedHosts != nil {
		t.Errorf("default FetchAllowedHosts = %v, want nil (no allowlist)", cfg.FetchAllowedHosts)
	}

	for field, got := range map[string]string{
		"AuthToken":           cfg.AuthToken,
		"SecretStoreURL":      cfg.SecretStoreURL,
		"VaultToken":          cfg.VaultToken,
		"SecretStoreUsername": cfg.SecretStoreUsername,
		"SecretStorePassword": cfg.SecretStorePassword,
		"LabosEnv":            cfg.LabosEnv,
		"S3Endpoint":          cfg.S3Endpoint,
		"S3Bucket":            cfg.S3Bucket,
		"S3Region":            cfg.S3Region,
		"S3AccessKey":         cfg.S3AccessKey,
		"S3SecretKey":         cfg.S3SecretKey,
		"LogServer":           cfg.LogServer,
	} {
		if got != "" {
			t.Errorf("default %s = %q, want empty", field, got)
		}
	}
}

// TestDefaultAddrIsLoopback pins the property, not just the constant: the listen address is the
// service's first line of defence, so a default that bound every interface must fail visibly here.
func TestDefaultAddrIsLoopback(t *testing.T) {
	t.Parallel()

	if !strings.HasPrefix(DefaultAddr, "127.0.0.1:") {
		t.Errorf("DefaultAddr = %q, want a 127.0.0.1 address", DefaultAddr)
	}
}

func TestLoadAddr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{"no args at all", nil, DefaultAddr},
		{"program name only", []string{progName}, DefaultAddr},
		{"address override", []string{progName, "0.0.0.0:9999"}, "0.0.0.0:9999"},
		{"trailing args are ignored", []string{progName, ":9999", "unused"}, ":9999"},
		{
			// Pinned as current behavior, not endorsed: an empty args[1] is taken verbatim, and
			// net/http resolves "" to ":http" (port 80 on every interface). Update this row if
			// Load is later changed to reject it.
			"an empty address argument is taken verbatim", []string{progName, ""}, "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := mustLoad(t, tt.args, nil).Addr; got != tt.want {
				t.Errorf("Addr = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestLoadSecretStoreURLPrecedence mirrors go-packages/settings.getSecretStoreSettings' precedence:
// a standard Nomad job spec injects VAULT_ADDR alone, so that alone must engage Vault too.
func TestLoadSecretStoreURLPrecedence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		vaultAddr string
		override  string
		want      string
	}{
		{"neither set means Vault is not configured", "", "", ""},
		{"VAULT_ADDR alone engages Vault", "http://vault:8200", "", "http://vault:8200"},
		{"SECRET_STORE_URL alone engages Vault", "", "http://ss:8200", "http://ss:8200"},
		{"SECRET_STORE_URL overrides VAULT_ADDR", "http://vault:8200", "http://ss:8200", "http://ss:8200"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := mustLoad(t, []string{progName}, map[string]string{
				VaultAddrEnv:      tt.vaultAddr,
				SecretStoreURLEnv: tt.override,
			})
			if cfg.SecretStoreURL != tt.want {
				t.Errorf("SecretStoreURL = %q, want %q", cfg.SecretStoreURL, tt.want)
			}
		})
	}
}

// The three setting tables below are package-level because two or more tests share each of them,
// and read-only since parallel subtests iterate them concurrently.
//
// durationSettings pairs every duration env var with the field it must reach and the default it
// must otherwise keep; override values are distinct so a mispairing is recognizable on sight.
var durationSettings = []struct {
	envVar   string
	get      func(Config) time.Duration
	def      time.Duration
	override time.Duration
}{
	{ReadHeaderTimeoutEnv, func(c Config) time.Duration { return c.ReadHeaderTimeout }, DefaultReadHeaderTimeout, 11 * time.Second},
	{ReadTimeoutEnv, func(c Config) time.Duration { return c.ReadTimeout }, DefaultReadTimeout, 4 * time.Minute},
	{WriteTimeoutEnv, func(c Config) time.Duration { return c.WriteTimeout }, DefaultWriteTimeout, 9 * time.Minute},
	{IdleTimeoutEnv, func(c Config) time.Duration { return c.IdleTimeout }, DefaultIdleTimeout, 61 * time.Second},
	{ShutdownGraceEnv, func(c Config) time.Duration { return c.ShutdownGrace }, DefaultShutdownGrace, 3 * time.Minute},
	{SubmitTimeoutEnv, func(c Config) time.Duration { return c.SubmitTimeout }, DefaultSubmitTimeout, 31 * time.Second},
	{FetchTimeoutEnv, func(c Config) time.Duration { return c.FetchTimeout }, DefaultFetchTimeout, 62 * time.Second},
	{S3TimeoutEnv, func(c Config) time.Duration { return c.S3Timeout }, DefaultS3Timeout, 63 * time.Second},
	{PresignTTLEnv, func(c Config) time.Duration { return c.PresignTTL }, DefaultPresignTTL, 16 * time.Minute},
}

// TestLoadDurationOverridesArePairedCorrectly guards against a swapped env-var/field pairing: each
// variable is set alone and every OTHER field must still hold its default.
func TestLoadDurationOverridesArePairedCorrectly(t *testing.T) {
	t.Parallel()

	for _, s := range durationSettings {
		t.Run(s.envVar, func(t *testing.T) {
			t.Parallel()
			cfg := mustLoad(t, []string{progName}, map[string]string{s.envVar: s.override.String()})

			for _, other := range durationSettings {
				want := other.def
				if other.envVar == s.envVar {
					want = s.override
				}
				if got := other.get(cfg); got != want {
					t.Errorf("with only %s=%s set: %s = %s, want %s",
						s.envVar, s.override, other.envVar, got, want)
				}
			}
		})
	}
}

func TestLoadRejectsMalformedDurations(t *testing.T) {
	t.Parallel()

	// Every duration goes through overrideDuration, so one variable is enough to exercise the
	// parse and positivity branches.
	values := []struct {
		name      string
		value     string
		wantInErr string
	}{
		{"not a duration at all", "soon", "invalid duration"},
		{"number with no unit", "30", "invalid duration"},
		{"whitespace", " ", "invalid duration"},
		{"zero", "0", "must be positive"},
		{"negative", "-5s", "must be positive"},
	}

	for _, tt := range values {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := Load([]string{progName}, env(map[string]string{SubmitTimeoutEnv: tt.value}), noFiles(t))
			requireErrContaining(t, err, SubmitTimeoutEnv, tt.wantInErr)
		})
	}
}

// byteSizeSettings pairs each byte-size env var with its field, across the two helpers that back
// them (overrideBytes for the plain int, overrideBytes64 for the two int64 fields).
var byteSizeSettings = []struct {
	envVar string
	get    func(Config) int64
	def    int64
}{
	{MaxHeaderBytesEnv, func(c Config) int64 { return int64(c.MaxHeaderBytes) }, int64(DefaultMaxHeaderBytes)},
	{FetchMaxBytesEnv, func(c Config) int64 { return c.FetchMaxBytes }, DefaultFetchMaxBytes},
	{S3MaxBytesEnv, func(c Config) int64 { return c.S3MaxBytes }, DefaultS3MaxBytes},
}

func TestLoadByteSizeOverrides(t *testing.T) {
	t.Parallel()

	for _, s := range byteSizeSettings {
		t.Run(s.envVar, func(t *testing.T) {
			t.Parallel()
			cfg := mustLoad(t, []string{progName}, map[string]string{s.envVar: "8192"})
			if got := s.get(cfg); got != 8192 {
				t.Errorf("%s=8192 gave %d, want 8192", s.envVar, got)
			}

			for _, other := range byteSizeSettings {
				if other.envVar == s.envVar {
					continue
				}
				if got := other.get(cfg); got != other.def {
					t.Errorf("with only %s set: %s = %d, want its default %d",
						s.envVar, other.envVar, got, other.def)
				}
			}
		})
	}
}

// TestLoadByteSizeOverrideAcceptsLargeValues documents the intended range of the two int64
// byte-size settings; the int64-ness itself is enforced by the field types, not by this test.
func TestLoadByteSizeOverrideAcceptsLargeValues(t *testing.T) {
	t.Parallel()

	const large = int64(5_000_000_000)
	cfg := mustLoad(t, []string{progName}, map[string]string{
		FetchMaxBytesEnv: "5000000000",
		S3MaxBytesEnv:    "5000000000",
	})
	if cfg.FetchMaxBytes != large {
		t.Errorf("FetchMaxBytes = %d, want %d", cfg.FetchMaxBytes, large)
	}
	if cfg.S3MaxBytes != large {
		t.Errorf("S3MaxBytes = %d, want %d", cfg.S3MaxBytes, large)
	}
}

func TestLoadRejectsMalformedByteSizes(t *testing.T) {
	t.Parallel()

	// "64KiB" is called out specifically: these are plain byte counts, not Go duration-style
	// suffixed values.
	values := []string{"64KiB", "abc", "1.5", "0", "-1", " "}

	for _, s := range byteSizeSettings {
		for _, v := range values {
			t.Run(s.envVar+"="+v, func(t *testing.T) {
				t.Parallel()
				_, err := Load([]string{progName}, env(map[string]string{s.envVar: v}), noFiles(t))
				requireErrContaining(t, err, s.envVar, "invalid byte size")
			})
		}
	}
}

var boolSettings = []struct {
	envVar string
	get    func(Config) bool
}{
	{AllowPrivateTargetsEnv, func(c Config) bool { return c.AllowPrivateTargets }},
	{S3InsecureEnv, func(c Config) bool { return c.S3Insecure }},
}

func TestLoadBoolOverrides(t *testing.T) {
	t.Parallel()

	values := map[string]bool{
		"true": true, "TRUE": true, "True": true, "1": true, "t": true,
		"false": false, "FALSE": false, "0": false, "f": false,
	}

	for _, s := range boolSettings {
		for raw, want := range values {
			t.Run(s.envVar+"="+raw, func(t *testing.T) {
				t.Parallel()
				cfg := mustLoad(t, []string{progName}, map[string]string{s.envVar: raw})
				if got := s.get(cfg); got != want {
					t.Errorf("%s=%q gave %v, want %v", s.envVar, raw, got, want)
				}
			})
		}
	}
}

// TestOverrideBoolAppliesAnExplicitFalse exists because both bool settings default to false, so
// TestLoadBoolOverrides' "false" rows only prove that spelling is ACCEPTED, not APPLIED — an
// overrideBool that always returned def on a false parse would pass that test unchanged.
func TestOverrideBoolAppliesAnExplicitFalse(t *testing.T) {
	t.Parallel()

	got, err := overrideBool(env(map[string]string{"X": "false"}), "X", true)
	if err != nil {
		t.Fatalf("overrideBool returned an unexpected error: %v", err)
	}
	if got {
		t.Error(`overrideBool(def=true, raw="false") = true, want false`)
	}
}

func TestLoadRejectsMalformedBools(t *testing.T) {
	t.Parallel()

	// "yes"/"on"/"2" read as true to a human but strconv.ParseBool rejects them; silently
	// defaulting to false would leave an operator believing the opposite of reality.
	for _, s := range boolSettings {
		for _, v := range []string{"yes", "on", "2", "maybe"} {
			t.Run(s.envVar+"="+v, func(t *testing.T) {
				t.Parallel()
				_, err := Load([]string{progName}, env(map[string]string{s.envVar: v}), noFiles(t))
				requireErrContaining(t, err, s.envVar, "invalid boolean")
			})
		}
	}
}

func TestLoadLogLevel(t *testing.T) {
	t.Parallel()

	tests := []struct{ name, set, want string }{
		{"unset falls back to the default", "", DefaultLogLevel},
		{"an explicit level is taken as-is", "debug", "debug"},
		// Not validated here to keep this package stdlib-only; main.go's SetLogLevel rejects a bad level.
		{"an unknown level is passed through for main to reject", "shout", "shout"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := mustLoad(t, []string{progName}, map[string]string{LogLevelEnv: tt.set})
			if cfg.LogLevel != tt.want {
				t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, tt.want)
			}
		})
	}
}

// TestLoadPassesThroughStringSettings covers the plain getenv-to-field assignments, each asserted
// by name against a value unique to it since a mispairing here is otherwise invisible at runtime.
func TestLoadPassesThroughStringSettings(t *testing.T) {
	t.Parallel()

	m := map[string]string{
		AuthTokenEnv:           "the-token",
		VaultTokenEnv:          "the-vault-token",
		SecretStoreUsernameEnv: "the-username",
		SecretStorePasswordEnv: "the-password",
		LabosEnvEnv:            "staging",
		S3EndpointEnv:          "minio.internal:9000",
		S3BucketEnv:            "print-documents",
		S3RegionEnv:            "eu-west-1",
		S3AccessKeyEnv:         "the-access-key",
		S3SecretKeyEnv:         "the-secret-key",
		LogServerEnv:           "logstash.internal:514",
	}
	cfg := mustLoad(t, []string{progName}, m)

	for field, pair := range map[string]struct{ got, want string }{
		"AuthToken":           {cfg.AuthToken, m[AuthTokenEnv]},
		"VaultToken":          {cfg.VaultToken, m[VaultTokenEnv]},
		"SecretStoreUsername": {cfg.SecretStoreUsername, m[SecretStoreUsernameEnv]},
		"SecretStorePassword": {cfg.SecretStorePassword, m[SecretStorePasswordEnv]},
		"LabosEnv":            {cfg.LabosEnv, m[LabosEnvEnv]},
		"S3Endpoint":          {cfg.S3Endpoint, m[S3EndpointEnv]},
		"S3Bucket":            {cfg.S3Bucket, m[S3BucketEnv]},
		"S3Region":            {cfg.S3Region, m[S3RegionEnv]},
		"S3AccessKey":         {cfg.S3AccessKey, m[S3AccessKeyEnv]},
		"S3SecretKey":         {cfg.S3SecretKey, m[S3SecretKeyEnv]},
		"LogServer":           {cfg.LogServer, m[LogServerEnv]},
	} {
		if pair.got != pair.want {
			t.Errorf("%s = %q, want %q", field, pair.got, pair.want)
		}
	}
}

func TestSplitHostList(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want []string
		// wantNil distinguishes a nil result from an empty-but-non-nil one, which slices.Equal
		// alone cannot express.
		wantNil   bool
		wantInErr string
	}{
		{name: "empty means no allowlist", raw: "", want: nil, wantNil: true},
		{name: "single host", raw: "s3.example.com", want: []string{"s3.example.com"}},
		{name: "several hosts", raw: "a.example.com,b.example.com", want: []string{"a.example.com", "b.example.com"}},
		{name: "surrounding whitespace is trimmed", raw: " a.example.com , b.example.com ", want: []string{"a.example.com", "b.example.com"}},
		{name: "entries are lowercased", raw: "S3.Example.COM", want: []string{"s3.example.com"}},
		{name: "empty entries are dropped", raw: "a.example.com,,b.example.com", want: []string{"a.example.com", "b.example.com"}},
		{name: "leading and trailing commas are dropped", raw: ",a.example.com,", want: []string{"a.example.com"}},
		{name: "only commas yields no allowlist", raw: ",,,", want: nil, wantNil: true},

		// fetch.hostAllowed matches on a label boundary (host == suffix, or ends in "."+suffix),
		// not a plain string suffix — a scheme/port/userinfo/path fragment can never match, and a
		// leading dot would match under plain suffix matching but not under the label-boundary
		// rule, so it must be rejected here or every such fetch would 403 with no hint why.
		{name: "a scheme is rejected", raw: "http://a.example.com", wantInErr: "invalid host entry"},
		{name: "a port is rejected", raw: "a.example.com:443", wantInErr: "invalid host entry"},
		{name: "userinfo is rejected", raw: "user@a.example.com", wantInErr: "invalid host entry"},
		{name: "a path is rejected", raw: "a.example.com/objects", wantInErr: "invalid host entry"},
		{name: "a leading dot is rejected", raw: ".example.com", wantInErr: "invalid host entry"},
		{name: "a trailing dot is rejected", raw: "example.com.", wantInErr: "invalid host entry"},
		{name: "one bad entry rejects the whole list", raw: "good.example.com,http://bad.example.com", wantInErr: "http://bad.example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := splitHostList(tt.raw)

			if tt.wantInErr != "" {
				requireErrContaining(t, err, FetchAllowedHostsEnv, tt.wantInErr)
				if got != nil {
					t.Errorf("splitHostList(%q) returned %v alongside its error, want nil", tt.raw, got)
				}
				return
			}

			if err != nil {
				t.Fatalf("splitHostList(%q) returned an unexpected error: %v", tt.raw, err)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("splitHostList(%q) = %v, want %v", tt.raw, got, tt.want)
			}
			if tt.wantNil && got != nil {
				t.Errorf("splitHostList(%q) = %#v, want exactly nil", tt.raw, got)
			}
		})
	}
}

// TestLoadWiresFetchAllowedHosts proves splitHostList is actually reached from Load — the table
// above tests the function, this tests the wiring.
func TestLoadWiresFetchAllowedHosts(t *testing.T) {
	t.Parallel()

	t.Run("parsed list reaches the field", func(t *testing.T) {
		t.Parallel()
		cfg := mustLoad(t, []string{progName}, map[string]string{FetchAllowedHostsEnv: "A.example.com, b.example.com"})
		if want := []string{"a.example.com", "b.example.com"}; !slices.Equal(cfg.FetchAllowedHosts, want) {
			t.Errorf("FetchAllowedHosts = %v, want %v", cfg.FetchAllowedHosts, want)
		}
	})

	t.Run("a bad entry fails startup", func(t *testing.T) {
		t.Parallel()
		_, err := Load([]string{progName}, env(map[string]string{FetchAllowedHostsEnv: "s3.example.com:443"}), noFiles(t))
		requireErrContaining(t, err, FetchAllowedHostsEnv, "invalid host entry")
	})
}

// writeBudgetFor and shutdownBudgetFor mirror validate's two budget expressions so a changed
// default cannot silently demote a boundary case into an ordinary passing one. fetchOrS3 is
// whichever of FetchTimeout/S3Timeout is larger — validate takes their max, never their sum.
func writeBudgetFor(readTimeout, fetchOrS3 time.Duration) time.Duration {
	return readTimeout + fetchOrS3 + DefaultSubmitTimeout
}

func shutdownBudgetFor(fetchOrS3 time.Duration) time.Duration {
	return fetchOrS3 + DefaultSubmitTimeout
}

// defaultFetchOrS3 is the max() term as the shipped defaults produce it.
var defaultFetchOrS3 = max(DefaultFetchTimeout, DefaultS3Timeout)

// TestLoadValidatesTimeoutBudgets covers validate's three cross-value checks.
func TestLoadValidatesTimeoutBudgets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		envs map[string]string
		// wantInErr is the env var the error must name; empty means the combination must be accepted.
		wantInErr string
	}{
		{
			name: "the shipped defaults are self-consistent",
			envs: nil,
		},

		{
			name: "read-header timeout may equal the read timeout",
			envs: map[string]string{ReadHeaderTimeoutEnv: DefaultReadTimeout.String()},
		},
		{
			name:      "read-header timeout above the read timeout is rejected",
			envs:      map[string]string{ReadHeaderTimeoutEnv: (DefaultReadTimeout + time.Second).String()},
			wantInErr: ReadHeaderTimeoutEnv,
		},

		{
			name:      "write timeout exactly at the budget is rejected",
			envs:      map[string]string{WriteTimeoutEnv: writeBudgetFor(DefaultReadTimeout, defaultFetchOrS3).String()},
			wantInErr: WriteTimeoutEnv,
		},
		{
			name: "write timeout just above the budget is accepted",
			envs: map[string]string{WriteTimeoutEnv: (writeBudgetFor(DefaultReadTimeout, defaultFetchOrS3) + time.Second).String()},
		},
		{
			// A historical value, kept as a literal on purpose: the old 6m default stopped
			// covering the budget once FetchTimeout/SubmitTimeout became real.
			name:      "the superseded 6m write timeout no longer validates",
			envs:      map[string]string{WriteTimeoutEnv: "6m"},
			wantInErr: WriteTimeoutEnv,
		},
		{
			// At the shipped defaults FetchTimeout == S3Timeout, so dropping S3Timeout from the
			// write budget's max() would pass unnoticed; this case makes S3 strictly the larger
			// term so an under-covering WriteTimeout is unambiguously caught.
			name: "the write budget charges S3 when it is the larger of the two",
			envs: map[string]string{
				FetchTimeoutEnv:  "10s",
				S3TimeoutEnv:     "120s",
				WriteTimeoutEnv:  (writeBudgetFor(DefaultReadTimeout, defaultFetchOrS3) + time.Second).String(),
				ShutdownGraceEnv: "3m",
			},
			wantInErr: WriteTimeoutEnv,
		},

		{
			name:      "shutdown grace exactly at the budget is rejected",
			envs:      map[string]string{ShutdownGraceEnv: shutdownBudgetFor(defaultFetchOrS3).String()},
			wantInErr: ShutdownGraceEnv,
		},
		{
			name: "shutdown grace just above the budget is accepted",
			envs: map[string]string{ShutdownGraceEnv: (shutdownBudgetFor(defaultFetchOrS3) + time.Second).String()},
		},

		{
			name: "S3 timeout does not add to the budget when fetch is larger",
			envs: map[string]string{
				FetchTimeoutEnv:  "60s",
				S3TimeoutEnv:     "10s",
				ShutdownGraceEnv: (shutdownBudgetFor(60*time.Second) + time.Second).String(),
			},
			// Summed, the budget would be 70s+30s and this would fail.
		},
		{
			name: "the larger of fetch and S3 sets the budget",
			envs: map[string]string{
				FetchTimeoutEnv:  "10s",
				S3TimeoutEnv:     "60s",
				ShutdownGraceEnv: (shutdownBudgetFor(60*time.Second) + time.Second).String(),
			},
		},
		{
			// Between the two budgets: passes if validate wrongly took the smaller of the pair,
			// fails for the real max().
			name: "and it really is the larger, not the smaller",
			envs: map[string]string{
				FetchTimeoutEnv:  "10s",
				S3TimeoutEnv:     "60s",
				ShutdownGraceEnv: (shutdownBudgetFor(10*time.Second) + 30*time.Second).String(),
			},
			wantInErr: ShutdownGraceEnv,
		},
		{
			// Pins a real regression: summing the Fetch/S3 terms unconditionally meant a
			// ShutdownGrace pinned before object storage existed refused to start on upgrade alone.
			name: "configuring S3 does not change the budgets",
			envs: map[string]string{
				S3EndpointEnv:    "minio.internal:9000",
				S3BucketEnv:      "print-documents",
				ShutdownGraceEnv: DefaultShutdownGrace.String(),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := Load([]string{progName}, env(tt.envs), noFiles(t))

			if tt.wantInErr == "" {
				if err != nil {
					t.Fatalf("Load(%v) returned an unexpected error: %v", tt.envs, err)
				}
				return
			}
			requireErrContaining(t, err, tt.wantInErr)
		})
	}
}

// ---------------------------------------------------------------------------
// Config-file layer (Stage 3)
// ---------------------------------------------------------------------------

// TestLoadIgnoresTheConfigFileUnlessNamed asserts directly what every env-only case above already
// proves structurally: readFile is never called when PRINT_GATEWAY_CONFIG is unset.
func TestLoadIgnoresTheConfigFileUnlessNamed(t *testing.T) {
	t.Parallel()

	cfg := mustLoad(t, []string{progName}, map[string]string{AuthTokenEnv: "t"})

	if cfg.ConfigFilePath != "" {
		t.Errorf("ConfigFilePath = %q, want empty when %s is unset", cfg.ConfigFilePath, ConfigPathEnv)
	}
	if got := cfg.FileSourcedKeys(); got != nil {
		t.Errorf("FileSourcedKeys() = %v, want nil when no file was read", got)
	}
}

// TestLoadEmptyFileObjectMatchesEnvOnlyStartup is the compatibility guarantee: an active-but-empty
// file ({}) must produce a Config identical, field by field, to no file at all.
func TestLoadEmptyFileObjectMatchesEnvOnlyStartup(t *testing.T) {
	t.Parallel()

	envs := map[string]string{
		AuthTokenEnv:  "t",
		S3EndpointEnv: "minio.internal:9000",
		S3BucketEnv:   "print-documents",
		LogLevelEnv:   "debug",
	}
	without := mustLoad(t, []string{progName}, envs)
	withEmptyFile := mustLoadFile(t, []string{progName}, envs, "{}")

	// Config carries an unexported sources map, so reflect.DeepEqual/== on the whole struct isn't
	// an option — the exported, setting-bearing fields are compared explicitly instead.
	checks := []struct {
		field         string
		without, with any
	}{
		{"Addr", without.Addr, withEmptyFile.Addr},
		{"AddrSource", without.AddrSource, withEmptyFile.AddrSource},
		{"AuthToken", without.AuthToken, withEmptyFile.AuthToken},
		{"ReadHeaderTimeout", without.ReadHeaderTimeout, withEmptyFile.ReadHeaderTimeout},
		{"ReadTimeout", without.ReadTimeout, withEmptyFile.ReadTimeout},
		{"WriteTimeout", without.WriteTimeout, withEmptyFile.WriteTimeout},
		{"IdleTimeout", without.IdleTimeout, withEmptyFile.IdleTimeout},
		{"MaxHeaderBytes", without.MaxHeaderBytes, withEmptyFile.MaxHeaderBytes},
		{"MaxUploadBytes", without.MaxUploadBytes, withEmptyFile.MaxUploadBytes},
		{"MaxJSONBytes", without.MaxJSONBytes, withEmptyFile.MaxJSONBytes},
		{"ShutdownGrace", without.ShutdownGrace, withEmptyFile.ShutdownGrace},
		{"SubmitTimeout", without.SubmitTimeout, withEmptyFile.SubmitTimeout},
		{"FetchTimeout", without.FetchTimeout, withEmptyFile.FetchTimeout},
		{"FetchMaxBytes", without.FetchMaxBytes, withEmptyFile.FetchMaxBytes},
		{"AllowPrivateTargets", without.AllowPrivateTargets, withEmptyFile.AllowPrivateTargets},
		{"S3Endpoint", without.S3Endpoint, withEmptyFile.S3Endpoint},
		{"S3Bucket", without.S3Bucket, withEmptyFile.S3Bucket},
		{"S3Region", without.S3Region, withEmptyFile.S3Region},
		{"S3Insecure", without.S3Insecure, withEmptyFile.S3Insecure},
		{"S3Timeout", without.S3Timeout, withEmptyFile.S3Timeout},
		{"S3MaxBytes", without.S3MaxBytes, withEmptyFile.S3MaxBytes},
		{"S3AccessKey", without.S3AccessKey, withEmptyFile.S3AccessKey},
		{"S3SecretKey", without.S3SecretKey, withEmptyFile.S3SecretKey},
		{"PresignTTL", without.PresignTTL, withEmptyFile.PresignTTL},
		{"LogServer", without.LogServer, withEmptyFile.LogServer},
		{"LogLevel", without.LogLevel, withEmptyFile.LogLevel},
	}
	for _, c := range checks {
		if c.without != c.with {
			t.Errorf("%s differs between no-file and empty-file-object startup: %v vs %v", c.field, c.without, c.with)
		}
	}
	if !slices.Equal(without.FetchAllowedHosts, withEmptyFile.FetchAllowedHosts) {
		t.Errorf("FetchAllowedHosts differs: %v vs %v", without.FetchAllowedHosts, withEmptyFile.FetchAllowedHosts)
	}

	// The one field an active-but-empty file is allowed to change: it was genuinely read.
	if withEmptyFile.ConfigFilePath != testConfigPath {
		t.Errorf("ConfigFilePath = %q, want %q", withEmptyFile.ConfigFilePath, testConfigPath)
	}
	if got := withEmptyFile.FileSourcedKeys(); got != nil {
		t.Errorf("FileSourcedKeys() = %v, want nil for an empty {} file", got)
	}
}

// TestLoadFileValuesWinOverEnv sets an env var AND the file to different values across several
// kinds and asserts the file's value survives. The env values would fail validate if applied
// (9m ReadTimeout), so a precedence bug lets the wrong value through loudly, not by coincidence.
func TestLoadFileValuesWinOverEnv(t *testing.T) {
	t.Parallel()

	envs := map[string]string{
		AuthTokenEnv:      "t",
		ReadTimeoutEnv:    "9m",
		MaxUploadBytesEnv: "123456",
		S3InsecureEnv:     "true",
		S3EndpointEnv:     "minio.internal:9000",
	}
	fileBody := `{
		"resource/printgateway": {
			"timeouts": {"read": "4m"},
			"limits": {"maxUploadBytes": 98765},
			"objectStore": {"insecure": false}
		},
		"resource/file_storage": {"host": "s3.example.com:9000"}
	}`
	cfg := mustLoadFile(t, []string{progName}, envs, fileBody)

	if cfg.ReadTimeout != 4*time.Minute {
		t.Errorf("ReadTimeout = %s, want 4m (the file's value)", cfg.ReadTimeout)
	}
	if cfg.MaxUploadBytes != 98765 {
		t.Errorf("MaxUploadBytes = %d, want 98765 (the file's value)", cfg.MaxUploadBytes)
	}
	if cfg.S3Insecure != false {
		t.Errorf("S3Insecure = %v, want false (the file's value)", cfg.S3Insecure)
	}
	if cfg.S3Endpoint != "s3.example.com:9000" {
		t.Errorf("S3Endpoint = %q, want %q (the file's value)", cfg.S3Endpoint, "s3.example.com:9000")
	}
}

// fileOverrideCase is one row of the anti-mispairing table used by
// TestLoadFileOverridesArePairedCorrectly: a distinct value for when it's the ONE setting the
// file supplies, and a distinct value for when it's left to its own env var instead.
type fileOverrideCase struct {
	envVar    string
	jsonPath  string
	envRaw    string
	envWant   any
	fileValue any
	fileWant  any
	get       func(Config) any
}

// fileOverrideRows mirrors mergeFileConfig's own merge tables one row per setting. allowPrivateTargets
// has no row deliberately: it has no file field to test.
var fileOverrideRows = []fileOverrideCase{
	{ReadHeaderTimeoutEnv, "resource/printgateway.timeouts.readHeader", "11s", 11 * time.Second, "13s", 13 * time.Second, func(c Config) any { return c.ReadHeaderTimeout }},
	{ReadTimeoutEnv, "resource/printgateway.timeouts.read", "4m", 4 * time.Minute, "3m45s", 3*time.Minute + 45*time.Second, func(c Config) any { return c.ReadTimeout }},
	{WriteTimeoutEnv, "resource/printgateway.timeouts.write", "9m", 9 * time.Minute, "10m", 10 * time.Minute, func(c Config) any { return c.WriteTimeout }},
	{IdleTimeoutEnv, "resource/printgateway.timeouts.idle", "61s", 61 * time.Second, "70s", 70 * time.Second, func(c Config) any { return c.IdleTimeout }},
	{ShutdownGraceEnv, "resource/printgateway.timeouts.shutdownGrace", "3m", 3 * time.Minute, "4m", 4 * time.Minute, func(c Config) any { return c.ShutdownGrace }},
	{SubmitTimeoutEnv, "resource/printgateway.timeouts.submit", "31s", 31 * time.Second, "35s", 35 * time.Second, func(c Config) any { return c.SubmitTimeout }},
	{FetchTimeoutEnv, "resource/printgateway.fetch.timeout", "62s", 62 * time.Second, "70s", 70 * time.Second, func(c Config) any { return c.FetchTimeout }},
	{S3TimeoutEnv, "resource/printgateway.objectStore.timeout", "63s", 63 * time.Second, "75s", 75 * time.Second, func(c Config) any { return c.S3Timeout }},
	{PresignTTLEnv, "resource/printgateway.objectStore.presignTtl", "16m", 16 * time.Minute, "20m", 20 * time.Minute, func(c Config) any { return c.PresignTTL }},

	{MaxHeaderBytesEnv, "resource/printgateway.limits.maxHeaderBytes", "8192", 8192, 16384, 16384, func(c Config) any { return c.MaxHeaderBytes }},
	{FetchMaxBytesEnv, "resource/printgateway.fetch.maxBytes", "9000000", int64(9000000), 9100000, int64(9100000), func(c Config) any { return c.FetchMaxBytes }},
	{S3MaxBytesEnv, "resource/printgateway.objectStore.maxBytes", "9500000", int64(9500000), 9600000, int64(9600000), func(c Config) any { return c.S3MaxBytes }},
	{MaxUploadBytesEnv, "resource/printgateway.limits.maxUploadBytes", "9800000", int64(9800000), 9900000, int64(9900000), func(c Config) any { return c.MaxUploadBytes }},
	{MaxJSONBytesEnv, "resource/printgateway.limits.maxJsonBytes", "4096", int64(4096), 5000, int64(5000), func(c Config) any { return c.MaxJSONBytes }},

	{S3InsecureEnv, "resource/printgateway.objectStore.insecure", "false", false, true, true, func(c Config) any { return c.S3Insecure }},

	{S3BucketEnv, "resource/printgateway.objectStore.bucket", "env-bucket", "env-bucket", "file-bucket", "file-bucket", func(c Config) any { return c.S3Bucket }},
	{S3RegionEnv, "resource/printgateway.objectStore.region", "env-region", "env-region", "file-region", "file-region", func(c Config) any { return c.S3Region }},

	// These three live under the shared resource/file_storage block (endpoint/credentials are
	// cross-cutting infrastructure, not this service's own setting).
	{S3EndpointEnv, "resource/file_storage.host", "env-endpoint.example:9000", "env-endpoint.example:9000", "file-endpoint.example:9000", "file-endpoint.example:9000", func(c Config) any { return c.S3Endpoint }},
	{S3AccessKeyEnv, "resource/file_storage.s3-user", "env-access-key", "env-access-key", "file-access-key", "file-access-key", func(c Config) any { return c.S3AccessKey }},
	{S3SecretKeyEnv, "resource/file_storage.s3-password", "env-secret-key", "env-secret-key", "file-secret-key", "file-secret-key", func(c Config) any { return c.S3SecretKey }},

	// LogServerEnv is the one row fileWith can't build: resource/log splits Config.LogServer's
	// single "host:port" string into two file fields. fileValue is unused (see fileBodyFor).
	{LogServerEnv, "resource/log", "env-log:5044", "env-log:5044", nil, "file-log:5044", func(c Config) any { return c.LogServer }},
	{LogLevelEnv, "resource/printgateway.logLevel", "debug", "debug", "warn", "warn", func(c Config) any { return c.LogLevel }},
}

// fileBodyFor builds the single-setting file body for one fileOverrideRows case; resource/log is
// the one exception to the generic dotted-path mapping (see the LogServerEnv row's comment).
func fileBodyFor(c fileOverrideCase) string {
	if c.jsonPath == "resource/log" {
		return `{"resource/log":{"host":"file-log","port":"5044"}}`
	}
	return fileWith(c.jsonPath, c.fileValue)
}

func TestLoadFileOverridesArePairedCorrectly(t *testing.T) {
	t.Parallel()

	for _, current := range fileOverrideRows {
		t.Run(current.envVar, func(t *testing.T) {
			t.Parallel()

			// current.envVar also gets its own env value here, which is what proves file beats
			// env (not merely file beats default) for the row under test.
			envs := map[string]string{AuthTokenEnv: "t"}
			for _, other := range fileOverrideRows {
				envs[other.envVar] = other.envRaw
			}

			cfg := mustLoadFile(t, []string{progName}, envs, fileBodyFor(current))

			if got := current.get(cfg); got != current.fileWant {
				t.Errorf("with only %s set via the file: %s = %v, want %v", current.jsonPath, current.envVar, got, current.fileWant)
			}
			if want := testConfigPath + ":" + current.jsonPath; cfg.Source(current.envVar) != want {
				t.Errorf("with only %s set via the file: Source(%s) = %q, want %q",
					current.jsonPath, current.envVar, cfg.Source(current.envVar), want)
			}
			for _, other := range fileOverrideRows {
				if other.envVar == current.envVar {
					continue
				}
				if got := other.get(cfg); got != other.envWant {
					t.Errorf("with %s set via the file: %s (left to its own env var) = %v, want %v",
						current.jsonPath, other.envVar, got, other.envWant)
				}
				if got := cfg.Source(other.envVar); got != other.envVar {
					t.Errorf("with %s set via the file: Source(%s) = %q, want the bare env var name (the file never named it)",
						current.jsonPath, other.envVar, got)
				}
			}
		})
	}
}

// TestLoadFileZeroValuesAreRejected proves the file layer applies the same positivity rule env
// already does, through the shared parseDuration/validateBytes64/fileBytesInt helpers.
func TestLoadFileZeroValuesAreRejected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		jsonPath  string
		value     any
		wantInErr string
	}{
		{"a zero duration", "resource/printgateway.timeouts.read", "0s", "must be positive"},
		{"a negative duration", "resource/printgateway.timeouts.read", "-5s", "must be positive"},
		{"a zero int64 byte size", "resource/printgateway.limits.maxUploadBytes", 0, "invalid byte size"},
		{"a negative int64 byte size", "resource/printgateway.limits.maxUploadBytes", -1, "invalid byte size"},
		{"a zero int byte size", "resource/printgateway.limits.maxHeaderBytes", 0, "invalid byte size"},
		{"a negative int byte size", "resource/printgateway.limits.maxHeaderBytes", -1, "invalid byte size"},
		{"a zero fetch byte size", "resource/printgateway.fetch.maxBytes", 0, "invalid byte size"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			envs := map[string]string{AuthTokenEnv: "t", ConfigPathEnv: testConfigPath}
			_, err := Load([]string{progName}, env(envs), files(map[string]string{testConfigPath: fileWith(tt.jsonPath, tt.value)}))
			requireErrContaining(t, err, testConfigPath, tt.jsonPath, tt.wantInErr)
		})
	}
}

// TestLoadFileRejectsSecretAndEnvOnlyKeys proves allowPrivateTargets (which has no field anywhere
// in fileConfig) and secret/bootstrap keys are rejected as an "unknown field", not silently ignored.
func TestLoadFileRejectsSecretAndEnvOnlyKeys(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{"allowPrivateTargets at the top level", `{"allowPrivateTargets": true}`},
		{"allowPrivateTargets under resource/printgateway", `{"resource/printgateway": {"allowPrivateTargets": true}}`},
		{"a bare token key", `{"token": "shh"}`},
		{"an s3AccessKey key", `{"s3AccessKey": "AKIA..."}`},
		{"a vaultAddr key", `{"vaultAddr": "http://vault:8200"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			envs := map[string]string{AuthTokenEnv: "t", ConfigPathEnv: testConfigPath}
			_, err := Load([]string{progName}, env(envs), files(map[string]string{testConfigPath: tt.body}))
			requireErrContaining(t, err, "unknown field")
		})
	}
}

// TestLoadFileAllowedHosts covers fetch.allowedHosts's three behaviors: an explicit empty array
// suppresses the env allowlist, a non-empty array normalizes like the env-sourced list, and a bad
// entry's error names the JSON path, not the env var.
func TestLoadFileAllowedHosts(t *testing.T) {
	t.Parallel()

	t.Run("an explicit empty array suppresses the env allowlist", func(t *testing.T) {
		t.Parallel()
		cfg := mustLoadFile(t, []string{progName}, map[string]string{
			AuthTokenEnv:         "t",
			FetchAllowedHostsEnv: "s3.example.com",
		}, `{"resource/printgateway": {"fetch": {"allowedHosts": []}}}`)
		if cfg.FetchAllowedHosts != nil {
			t.Errorf("FetchAllowedHosts = %v, want nil (file explicitly says no allowlist)", cfg.FetchAllowedHosts)
		}
	})

	t.Run("a non-empty array normalizes like the env-sourced list", func(t *testing.T) {
		t.Parallel()
		cfg := mustLoadFile(t, []string{progName}, map[string]string{AuthTokenEnv: "t"}, `{"resource/printgateway": {"fetch": {"allowedHosts": ["S3.Example.COM"]}}}`)
		if want := []string{"s3.example.com"}; !slices.Equal(cfg.FetchAllowedHosts, want) {
			t.Errorf("FetchAllowedHosts = %v, want %v", cfg.FetchAllowedHosts, want)
		}
	})

	t.Run("a bad entry names the JSON path, not the env var", func(t *testing.T) {
		t.Parallel()
		envs := map[string]string{AuthTokenEnv: "t", ConfigPathEnv: testConfigPath}
		_, err := Load([]string{progName}, env(envs), files(map[string]string{
			testConfigPath: `{"resource/printgateway": {"fetch": {"allowedHosts": ["s3.example.com:443"]}}}`,
		}))
		requireErrContaining(t, err, testConfigPath+":resource/printgateway.fetch.allowedHosts", "invalid host entry")
		if err != nil && strings.Contains(err.Error(), FetchAllowedHostsEnv) {
			t.Errorf("error %q names the env var %s; a file-sourced value must be labeled by its JSON path instead", err, FetchAllowedHostsEnv)
		}
	})
}

// TestLoadFileEmptyStringSuppressesAnEnvValue proves "" is a deliberate, env-suppressing value for
// objectStore.endpoint — unlike service.logLevel.
func TestLoadFileEmptyStringSuppressesAnEnvValue(t *testing.T) {
	t.Parallel()

	cfg := mustLoadFile(t, []string{progName}, map[string]string{
		AuthTokenEnv:  "t",
		S3EndpointEnv: "minio.internal:9000",
	}, `{"resource/file_storage": {"host": ""}}`)

	if cfg.S3Endpoint != "" {
		t.Errorf("S3Endpoint = %q, want empty (the file explicitly suppressed the env value)", cfg.S3Endpoint)
	}
}

// TestLoadFileEmptyStringSuppressesAnEnvCredential is the security-relevant counterpart for a
// credential specifically: blanking objectStore.accessKey in the file must stop a stale env-sourced
// key from being used. See TestResolveS3CredentialsFileSuppressionFailsClosed in package secrets
// for the end-to-end assertion; this only proves the Config field.
func TestLoadFileEmptyStringSuppressesAnEnvCredential(t *testing.T) {
	t.Parallel()

	cfg := mustLoadFile(t, []string{progName}, map[string]string{
		AuthTokenEnv:   "t",
		S3AccessKeyEnv: "stale-env-access-key",
	}, `{"resource/file_storage": {"s3-user": ""}}`)

	if cfg.S3AccessKey != "" {
		t.Errorf("S3AccessKey = %q, want empty (the file explicitly suppressed the stale env credential)", cfg.S3AccessKey)
	}
}

// TestLoadFileLogLevelEmptyStringIsRejected is the one file-sourced string where "" is a startup
// error rather than a suppression: an empty LogLevel is meaningless to logger.SetLogLevel.
func TestLoadFileLogLevelEmptyStringIsRejected(t *testing.T) {
	t.Parallel()

	envs := map[string]string{AuthTokenEnv: "t", ConfigPathEnv: testConfigPath}
	_, err := Load([]string{progName}, env(envs), files(map[string]string{
		testConfigPath: `{"resource/printgateway": {"logLevel": ""}}`,
	}))
	requireErrContaining(t, err, testConfigPath+":resource/printgateway.logLevel", "must not be empty")
}

// TestLoadFileLogServerCombinesHostAndPort proves mergeFileConfig's resource/log handling: two
// file fields combine into Config.LogServer's "host:port" string, suppressed to "" only when BOTH
// sides resolve empty, and otherwise passed through verbatim (even lopsided) for
// secrets.ResolveLogServer to accept or reject.
func TestLoadFileLogServerCombinesHostAndPort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "both host and port present combine into host:port",
			body: `{"resource/log": {"host": "logstash.internal", "port": "514"}}`,
			want: "logstash.internal:514",
		},
		{
			name: "both explicitly empty suppresses LogServer",
			body: `{"resource/log": {"host": "", "port": ""}}`,
			want: "",
		},
		{
			name: "host alone still combines, with an empty port half",
			body: `{"resource/log": {"host": "logstash.internal"}}`,
			want: "logstash.internal:",
		},
		{
			name: "port alone still combines, with an empty host half",
			body: `{"resource/log": {"port": "514"}}`,
			want: ":514",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := mustLoadFile(t, []string{progName}, map[string]string{
				AuthTokenEnv: "t",
				LogServerEnv: "stale-env-log:9999",
			}, tt.body)

			if cfg.LogServer != tt.want {
				t.Errorf("LogServer = %q, want %q", cfg.LogServer, tt.want)
			}
			if want := testConfigPath + ":resource/log"; cfg.Source(LogServerEnv) != want {
				t.Errorf("Source(%s) = %q, want %q", LogServerEnv, cfg.Source(LogServerEnv), want)
			}
		})
	}
}

// TestLoadValidateErrorsNameTheFileSource is the provenance proof: a file-sourced value that trips
// validate's cross-field budget check must be labeled by the file's "<path>:<jsonPath>", while an
// untouched setting on the other side of the same message still reads by its bare env var name.
func TestLoadValidateErrorsNameTheFileSource(t *testing.T) {
	t.Parallel()

	// Default ReadTimeout (5m) + max(FetchTimeout,S3Timeout) (60s) + SubmitTimeout (30s) = 6.5m;
	// a file-sourced WriteTimeout of 1m must trip validate's write-budget check.
	envs := map[string]string{AuthTokenEnv: "t", ConfigPathEnv: testConfigPath}
	_, err := Load([]string{progName}, env(envs), files(map[string]string{
		testConfigPath: fileWith("resource/printgateway.timeouts.write", "1m"),
	}))

	requireErrContaining(t, err, testConfigPath+":resource/printgateway.timeouts.write", ReadTimeoutEnv)
	if err != nil && strings.Contains(err.Error(), WriteTimeoutEnv) {
		t.Errorf("error %q names the bare env var %s for a file-sourced value; want only the file label", err, WriteTimeoutEnv)
	}
}

// TestConfigSourceFallsBackToTheEnvVarName is the nil-map-safety proof: on a zero-value Config,
// Source must return the name unchanged rather than panicking on a nil map read.
func TestConfigSourceFallsBackToTheEnvVarName(t *testing.T) {
	t.Parallel()

	var cfg Config
	if got := cfg.Source("SOME_ENV"); got != "SOME_ENV" {
		t.Errorf(`Source("SOME_ENV") = %q, want "SOME_ENV" unchanged`, got)
	}
}

// TestLoadFileExplicitNullBehavesAsAbsent proves a JSON `null` for a scalar decodes to the same
// nil pointer as an absent key, leaving that setting alone rather than zeroing it or erroring.
func TestLoadFileExplicitNullBehavesAsAbsent(t *testing.T) {
	t.Parallel()

	cfg := mustLoadFile(t, []string{progName}, map[string]string{AuthTokenEnv: "t"}, `{"resource/printgateway": {"timeouts": {"write": null}}}`)

	if cfg.WriteTimeout != DefaultWriteTimeout {
		t.Errorf("WriteTimeout = %s, want the default %s when the file sets it to null", cfg.WriteTimeout, DefaultWriteTimeout)
	}
	if got := cfg.FileSourcedKeys(); got != nil {
		t.Errorf("FileSourcedKeys() = %v, want nil: a null value must not be recorded as file-sourced", got)
	}
}

func TestLoadFileMissingPathErrors(t *testing.T) {
	t.Parallel()

	envs := map[string]string{AuthTokenEnv: "t", ConfigPathEnv: "/nope/does-not-exist.json"}
	_, err := Load([]string{progName}, env(envs), files(nil))
	requireErrContaining(t, err, ConfigPathEnv, "/nope/does-not-exist.json")
}

func TestLoadFileMalformedJSONErrors(t *testing.T) {
	t.Parallel()

	envs := map[string]string{AuthTokenEnv: "t", ConfigPathEnv: testConfigPath}
	_, err := Load([]string{progName}, env(envs), files(map[string]string{testConfigPath: `{not valid json`}))
	requireErrContaining(t, err, testConfigPath)
}

// TestLoadFileTrailingContentErrors proves a second concatenated JSON value is rejected, not
// silently discarded.
func TestLoadFileTrailingContentErrors(t *testing.T) {
	t.Parallel()

	envs := map[string]string{AuthTokenEnv: "t", ConfigPathEnv: testConfigPath}
	_, err := Load([]string{progName}, env(envs), files(map[string]string{testConfigPath: `{} {}`}))
	requireErrContaining(t, err, testConfigPath, "exactly one JSON value")
}

// TestLoadFileTypeMismatchNamesTheJSONPathNotAGoType proves a type-mismatched value is rejected in
// JSON vocabulary, never a Go type name a reader has no way to recognize.
func TestLoadFileTypeMismatchNamesTheJSONPathNotAGoType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		fileContent string
		wantIn      []string
		wantNotIn   string
	}{
		{"a string where a byte count is expected", `{"resource/printgateway":{"limits":{"maxUploadBytes":"64KiB"}}}`, []string{"maxUploadBytes"}, "config."},
		{"a fractional number where a byte count is expected", `{"resource/printgateway":{"limits":{"maxUploadBytes":65536.5}}}`, []string{"maxUploadBytes"}, "config."},
		{"a scalar where a group object is expected", `{"resource/printgateway":{"timeouts":"10s"}}`, []string{"timeouts"}, "config."},
		{"a top-level array instead of an object", `[1,2]`, []string{"JSON object"}, "config."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			envs := map[string]string{AuthTokenEnv: "t", ConfigPathEnv: testConfigPath}
			_, err := Load([]string{progName}, env(envs), files(map[string]string{testConfigPath: tt.fileContent}))
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			for _, want := range tt.wantIn {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
			if strings.Contains(err.Error(), tt.wantNotIn) {
				t.Errorf("error %q leaks a Go type name (%q) instead of JSON vocabulary", err, tt.wantNotIn)
			}
		})
	}
}

// TestLoadFileEmptyOrNullBodyErrors proves a zero-byte file and a literal JSON `null` document
// both fail with a diagnosable message rather than a bare decoder "EOF".
func TestLoadFileEmptyOrNullBodyErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		fileContent string
		wantInErr   string
	}{
		{"an empty file", "", "empty"},
		{"a whitespace-only file", "   \n\t  ", "empty"},
		{"a literal JSON null", "null", "null"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			envs := map[string]string{AuthTokenEnv: "t", ConfigPathEnv: testConfigPath}
			_, err := Load([]string{progName}, env(envs), files(map[string]string{testConfigPath: tt.fileContent}))
			requireErrContaining(t, err, testConfigPath, tt.wantInErr)
		})
	}
}

// TestLoadFileInvalidAddrErrors proves a malformed service.addr fails fast at startup, labeled
// with the file source, rather than reaching net.Listen and failing after "listening" was logged.
func TestLoadFileInvalidAddrErrors(t *testing.T) {
	t.Parallel()

	envs := map[string]string{AuthTokenEnv: "t", ConfigPathEnv: testConfigPath}
	_, err := Load([]string{progName}, env(envs), files(map[string]string{testConfigPath: fileWith("resource/printgateway.addr", "not-a-valid-address")}))
	requireErrContaining(t, err, testConfigPath, "resource/printgateway.addr", "not-a-valid-address")
}

// TestLoadAddrPrecedenceWithFile pins the one exception to "file always wins over env": Addr's
// precedence stays argv -> file -> default.
func TestLoadAddrPrecedenceWithFile(t *testing.T) {
	t.Parallel()

	t.Run("argv wins over the file", func(t *testing.T) {
		t.Parallel()
		cfg := mustLoadFile(t, []string{progName, "0.0.0.0:7777"}, map[string]string{AuthTokenEnv: "t"}, fileWith("resource/printgateway.addr", "0.0.0.0:8888"))
		if cfg.Addr != "0.0.0.0:7777" {
			t.Errorf("Addr = %q, want the argv value %q", cfg.Addr, "0.0.0.0:7777")
		}
		if cfg.AddrSource != AddrSourceArgv {
			t.Errorf("AddrSource = %q, want %q", cfg.AddrSource, AddrSourceArgv)
		}
	})

	t.Run("the file wins over the default when there is no argv", func(t *testing.T) {
		t.Parallel()
		cfg := mustLoadFile(t, []string{progName}, map[string]string{AuthTokenEnv: "t"}, fileWith("resource/printgateway.addr", "0.0.0.0:8888"))
		if cfg.Addr != "0.0.0.0:8888" {
			t.Errorf("Addr = %q, want the file's value %q", cfg.Addr, "0.0.0.0:8888")
		}
		if cfg.AddrSource != AddrSourceFile {
			t.Errorf("AddrSource = %q, want %q", cfg.AddrSource, AddrSourceFile)
		}
	})

	t.Run("neither argv nor file leaves the default", func(t *testing.T) {
		t.Parallel()
		cfg := mustLoadFile(t, []string{progName}, map[string]string{AuthTokenEnv: "t"}, "{}")
		if cfg.Addr != DefaultAddr {
			t.Errorf("Addr = %q, want the default %q", cfg.Addr, DefaultAddr)
		}
		if cfg.AddrSource != AddrSourceDefault {
			t.Errorf("AddrSource = %q, want %q", cfg.AddrSource, AddrSourceDefault)
		}
	})
}
