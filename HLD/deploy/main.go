// Initial prototype Print Gateway.
//
// Accepts a print request over HTTP, either multipart/form-data (file
// attached) or application/json ({"printer","file_url"} — server downloads
// the file itself). Either way, once the file is on local disk it's handed
// to CUPS via `lp -d <printer> <path>` — this server does not talk IPP
// itself and does not know about PPDs/media/resolution; CUPS's own queue
// configuration (already set up, static PPD, ippfix if that printer needs
// it) handles all of that. See internal/httpapi for the request contract.
//
// This file is wiring only: build the dependencies, start the server, and
// wait for either it to fail or a shutdown signal to arrive.
package main

import (
	"context"
	"errors"
	"fmt"
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
	"printgateway/internal/printgw"
	"printgateway/internal/secrets"
)

func main() {
	// Constructed WITHOUT a Host: GetLoggerWithSettings' own internal
	// setLogstashLogger call swallows a dial failure and reports success
	// regardless (logs@v1.5.2/logs.go), so giving it nothing to dial and
	// calling logger.SetLogstashLogger ourselves below — whose error we DO
	// check — is what lets a broken logstash address actually be noticed.
	// FormatJSON is what makes LogMetaData's fields (job_id, status,
	// duration, ...) queryable once they reach logstash; console output
	// stays human-readable regardless (createLogstashLogger always sets
	// ConsoleFormatter for it) but now goes to stderr — logrus.New()'s
	// default — rather than the old GetConsoleLogger()'s stdout. A wrapper
	// that only captured stdout needs updating.
	//
	// The discarded error is safe today: GetLoggerWithSettings has no
	// failure path of its own (it does no I/O; that's exactly why we drive
	// SetLogstashLogger separately below), so it always returns nil.
	logger, _ := logs.GetLoggerWithSettings(logs.LogsSettings{Format: logs.FormatJSON}, config.ServiceName)
	startupMeta := &logs.LogMetaData{Service: config.ServiceName}

	cfg, err := config.Load(os.Args, os.Getenv)
	if err != nil {
		logger.LogError(fmt.Sprintf("invalid configuration: %v", err), startupMeta)
		os.Exit(1)
	}

	// Set before anything else logs, so PRINT_GATEWAY_LOG_LEVEL actually
	// governs every startup line that follows it — not just request
	// handling.
	if err := logger.SetLogLevel(cfg.LogLevel); err != nil {
		logger.LogError(fmt.Sprintf("%s: invalid level %q, defaulting to info: %v", config.LogLevelEnv, cfg.LogLevel, err), startupMeta)
	}

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
		// F2: a service that cannot resolve a print token cannot serve any
		// request. Previously this logged and continued, so a misconfigured
		// deploy printed "listening" and looked healthy while requireToken
		// answered 503 to everything forever. Fail fast instead.
		//
		// Distinct prefix from config.Load's "invalid configuration" above:
		// this can be a live outage (Vault unreachable, env also unset), not
		// only a misconfigured value, and an operator grepping for
		// "invalid configuration" during an outage should not be misdirected
		// at env/flag parsing.
		logger.LogError(fmt.Sprintf("cannot start: %v", err), startupMeta)
		os.Exit(1)
	}
	cfg.AuthToken = token
	logger.LogInfo(fmt.Sprintf("print token resolved from %s", tokenSource), startupMeta)

	// SSRF defense (HLD §11.3, P0-4): logged at startup, not just enforced
	// silently, because both are exactly the kind of misconfiguration that
	// should be loud rather than discovered later from an incident.
	//
	// AllowPrivateTargets=true is a total bypass of every target check
	// (address, port, and the post-connect recheck alike), so this one uses
	// LogError rather than LogInfo: PRINT_GATEWAY_LOG_LEVEL=warn/error is a
	// plausible production setting, and this line must survive it or the
	// "logged loudly" guarantee the README makes is false.
	if cfg.AllowPrivateTargets {
		logger.LogError(fmt.Sprintf("%s=true: file_url target checks (address AND port) are disabled — do not set this in a deployment reachable by an untrusted caller",
			config.AllowPrivateTargetsEnv), startupMeta)
	}
	if len(cfg.FetchAllowedHosts) == 0 {
		logger.LogInfo(fmt.Sprintf("%s not set: file_url may target any public host", config.FetchAllowedHostsEnv), startupMeta)
	} else {
		logger.LogInfo(fmt.Sprintf("file_url restricted to hosts: %s", strings.Join(cfg.FetchAllowedHosts, ", ")), startupMeta)
	}

	fetcher := fetch.NewSafeFetcher(cfg.AllowPrivateTargets, cfg.FetchAllowedHosts, cfg.FetchMaxBytes)
	svc := printgw.NewService(cups.NewLPSubmitter(), fetcher, printgw.Timeouts{Submit: cfg.SubmitTimeout, Fetch: cfg.FetchTimeout})
	api := httpapi.New(cfg, logger, svc)
	server := httpapi.NewServer(api)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		logger.LogInfo(fmt.Sprintf("print gateway (prototype) listening on %s", cfg.Addr), startupMeta)
		serveErr <- server.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.LogError(fmt.Sprintf("server exited: %v", err), startupMeta)
			os.Exit(1)
		}

	case <-ctx.Done():
		stop() // restore default signal behavior so a second signal can force-kill
		logger.LogInfo("shutdown signal received, draining in-flight requests", startupMeta)

		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownGrace)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.LogError(fmt.Sprintf("graceful shutdown did not complete within %s: %v", cfg.ShutdownGrace, err), startupMeta)
			os.Exit(1)
		}
		logger.LogInfo("shutdown complete", startupMeta)
	}
}
