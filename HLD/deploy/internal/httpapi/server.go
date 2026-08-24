package httpapi

import "net/http"

// NewServer builds the HTTP server for this API, on its own ServeMux rather
// than http.DefaultServeMux.
//
// requestContext wraps the mux rather than each route, so every route — including
// any added later — is guaranteed a correlation id. requireToken stays per-route
// because it is deliberately not universal: /health and /ready must answer an
// unauthenticated probe.
func NewServer(a *API) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/print", a.requireToken(a.printHandler))

	return &http.Server{
		Addr:    a.cfg.Addr,
		Handler: a.requestContext(mux),
	}
}
