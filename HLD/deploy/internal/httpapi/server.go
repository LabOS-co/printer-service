package httpapi

import "net/http"

// NewServer builds the HTTP server for this API, on its own ServeMux rather
// than http.DefaultServeMux.
func NewServer(a *API) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/print", a.requireToken(a.printHandler))

	return &http.Server{
		Addr:    a.cfg.Addr,
		Handler: mux,
	}
}
