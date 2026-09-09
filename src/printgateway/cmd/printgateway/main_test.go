package main

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LabOS-co/go-packages/logs"

	"printgateway/internal/config"
)

// envMap builds a getenv func from a plain map, defaulting to "" for any key not present.
func envMap(vars map[string]string) func(string) string {
	return func(key string) string {
		return vars[key]
	}
}

// recordingLogger captures every logs.Logger call, not just LogError/LogInfo, so a test asserting
// "nothing was logged" can't pass while an unmocked method went unnoticed. Mutex-guarded: run()'s
// listener goroutine logs concurrently with the test goroutine reading .infos/.errors in several
// tests below.
type recordingLogger struct {
	logs.LoggerMock
	mu     sync.Mutex
	errors []string
	infos  []string
	other  int // LogDebug/LogAPIError/LogAPICompletion/LogDBQuery call count
}

func (r *recordingLogger) LogError(msg string, _ *logs.LogMetaData) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errors = append(r.errors, msg)
	return nil
}

func (r *recordingLogger) LogInfo(msg string, _ *logs.LogMetaData) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.infos = append(r.infos, msg)
	return nil
}

func (r *recordingLogger) LogDebug(string, *logs.LogMetaData) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.other++
	return nil
}
func (r *recordingLogger) LogAPIError(string, *logs.LogMetaData) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.other++
	return nil
}
func (r *recordingLogger) LogAPICompletion(*logs.LogMetaData) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.other++
	return nil
}
func (r *recordingLogger) LogDBQuery(string, *logs.LogMetaData) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.other++
	return nil
}

func (r *recordingLogger) totalCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.errors) + len(r.infos) + r.other
}

// noReadFile fails the test if called; every test using it asserts env-only behavior.
func noReadFile(t *testing.T) func(string) ([]byte, error) {
	t.Helper()
	return func(path string) ([]byte, error) {
		t.Fatalf("readFile unexpectedly called with %q", path)
		return nil, nil
	}
}

func TestRunReturnsErrorOnInvalidConfig(t *testing.T) {
	t.Parallel()

	err := run(context.Background(), func() {}, envMap(map[string]string{
		config.AuthTokenEnv:   "t",
		config.ReadTimeoutEnv: "not-a-duration",
	}), noReadFile(t), &recordingLogger{})
	if err == nil {
		t.Fatal("expected an error for a malformed PRINT_GATEWAY_READ_TIMEOUT, got nil")
	}
	if !strings.Contains(err.Error(), config.ReadTimeoutEnv) {
		t.Errorf("error = %q, want it to name %s", err.Error(), config.ReadTimeoutEnv)
	}
}

func TestRunReturnsErrorWhenNoPrintTokenIsResolvable(t *testing.T) {
	t.Parallel()

	err := run(context.Background(), func() {}, envMap(nil), noReadFile(t), &recordingLogger{})
	if err == nil {
		t.Fatal("expected an error when neither Vault nor PRINT_GATEWAY_TOKEN produce a token")
	}
	if !strings.Contains(err.Error(), "print token unavailable") {
		t.Errorf("error = %q, want it to name the print-token failure", err.Error())
	}
}

// waitForDial polls addr until a connection succeeds, proving a server has started accepting
// connections without a fixed, guessable sleep.
func waitForDial(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("nothing accepted connections at %s within %s", addr, timeout)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// freeAddr binds a listener just to learn an unused port, then releases it. A small race remains
// (another process could grab it first), accepted the same way every other test here does.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// envWithPort merges vars with PORT taken from addr ("host:port") and BindHost pinned to
// 127.0.0.1, so a test can still pin run()'s listener to a specific, pre-reserved local address
// now that Addr is env-only rather than a positional argument. Pinning BindHost explicitly matters
// here, not just for dialing back in: freeAddr (below) only ever reserves a port on 127.0.0.1, and
// letting run() fall through to its 0.0.0.0 default would (a) widen freeAddr's already-inherent
// TOCTOU race — a port free on loopback isn't necessarily free on the wildcard address — and (b)
// open a LAN-reachable listener for the duration of the test, not just a loopback one.
func envWithPort(t *testing.T, addr string, vars map[string]string) map[string]string {
	t.Helper()
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("invalid test address %q: %v", addr, err)
	}
	merged := make(map[string]string, len(vars)+2)
	for k, v := range vars {
		merged[k] = v
	}
	merged[config.PortEnv] = port
	merged[config.BindHostEnv] = "127.0.0.1"
	return merged
}

// TestRunGracefulShutdownReturnsNil drives the shutdown path via a cancellable context rather than
// a real OS signal, and proves the server was accepting connections before shutdown, stopSignals
// ran while it still was, and the server has genuinely stopped accepting afterward.
func TestRunGracefulShutdownReturnsNil(t *testing.T) {
	t.Parallel()

	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())

	// stopSignals dials addr itself: success proves the listener was still open when it ran,
	// i.e. before Shutdown closed it. A mutant moving stopSignals() after server.Shutdown makes
	// this dial fail instead.
	var stopSignalsDialOK bool
	stopSignals := func() {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		stopSignalsDialOK = err == nil
		if err == nil {
			conn.Close()
		}
	}

	runErr := make(chan error, 1)
	logger := &recordingLogger{}
	go func() {
		runErr <- run(ctx, stopSignals, envMap(envWithPort(t, addr, map[string]string{
			config.AuthTokenEnv: "test-token",
		})), noReadFile(t), logger)
	}()

	waitForDial(t, addr, 5*time.Second)
	cancel()

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("run returned an error on graceful shutdown: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return after ctx was cancelled")
	}

	if !stopSignalsDialOK {
		t.Error("stopSignals ran after the listener had already closed; it must run before Shutdown (a second signal should still be able to force-kill)")
	}

	if conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond); err == nil {
		conn.Close()
		t.Error("still accepting connections after run() returned nil")
	}
}

// TestRunReturnsErrorOnListenFailure occupies a real port first so ListenAndServe fails
// immediately and deterministically. Occupies config.DefaultBindHost specifically (not just
// 127.0.0.1): run() now binds the default host unless told otherwise, and 0.0.0.0 vs. 127.0.0.1
// don't reliably conflict as separate bind targets on every platform, so occupying anything else
// risks run() binding successfully instead of failing.
func TestRunReturnsErrorOnListenFailure(t *testing.T) {
	t.Parallel()

	ln, err := net.Listen("tcp", config.DefaultBindHost+":0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	// ln.Addr().String() can render as "[::]:<port>" on some platforms even though it was bound to
	// 0.0.0.0 — take just the port and rebuild the address the way run() itself will report it
	// (config.DefaultBindHost:<port>), rather than asserting against that platform-specific rendering.
	_, occupiedPort, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	occupied := config.DefaultBindHost + ":" + occupiedPort

	// Not envWithPort: that helper pins BindHost to 127.0.0.1, but this test specifically needs
	// run() to bind the same DefaultBindHost the occupying listener above used, or there's no
	// conflict to fail on.
	err = run(context.Background(), func() {}, envMap(map[string]string{
		config.AuthTokenEnv: "test-token",
		config.PortEnv:      occupiedPort,
	}), noReadFile(t), &recordingLogger{})
	if err == nil {
		t.Fatalf("expected an error binding an already-occupied address %s, got nil", occupied)
	}
	if !strings.Contains(err.Error(), occupied) {
		t.Errorf("error = %q, want it to name the occupied address %s", err.Error(), occupied)
	}
}

// TestRunCoversS3AndPrivateTargetsWarningPaths exercises two lines only reachable through run()
// itself: the presigner/objectGetter assignment from a non-nil store, and the AllowPrivateTargets warning.
func TestRunCoversS3AndPrivateTargetsWarningPaths(t *testing.T) {
	t.Parallel()

	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	logger := &recordingLogger{}

	runErr := make(chan error, 1)
	go func() {
		runErr <- run(ctx, func() {}, envMap(envWithPort(t, addr, map[string]string{
			config.AuthTokenEnv:           "test-token",
			config.S3EndpointEnv:          "localhost:9000",
			config.S3BucketEnv:            "docs",
			config.S3AccessKeyEnv:         "access-key",
			config.S3SecretKeyEnv:         "secret-key",
			config.S3RegionEnv:            "us-east-1",
			config.AllowPrivateTargetsEnv: "true",
		})), noReadFile(t), logger)
	}()

	waitForDial(t, addr, 5*time.Second)
	cancel()

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("run returned an error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return after ctx was cancelled")
	}

	foundObjectStoreEnabled := false
	foundPrivateTargetsWarning := false
	for _, msg := range logger.infos {
		if strings.Contains(msg, "object storage enabled") {
			foundObjectStoreEnabled = true
		}
	}
	for _, msg := range logger.errors {
		if strings.Contains(msg, config.AllowPrivateTargetsEnv) {
			foundPrivateTargetsWarning = true
		}
	}
	if !foundObjectStoreEnabled {
		t.Errorf("expected an 'object storage enabled' LogInfo, got infos=%v", logger.infos)
	}
	if !foundPrivateTargetsWarning {
		t.Errorf("expected a LogError naming %s, got errors=%v", config.AllowPrivateTargetsEnv, logger.errors)
	}
}

func TestNewObjectStoreUnconfiguredIsSilentlyNil(t *testing.T) {
	t.Parallel()

	logger := &recordingLogger{}
	cfg, err := config.Load(envMap(map[string]string{config.AuthTokenEnv: "t"}), noReadFile(t))
	if err != nil {
		t.Fatal(err)
	}

	store := newObjectStore(cfg, logger, &logs.LogMetaData{})
	if store != nil {
		t.Fatal("expected a nil store when neither S3Endpoint nor S3Bucket is configured")
	}
	if logger.totalCalls() != 0 {
		t.Errorf("expected no log calls at all for the deliberately-unconfigured case, got errors=%v infos=%v other=%d", logger.errors, logger.infos, logger.other)
	}
}

func TestNewObjectStoreHalfConfiguredIsNilAndLogged(t *testing.T) {
	t.Parallel()

	logger := &recordingLogger{}
	cfg, err := config.Load(envMap(map[string]string{
		config.AuthTokenEnv:  "t",
		config.S3EndpointEnv: "http://localhost:9000",
		// S3BucketEnv deliberately left unset.
	}), noReadFile(t))
	if err != nil {
		t.Fatal(err)
	}

	store := newObjectStore(cfg, logger, &logs.LogMetaData{})
	if store != nil {
		t.Fatal("expected a nil store for a half-configured S3 (endpoint set, bucket not)")
	}
	if len(logger.errors) != 1 || !strings.Contains(logger.errors[0], "must both be set") {
		t.Errorf("expected exactly one LogError naming the half-configuration, got %v", logger.errors)
	}
}

func TestNewObjectStoreMissingCredentialsIsNilAndLogged(t *testing.T) {
	t.Parallel()

	logger := &recordingLogger{}
	cfg, err := config.Load(envMap(map[string]string{
		config.AuthTokenEnv:  "t",
		config.S3EndpointEnv: "http://localhost:9000",
		config.S3BucketEnv:   "docs",
		// No Vault, no S3AccessKeyEnv/S3SecretKeyEnv.
	}), noReadFile(t))
	if err != nil {
		t.Fatal(err)
	}

	store := newObjectStore(cfg, logger, &logs.LogMetaData{})
	if store != nil {
		t.Fatal("expected a nil store when no S3 credentials resolve from Vault or env")
	}
	if len(logger.errors) != 1 || !strings.Contains(logger.errors[0], "no S3 credentials resolved") {
		t.Errorf("expected exactly one LogError naming the missing credentials, got %v", logger.errors)
	}
}

func TestNewObjectStoreFullyConfiguredSucceeds(t *testing.T) {
	t.Parallel()

	logger := &recordingLogger{}
	cfg, err := config.Load(envMap(map[string]string{
		config.AuthTokenEnv:   "t",
		config.S3EndpointEnv:  "localhost:9000",
		config.S3BucketEnv:    "docs",
		config.S3AccessKeyEnv: "access-key",
		config.S3SecretKeyEnv: "secret-key",
		config.S3RegionEnv:    "us-east-1",
	}), noReadFile(t))
	if err != nil {
		t.Fatal(err)
	}

	// cloud_storage.NewS3 builds a minio.Client lazily, so this succeeds without a live MinIO endpoint.
	store := newObjectStore(cfg, logger, &logs.LogMetaData{})
	if store == nil {
		t.Fatalf("expected a non-nil store for a fully-configured S3 setup; errors=%v", logger.errors)
	}
	found := false
	for _, msg := range logger.infos {
		if strings.Contains(msg, "object storage enabled") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a LogInfo announcing object storage is enabled, got %v", logger.infos)
	}
	if len(logger.errors) != 0 {
		t.Errorf("expected no LogError calls for a fully-configured S3 setup, got %v", logger.errors)
	}
}

func TestNewObjectStoreEmptyRegionWarnsButStillSucceeds(t *testing.T) {
	t.Parallel()

	logger := &recordingLogger{}
	cfg, err := config.Load(envMap(map[string]string{
		config.AuthTokenEnv:   "t",
		config.S3EndpointEnv:  "localhost:9000",
		config.S3BucketEnv:    "docs",
		config.S3AccessKeyEnv: "access-key",
		config.S3SecretKeyEnv: "secret-key",
		// S3RegionEnv deliberately left unset.
	}), noReadFile(t))
	if err != nil {
		t.Fatal(err)
	}

	store := newObjectStore(cfg, logger, &logs.LogMetaData{})
	if store == nil {
		t.Fatalf("expected a non-nil store even with an empty region (a warning, not a refusal); errors=%v", logger.errors)
	}
	found := false
	for _, msg := range logger.errors {
		if strings.Contains(msg, "presigned URLs may be silently signed for the wrong region") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a LogError warning about the empty region, got %v", logger.errors)
	}
}

// ---------------------------------------------------------------------------
// Config-file layer (Stage 3)
// ---------------------------------------------------------------------------

// mapReadFile is a readFile stand-in backed by a map of path -> raw content.
func mapReadFile(m map[string]string) func(string) ([]byte, error) {
	return func(path string) ([]byte, error) {
		if raw, ok := m[path]; ok {
			return []byte(raw), nil
		}
		return nil, fmt.Errorf("no such file: %q", path)
	}
}

func TestRunReturnsErrorOnMissingConfigFile(t *testing.T) {
	t.Parallel()

	const missing = "/etc/printgateway/does-not-exist.json"
	err := run(context.Background(), func() {}, envMap(map[string]string{
		config.AuthTokenEnv:  "t",
		config.ConfigPathEnv: missing,
	}), mapReadFile(nil), &recordingLogger{})

	if err == nil {
		t.Fatal("expected an error for a named-but-missing config file, got nil")
	}
	if !strings.Contains(err.Error(), config.ConfigPathEnv) || !strings.Contains(err.Error(), missing) {
		t.Errorf("error = %q, want it to name both %s and %s", err.Error(), config.ConfigPathEnv, missing)
	}
}

// TestRunStartsFromAConfigFile proves a config file's settings reach the live listener, not just
// the returned Config, using a harmless timeout key (the address itself is env-only now; see
// config.TestLoadFileAddrKeyIsRejected for why resource/printgateway.addr no longer works — that's
// config.Load's own validation, so it's proven once there rather than re-proven through run() here).
func TestRunStartsFromAConfigFile(t *testing.T) {
	t.Parallel()

	addr := freeAddr(t)
	const configPath = "/etc/printgateway/run-file-test.json"
	fileBody := `{"resource/printgateway": {"timeouts": {"idle": "90s"}}}`

	ctx, cancel := context.WithCancel(context.Background())
	logger := &recordingLogger{}

	runErr := make(chan error, 1)
	go func() {
		runErr <- run(ctx, func() {}, envMap(envWithPort(t, addr, map[string]string{
			config.AuthTokenEnv:  "test-token",
			config.ConfigPathEnv: configPath,
		})), mapReadFile(map[string]string{configPath: fileBody}), logger)
	}()

	waitForDial(t, addr, 5*time.Second)
	cancel()

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("run returned an error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return after ctx was cancelled")
	}
}

// TestRunLogsFileSourcedSettingsWithTheirFileOrigin asserts the LogInfo/LogError lines name the
// file path and key for a file-sourced setting, while a setting NOT sourced from the file in the
// same run still logs its bare env-var name.
func TestRunLogsFileSourcedSettingsWithTheirFileOrigin(t *testing.T) {
	t.Parallel()

	addr := freeAddr(t)
	const configPath = "/etc/printgateway/run-source-test.json"
	// timeouts.write=9m still comfortably clears validate's write budget at the other defaults, so
	// this is purely about provenance, not about tripping validate.
	fileBody := `{"resource/printgateway": {"timeouts": {"write": "9m"}}}`

	ctx, cancel := context.WithCancel(context.Background())
	logger := &recordingLogger{}

	runErr := make(chan error, 1)
	go func() {
		runErr <- run(ctx, func() {}, envMap(envWithPort(t, addr, map[string]string{
			config.AuthTokenEnv:  "test-token",
			config.ConfigPathEnv: configPath,
		})), mapReadFile(map[string]string{configPath: fileBody}), logger)
	}()

	waitForDial(t, addr, 5*time.Second)
	cancel()

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("run returned an error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return after ctx was cancelled")
	}

	foundFileOrigin := false
	for _, msg := range logger.errors {
		if strings.Contains(msg, configPath) && strings.Contains(msg, config.WriteTimeoutEnv) {
			foundFileOrigin = true
		}
	}
	if !foundFileOrigin {
		t.Errorf("expected a LogError naming both %s and %s, got errors=%v", configPath, config.WriteTimeoutEnv, logger.errors)
	}

	// FetchAllowedHostsEnv was supplied by neither the file nor the environment, so its startup
	// line must still read the bare env var name even with the file layer active in this run.
	foundBareNeedle := false
	for _, msg := range logger.infos {
		if strings.Contains(msg, config.FetchAllowedHostsEnv) && strings.Contains(msg, "file_url may target any public host") {
			foundBareNeedle = true
		}
	}
	if !foundBareNeedle {
		t.Errorf("expected a LogInfo naming the bare %s (not file-sourced), got infos=%v", config.FetchAllowedHostsEnv, logger.infos)
	}
}

// TestRunAllowPrivateTargetsStaysEnvOnly proves that even with a config file active in the same
// run, AllowPrivateTargetsEnv's warning still names the bare env var, since it has no field
// anywhere in fileConfig's type tree and so can never be file-labeled.
func TestRunAllowPrivateTargetsStaysEnvOnly(t *testing.T) {
	t.Parallel()

	addr := freeAddr(t)
	const configPath = "/etc/printgateway/run-private-targets-test.json"
	// A harmless, unrelated file-sourced key: proves the file layer is genuinely active in this run.
	fileBody := `{"resource/printgateway": {"timeouts": {"idle": "70s"}}}`

	ctx, cancel := context.WithCancel(context.Background())
	logger := &recordingLogger{}

	runErr := make(chan error, 1)
	go func() {
		runErr <- run(ctx, func() {}, envMap(envWithPort(t, addr, map[string]string{
			config.AuthTokenEnv:           "test-token",
			config.ConfigPathEnv:          configPath,
			config.AllowPrivateTargetsEnv: "true",
		})), mapReadFile(map[string]string{configPath: fileBody}), logger)
	}()

	waitForDial(t, addr, 5*time.Second)
	cancel()

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("run returned an error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return after ctx was cancelled")
	}

	foundBareWarning := false
	for _, msg := range logger.errors {
		if strings.Contains(msg, config.AllowPrivateTargetsEnv) {
			if strings.Contains(msg, configPath) {
				t.Errorf("AllowPrivateTargets warning %q names the config file path; it must never be file-labeled", msg)
			}
			foundBareWarning = true
		}
	}
	if !foundBareWarning {
		t.Errorf("expected a LogError naming the bare %s, got errors=%v", config.AllowPrivateTargetsEnv, logger.errors)
	}
}
