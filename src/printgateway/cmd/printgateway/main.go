// Initial prototype Print Gateway.
//
// Accepts a print request over HTTP — multipart/form-data, or JSON naming a file_url or s3_key —
// and hands the resulting local file to CUPS via `lp -d <printer> <path>`; CUPS's own queue
// configuration handles PPD/media/resolution. See internal/httpapi for the request contract.
//
// This file is wiring only: build the dependencies, start the server, and wait for either it to
// fail or a shutdown signal to arrive.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/LabOS-co/go-packages/logs"

	"printgateway/internal/config"
	"printgateway/internal/cups"
	"printgateway/internal/fetch"
	"printgateway/internal/httpapi"
	"printgateway/internal/objstore"
	"printgateway/internal/printgw"
	"printgateway/internal/secrets"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Constructed WITHOUT a Host: GetLoggerWithSettings' own logstash setup silently swallows a
	// dial failure, so run() calls SetLogstashLogger itself instead, where the error IS checked.
	// The discarded error here is safe: GetLoggerWithSettings does no I/O and always returns nil.
	logger, _ := logs.GetLoggerWithSettings(logs.LogsSettings{Format: logs.FormatJSON}, config.ServiceName)

	if err := run(ctx, stop, os.Getenv, os.ReadFile, logger); err != nil {
		os.Exit(1)
	}
}

// run holds every step of startup, request serving, and shutdown; main() is a thin os.Exit
// wrapper around it (the standard Go testable-main pattern).
//
// logger is injected rather than constructed inside run because the real logstashLogger
// increments an unsynchronized package-global counter, which parallel tests would race on.
// stopSignals is passed separately from ctx so the shutdown branch can restore default signal
// disposition before Shutdown (letting a second signal force-kill) while a test drives that
// branch with a plain context.WithCancel instead of a real OS signal — deriving the context
// from signal.NotifyContext inside run itself would register a live signal handler per test.
func run(ctx context.Context, stopSignals func(), getenv func(string) string, readFile func(string) ([]byte, error), logger logs.Logger) error {
	startupMeta := &logs.LogMetaData{Service: config.ServiceName}

	cfg, err := config.Load(getenv, readFile)
	if err != nil {
		logger.LogError(fmt.Sprintf("invalid configuration: %v", err), startupMeta)
		return err
	}

	// Set before anything else logs, so PRINT_GATEWAY_LOG_LEVEL governs every startup line that follows.
	if err := logger.SetLogLevel(cfg.LogLevel); err != nil {
		logger.LogError(fmt.Sprintf("%s: invalid level %q, defaulting to info: %v", cfg.Source(config.LogLevelEnv), cfg.LogLevel, err), startupMeta)
	}

	// LogError, not LogInfo: the config-file/env precedence is inverted relative to every ops reflex,
	// so an operator needs this line to survive a warn/error log level.
	if cfg.ConfigFilePath != "" {
		keys := cfg.FileSourcedKeys()
		supplied := "(no settings)"
		if len(keys) > 0 {
			supplied = strings.Join(keys, ", ")
		}
		logger.LogError(fmt.Sprintf("config file %s supplied: %s", cfg.ConfigFilePath, supplied), startupMeta)
	}

	// SetLogstashLogger opens a UDP socket logs.Logger has no way to close; harmless under main,
	// but a repeated call to run() (as in a test) would leak one socket per call.
	if host, port, source := secrets.ResolveLogServer(cfg, logger, startupMeta); host != "" {
		if err := logger.SetLogstashLogger(host, port); err != nil {
			logger.LogError(fmt.Sprintf("logstash dial to %s:%d (%s) failed: %v; continuing with console-only logging",
				host, port, source, err), startupMeta)
		} else {
			logger.LogInfo(fmt.Sprintf("logstash logging enabled via %s (%s:%d)", source, host, port), startupMeta)
		}
	}

	token, tokenSource, err := secrets.ResolveToken(cfg, logger, startupMeta)
	if err != nil {
		// Fail fast: a service that can't resolve a print token would otherwise log "listening"
		// and look healthy while requireToken answers 503 to everything forever. Distinct message
		// prefix from config.Load's "invalid configuration" above, since this can be a live outage
		// (Vault unreachable) rather than a bad value.
		logger.LogError(fmt.Sprintf("cannot start: %v", err), startupMeta)
		return err
	}
	cfg.AuthToken = token
	logger.LogInfo(fmt.Sprintf("print token resolved from %s", tokenSource), startupMeta)

	// LogError, not LogInfo: AllowPrivateTargets=true is a total SSRF-check bypass and must
	// survive a warn/error PRINT_GATEWAY_LOG_LEVEL.
	if cfg.AllowPrivateTargets {
		logger.LogError(fmt.Sprintf("%s=true: file_url target checks (address AND port) are disabled — do not set this in a deployment reachable by an untrusted caller",
			config.AllowPrivateTargetsEnv), startupMeta)
	}
	if len(cfg.FetchAllowedHosts) == 0 {
		logger.LogInfo(fmt.Sprintf("%s not set: file_url may target any public host", cfg.Source(config.FetchAllowedHostsEnv)), startupMeta)
	} else {
		logger.LogInfo(fmt.Sprintf("file_url restricted to hosts: %s", strings.Join(cfg.FetchAllowedHosts, ", ")), startupMeta)
	}

	// store stays a concrete, nilable *objstore.MinIO so assigning it into the two interface vars
	// below is only done when non-nil — avoiding the non-nil-interface-wrapping-a-nil-pointer trap.
	store := newObjectStore(cfg, logger, startupMeta)
	var objectGetter printgw.ObjectStore
	var presigner httpapi.Presigner
	if store != nil {
		objectGetter = store
		presigner = store
	}

	fetcher := fetch.NewSafeFetcher(cfg.AllowPrivateTargets, cfg.FetchAllowedHosts, cfg.FetchMaxBytes)
	svc := printgw.NewService(cups.NewLPSubmitter(), fetcher, objectGetter,
		printgw.Timeouts{Submit: cfg.SubmitTimeout, Fetch: cfg.FetchTimeout, S3: cfg.S3Timeout}, cfg.S3MaxBytes)
	api := httpapi.New(cfg, logger, svc, presigner)
	server := httpapi.NewServer(api)

	// Listen before logging "listening": BindHost is an unvalidated env value (an operator typo
	// there fails at net.Listen, not at config validation), so announcing an address before it's
	// known to actually be bindable would repeat the exact "listening on <typo>" trap the deleted
	// config-file address validation used to guard against.
	ln, err := net.Listen("tcp", cfg.Addr())
	if err != nil {
		logger.LogError(fmt.Sprintf("cannot listen on %s: %v", cfg.Addr(), err), startupMeta)
		return err
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.LogInfo(fmt.Sprintf("print gateway (prototype) listening on %s", ln.Addr()), startupMeta)
		serveErr <- server.Serve(ln)
	}()

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.LogError(fmt.Sprintf("server exited: %v", err), startupMeta)
			return err
		}
		return nil

	case <-ctx.Done():
		stopSignals() // restore default signal behavior so a second signal can force-kill
		logger.LogInfo("shutdown signal received, draining in-flight requests", startupMeta)

		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownGrace)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.LogError(fmt.Sprintf("graceful shutdown did not complete within %s: %v", cfg.ShutdownGrace, err), startupMeta)
			return err
		}
		logger.LogInfo("shutdown complete", startupMeta)
		return nil
	}
}

// newObjectStore builds the S3/MinIO-backed ObjectStore, returning nil whenever object storage
// isn't usable — never an error, since multipart upload remains the primary intake path and a
// missing or broken S3 config should just leave the s3_key/presign paths answering 503.
func newObjectStore(cfg config.Config, logger logs.Logger, meta *logs.LogMetaData) *objstore.MinIO {
	switch {
	case cfg.S3Endpoint == "" && cfg.S3Bucket == "":
		return nil // object storage deliberately not configured; nothing to log
	case cfg.S3Endpoint == "" || cfg.S3Bucket == "":
		logger.LogError(fmt.Sprintf("%s and %s must both be set (%s=%q, %s=%q); object storage disabled",
			cfg.Source(config.S3EndpointEnv), cfg.Source(config.S3BucketEnv),
			cfg.Source(config.S3EndpointEnv), cfg.S3Endpoint, cfg.Source(config.S3BucketEnv), cfg.S3Bucket), meta)
		return nil
	}

	accessKey, secretKey, source := secrets.ResolveS3Credentials(cfg, logger, meta)
	if accessKey == "" || secretKey == "" {
		logger.LogError(fmt.Sprintf("%s is set but no S3 credentials resolved from vault or %s/%s; object storage disabled",
			cfg.Source(config.S3EndpointEnv), cfg.Source(config.S3AccessKeyEnv), cfg.Source(config.S3SecretKeyEnv)), meta)
		return nil
	}

	if cfg.S3Region == "" {
		// A failed bucket-location lookup on a non-AWS backend silently signs as "us-east-1"
		// instead of erroring (see CloudStorageStreamingClient.PresignGetURL), so this is LogError.
		logger.LogError(fmt.Sprintf("%s is set but %s is empty: presigned URLs may be silently signed for the wrong region against a non-AWS endpoint",
			cfg.Source(config.S3EndpointEnv), cfg.Source(config.S3RegionEnv)), meta)
	}

	store, err := objstore.New(cfg.S3Endpoint, cfg.S3Bucket, accessKey, secretKey, cfg.S3Region, cfg.S3Insecure, logger, meta)
	if err != nil {
		logger.LogError(fmt.Sprintf("object storage init failed: %v; object storage disabled", err), meta)
		return nil
	}
	logger.LogInfo(fmt.Sprintf("object storage enabled: endpoint=%s bucket=%s credentials-source=%s", cfg.S3Endpoint, cfg.S3Bucket, source), meta)
	return store
}
