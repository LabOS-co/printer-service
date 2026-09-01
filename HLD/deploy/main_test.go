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

// recordingLogger captures LogError/LogInfo calls for the newObjectStore
// tests below, which assert on *why* a given configuration was accepted or
// rejected, not just on the returned *objstore.MinIO being nil or not.
type recordingLogger struct {
	logs.LoggerMock
	errors []string
	infos  []string
}

func (r *recordingLogger) LogError(msg string, _ *logs.LogMetaData) error {
	r.errors = append(r.errors, msg)
	return nil
}

func (r *recordingLogger) LogInfo(msg string, _ *logs.LogMetaData) error {
	r.infos = append(r.infos, msg)
	return nil
}

// envMap builds a getenv func from a plain map, defaulting to "" for any
// key not present — the shape every test below needs, since config.Load and
// the secrets resolvers each read a handful of named variables.
func envMap(vars map[string]string) func(string) string {
	return func(key string) string {
		return vars[key]
	}
}

func TestRunReturnsErrorOnInvalidConfig(t *testing.T) {
	t.Parallel()

	err := run(context.Background(), func() {}, []string{"printgateway"}, envMap(map[string]string{
		config.AuthTokenEnv:   "t",
		config.ReadTimeoutEnv: "not-a-duration",
	}))
	if err == nil {
		t.Fatal("expected an error for a malformed PRINT_GATEWAY_READ_TIMEOUT, got nil")
	}
}

func TestRunReturnsErrorWhenNoPrintTokenIsResolvable(t *testing.T) {
	t.Parallel()

	err := run(context.Background(), func() {}, []string{"printgateway"}, envMap(nil))
	if err == nil {
		t.Fatal("expected an error when neither Vault nor PRINT_GATEWAY_TOKEN produce a token (F2)")
	}
	if !strings.Contains(err.Error(), "print token unavailable") {
		t.Errorf("error = %q, want it to name the print-token failure", err.Error())
	}
}

// TestRunGracefulShutdownReturnsNil drives the shutdown path via a
// cancellable context rather than a real OS signal — this project has
// already hit real cross-platform signal-delivery differences once (see the
// A3 history in the plan), and a test should not depend on that mechanism
// working the same way on every OS. stopSignals is asserted to have been
// called, matching run's documented contract for a signal.NotifyContext-style
// ctx (see run's own doc comment for why that call must happen before
// Shutdown, not just on the way out via main's own defer).
func TestRunGracefulShutdownReturnsNil(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	var stopCalled bool
	stopSignals := func() { stopCalled = true }

	runErr := make(chan error, 1)
	go func() {
		runErr <- run(ctx, stopSignals, []string{"printgateway", "127.0.0.1:0"}, envMap(map[string]string{
			config.AuthTokenEnv: "test-token",
		}))
	}()

	// No exported hook reports "the listener is up" (ListenAndServe owns
	// that internally), so give the goroutine above a short, generous
	// window to reach its blocking Serve call before asking it to stop.
	time.Sleep(150 * time.Millisecond)
	cancel()

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("run returned an error on graceful shutdown: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return after ctx was cancelled")
	}
	if !stopCalled {
		t.Error("stopSignals was never called; a second signal during drain would not force-kill")
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
	}))
	if err == nil {
		t.Fatalf("expected an error binding an already-occupied address %s, got nil", occupied)
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
	if len(logger.errors) != 0 || len(logger.infos) != 0 {
		t.Errorf("expected no log lines for the deliberately-unconfigured case, got errors=%v infos=%v", logger.errors, logger.infos)
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
