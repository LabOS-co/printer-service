package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"net/http"
	"runtime/debug"
	"strconv"
	"time"

	"printgateway/internal/apperr"
)

// authTokenHeader carries the shared secret the calling system was issued.
const authTokenHeader = "X-Labos-Print-Token"

// requestIDHeader is the labOS-wide correlation id convention.
const requestIDHeader = "X-Laas-Identifier"

// maxRequestIDLen caps a caller-supplied request id.
const maxRequestIDLen = 128

type ctxKey int

const requestIDKey ctxKey = iota

// requestIDFrom reads the id requestContext put on the request context,
// returning "" if requestContext never ran.
func requestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// newRequestID generates a correlation id when the caller didn't supply one.
func newRequestID() string {
	return "req-" + rand.Text()
}

// sanitizeRequestID accepts a caller-supplied id only if it is printable
// ASCII of plausible length, so it can never become a log-injection vector.
// Rejects the full 0x00-0x1f/0x7f-0xff range rather than enumerating
// specific bytes: that range also covers C1 controls, Unicode line
// separators, and invalid UTF-8 that a naive rune-wise control-char check
// would let through.
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

// logSafeRequestID renders a rejected id for a log line, quoted and
// truncated so it can't inject control bytes or bloat the log itself.
func logSafeRequestID(raw string) string {
	const maxLogged = 64
	if len(raw) > maxLogged {
		return fmt.Sprintf("%q (truncated from %d bytes)", raw[:maxLogged], len(raw))
	}
	return fmt.Sprintf("%q", raw)
}

// requestContext assigns every request a correlation id — the caller's own
// X-Laas-Identifier if present and well-formed, else a freshly generated
// one — and stores it on the request context.
func (a *API) requestContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := r.Header.Get(requestIDHeader)
		id := sanitizeRequestID(raw)
		if id == "" {
			id = newRequestID()
			// Log a rejected caller-supplied id rather than silently
			// relabelling it, so a broken cross-service trace is visible.
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
// the calling system was issued.
func (a *API) requireToken(next http.HandlerFunc) http.HandlerFunc {
	expected := a.cfg.AuthToken
	return func(w http.ResponseWriter, r *http.Request) {
		if expected == "" {
			// Must fail closed here: ConstantTimeCompare returns 1 for two
			// zero-length slices, so without this branch an empty
			// AuthToken would authorize any request with no token header.
			a.fail(w, r, &apperr.HTTPError{
				Status: http.StatusServiceUnavailable,
				Public: "server is not configured for authentication",
				Internal: fmt.Errorf("no print token configured, refusing to serve unauthenticated (%s %s)",
					r.Method, r.URL.Path),
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

// panicRecovery recovers a panic from the handler chain below it and logs
// it with request correlation before responding 500; the client never sees
// the stack trace, only the same generic response a.fail sends for any
// other error.
//
// Deliberately nested INSIDE accessLog, not outside: this lets a.fail's 500
// go through accessLog's statusRecorder so the completion log's Status
// matches what the client actually got, at the cost of not recovering a
// panic in requestContext/maxBytes/accessLog itself.
func (a *API) panicRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			if rec == http.ErrAbortHandler {
				// net/http's own "close silently, no response" signal; must not
				// be turned into a manufactured 500.
				panic(rec)
			}
			if sr, ok := w.(*statusRecorder); ok && sr.wroteHeader {
				// A second WriteHeader is ignored by net/http but a.fail's body
				// would still be appended to what's already sent, corrupting the
				// response; abort the connection instead.
				md, _ := a.requestMeta(r)
				a.logger.LogError(fmt.Sprintf("panic after response already started (status %d already sent): %v\n%s",
					sr.status, rec, debug.Stack()), md)
				panic(http.ErrAbortHandler)
			}
			a.fail(w, r, &apperr.HTTPError{
				Status:   http.StatusInternalServerError,
				Public:   "internal server error",
				Internal: fmt.Errorf("panic: %v\n%s", rec, debug.Stack()),
			})
		}()
		next.ServeHTTP(w, r)
	})
}

// statusRecorder wraps http.ResponseWriter to capture the status code
// actually sent, so accessLog can report it after the handler runs.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (rec *statusRecorder) WriteHeader(code int) {
	if !rec.wroteHeader {
		rec.status = code
		rec.wroteHeader = true
	}
	rec.ResponseWriter.WriteHeader(code)
}

func (rec *statusRecorder) Write(b []byte) (int, error) {
	if !rec.wroteHeader {
		rec.WriteHeader(http.StatusOK)
	}
	return rec.ResponseWriter.Write(b)
}

// accessLog calls logs.Logger.LogAPICompletion exactly once per request,
// with the status actually sent, sitting outside requireToken and
// panicRecovery so both a 401 and a recovered panic still get logged with
// a duration.
//
// Bookkeeping runs in a defer so it still executes if next.ServeHTTP panics.
func (a *API) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			md, _ := a.requestMeta(r)
			ms := int(time.Since(start).Milliseconds())
			// Both fields are set: logs v1.5.2's formatters render the
			// human-readable message from ServiceDuration, not Duration.
			md.Duration = ms
			md.ServiceDuration = ms
			md.Status = strconv.Itoa(rec.status)
			a.logger.LogAPICompletion(md)
		}()
		next.ServeHTTP(rec, r)
	})
}

// maxBytes bounds every inbound request body via http.MaxBytesReader, sized
// by Content-Type (multipart uploads get the larger limit, everything else
// the tighter JSON one).
//
// Must wrap accessLog, not be nested inside it: MaxBytesReader detects an
// oversized request via an unexported interface type-asserted against the
// ResponseWriter it's given, which statusRecorder (accessLog's wrapper)
// doesn't satisfy — nesting the other way silently breaks the
// connection-close-on-overflow behavior.
func (a *API) maxBytes(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := a.cfg.MaxJSONBytes
		if isMultipart(r.Header.Get("Content-Type")) {
			limit = a.cfg.MaxUploadBytes
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next.ServeHTTP(w, r)
	})
}
