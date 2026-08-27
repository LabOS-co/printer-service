package httpapi

import (
	"log"
	"net/http"
	"strings"

	"github.com/LabOS-co/go-packages/logs"

	"printgateway/internal/config"
)

// NewServer builds the HTTP server for this API, on its own ServeMux rather
// than http.DefaultServeMux.
//
// requestContext wraps the mux rather than each route, so every route — including
// any added later — is guaranteed a correlation id. requireToken stays per-route
// because it is deliberately not universal: /health and /ready must answer an
// unauthenticated probe.
//
// Every timeout/limit below comes from a.cfg rather than being left at the
// http.Server zero value (P0-6): unset, a slow or silent client can hold a
// connection open forever, with nothing here to notice or reclaim it.
// Graceful shutdown itself is main's job (Shutdown is called there on
// SIGINT/SIGTERM) — this only builds the *http.Server that call acts on.
func NewServer(a *API) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/print", a.requireToken(a.printHandler))
	mux.HandleFunc("/files/presign", a.requireToken(a.presignHandler))

	return &http.Server{
		Addr:              a.cfg.Addr,
		Handler:           a.requestContext(mux),
		ReadHeaderTimeout: a.cfg.ReadHeaderTimeout,
		ReadTimeout:       a.cfg.ReadTimeout,
		WriteTimeout:      a.cfg.WriteTimeout,
		IdleTimeout:       a.cfg.IdleTimeout,
		MaxHeaderBytes:    a.cfg.MaxHeaderBytes,
		ErrorLog:          log.New(errorLogWriter{a.logger}, "", 0),
	}
}

// errorLogWriter bridges net/http's own error lines — a broken write, its
// internal enforcement of ReadHeaderTimeout, a panic recovered by the
// stdlib server itself — into the same logs.Logger every handler uses,
// instead of letting them fall through to net/http's default os.Stderr
// logger and land outside the labOS log stream.
type errorLogWriter struct{ logger logs.Logger }

func (w errorLogWriter) Write(p []byte) (int, error) {
	w.logger.LogError(strings.TrimSuffix(string(p), "\n"), &logs.LogMetaData{Service: config.ServiceName})
	return len(p), nil
}
