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
		Handler:           a.handlerChain(mux),
		ReadHeaderTimeout: a.cfg.ReadHeaderTimeout,
		ReadTimeout:       a.cfg.ReadTimeout,
		WriteTimeout:      a.cfg.WriteTimeout,
		IdleTimeout:       a.cfg.IdleTimeout,
		MaxHeaderBytes:    a.cfg.MaxHeaderBytes,
		ErrorLog:          log.New(errorLogWriter{a.logger}, "", 0),
	}
}

// handlerChain composes the middleware chain around mux: requestContext →
// maxBytes → accessLog → panicRecovery → requireToken (per-route) →
// handler. Extracted into its own function, rather than composed inline in
// NewServer, so a future httpapi test builds the exact production chain
// instead of hand-copying the nesting order — a copy that drifted from this
// one would still compile and still pass.
//
// This is the plan's own "Middleware order" section (panics →
// requestContext → accessLog → maxBytes → requireToken → handler) with two
// deliberate deviations, both verified live rather than assumed correct
// from the sketch:
//
//  1. maxBytes wraps accessLog, not the reverse. http.MaxBytesReader
//     signals the server that a request was cut short via an unexported
//     interface it type-asserts the ResponseWriter against — accessLog's
//     statusRecorder wrapper doesn't (and structurally can't) implement
//     that interface, so nesting maxBytes inside accessLog silently
//     defeats the size limit's connection-handling (verified live: no
//     "Connection: close", the connection reused, net/http draining part
//     of the oversized body anyway). See maxBytes's own doc comment.
//  2. panicRecovery is nested inside accessLog, not outside it. A panic
//     that unwinds past accessLog's own frame used to skip its
//     post-handler bookkeeping entirely, dropping the completion log on
//     exactly the request most worth one — accessLog's bookkeeping is now
//     deferred specifically so that no longer depends on nesting order,
//     but the nesting is kept anyway because it also lets a recovered
//     panic's 500 response be written through accessLog's statusRecorder,
//     so the logged Status matches what the client actually received. See
//     panicRecovery's own doc comment.
//
// requireToken itself stays per-route rather than joining this chain,
// because it is deliberately not universal — /health and /ready (not yet
// added) must answer an unauthenticated probe.
func (a *API) handlerChain(mux http.Handler) http.Handler {
	return a.requestContext(a.maxBytes(a.accessLog(a.panicRecovery(mux))))
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
