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
}

func New(cfg config.Config, logger logs.Logger, svc *printgw.Service) *API {
	return &API{
		cfg:    cfg,
		logger: logger,
		svc:    svc,
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

// fail translates err into an HTTP response. If err is (or wraps) an
// *apperr.HTTPError, its Internal detail — which may contain filesystem
// paths, subprocess stderr, or a downstream response body — is logged here
// and never serialized; only its Public text reaches the client.
// error_handler.HandleError logs again internally, but by then Err.Error()
// is already just the public text, so the sensitive detail is logged
// exactly once, by us.
func (a *API) fail(w http.ResponseWriter, r *http.Request, err error) {
	md, eh := a.requestMeta(r)
	var httpErr *apperr.HTTPError
	if errors.As(err, &httpErr) && httpErr.Internal != nil {
		a.logger.LogError(httpErr.Internal.Error(), md)
	}
	eh.HandleError(error_handler.APIError{
		StatusCode: apperr.StatusCodeOf(err),
		Err:        err,
	}, w)
}
