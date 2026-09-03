package secrets

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/LabOS-co/go-packages/encryption"
	"github.com/LabOS-co/go-packages/logs"
	"github.com/LabOS-co/go-packages/secret_store"

	"printgateway/internal/config"
)

// --- fakes -------------------------------------------------------------

// fakeSecretStoreClient implements secret_store.SecretStoreClient. Every
// resolver in this package only ever calls GetSecretValue, so every other
// method panics if invoked — a call there would mean this package started
// depending on a capability none of ResolveToken/ResolveLogServer/
// ResolveS3Credentials actually use.
type fakeSecretStoreClient struct {
	values map[string]map[string]any // path -> raw GetSecretValue result
	errs   map[string]error          // path -> error instead of a value

	calls []string
}

func (f *fakeSecretStoreClient) GetSecretValue(path string) (map[string]any, error) {
	f.calls = append(f.calls, path)
	if err, ok := f.errs[path]; ok {
		return nil, err
	}
	if v, ok := f.values[path]; ok {
		return v, nil
	}
	return nil, nil // a definitive miss, per SecretValueGetter's contract
}

func (f *fakeSecretStoreClient) unimplemented(name string) {
	panic(fmt.Sprintf("fakeSecretStoreClient.%s: not used by the secrets package, should never be called", name))
}

func (f *fakeSecretStoreClient) GetSecret(string) (*secret_store.SecretStoreSecret, error) {
	f.unimplemented("GetSecret")
	return nil, nil
}
func (f *fakeSecretStoreClient) RenewLease(string, int) (*secret_store.SecretStoreSecret, error) {
	f.unimplemented("RenewLease")
	return nil, nil
}
func (f *fakeSecretStoreClient) GetSecretsList(string) (*secret_store.SecretStoreSecret, error) {
	f.unimplemented("GetSecretsList")
	return nil, nil
}
func (f *fakeSecretStoreClient) GetSecretsListValue(string) (map[string]any, error) {
	f.unimplemented("GetSecretsListValue")
	return nil, nil
}
func (f *fakeSecretStoreClient) GetDatabaseCredentials(string) (*secret_store.DatabaseCredentials, error) {
	f.unimplemented("GetDatabaseCredentials")
	return nil, nil
}
func (f *fakeSecretStoreClient) RenewDatabaseCredentialsLease(string, int) (*secret_store.DatabaseCredentials, error) {
	f.unimplemented("RenewDatabaseCredentialsLease")
	return nil, nil
}

var _ secret_store.SecretStoreClient = (*fakeSecretStoreClient)(nil)

// kv2 wraps fields the way this package's own VaultClient.GetSecretValue
// returns them (see secret_store's unwrapFields doc comment) — nested one
// level under "data", un-unwrapped.
func kv2(fields map[string]any) map[string]any {
	return map[string]any{"data": fields}
}

// stubVaultClient swaps the package-level vaultClient var for the duration
// of the calling test. Package-level, so callers must NOT run in parallel
// with each other or with anything else that touches vaultClient.
func stubVaultClient(t *testing.T, client secret_store.SecretStoreClient, err error) {
	t.Helper()
	orig := vaultClient
	vaultClient = func(config.Config, logs.Logger, *logs.LogMetaData) (secret_store.SecretStoreClient, error) {
		return client, err
	}
	t.Cleanup(func() { vaultClient = orig })
}

// capturingLogger records which level each call landed at, so a test can
// pin the LogInfo-vs-LogError distinction the production code's own
// comments describe (e.g. "a missing optional key is not an error").
type capturingLogger struct {
	logs.LoggerMock
	infos  []string
	errors []string
}

func (c *capturingLogger) LogInfo(msg string, _ *logs.LogMetaData) error {
	c.infos = append(c.infos, msg)
	return nil
}

func (c *capturingLogger) LogError(msg string, _ *logs.LogMetaData) error {
	c.errors = append(c.errors, msg)
	return nil
}

// assertNoSecretLogged is the check the plan's own test spec for this
// package names explicitly: no secret value may ever reach a log line,
// success or failure. Call it at the end of any test that has a real secret
// value in play, passing every such value (blank ones are ignored so a
// zero-value case doesn't trivially pass by matching every line).
func assertNoSecretLogged(t *testing.T, log *capturingLogger, secrets ...string) {
	t.Helper()
	lines := make([]string, 0, len(log.infos)+len(log.errors))
	lines = append(lines, log.infos...)
	lines = append(lines, log.errors...)
	for _, line := range lines {
		for _, s := range secrets {
			if s != "" && strings.Contains(line, s) {
				t.Errorf("secret %q leaked into a log line: %q", s, line)
			}
		}
	}
}

// --- vaultPath -----------------------------------------------------------

func TestVaultPath(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		labosEnv string
		want     string
	}{
		{"empty env leaves path unprefixed", "", "config/print_gateway"},
		{"env prefixes the path", "staging", "staging/config/print_gateway"},
		{"trailing slash trimmed", "staging/", "staging/config/print_gateway"},
		{"leading and trailing slash trimmed", "/staging/", "staging/config/print_gateway"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := vaultPath(tc.labosEnv, "config/print_gateway"); got != tc.want {
				t.Errorf("vaultPath(%q, ...) = %q, want %q", tc.labosEnv, got, tc.want)
			}
		})
	}
}

// --- parseHostPort ---------------------------------------------------------

func TestParseHostPort(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		raw      string
		wantHost string
		wantPort int
		wantErr  bool
	}{
		{"valid host:port", "logstash:5000", "logstash", 5000, false},
		{"trims surrounding whitespace", "  logstash:5000  ", "logstash", 5000, false},
		{"missing port is rejected", "logstash", "", 0, true},
		{"empty host is rejected, not silently accepted", ":5000", "", 0, true},
		{"port zero is rejected", "logstash:0", "", 0, true},
		{"port above 65535 is rejected", "logstash:65536", "", 0, true},
		{"negative port is rejected", "logstash:-1", "", 0, true},
		{"non-numeric port is rejected", "logstash:abc", "", 0, true},
		{"IPv6 literal is re-bracketed for later dialing", "[::1]:5000", "[::1]", 5000, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			host, port, err := parseHostPort(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseHostPort(%q) = (%q, %d, nil), want an error", tc.raw, host, port)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseHostPort(%q) returned error: %v", tc.raw, err)
			}
			if host != tc.wantHost || port != tc.wantPort {
				t.Errorf("parseHostPort(%q) = (%q, %d), want (%q, %d)", tc.raw, host, port, tc.wantHost, tc.wantPort)
			}
		})
	}
}

// --- defaultVaultClient ----------------------------------------------------

func TestDefaultVaultClientRejectsAnUndecryptablePassword(t *testing.T) {
	t.Parallel()

	_, err := defaultVaultClient(config.Config{
		SecretStoreURL:      "https://vault.example.com",
		SecretStoreUsername: "svc",
		SecretStorePassword: "not valid ciphertext",
	}, logs.LoggerMock{}, nil)
	if err == nil {
		t.Fatal("defaultVaultClient with an undecryptable password returned no error")
	}
}

func TestDefaultVaultClientTokenConstructionNeedsNoNetwork(t *testing.T) {
	// Not parallel: t.Setenv (below) forbids it.

	// secret_store.Vault -> vault.NewClient -> vault.DefaultConfig reads
	// every VAULT_* env var via ReadEnvironment before this function ever
	// sees cfg — a developer machine or CI runner with e.g. VAULT_CACERT
	// pointed at a nonexistent file fails client construction for reasons
	// unrelated to this code (verified live: VAULT_CACERT=/no/such/file
	// makes this test fail with "Error loading CA File"). Pin a clean
	// environment explicitly rather than relying on whatever happens to be
	// ambient.
	for _, k := range []string{
		"VAULT_ADDR", "VAULT_AGENT_ADDR", "VAULT_CACERT", "VAULT_CACERT_BYTES", "VAULT_CAPATH",
		"VAULT_CLIENT_CERT", "VAULT_CLIENT_KEY", "VAULT_CLIENT_TIMEOUT", "VAULT_SRV_LOOKUP",
		"VAULT_SKIP_VERIFY", "VAULT_NAMESPACE", "VAULT_TLS_SERVER_NAME", "VAULT_WRAP_TTL",
		"VAULT_MAX_RETRIES", "VAULT_TOKEN", "VAULT_MFA", "VAULT_RATE_LIMIT", "VAULT_HTTP_PROXY",
		"VAULT_PROXY_ADDR", "VAULT_DISABLE_REDIRECTS",
	} {
		t.Setenv(k, "")
	}

	// A token-based client is validated and returned locally by
	// secret_store.Vault — no login round trip, unlike the userpass case
	// below — so this must succeed even though vault.example.com resolves
	// to nothing reachable here.
	client, err := defaultVaultClient(config.Config{
		SecretStoreURL: "https://vault.example.com",
		VaultToken:     "s.faketoken",
	}, logs.LoggerMock{}, nil)
	if err != nil {
		t.Fatalf("defaultVaultClient with a token returned error: %v", err)
	}
	if client == nil {
		t.Fatal("defaultVaultClient returned a nil client with no error")
	}
}

func TestDefaultVaultClientUserpassFailsWithoutAReachableServer(t *testing.T) {
	t.Parallel()

	encPass, err := encryption.Encrypt("real-password")
	if err != nil {
		t.Fatalf("test setup: encryption.Encrypt failed: %v", err)
	}

	// Unlike the token case, a userpass login performs a real login write —
	// port 1 on loopback refuses instantly rather than hanging on DNS, so
	// this stays fast while still exercising defaultVaultClient's real
	// network-failure path.
	_, err = defaultVaultClient(config.Config{
		SecretStoreURL:      "http://127.0.0.1:1",
		SecretStoreUsername: "svc",
		SecretStorePassword: encPass,
	}, logs.LoggerMock{}, nil)
	if err == nil {
		t.Fatal("defaultVaultClient against an unreachable server returned no error")
	}
	if strings.Contains(err.Error(), "real-password") {
		t.Errorf("decrypted Vault password leaked into the returned error: %v", err)
	}
}

// --- ResolveToken ------------------------------------------------------

func TestResolveTokenNoVaultConfigured(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		authToken  string
		wantToken  string
		wantSource string
		wantErr    bool
	}{
		{"env token present", "tok-123", "tok-123", "env", false},
		{"env token empty", "", "", "", true},
		{"env token whitespace-only", "   ", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			token, source, err := ResolveToken(config.Config{AuthToken: tc.authToken}, logs.LoggerMock{}, nil)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ResolveToken() = (%q, %q, nil), want an error", token, source)
				}
				if !strings.Contains(err.Error(), config.AuthTokenEnv) {
					t.Errorf("error %q does not name %s", err, config.AuthTokenEnv)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveToken() returned error: %v", err)
			}
			if token != tc.wantToken || source != tc.wantSource {
				t.Errorf("ResolveToken() = (%q, %q), want (%q, %q)", token, source, tc.wantToken, tc.wantSource)
			}
		})
	}
}

func TestResolveTokenFallsBackWhenVaultClientConstructionFails(t *testing.T) {
	// Not parallel: exercises the real defaultVaultClient (no seam), but
	// serializing everything that touches Vault-configured cfg keeps the
	// file's concurrency story simple to reason about.

	cfg := config.Config{
		SecretStoreURL:      "https://vault.example.com",
		SecretStoreUsername: "svc",
		SecretStorePassword: "not valid ciphertext", // fails encryption.Decrypt
		AuthToken:           "fallback-tok",
	}
	log := &capturingLogger{}
	token, source, err := ResolveToken(cfg, log, nil)
	if err != nil {
		t.Fatalf("ResolveToken() returned error: %v", err)
	}
	if token != "fallback-tok" || source != "env (vault fallback)" {
		t.Errorf("ResolveToken() = (%q, %q), want (%q, %q)", token, source, "fallback-tok", "env (vault fallback)")
	}
	assertNoSecretLogged(t, log, "fallback-tok", cfg.SecretStorePassword)
}

func TestResolveTokenErrorsWhenVaultFailsAndEnvIsEmpty(t *testing.T) {
	// fallbackToken's own TrimSpace check (secrets.go), distinct from
	// ResolveToken's no-Vault-configured emptiness check already covered by
	// TestResolveTokenNoVaultConfigured's "env token whitespace-only" case —
	// dropping this one specifically survived until this case existed.
	cases := []struct {
		name      string
		authToken string
	}{
		{"empty", ""},
		{"whitespace-only", "   "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Config{
				SecretStoreURL:      "https://vault.example.com",
				SecretStoreUsername: "svc",
				SecretStorePassword: "not valid ciphertext",
				AuthToken:           tc.authToken,
			}
			_, _, err := ResolveToken(cfg, logs.LoggerMock{}, nil)
			if err == nil {
				t.Fatal("ResolveToken() returned no error with vault broken and no usable env token")
			}
			if !strings.Contains(err.Error(), config.AuthTokenEnv) {
				t.Errorf("error %q does not name %s", err, config.AuthTokenEnv)
			}
		})
	}
}

func TestResolveTokenVaultSuccess(t *testing.T) {
	// Literal path/key, not the printTokenPath/printTokenKey constants: those
	// values are a cross-service contract with the labOS side's
	// gSecretManager (see secrets.go's doc comment on them), so a rename or
	// typo there must fail this test instead of silently agreeing with
	// itself.
	fake := &fakeSecretStoreClient{
		values: map[string]map[string]any{
			// Padded, so this also pins the trim-on-return fix: an untrimmed
			// "  vault-tok  " (e.g. a trailing newline from a `vault kv put`
			// heredoc) must not reach requireToken's comparison verbatim.
			"staging/config/print_gateway": kv2(map[string]any{"auth-token": "  vault-tok  "}),
		},
	}
	stubVaultClient(t, fake, nil)

	cfg := config.Config{SecretStoreURL: "https://vault.example.com", LabosEnv: "staging"}
	log := &capturingLogger{}
	token, source, err := ResolveToken(cfg, log, nil)
	if err != nil {
		t.Fatalf("ResolveToken() returned error: %v", err)
	}
	if token != "vault-tok" || source != "vault" {
		t.Errorf("ResolveToken() = (%q, %q), want (%q, %q) — the value must be trimmed", token, source, "vault-tok", "vault")
	}
	if len(fake.calls) != 1 || fake.calls[0] != "staging/config/print_gateway" {
		t.Errorf("client saw calls %v, want a single call to %q — LabosEnv must prefix the path", fake.calls, "staging/config/print_gateway")
	}
	assertNoSecretLogged(t, log, "vault-tok")
}

func TestResolveTokenVaultReadFailsFallsBackToEnv(t *testing.T) {
	fake := &fakeSecretStoreClient{
		errs: map[string]error{"config/print_gateway": errors.New("permission denied")},
	}
	stubVaultClient(t, fake, nil)

	cfg := config.Config{SecretStoreURL: "https://vault.example.com", AuthToken: "fallback-tok"}
	log := &capturingLogger{}
	token, source, err := ResolveToken(cfg, log, nil)
	if err != nil {
		t.Fatalf("ResolveToken() returned error: %v", err)
	}
	if token != "fallback-tok" || source != "env (vault fallback)" {
		t.Errorf("ResolveToken() = (%q, %q), want (%q, %q)", token, source, "fallback-tok", "env (vault fallback)")
	}
	assertNoSecretLogged(t, log, "fallback-tok")
}

func TestResolveTokenBlankVaultValueFallsBackToEnv(t *testing.T) {
	// A present-but-blank value is a successful GetSecretString call, not an
	// error — the trap ResolveToken's own emptiness check exists to catch.
	fake := &fakeSecretStoreClient{
		values: map[string]map[string]any{
			"config/print_gateway": kv2(map[string]any{"auth-token": "   "}),
		},
	}
	stubVaultClient(t, fake, nil)

	cfg := config.Config{SecretStoreURL: "https://vault.example.com", AuthToken: "fallback-tok"}
	log := &capturingLogger{}
	token, source, err := ResolveToken(cfg, log, nil)
	if err != nil {
		t.Fatalf("ResolveToken() returned error: %v", err)
	}
	if token != "fallback-tok" || source != "env (vault fallback)" {
		t.Errorf("ResolveToken() = (%q, %q), want (%q, %q) — a blank Vault value must not \"resolve\"", token, source, "fallback-tok", "env (vault fallback)")
	}
	assertNoSecretLogged(t, log, "fallback-tok")
}

// --- ResolveLogServer --------------------------------------------------

func TestResolveLogServerNoVaultConfigured(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		logServer  string
		wantHost   string
		wantPort   int
		wantSource string
	}{
		{"unset", "", "", 0, ""},
		{"valid env value", "logstash:5000", "logstash", 5000, "env"},
		{"malformed env value disables shipping, not an error", "not-a-hostport", "", 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			host, port, source := ResolveLogServer(config.Config{LogServer: tc.logServer}, logs.LoggerMock{}, nil)
			if host != tc.wantHost || port != tc.wantPort || source != tc.wantSource {
				t.Errorf("ResolveLogServer() = (%q, %d, %q), want (%q, %d, %q)",
					host, port, source, tc.wantHost, tc.wantPort, tc.wantSource)
			}
		})
	}
}

func TestResolveLogServerVaultSuccess(t *testing.T) {
	// Literal path/key (see the equivalent comment on TestResolveTokenVaultSuccess).
	fake := &fakeSecretStoreClient{
		values: map[string]map[string]any{
			"staging/config/print_gateway": kv2(map[string]any{"log-server": "logstash:5000"}),
		},
	}
	stubVaultClient(t, fake, nil)

	// LabosEnv set (pins vaultPath's prefix — dropping it left this green
	// before) and LogServer set to a value that must lose (pins that Vault
	// is actually preferred over env, not just present when env is absent).
	cfg := config.Config{SecretStoreURL: "https://vault.example.com", LabosEnv: "staging", LogServer: "ignored-env:9999"}
	host, port, source := ResolveLogServer(cfg, logs.LoggerMock{}, nil)
	if host != "logstash" || port != 5000 || source != "vault" {
		t.Errorf("ResolveLogServer() = (%q, %d, %q), want (%q, %d, %q)", host, port, source, "logstash", 5000, "vault")
	}
	if len(fake.calls) != 1 || fake.calls[0] != "staging/config/print_gateway" {
		t.Errorf("client saw calls %v, want a single call to %q — LabosEnv must prefix the path", fake.calls, "staging/config/print_gateway")
	}
}

func TestResolveLogServerVaultMalformedFallsBackToEnv(t *testing.T) {
	fake := &fakeSecretStoreClient{
		values: map[string]map[string]any{
			"config/print_gateway": kv2(map[string]any{"log-server": "not-a-hostport"}),
		},
	}
	stubVaultClient(t, fake, nil)

	cfg := config.Config{SecretStoreURL: "https://vault.example.com", LogServer: "backup:5001"}
	host, port, source := ResolveLogServer(cfg, logs.LoggerMock{}, nil)
	if host != "backup" || port != 5001 || source != "env" {
		t.Errorf("ResolveLogServer() = (%q, %d, %q), want (%q, %d, %q)", host, port, source, "backup", 5001, "env")
	}
}

func TestResolveLogServerMissingVaultKeyLogsInfoNotError(t *testing.T) {
	// The doc comment on ResolveLogServer is explicit: an absent optional
	// key is the overwhelmingly common case and must not read as an ERROR
	// on every startup. Pin the level AND that this is a real fallback to
	// env (not just a discarded return) — the reviewer found a mutation
	// that dropped ResolveLogServer's own env fallback on this exact path
	// undetected, because the previous version of this test discarded all
	// three return values.
	fake := &fakeSecretStoreClient{} // no configured value or error: a definitive miss
	stubVaultClient(t, fake, nil)

	log := &capturingLogger{}
	cfg := config.Config{SecretStoreURL: "https://vault.example.com", LogServer: "backup:5001"}
	host, port, source := ResolveLogServer(cfg, log, nil)

	if host != "backup" || port != 5001 || source != "env" {
		t.Errorf("ResolveLogServer() = (%q, %d, %q), want (%q, %d, %q) — a missing Vault key must fall back to env",
			host, port, source, "backup", 5001, "env")
	}
	if len(log.errors) != 0 {
		t.Errorf("LogError called %d times for a merely-absent optional key: %v", len(log.errors), log.errors)
	}
	if len(log.infos) == 0 {
		t.Error("LogInfo was never called for the absent-key case")
	}
}

func TestResolveLogServerVaultClientFailureFallsBackToEnv(t *testing.T) {
	cfg := config.Config{
		SecretStoreURL:      "https://vault.example.com",
		SecretStoreUsername: "svc",
		SecretStorePassword: "not valid ciphertext",
		LogServer:           "backup:5001",
	}
	host, port, source := ResolveLogServer(cfg, logs.LoggerMock{}, nil)
	if host != "backup" || port != 5001 || source != "env" {
		t.Errorf("ResolveLogServer() = (%q, %d, %q), want (%q, %d, %q)", host, port, source, "backup", 5001, "env")
	}
}

// --- ResolveS3Credentials ------------------------------------------------

func TestResolveS3CredentialsNoVaultConfigured(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		accessKey     string
		secretKey     string
		wantAccessKey string
		wantSecretKey string
		wantSource    string
	}{
		{"both set", "  ak  ", "sk", "ak", "sk", "env"}, // trimmed
		{"access key empty", "", "sk", "", "", ""},
		{"secret key empty", "ak", "", "", "", ""},
		{"both empty", "", "", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ak, sk, source := ResolveS3Credentials(config.Config{S3AccessKey: tc.accessKey, S3SecretKey: tc.secretKey}, logs.LoggerMock{}, nil)
			if ak != tc.wantAccessKey || sk != tc.wantSecretKey || source != tc.wantSource {
				t.Errorf("ResolveS3Credentials() = (%q, %q, %q), want (%q, %q, %q)",
					ak, sk, source, tc.wantAccessKey, tc.wantSecretKey, tc.wantSource)
			}
		})
	}
}

func TestResolveS3CredentialsVaultSuccess(t *testing.T) {
	// Literal path/key (see the equivalent comment on TestResolveTokenVaultSuccess).
	fake := &fakeSecretStoreClient{
		values: map[string]map[string]any{
			"staging/config/print_gateway": kv2(map[string]any{"s3-access-key": "  vault-ak  ", "s3-secret-key": "vault-sk"}),
		},
	}
	stubVaultClient(t, fake, nil)

	// LabosEnv set (pins vaultPath's prefix) and both env fields set to
	// values that must lose (pins Vault-over-env precedence, not just
	// Vault-when-env-is-absent).
	cfg := config.Config{SecretStoreURL: "https://vault.example.com", LabosEnv: "staging", S3AccessKey: "ignored-env-ak", S3SecretKey: "ignored-env-sk"}
	log := &capturingLogger{}
	ak, sk, source := ResolveS3Credentials(cfg, log, nil)
	if ak != "vault-ak" || sk != "vault-sk" || source != "vault" {
		t.Errorf("ResolveS3Credentials() = (%q, %q, %q), want (%q, %q, %q) — the value must be trimmed", ak, sk, source, "vault-ak", "vault-sk", "vault")
	}
	wantPath := "staging/config/print_gateway"
	if len(fake.calls) != 2 || fake.calls[0] != wantPath || fake.calls[1] != wantPath {
		t.Errorf("client saw calls %v, want two calls to %q — LabosEnv must prefix the path", fake.calls, wantPath)
	}
	assertNoSecretLogged(t, log, "vault-ak", "vault-sk")
}

func TestResolveS3CredentialsVaultPartialFailureFallsBackToEnv(t *testing.T) {
	cases := []struct {
		name string
		fake *fakeSecretStoreClient
	}{
		{
			"access key read fails",
			&fakeSecretStoreClient{errs: map[string]error{"config/print_gateway": errors.New("denied")}},
		},
		{
			"secret key present but blank",
			&fakeSecretStoreClient{values: map[string]map[string]any{
				"config/print_gateway": kv2(map[string]any{"s3-access-key": "vault-ak", "s3-secret-key": "   "}),
			}},
		},
		{
			// s3AccessKeyPath and s3SecretKeyPath are the same Vault path
			// (see secrets.go) — both fields are read from one fetched map,
			// so the only way for exactly one of ak/sk to fail is a
			// key-level miss within an otherwise-successful path read, not a
			// path-level transport error. This is that case: the field map
			// has the access key but not the secret key.
			"secret key missing from an otherwise-successful path read",
			&fakeSecretStoreClient{
				values: map[string]map[string]any{"config/print_gateway": kv2(map[string]any{"s3-access-key": "vault-ak"})},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubVaultClient(t, tc.fake, nil)

			cfg := config.Config{SecretStoreURL: "https://vault.example.com", S3AccessKey: "env-ak", S3SecretKey: "env-sk"}
			log := &capturingLogger{}
			ak, sk, source := ResolveS3Credentials(cfg, log, nil)
			if ak != "env-ak" || sk != "env-sk" || source != "env" {
				t.Errorf("ResolveS3Credentials() = (%q, %q, %q), want (%q, %q, %q)", ak, sk, source, "env-ak", "env-sk", "env")
			}
			assertNoSecretLogged(t, log, "env-ak", "env-sk")
		})
	}
}

func TestResolveS3CredentialsVaultClientFailureFallsBackToEnv(t *testing.T) {
	cfg := config.Config{
		SecretStoreURL:      "https://vault.example.com",
		SecretStoreUsername: "svc",
		SecretStorePassword: "not valid ciphertext",
		S3AccessKey:         "env-ak",
		S3SecretKey:         "env-sk",
	}
	log := &capturingLogger{}
	ak, sk, source := ResolveS3Credentials(cfg, log, nil)
	if ak != "env-ak" || sk != "env-sk" || source != "env" {
		t.Errorf("ResolveS3Credentials() = (%q, %q, %q), want (%q, %q, %q)", ak, sk, source, "env-ak", "env-sk", "env")
	}
	assertNoSecretLogged(t, log, "env-ak", "env-sk", cfg.SecretStorePassword)
}
