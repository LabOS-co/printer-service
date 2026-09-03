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
			// Unreachable through main(): secrets.ResolveToken now errors
			// whenever no source produces a token, and main exits 1 on that
			// (F2) before httpapi.New is ever called. This branch stays
			// anyway — it's the only thing standing between an empty
			// AuthToken (a future second entrypoint, a test building API
			// directly) and fail-OPEN: crypto/subtle.ConstantTimeCompare
			// returns 1 for two zero-length slices, so with this branch
			// removed an empty expected token would authorize any request
			// that sends no token header at all.
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

// panicRecovery recovers a panic from the handler chain below it, so a bug
// never reaches net/http's own recoverer without at least a job_id-bearing
// log line — the stdlib recoverer does log the panic (server.go wires
// http.Server.ErrorLog to this same logger, see server.go's comment), just
// with no request correlation and no application-level context. The stack
// trace is logged (Internal only); the client sees a generic 500 through
// the same a.fail path every other error uses, never the trace itself.
//
// Deliberately nested INSIDE accessLog (see NewServer), not outside it, even
// though the plan's "Middleware order" section sketches panics as
// outermost. Verified live why that sketch does not work: a panic that
// unwinds past accessLog's own frame skips its post-handler bookkeeping
// entirely if that bookkeeping is plain code after next.ServeHTTP rather
// than deferred, so the one request most worth a completion log — a
// handler that just crashed — would be the one request that never gets
// one (accessLog now defers its bookkeeping specifically so this holds
// regardless of nesting order too — belt and suspenders). Nesting
// panicRecovery inside accessLog additionally lets a.fail write the 500
// through accessLog's statusRecorder rather than the raw ResponseWriter,
// so the completion log's Status field matches what the client actually
// received instead of the recorder's untouched 200 default. The trade is
// that a panic inside requestContext, maxBytes, or accessLog itself (none
// of which parse untrusted input directly — their calls to the logger
// aside) is not recovered — judged the better trade than an accurate
// crash going unlogged.
//
// Two cases besides the ordinary "recover, log, respond 500" one:
//
//   - http.ErrAbortHandler is net/http's own "abandon this connection, no
//     response, no stack log" signal (httputil.ReverseProxy raises it
//     deliberately). Recovering it and turning it into a 500 would
//     manufacture a response for a handler that explicitly asked for none,
//     so it is re-panicked immediately instead, letting net/http's own
//     handling (silent connection close) take over.
//   - A panic after the response has already started (statusRecorder saw a
//     WriteHeader/Write call) cannot be answered with a fresh 500: net/http
//     ignores a second WriteHeader but still appends a.fail's body to
//     whatever was already sent, producing a corrupt response a lenient
//     client can partially parse as a plausible success — verified live,
//     a handler that writes 200 plus a partial JSON body and then panics
//     otherwise reaches the client as 200 with two concatenated JSON
//     documents. The only safe response to a mid-write panic is to abort
//     the connection outright, via the same http.ErrAbortHandler signal.
func (a *API) panicRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			if rec == http.ErrAbortHandler {
				panic(rec)
			}
			if sr, ok := w.(*statusRecorder); ok && sr.wroteHeader {
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
// actually sent, so accessLog can report it after the handler has already
// written the response. Defaults to 200: net/http itself does this when a
// handler writes a body without ever calling WriteHeader, so a recorder
// that never saw a call must report the same thing net/http would send.
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

// accessLog calls logs.Logger.LogAPICompletion exactly once per request —
// the labOS per-request hook that, before this, was never invoked anywhere
// in this service (see A2/D1's history: JobId was set correctly per request
// well before this, but nothing ever emitted a completion line). It sits
// outside requireToken (see NewServer) so a 401 is still recorded with a
// duration, and outside panicRecovery (see that function's doc comment for
// why), so a recovered panic — or one aborted via http.ErrAbortHandler — is
// logged exactly like any other response, with the status actually sent.
//
// The bookkeeping runs in a defer, not as plain code after next.ServeHTTP:
// a plain post-call statement is skipped entirely if the call above it
// panics and the panic is never recovered below this frame — which used to
// be exactly what happened here before panicRecovery was nested inside this
// function (see its doc comment). The defer is kept even with that nesting,
// so this invariant no longer depends on which middleware sits where.
func (a *API) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			md, _ := a.requestMeta(r)
			ms := int(time.Since(start).Milliseconds())
			// logs v1.5.2's formatters (both LabOSFormatter and the JSON
			// path — see logstash_logger.go's APICompletedPattern call
			// sites) render the human-readable completion message from
			// ServiceDuration, not Duration. Verified live against a UDP
			// logstash listener: setting only Duration produces a correct
			// structured "duration" field but a message reading literally
			// "Duration: 0 ms" on every request. Both are set so the
			// message text and the structured field agree.
			md.Duration = ms
			md.ServiceDuration = ms
			md.Status = strconv.Itoa(rec.status)
			a.logger.LogAPICompletion(md)
		}()
		next.ServeHTTP(rec, r)
	})
}

// maxBytes bounds every inbound request body via http.MaxBytesReader before
// the request reaches requireToken or any handler (see NewServer's ordering
// and the "Middleware order" section of the plan this implements).
// http.MaxBytesReader is lazy — wrapping the body costs nothing until
// something actually reads it — so applying it here, above auth, makes "the
// body is bounded" an unconditional property of the whole stack rather than
// something that depends on which handler happens to run; it also means an
// unauthenticated caller sending an oversized body is rejected by
// requireToken without a single body byte ever being read, since
// requireToken only inspects a header.
//
// Deliberately OUTSIDE accessLog (wrapping it, not wrapped by it), unlike
// panicRecovery: http.MaxBytesReader signals the server that a request was
// cut short via an unexported requestTooLarger interface it type-asserts
// the ResponseWriter it was given against (net/http/server.go). statusRecorder
// (accessLog's wrapper) embeds http.ResponseWriter as an interface field, so
// that unexported method is never promoted and the assertion fails — and it
// cannot be implemented from this package, since an unexported method
// declared in another package can never be satisfied here. Verified live:
// with maxBytes nested inside accessLog, an oversized request gets no
// "Connection: close", the connection is reused, and net/http reads and
// discards up to 256 KiB of the rejected body after the handler returns.
// Wrapping accessLog instead — so MaxBytesReader receives the real
// *http.response — restores the intended close-and-stop-reading behavior,
// and the request is still recorded with a duration and status either way.
//
// The limit is chosen from Content-Type rather than being one flat value:
// a multipart upload can legitimately be large (MaxUploadBytes), while the
// JSON print-by-reference and presign intakes are small, structured
// payloads with no legitimate reason to approach that size (MaxJSONBytes).
// An unrecognized Content-Type gets the tighter JSON limit; for /print this
// is inert in practice (printHandler's own 415 rejection never reads
// r.Body for a Content-Type it doesn't recognize), but /files/presign
// decodes its JSON body regardless of Content-Type, so the tighter default
// still matters there.
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
