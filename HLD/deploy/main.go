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
	logger := logs.GetConsoleLogger()
	startupMeta := &logs.LogMetaData{Service: config.ServiceName}

	cfg, err := config.Load(os.Args, os.Getenv)
	if err != nil {
		logger.LogError(fmt.Sprintf("invalid configuration: %v", err), startupMeta)
		os.Exit(1)
	}

	token, tokenSource, err := secrets.ResolveToken(cfg, logger, startupMeta)
	if err != nil {
		logger.LogError(fmt.Sprintf("invalid configuration: %v", err), startupMeta)
		os.Exit(1)
	}
	cfg.AuthToken = token
	if tokenSource != "" {
		logger.LogInfo(fmt.Sprintf("print token resolved from %s", tokenSource), startupMeta)
	} else {
		// Not a startup failure (requireToken answers 503 to everything
		// instead — see its comment) but this is the moment an operator
		// most needs a line saying so: silence here previously meant the
		// service would come up looking healthy while unable to serve any
		// request at all.
		logger.LogInfo("no print token resolved from Vault or "+config.AuthTokenEnv+"; every request will be answered 503", startupMeta)
	}

	svc := printgw.NewService(cups.NewLPSubmitter(), fetch.NewHTTPFetcher(), cfg.SubmitTimeout)
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
