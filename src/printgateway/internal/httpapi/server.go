package httpapi

import (
	"log"
	"net/http"
	"strings"

	"github.com/LabOS-co/go-packages/logs"
	"github.com/go-chi/chi/v5"

	"printgateway/internal/config"
)

// NewServer builds the HTTP server for this API on its own chi router.
// Timeouts/limits all come from a.cfg rather than the http.Server zero
// value, which would let a slow or silent client hold a connection forever.
func NewServer(a *API) *http.Server {
	mux := chi.NewRouter()
	mux.HandleFunc("/print", a.requireToken(a.printHandler))
	mux.HandleFunc("/files/presign", a.requireToken(a.presignHandler))
	// Unauthenticated: the network-proxy's health check has no token.
	mux.HandleFunc("/status", a.statusHandler)

	return &http.Server{
		Addr:              a.cfg.Addr(),
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
// handler. Extracted so tests build the exact production chain rather than
// hand-copying the nesting order. requireToken stays per-route since it's
// deliberately not universal (unauthenticated health checks need to bypass
// it). See maxBytes's and panicRecovery's own doc comments for why their
// nesting order specifically is load-bearing.
func (a *API) handlerChain(mux http.Handler) http.Handler {
	return a.requestContext(a.maxBytes(a.accessLog(a.panicRecovery(mux))))
}

// errorLogWriter routes net/http's own error lines into the same
// logs.Logger every handler uses, instead of net/http's default os.Stderr.
type errorLogWriter struct{ logger logs.Logger }

func (w errorLogWriter) Write(p []byte) (int, error) {
	w.logger.LogError(strings.TrimSuffix(string(p), "\n"), &logs.LogMetaData{Service: config.ServiceName})
	return len(p), nil
}
