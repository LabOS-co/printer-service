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

// NewServer builds the HTTP server for this API on its own chi router. It is
// a pure builder — no I/O, no global state — so it stays safe to call from
// every handler test; the Consul self-registration side effect that used to
// live here (via system_api.Register) is done once, in main.go's run(), after
// the listener is actually up. See run's own comment for why.
//
// Timeouts/limits all come from a.cfg rather than the http.Server zero
// value, which would let a slow or silent client hold a connection forever.
func NewServer(a *API) *http.Server {
	mux := chi.NewRouter()
	mux.HandleFunc("/print", a.requireToken(a.printHandler))
	mux.HandleFunc("/files/presign", a.requireToken(a.presignHandler))
	// Unauthenticated: the network-proxy's and Nomad's health checks have no
	// token. system_api.Status (not system_api.Register) so this stays free
	// of Register's bundled Consul-registration side effect.
	mux.Get("/status", system_api.Status)
	// chi's defaults for an unmatched method/path are an empty 405 body and a
	// plain-text 404; route both through the same labOS error envelope every
	// other response uses instead.
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

// methodNotAllowed answers chi's global 405 case with the same labOS error
// envelope every other failure uses, rather than chi's empty-body default.
// /status is the only method-routed path today (/print and /files/presign
// use HandleFunc, i.e. all methods), hence the single Allow value — RFC 9110
// §15.5.6 requires it on a 405, and overriding chi's default handler drops
// the Allow header chi would otherwise have set from the route itself.
func (a *API) methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Allow", http.MethodGet)
	a.fail(w, r, &apperr.HTTPError{Status: http.StatusMethodNotAllowed, Public: "use GET"})
}

// notFound answers chi's global 404 case with the same labOS error envelope
// every other failure uses, rather than chi's plain-text default.
func (a *API) notFound(w http.ResponseWriter, r *http.Request) {
	a.fail(w, r, &apperr.HTTPError{Status: http.StatusNotFound, Public: "not found"})
}

// errorLogWriter routes net/http's own error lines into the same
// logs.Logger every handler uses, instead of net/http's default os.Stderr.
type errorLogWriter struct{ logger logs.Logger }

func (w errorLogWriter) Write(p []byte) (int, error) {
	w.logger.LogError(strings.TrimSuffix(string(p), "\n"), &logs.LogMetaData{Service: config.ServiceName})
	return len(p), nil
}
