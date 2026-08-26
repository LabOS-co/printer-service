package httpapi

import (
	"context"
	"crypto/rand"
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

// requestIDHeader is the labOS-wide correlation id convention (the
// kibana-search skill queries by this value). We honor one supplied by the
// caller so a request can be traced across services, and generate one
// ourselves otherwise so every request is still correlatable in isolation.
const requestIDHeader = "X-Laas-Identifier"

// maxRequestIDLen keeps a caller-supplied id from bloating log lines; there
// is no protocol reason for it to be long.
const maxRequestIDLen = 128

type ctxKey int

const requestIDKey ctxKey = iota

// requestIDFrom reads the id requestContext put on the request context. It
// returns "" if requestContext was never run (should not happen in
// production wiring, but callers must not crash if it does).
func requestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// newRequestID generates a correlation id when the caller didn't supply one.
//
// rand.Text is used rather than rand.Read+hex because as of Go 1.24 it
// cannot fail: there is no error branch, and therefore no fallback path that
// could hand the same constant id to every concurrent request.
func newRequestID() string {
	return "req-" + rand.Text()
}

// sanitizeRequestID accepts a caller-supplied id only if it is entirely
// printable ASCII and of plausible length, so the correlation id can never
// become an injection vector into a structured log field.
//
// The test is byte-wise and allowlist-shaped on purpose. A rune-wise
// `r < 0x20 || r == 0x7f` check — the obvious formulation, and the one this
// replaced — lets three classes through, all of which net/http accepts in a
// header value: the C1 controls (U+0085 NEL is a line terminator to many log
// consumers), the Unicode line/paragraph separators (U+2028/U+2029), and
// bytes that are not valid UTF-8 at all (0xff). Rejecting everything outside
// 0x20..0x7e covers all three without enumerating them.
func sanitizeRequestID(id string) string {
	if id == "" || len(id) > maxRequestIDLen {
		return ""
	}
	for i := 0; i < len(id); i++ {
		if id[i] < 0x20 || id[i] > 0x7e {
			return ""
		}
	}
	return id
}

// logSafeRequestID renders a rejected id for a log line: quoted so any
// control byte is escaped rather than emitted, and truncated so an
// over-length id cannot itself bloat the log.
func logSafeRequestID(raw string) string {
	const maxLogged = 64
	if len(raw) > maxLogged {
		return fmt.Sprintf("%q (truncated from %d bytes)", raw[:maxLogged], len(raw))
	}
	return fmt.Sprintf("%q", raw)
}

// requestContext assigns every request a correlation id — the caller's own
// X-Laas-Identifier if present and well-formed, else a freshly generated
// one — and stores it on the request context. Downstream handlers build a
// *logs.LogMetaData from it per request (see API.requestMeta) rather than
// sharing one mutable pointer across all concurrent requests (P0-5).
//
// Shaped as func(http.Handler) http.Handler so it can wrap the whole mux
// once (see NewServer) instead of being repeated per route: a route that
// forgot it would still serve, just with an empty id and no echoed header,
// which is the kind of omission that degrades silently.
func (a *API) requestContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := r.Header.Get(requestIDHeader)
		id := sanitizeRequestID(raw)
		if id == "" {
			id = newRequestID()
			// Distinguish "caller sent nothing" from "caller sent something we
			// refused". Silently relabelling a supplied id breaks a
			// cross-service trace exactly when someone is trying to follow
			// one, so say so, and name the substitute.
			if raw != "" {
				md, _ := a.requestMeta(r)
				md.JobId = id
				a.logger.LogInfo(fmt.Sprintf("rejected caller-supplied %s %s; using %s instead",
					requestIDHeader, logSafeRequestID(raw), id), md)
			}
		}
		w.Header().Set(requestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
	})
}

// requireToken rejects any request that does not carry the shared secret
// the calling system was issued. The token is the second line of defence
// only — the listen address is what actually keeps the server off other
// networks — but it stops any other host that can already reach the port
// from printing.
func (a *API) requireToken(next http.HandlerFunc) http.HandlerFunc {
	expected := a.cfg.AuthToken
	return func(w http.ResponseWriter, r *http.Request) {
		if expected == "" {
			a.fail(w, r, &apperr.HTTPError{
				Status: http.StatusServiceUnavailable,
				Public: "server is not configured for authentication",
				// Naming only %s would be misleading now that the token can
				// also come from Vault (see internal/secrets): an empty
				// cfg.AuthToken here means resolution produced nothing from
				// EITHER source, not specifically that this one env var is
				// unset.
				Internal: fmt.Errorf("no print token resolved from Vault or %s, refusing to serve unauthenticated (%s %s)",
					config.AuthTokenEnv, r.Method, r.URL.Path),
			})
			return
		}
		// Constant-time compare so a caller cannot recover the token by timing.
		if subtle.ConstantTimeCompare([]byte(r.Header.Get(authTokenHeader)), []byte(expected)) != 1 {
			a.fail(w, r, &apperr.HTTPError{
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
