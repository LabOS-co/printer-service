package httpapi

import (
	"crypto/subtle"
	"fmt"
	"net/http"

	"printgateway/internal/apperr"
	"printgateway/internal/config"
)

// The calling system sends this header on every request; the expected
// value is supplied to the server through the environment so it never sits
// in the code.
const authTokenHeader = "X-Labos-Print-Token"

// requireToken rejects any request that does not carry the shared secret
// the calling system was issued. The token is the second line of defence
// only — the listen address is what actually keeps the server off other
// networks — but it stops any other host that can already reach the port
// from printing.
func (a *API) requireToken(next http.HandlerFunc) http.HandlerFunc {
	expected := a.cfg.AuthToken
	return func(w http.ResponseWriter, r *http.Request) {
		if expected == "" {
			a.fail(w, &apperr.HTTPError{
				Status: http.StatusServiceUnavailable,
				Public: "server is not configured for authentication",
				Internal: fmt.Errorf("%s is not set, refusing to serve unauthenticated (%s %s)",
					config.AuthTokenEnv, r.Method, r.URL.Path),
			})
			return
		}
		// Constant-time compare so a caller cannot recover the token by timing.
		if subtle.ConstantTimeCompare([]byte(r.Header.Get(authTokenHeader)), []byte(expected)) != 1 {
			a.fail(w, &apperr.HTTPError{
				Status: http.StatusUnauthorized,
				Public: "unauthorized",
				Internal: fmt.Errorf("rejected %s %s from %s: bad or missing %s",
					r.Method, r.URL.Path, r.RemoteAddr, authTokenHeader),
			})
			return
		}
		next(w, r)
	}
}
