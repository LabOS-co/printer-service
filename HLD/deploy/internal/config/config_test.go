package config

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// env builds Load's getenv parameter from a map, so no case touches the real
// process environment and every one of them can run in parallel. This is the
// whole reason Load takes getenv rather than calling os.Getenv itself.
func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// progName stands in for os.Args[0]: Load reads the address from args[1], so
// a run with no override still has one element.
const progName = "printgateway"

// mustLoad fails the test if Load errors. Used by the cases that are about
// what a value becomes, not about rejection.
func mustLoad(t *testing.T, args []string, m map[string]string) Config {
	t.Helper()
	cfg, err := Load(args, env(m))
	if err != nil {
		t.Fatalf("Load(%v, %v) returned an unexpected error: %v", args, m, err)
	}
	return cfg
}

// requireErrContaining asserts err is non-nil and its text mentions every
// given substring. Naming the offending variable is the entire point of Load
// returning an error rather than silently keeping a default, so the variable
// name is asserted, not just that something failed.
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

	// One row per default so a changed constant fails by name instead of
	// showing up as an opaque struct diff. %T is printed alongside each
	// value because these are compared as `any`: if a default's type ever
	// changed (DefaultMaxHeaderBytes becoming a typed int64, say) the
	// comparison would fail on dynamic type while both sides printed
	// identically, which is baffling without the type.
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

	// Asserted as nil specifically, not merely empty: an empty-but-non-nil
	// allowlist would mean the same thing to fetch.hostAllowed today, but
	// "no allowlist configured" is the meaningful default and nil is how
	// Load expresses it.
	if cfg.FetchAllowedHosts != nil {
		t.Errorf("default FetchAllowedHosts = %v, want nil (no allowlist)", cfg.FetchAllowedHosts)
	}

	// Every string setting is unset by default; a stray default on any of
	// them would silently engage Vault or S3 in a deployment that configured
	// neither.
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

// TestDefaultAddrIsLoopback pins the property, not just the constant: the
// service authenticates with a shared token, and the listen address is what
// the README calls the actual first line of defence. A default that bound
// every interface would give that away without any code change looking wrong.
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
			// Pinned as current behavior, NOT endorsed: an empty args[1]
			// (a Nomad job spec interpolating an unset variable) is taken
			// verbatim, and net/http resolves Addr "" to ":http" — port 80
			// on EVERY interface, which is precisely what
			// TestDefaultAddrIsLoopback exists to prevent. Recorded as a
			// production follow-up rather than fixed here, since this stage
			// is test-only; if Load is later changed to reject it, this row
			// is the one to update.
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

// TestLoadSecretStoreURLPrecedence covers the F1 review's second finding: a
// standard labOS Nomad job spec injects VAULT_ADDR and nothing else, so
// reading only SECRET_STORE_URL left Vault silently never contacted, logged
// identically to "no Vault on purpose". The precedence mirrors
// go-packages/settings.getSecretStoreSettings exactly.
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

// The three setting tables below are package-level because two or more tests
// share each of them. They are read-only: parallel subtests iterate them
// concurrently, so anything that appended to one at runtime would be a data
// race. Tables used by a single test stay local to it (see apperr_test.go).
//
// durationSettings pairs every duration env var with the field it must reach
// and the default it must otherwise keep. The override values are all
// distinct, so a value landing in the wrong field is recognizable on sight;
// each is also a legal value for its own setting, so the case is exercising
// the pairing rather than validate. A mispairing surfaces either as the
// wrong field holding the value or — where the swapped pair happens to break
// a budget inequality — as an unexpected error from Load; both fail here,
// which is what matters.
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

// TestLoadDurationOverridesArePairedCorrectly is the guard on the one thing
// Load's override table exists to make impossible. Writing ReadTimeoutEnv
// into &cfg.WriteTimeout would compile, vet clean, and be invisible in
// review; here each variable is set on its own and every OTHER field is
// asserted to still hold its default, so a mispairing fails twice — once on
// the field that did not change, once on the field that changed and should
// not have.
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

	// Every duration goes through the same overrideDuration, and the table
	// above is what proves all nine are read and assigned, so one variable
	// is enough to exercise the parse and positivity branches.
	values := []struct {
		name      string
		value     string
		wantInErr string
	}{
		{"not a duration at all", "soon", "invalid duration"},
		{"number with no unit", "30", "invalid duration"},
		{"whitespace", " ", "invalid duration"},
		// net/http guards every timeout with `if d > 0`, so a non-positive
		// value does not mean "very short", it means no timeout at all —
		// silently restoring the exact exposure these settings exist to close.
		{"zero", "0", "must be positive"},
		{"negative", "-5s", "must be positive"},
	}

	for _, tt := range values {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := Load([]string{progName}, env(map[string]string{SubmitTimeoutEnv: tt.value}))
			requireErrContaining(t, err, SubmitTimeoutEnv, tt.wantInErr)
		})
	}
}

// byteSizeSettings pairs each byte-size env var with its field. The three go
// through two different helpers (overrideBytes for the plain int,
// overrideBytes64 for the two int64 fields), so each is exercised on its own
// rather than one standing in for all three.
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

// TestLoadByteSizeOverrideAcceptsLargeValues documents the intended range of
// the two int64 byte-size settings.
//
// It does NOT prove the int64-ness: on any 64-bit builder Go's int is also
// 64 bits, so strconv.Atoi accepts these values too and swapping
// overrideBytes64 back to overrideBytes would still pass here. The 32-bit
// property is enforced by the field types (FetchMaxBytes/S3MaxBytes are
// declared int64), not by this test.
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

	// "64KiB" is the specific mistake the README calls out: these are plain
	// byte counts, not Go duration-style suffixed values, and accepting the
	// suffixed spelling silently would be worse than rejecting it.
	values := []string{"64KiB", "abc", "1.5", "0", "-1", " "}

	for _, s := range byteSizeSettings {
		for _, v := range values {
			t.Run(s.envVar+"="+v, func(t *testing.T) {
				t.Parallel()
				_, err := Load([]string{progName}, env(map[string]string{s.envVar: v}))
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

// TestOverrideBoolAppliesAnExplicitFalse exists because TestLoadBoolOverrides
// structurally cannot catch a dropped false. Both bool settings default to
// false today, so its nine `false` rows only prove those spellings are
// ACCEPTED, not that the parsed value is APPLIED — an overrideBool that
// returned def whenever ParseBool yielded false passes that test unchanged
// (confirmed by mutation). Exercising the helper directly against a true
// default is what makes the false path observable, and it is what will keep
// the first bool setting that defaults to true honest.
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

	// "yes"/"on"/"2" all read as true to a human and are rejected by
	// strconv.ParseBool. Silently defaulting them to false would leave an
	// operator who wrote ALLOW_PRIVATE_TARGETS=yes believing the opposite of
	// what the service is doing.
	for _, s := range boolSettings {
		for _, v := range []string{"yes", "on", "2", "maybe"} {
			t.Run(s.envVar+"="+v, func(t *testing.T) {
				t.Parallel()
				_, err := Load([]string{progName}, env(map[string]string{s.envVar: v}))
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
		// Deliberately not validated here: doing so would pull logrus into a
		// package that is otherwise stdlib-only. main.go's SetLogLevel call
		// is where a bad level is caught and logged.
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

// TestLoadPassesThroughStringSettings covers the plain getenv-to-field
// assignments. A mispairing among these is invisible at runtime — the field
// simply stays empty and the feature silently does not engage — so each is
// asserted by name against a value unique to it.
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
		// wantNil distinguishes a nil result from an empty-but-non-nil one.
		// slices.Equal treats those as equal, so want alone cannot express
		// it, and "no allowlist" is a meaningful state worth pinning exactly.
		wantNil   bool
		wantInErr string
	}{
		{name: "empty means no allowlist", raw: "", want: nil, wantNil: true},
		{name: "single host", raw: "s3.example.com", want: []string{"s3.example.com"}},
		{name: "several hosts", raw: "a.example.com,b.example.com", want: []string{"a.example.com", "b.example.com"}},
		{name: "surrounding whitespace is trimmed", raw: " a.example.com , b.example.com ", want: []string{"a.example.com", "b.example.com"}},
		{name: "entries are lowercased", raw: "S3.Example.COM", want: []string{"s3.example.com"}},
		// A stray comma should not be a startup failure for a setting whose
		// empty value is itself a valid, meaningful choice.
		{name: "empty entries are dropped", raw: "a.example.com,,b.example.com", want: []string{"a.example.com", "b.example.com"}},
		{name: "leading and trailing commas are dropped", raw: ",a.example.com,", want: []string{"a.example.com"}},
		{name: "only commas yields no allowlist", raw: ",,,", want: nil, wantNil: true},

		// fetch.hostAllowed matches on a LABEL BOUNDARY —
		// `host == suffix || strings.HasSuffix(host, "."+suffix)`
		// (guard.go) — not on a plain string suffix. That is exactly why
		// each of these has to be rejected at startup: with a scheme, port,
		// userinfo or path still attached, neither arm can ever match a
		// hostname; and a leading dot turns the second arm into a search for
		// "..example.com", which no hostname contains. (Under plain suffix
		// matching a leading-dot entry WOULD match, so the boundary rule is
		// what makes this rejection necessary rather than cosmetic.) Left
		// unrejected, every file_url fetch would 403 with no hint why.
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

// TestLoadWiresFetchAllowedHosts proves splitHostList is actually reached
// from Load — the table above tests the function, this tests the wiring.
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
		_, err := Load([]string{progName}, env(map[string]string{FetchAllowedHostsEnv: "s3.example.com:443"}))
		requireErrContaining(t, err, FetchAllowedHostsEnv, "invalid host entry")
	})
}

// writeBudgetFor and shutdownBudgetFor mirror validate's two budget
// expressions, derived from the same constants rather than hand-computed, so
// that a changed default cannot silently demote a boundary case below into
// an ordinary inside-the-limit case that still passes. fetchOrS3 is whichever
// of FetchTimeout/S3Timeout the case leaves larger — validate takes their
// max, never their sum.
func writeBudgetFor(readTimeout, fetchOrS3 time.Duration) time.Duration {
	return readTimeout + fetchOrS3 + DefaultSubmitTimeout
}

func shutdownBudgetFor(fetchOrS3 time.Duration) time.Duration {
	return fetchOrS3 + DefaultSubmitTimeout
}

// defaultFetchOrS3 is the max() term as the shipped defaults produce it.
var defaultFetchOrS3 = max(DefaultFetchTimeout, DefaultS3Timeout)

// TestLoadValidatesTimeoutBudgets covers validate's three cross-value checks.
// Each value here is individually plausible and only the combination is
// wrong, which is exactly the class of mistake nothing downstream reports:
// net/http just applies whichever deadline expires first.
func TestLoadValidatesTimeoutBudgets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		envs map[string]string
		// wantInErr is the env var the error must name; empty means the
		// combination must be accepted.
		wantInErr string
	}{
		{
			name: "the shipped defaults are self-consistent",
			envs: nil,
		},

		// ReadHeaderTimeout <= ReadTimeout. A header deadline that outlives
		// the whole-request deadline can never be the one that fires, so
		// setting it is a no-op the operator will believe took effect.
		{
			name: "read-header timeout may equal the read timeout",
			envs: map[string]string{ReadHeaderTimeoutEnv: DefaultReadTimeout.String()},
		},
		{
			name:      "read-header timeout above the read timeout is rejected",
			envs:      map[string]string{ReadHeaderTimeoutEnv: (DefaultReadTimeout + time.Second).String()},
			wantInErr: ReadHeaderTimeoutEnv,
		},

		// WriteTimeout > ReadTimeout + max(Fetch,S3) + Submit. The write
		// deadline is armed when headers are parsed, so it has to cover the
		// body read, the download, and lp — not just the response write. Too
		// low and a slow request is accepted, printed, and THEN fails on the
		// response write, so the caller retries and the document prints twice.
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
			// The A3-era 6m default stopped covering the budget once
			// FetchTimeout and SubmitTimeout became real; this is the
			// regression that raised it to 8m. Kept as a literal on purpose:
			// it is a historical value, not a derived one.
			name:      "the superseded 6m write timeout no longer validates",
			envs:      map[string]string{WriteTimeoutEnv: "6m"},
			wantInErr: WriteTimeoutEnv,
		},
		{
			// The shutdown-grace cases below pin max() for their own budget,
			// but nothing pinned it for the WRITE budget: dropping S3Timeout
			// from validate's writeBudget max() passed the entire suite
			// (confirmed by mutation), because at the defaults FetchTimeout
			// and S3Timeout are equal and therefore indistinguishable. With
			// S3Timeout the larger of the two, a WriteTimeout that cannot
			// cover an s3_key download would then be ACCEPTED — the request
			// is read, downloaded, printed, and only then fails on the
			// response write, which is the duplicate print this check exists
			// to prevent. ShutdownGrace is raised here so the write check is
			// unambiguously the one that must fire.
			name: "the write budget charges S3 when it is the larger of the two",
			envs: map[string]string{
				FetchTimeoutEnv:  "10s",
				S3TimeoutEnv:     "120s",
				WriteTimeoutEnv:  (writeBudgetFor(DefaultReadTimeout, defaultFetchOrS3) + time.Second).String(),
				ShutdownGraceEnv: "3m",
			},
			wantInErr: WriteTimeoutEnv,
		},

		// ShutdownGrace > max(Fetch,S3) + Submit, so a SIGTERM during a
		// request already using its full budget does not truncate the print
		// the grace period exists to let finish.
		{
			name:      "shutdown grace exactly at the budget is rejected",
			envs:      map[string]string{ShutdownGraceEnv: shutdownBudgetFor(defaultFetchOrS3).String()},
			wantInErr: ShutdownGraceEnv,
		},
		{
			name: "shutdown grace just above the budget is accepted",
			envs: map[string]string{ShutdownGraceEnv: (shutdownBudgetFor(defaultFetchOrS3) + time.Second).String()},
		},

		// max(Fetch,S3), not their sum — a single request only ever exercises
		// one of file_url/s3_key, and summing would charge every deployment
		// for S3Timeout even with object storage unconfigured.
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
			// Between the two budgets: inside the limit if validate wrongly
			// took the SMALLER of the pair, outside it for the real max().
			name: "and it really is the larger, not the smaller",
			envs: map[string]string{
				FetchTimeoutEnv:  "10s",
				S3TimeoutEnv:     "60s",
				ShutdownGraceEnv: (shutdownBudgetFor(10*time.Second) + 30*time.Second).String(),
			},
			wantInErr: ShutdownGraceEnv,
		},
		{
			// validate never reads S3Endpoint, so configuring object storage
			// must not move either budget. This is the E1 review's reported
			// regression in its honest form: summing the Fetch/S3 terms
			// unconditionally meant a ShutdownGrace pinned before object
			// storage existed refused to start on upgrade alone. (Setting
			// ShutdownGrace to its own default would only have duplicated
			// the defaults case above — S3 has to actually be configured for
			// this to pin anything.)
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
			_, err := Load([]string{progName}, env(tt.envs))

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
