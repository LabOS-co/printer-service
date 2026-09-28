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
	"github.com/LabOS-co/go-packages/system_api"
	"github.com/LabOS-co/go-packages/system_args"
	"github.com/go-chi/chi/v5"

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

	// GetLoggerWithSettings does no I/O and always returns nil here (no Host set), so the
	// discarded error is safe; run() sets up logstash itself below, where the error IS checked.
	logger, _ := logs.GetLoggerWithSettings(logs.LogsSettings{Format: logs.FormatJSON}, config.ServiceName)

	registerToConsul, err := resolveRegisterToConsul()
	if err != nil {
		logger.LogError(fmt.Sprintf("invalid configuration: %v", err), &logs.LogMetaData{Service: config.ServiceName})
		os.Exit(1)
	}

	if err := run(ctx, stop, os.Getenv, os.ReadFile, logger, registerToConsul); err != nil {
		os.Exit(1)
	}
}

// resolveRegisterToConsul turns system_args.ShouldRegisterToConsul's panic on a malformed
// PORT/LABOS_ENV/GATEWAY/CONSUL_ADDR value into a clean, logged startup failure.
func resolveRegisterToConsul() (register bool, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("system_args: %v", r)
		}
	}()
	return system_args.ShouldRegisterToConsul(), nil
}

// run holds startup, request serving, and shutdown; main() is a thin os.Exit wrapper around it.
// logger is injected because logstashLogger has an unsynchronized global counter tests would
// race on, and registerToConsul is resolved by main() because system_args's flag.Parse/sync.Once
// must run at most once against the real os.Args, not a test binary's.
func run(ctx context.Context, stopSignals func(), getenv func(string) string, readFile func(string) ([]byte, error), logger logs.Logger, registerToConsul bool) error {
	startupMeta := &logs.LogMetaData{Service: config.ServiceName}

	cfg, err := config.Load(getenv, readFile)
	if err != nil {
		logger.LogError(fmt.Sprintf("invalid configuration: %v", err), startupMeta)
		return err
	}

	// Set before anything else logs, so PRINT_GATEWAY_LOG_LEVEL governs every line that follows.
	if err := logger.SetLogLevel(cfg.LogLevel); err != nil {
		logger.LogError(fmt.Sprintf("%s: invalid level %q, defaulting to info: %v", cfg.Source(config.LogLevelEnv), cfg.LogLevel, err), startupMeta)
	}

	// LogError, not LogInfo: config-file/env precedence is inverted relative to every ops
	// reflex, so this must survive a warn/error log level.
	if cfg.ConfigFilePath != "" {
		keys := cfg.FileSourcedKeys()
		supplied := "(no settings)"
		if len(keys) > 0 {
			supplied = strings.Join(keys, ", ")
		}
		logger.LogError(fmt.Sprintf("config file %s supplied: %s", cfg.ConfigFilePath, supplied), startupMeta)
	}

	// SetLogstashLogger opens a UDP socket logs.Logger can't close; repeated run() calls would leak one.
	if host, port, source := secrets.ResolveLogServer(cfg, logger, startupMeta); host != "" {
		if err := logger.SetLogstashLogger(host, port); err != nil {
			logger.LogError(fmt.Sprintf("logstash dial to %s:%d (%s) failed: %v; continuing with console-only logging",
				host, port, source, err), startupMeta)
		} else {
			logger.LogInfo(fmt.Sprintf("logstash logging enabled via %s (%s:%d)", source, host, port), startupMeta)
		}
	}

	if cfg.RequireAuth {
		token, tokenSource, err := secrets.ResolveToken(cfg, logger, startupMeta)
		if err != nil {
			// Fail fast: otherwise this would log "listening" and look healthy while every
			// request gets 503 from requireToken forever.
			logger.LogError(fmt.Sprintf("cannot start: %v", err), startupMeta)
			return err
		}
		cfg.AuthToken = token
		logger.LogInfo(fmt.Sprintf("print token resolved from %s", tokenSource), startupMeta)
	} else {
		// LogError, not LogInfo: this deployment posture must not be missed at a warn/error log level.
		logger.LogError(fmt.Sprintf("%s=false: /print and /files/presign accept unauthenticated requests",
			config.RequireAuthEnv), startupMeta)
	}

	// LogError, not LogInfo: this is a total SSRF bypass that must survive a warn/error log level.
	if cfg.AllowPrivateTargets {
		logger.LogError(fmt.Sprintf("%s=true: file_url target checks (address AND port) are disabled — do not set this in a deployment reachable by an untrusted caller",
			config.AllowPrivateTargetsEnv), startupMeta)
	}
	if len(cfg.FetchAllowedHosts) == 0 {
		logger.LogInfo(fmt.Sprintf("%s not set: file_url may target any public host", cfg.Source(config.FetchAllowedHostsEnv)), startupMeta)
	} else {
		logger.LogInfo(fmt.Sprintf("file_url restricted to hosts: %s", strings.Join(cfg.FetchAllowedHosts, ", ")), startupMeta)
	}

	// store stays a concrete, nilable *objstore.MinIO: assigning a nil *MinIO into the interface
	// vars below would make them non-nil interfaces wrapping a nil pointer, so that's guarded here.
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

	// Listen before logging "listening": BindHost is unvalidated, so a typo must fail here, not after.
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

	// Local-dev-only Consul self-registration; inert under Nomad, which owns it via the job spec.
	// Runs in its own goroutine so a stalled Consul agent can't delay shutdown or the listener.
	if registerToConsul {
		// throwaway router: Register also mounts /status, which NewServer already did.
		go system_api.Register(chi.NewRouter(), config.ServiceName, cfg.Port, logger)
	}

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

// newObjectStore builds the S3/MinIO-backed ObjectStore, returning nil (never an error) whenever
// object storage isn't usable — multipart upload remains the primary intake path.
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
		// A failed bucket-location lookup on a non-AWS backend silently signs as "us-east-1" instead of erroring.
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
