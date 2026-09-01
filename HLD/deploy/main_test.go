package main

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/LabOS-co/go-packages/logs"

	"printgateway/internal/config"
)

// envMap builds a getenv func from a plain map, defaulting to "" for any
// key not present — the shape every test below needs, since config.Load and
// the secrets resolvers each read a handful of named variables.
func envMap(vars map[string]string) func(string) string {
	return func(key string) string {
		return vars[key]
	}
}

// recordingLogger captures every logs.Logger call this file's tests need to
// assert on — not just LogError/LogInfo. An Opus review of this stage found
// the first draft only overrode those two, so a test asserting "nothing was
// logged" could pass while a LogDebug/LogAPIError/LogAPICompletion call went
// unnoticed. No mutex: every test constructs its own instance, and nothing
// under test here spawns a goroutine (objstore.New, config.Load, and the
// secrets resolvers are all synchronous), unlike httpapi's capturingLogger,
// which is shared across a concurrency test.
type recordingLogger struct {
	logs.LoggerMock
	errors []string
	infos  []string
	other  int // LogDebug/LogAPIError/LogAPICompletion/LogDBQuery call count
}

func (r *recordingLogger) LogError(msg string, _ *logs.LogMetaData) error {
	r.errors = append(r.errors, msg)
	return nil
}

func (r *recordingLogger) LogInfo(msg string, _ *logs.LogMetaData) error {
	r.infos = append(r.infos, msg)
	return nil
}

func (r *recordingLogger) LogDebug(string, *logs.LogMetaData) error    { r.other++; return nil }
func (r *recordingLogger) LogAPIError(string, *logs.LogMetaData) error { r.other++; return nil }
func (r *recordingLogger) LogAPICompletion(*logs.LogMetaData) error    { r.other++; return nil }
func (r *recordingLogger) LogDBQuery(string, *logs.LogMetaData) error  { r.other++; return nil }

func (r *recordingLogger) totalCalls() int {
	return len(r.errors) + len(r.infos) + r.other
}

func TestRunReturnsErrorOnInvalidConfig(t *testing.T) {
	t.Parallel()

	err := run(context.Background(), func() {}, []string{"printgateway"}, envMap(map[string]string{
		config.AuthTokenEnv:   "t",
		config.ReadTimeoutEnv: "not-a-duration",
	}), &recordingLogger{})
	if err == nil {
		t.Fatal("expected an error for a malformed PRINT_GATEWAY_READ_TIMEOUT, got nil")
	}
	// Not just "some error" - an Opus review found this test passed even
	// when mutated to always fail regardless of which config check tripped,
	// since it never inspected the error's content.
	if !strings.Contains(err.Error(), config.ReadTimeoutEnv) {
		t.Errorf("error = %q, want it to name %s", err.Error(), config.ReadTimeoutEnv)
	}
}

func TestRunReturnsErrorWhenNoPrintTokenIsResolvable(t *testing.T) {
	t.Parallel()

	err := run(context.Background(), func() {}, []string{"printgateway"}, envMap(nil), &recordingLogger{})
	if err == nil {
		t.Fatal("expected an error when neither Vault nor PRINT_GATEWAY_TOKEN produce a token (F2)")
	}
	if !strings.Contains(err.Error(), "print token unavailable") {
		t.Errorf("error = %q, want it to name the print-token failure", err.Error())
	}
}

// waitForDial polls addr until a connection succeeds (or the deadline
// passes), for proving a server has actually started accepting connections
// without a fixed, guessable sleep.
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

// freeAddr binds a listener just to learn an unused port, then releases it -
// the standard "get a free port" trick. A small race remains (another
// process could grab it before run's own ListenAndServe does), accepted as
// the same tradeoff every other test in this package already makes.
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

// TestRunGracefulShutdownReturnsNil drives the shutdown path via a
// cancellable context rather than a real OS signal (see run's own doc
// comment for why a test must not register a live SIGINT/SIGTERM handler),
// and proves three things an Opus review found the first draft's version
// didn't actually check: the server was really accepting connections before
// shutdown, stopSignals ran while it still was (not after Shutdown had
// already torn it down), and the server has genuinely stopped accepting
// afterward - not just that run() happened to return nil.
func TestRunGracefulShutdownReturnsNil(t *testing.T) {
	t.Parallel()

	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())

	// stopSignals dials addr itself: if this succeeds, the listener was
	// still open at the moment stopSignals ran, proving it fires before
	// Shutdown closes the listener - exactly the ordering run's doc comment
	// says matters. A mutant moving the stopSignals() call to after
	// server.Shutdown(shutdownCtx) makes this dial fail instead.
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
		runErr <- run(ctx, stopSignals, []string{"printgateway", addr}, envMap(map[string]string{
			config.AuthTokenEnv: "test-token",
		}), logger)
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

	// The listener must actually be gone now, not merely "run() returned".
	if conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond); err == nil {
		conn.Close()
		t.Error("still accepting connections after run() returned nil")
	}
}

// TestRunReturnsErrorOnListenFailure occupies a real port first so
// ListenAndServe fails immediately and deterministically, instead of
// relying on a sleep-and-hope race to catch a startup error.
func TestRunReturnsErrorOnListenFailure(t *testing.T) {
	t.Parallel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	occupied := ln.Addr().String()

	err = run(context.Background(), func() {}, []string{"printgateway", occupied}, envMap(map[string]string{
		config.AuthTokenEnv: "test-token",
	}), &recordingLogger{})
	if err == nil {
		t.Fatalf("expected an error binding an already-occupied address %s, got nil", occupied)
	}
	// Not just "some error" - an Opus review found this test passed even
	// when mutated so every run() call fails for an unrelated reason.
	if !strings.Contains(err.Error(), occupied) {
		t.Errorf("error = %q, want it to name the occupied address %s", err.Error(), occupied)
	}
}

// TestRunCoversS3AndPrivateTargetsWarningPaths exercises two run()-level
// lines that newObjectStore's own direct tests below can't reach on their
// own: the presigner/objectGetter assignment from a non-nil store
// (main.go, guarded against the typed-nil-in-interface trap - see that
// comment) and the loud AllowPrivateTargets warning, both only reachable by
// calling run() itself, not newObjectStore in isolation.
func TestRunCoversS3AndPrivateTargetsWarningPaths(t *testing.T) {
	t.Parallel()

	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	logger := &recordingLogger{}

	runErr := make(chan error, 1)
	go func() {
		runErr <- run(ctx, func() {}, []string{"printgateway", addr}, envMap(map[string]string{
			config.AuthTokenEnv:           "test-token",
			config.S3EndpointEnv:          "localhost:9000",
			config.S3BucketEnv:            "docs",
			config.S3AccessKeyEnv:         "access-key",
			config.S3SecretKeyEnv:         "secret-key",
			config.S3RegionEnv:            "us-east-1",
			config.AllowPrivateTargetsEnv: "true",
		}), logger)
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
	cfg, err := config.Load([]string{"printgateway"}, envMap(map[string]string{config.AuthTokenEnv: "t"}))
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
	cfg, err := config.Load([]string{"printgateway"}, envMap(map[string]string{
		config.AuthTokenEnv:  "t",
		config.S3EndpointEnv: "http://localhost:9000",
		// S3BucketEnv deliberately left unset.
	}))
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
	cfg, err := config.Load([]string{"printgateway"}, envMap(map[string]string{
		config.AuthTokenEnv:  "t",
		config.S3EndpointEnv: "http://localhost:9000",
		config.S3BucketEnv:   "docs",
		// No Vault, no S3AccessKeyEnv/S3SecretKeyEnv.
	}))
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
	cfg, err := config.Load([]string{"printgateway"}, envMap(map[string]string{
		config.AuthTokenEnv:   "t",
		config.S3EndpointEnv:  "localhost:9000",
		config.S3BucketEnv:    "docs",
		config.S3AccessKeyEnv: "access-key",
		config.S3SecretKeyEnv: "secret-key",
		config.S3RegionEnv:    "us-east-1",
	}))
	if err != nil {
		t.Fatal(err)
	}

	// cloud_storage.NewS3 builds a minio.Client lazily — no network I/O at
	// construction, so this succeeds without a live MinIO endpoint (the same
	// property objstore's own stage-4 tests already rely on).
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
	// A fully-valid config must not ALSO emit the empty-region warning - an
	// Opus review found mutating the `cfg.S3Region == ""` guard to always-true
	// survived undetected, since nothing here checked for its absence.
	if len(logger.errors) != 0 {
		t.Errorf("expected no LogError calls for a fully-configured S3 setup, got %v", logger.errors)
	}
}

func TestNewObjectStoreEmptyRegionWarnsButStillSucceeds(t *testing.T) {
	t.Parallel()

	logger := &recordingLogger{}
	cfg, err := config.Load([]string{"printgateway"}, envMap(map[string]string{
		config.AuthTokenEnv:   "t",
		config.S3EndpointEnv:  "localhost:9000",
		config.S3BucketEnv:    "docs",
		config.S3AccessKeyEnv: "access-key",
		config.S3SecretKeyEnv: "secret-key",
		// S3RegionEnv deliberately left unset.
	}))
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
