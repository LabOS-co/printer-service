// Package httpapi is the HTTP surface of the Print Gateway: request
// parsing, auth, and translating printgw results into responses.
package httpapi

import (
	"errors"
	"net/http"

	"github.com/LabOS-co/go-packages/error_handler"
	"github.com/LabOS-co/go-packages/logs"

	"printgateway/internal/apperr"
	"printgateway/internal/config"
	"printgateway/internal/printgw"
)

// API holds this service's shared, request-independent dependencies. The
// logger itself is safe to share (it was never the problem); a
// *logs.LogMetaData and its paired error_handler.ErrorHandler are not, since
// error_handler.NewErrorHandler binds metadata at construction and mutating
// it per request would be a data race across concurrent requests (P0-5).
// Those are built fresh per request instead — see requestMeta.
type API struct {
	cfg    config.Config
	logger logs.Logger
	svc    *printgw.Service

	// objectStore is nil when S3 is not configured (config.S3Endpoint ==
	// ""); presignHandler is the only place that checks for that, the same
	// way printgw.Service.PrintS3Key checks its own copy of the same
	// underlying value (passed to New separately, typed as each package's
	// own narrow interface — see printgw.ObjectStore's doc comment for why
	// this isn't one shared interface). Held here too, rather than only
	// inside svc, because presigning is not a print operation and has no
	// reason to go through Service at all.
	objectStore Presigner
}

func New(cfg config.Config, logger logs.Logger, svc *printgw.Service, objectStore Presigner) *API {
	return &API{
		cfg:         cfg,
		logger:      logger,
		svc:         svc,
		objectStore: objectStore,
	}
}

// requestMeta builds a *logs.LogMetaData/error_handler.ErrorHandler pair
// scoped to one request, correlated by the id requestContext attached to
// the request context. A pointer reachable only from the request that
// created it, on the single goroutine net/http runs it on, is ordinary
// single-threaded mutation rather than shared state. The plan's
// 50-concurrent -race test is what will keep that true as the handler chain
// grows; it is not written yet, so for now the invariant rests on nothing
// hoisting this pair onto the API struct.
//
// JobId now does reach the log text: main.go builds its logger via
// logs.GetLoggerWithSettings (Workstream D), not the old GetConsoleLogger()
// whose every method silently discarded its metadata argument. One caveat
// that -race test will need to account for when it's written: the
// logstashLogger this now runs through increments an unsynchronized
// package-global sequence number per log call (logs@v1.5.2/
// logstash_logger.go) — a real race across concurrent requests, not fixable
// from this side. Tracked as a known upstream issue, not this file's bug.
func (a *API) requestMeta(r *http.Request) (*logs.LogMetaData, error_handler.ErrorHandler) {
	md := &logs.LogMetaData{Service: config.ServiceName, JobId: requestIDFrom(r.Context())}
	return md, error_handler.NewErrorHandler(a.logger, md)
}

// errFailCalledImproperly stands in for err when fail is called in a way no
// current call site does (nil, or a typed-nil *apperr.HTTPError — see
// fail's own doc comment). Package-level and immutable: every field is
// read-only after construction, so sharing one instance across concurrent
// requests is safe the same way a constant would be.
var errFailCalledImproperly = &apperr.HTTPError{Status: http.StatusInternalServerError, Public: "internal server error"}

// fail translates err into an HTTP response. If err is (or wraps) an
// *apperr.HTTPError, its Internal detail — which may contain filesystem
// paths, subprocess stderr, or a downstream response body — is logged here
// and never serialized; only its Public text reaches the client.
// error_handler.HandleError logs again internally, but by then Err.Error()
// is already just the public text, so the sensitive detail is logged
// exactly once, by us.
//
// Three cases besides the ordinary one are guarded, in increasing order of
// how likely they are to ever actually happen:
//
//   - err == nil would otherwise reach error_handler.HandleError's own
//     Err.Error() call on a nil interface.
//   - A typed-nil *apperr.HTTPError (a non-nil error interface whose
//     concrete value is a nil pointer — see apperr.StatusCodeOf's doc
//     comment for how that's constructed) would dereference that nil
//     pointer here at httpErr.Internal, or later at (*HTTPError).Error()'s
//     own e.Public.
//   - An err that is not (or does not wrap) an *apperr.HTTPError at all is
//     the one that matters most: error_handler@v1.2.4's handleAPIError puts
//     Err.Error() directly into the response body's errorDetails.details
//     (verified by reading it), so any such error reaches the client
//     byte-for-byte. Every current dependency (cups.LPSubmitter,
//     fetch.SafeFetcher, objstore.MinIO, and now printgw.Service.submit's
//     own re-wrap) classifies its own failures before returning, so this is
//     not reachable today — but "no current caller does this" describing a
//     path that would leak a temp file path or subprocess stderr to an
//     authenticated caller is exactly the kind of invariant this function
//     should enforce structurally, not merely benefit from by convention.
//
// fail is the last function standing between a handler and a response — the
// cost of defending all three (one switch, one package-level fallback
// error) is cheap next to what a caller-visible leak, or a panic inside
// error handling itself, would look like.
func (a *API) fail(w http.ResponseWriter, r *http.Request, err error) {
	md, eh := a.requestMeta(r)

	var httpErr *apperr.HTTPError
	switch matched := errors.As(err, &httpErr); {
	case err == nil:
		a.logger.LogError("fail called with a nil error (caller bug)", md)
		err = errFailCalledImproperly
	case matched && httpErr == nil:
		a.logger.LogError("fail called with a typed-nil *apperr.HTTPError (caller bug)", md)
		err = errFailCalledImproperly
	case !matched:
		a.logger.LogError(err.Error(), md)
		err = errFailCalledImproperly
	case httpErr.Internal != nil:
		a.logger.LogError(httpErr.Internal.Error(), md)
	}

	eh.HandleError(error_handler.APIError{
		StatusCode: apperr.StatusCodeOf(err),
		Err:        err,
	}, w)
}
