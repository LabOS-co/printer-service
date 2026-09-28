package httpapi

import (
	"log"
	"net/http"
	"strings"

	"github.com/LabOS-co/go-packages/logs"
	"github.com/LabOS-co/go-packages/system_api"
	"github.com/go-chi/chi/v5"

	"printgateway/internal/apperr"
	"printgateway/internal/config"
)

// NewServer builds the HTTP server for this API on its own chi router: a pure builder, no I/O,
// no global state, safe to call from every handler test. Timeouts/limits come from a.cfg rather
// than the http.Server zero value, which would let a slow or silent client hold a connection forever.
func NewServer(a *API) *http.Server {
	mux := chi.NewRouter()
	mux.HandleFunc("/print", a.requireToken(a.printHandler))
	mux.HandleFunc("/files/presign", a.requireToken(a.presignHandler))
	// Unauthenticated: health checks have no token. system_api.Status, not Register, so this
	// stays free of Register's bundled Consul-registration side effect.
	mux.Get("/status", system_api.Status)
	// Route chi's default empty-405/plain-text-404 through the same labOS error envelope.
	mux.MethodNotAllowed(a.methodNotAllowed)
	mux.NotFound(a.notFound)

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

// handlerChain composes the middleware chain around mux: requestContext -> maxBytes -> accessLog
// -> panicRecovery -> requireToken (per-route) -> handler. Extracted so tests build the exact
// production chain; see maxBytes/panicRecovery for why the order is load-bearing.
func (a *API) handlerChain(mux http.Handler) http.Handler {
	return a.requestContext(a.maxBytes(a.accessLog(a.panicRecovery(mux))))
}

// methodNotAllowed answers chi's global 405 with the labOS error envelope. /status is the only
// method-routed path today, hence the single Allow value (RFC 9110 §15.5.6 requires it, and
// overriding chi's default handler drops the Allow header it would otherwise set).
func (a *API) methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Allow", http.MethodGet)
	a.fail(w, r, &apperr.HTTPError{Status: http.StatusMethodNotAllowed, Public: "use GET"})
}

// notFound answers chi's global 404 with the same labOS error envelope every other failure uses.
func (a *API) notFound(w http.ResponseWriter, r *http.Request) {
	a.fail(w, r, &apperr.HTTPError{Status: http.StatusNotFound, Public: "not found"})
}

// errorLogWriter routes net/http's own error lines into the same logs.Logger every handler uses.
type errorLogWriter struct{ logger logs.Logger }

func (w errorLogWriter) Write(p []byte) (int, error) {
	w.logger.LogError(strings.TrimSuffix(string(p), "\n"), &logs.LogMetaData{Service: config.ServiceName})
	return len(p), nil
}
