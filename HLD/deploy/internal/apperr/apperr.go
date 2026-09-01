// Package apperr associates an error with the HTTP status it should
// produce, and keeps the message a caller may see separate from the detail
// that should only reach the log.
package apperr

import (
	"errors"
	"net/http"
)

// HTTPError pairs an error with an HTTP status. Public is safe to return
// to any caller; Internal (optional) is the full diagnostic — a filesystem
// path, a subprocess's stderr, a downstream response body — that must
// never be serialized back to a client.
//
// error_handler.HandleError copies Error() verbatim into the client-facing
// JSON body, which is exactly why Error() returns Public and not Internal.
type HTTPError struct {
	Status   int
	Public   string
	Internal error
}

func (e *HTTPError) Error() string { return e.Public }
func (e *HTTPError) Unwrap() error { return e.Internal }

// StatusCodeOf returns the HTTP status associated with err via errors.As,
// defaulting to 500 if err is not (or does not wrap) an *HTTPError.
//
// The httpErr != nil check matters: errors.As reports a match based on the
// error's dynamic TYPE, not its value, so a non-nil error interface whose
// concrete value is a nil *HTTPError pointer (a typed-nil, constructible by
// e.g. `var e *HTTPError; return e` from a function returning error) still
// satisfies it. Without the check, httpErr.Status below would dereference
// that nil pointer instead of falling through to the documented 500
// default — no production call site constructs one today, but this is a
// public function of a small package other code depends on for exactly
// this classification, so the documented default is worth actually
// guaranteeing rather than assuming callers stay well-behaved forever.
func StatusCodeOf(err error) int {
	var httpErr *HTTPError
	if errors.As(err, &httpErr) && httpErr != nil {
		return httpErr.Status
	}
	return http.StatusInternalServerError
}
