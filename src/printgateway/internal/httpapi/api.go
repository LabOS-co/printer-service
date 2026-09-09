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

// API holds this service's shared, request-independent dependencies.
type API struct {
	cfg    config.Config
	logger logs.Logger
	svc    *printgw.Service

	// objectStore is nil when S3 is not configured; presigning doesn't go
	// through svc, so it's held here rather than only inside it.
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

// requestMeta builds a per-request LogMetaData/ErrorHandler pair, correlated
// by request id. Built fresh on every call rather than cached on API: error_handler
// binds metadata at construction, so sharing one instance would race across
// concurrent requests.
func (a *API) requestMeta(r *http.Request) (*logs.LogMetaData, error_handler.ErrorHandler) {
	md := &logs.LogMetaData{Service: config.ServiceName, JobId: requestIDFrom(r.Context())}
	return md, error_handler.NewErrorHandler(a.logger, md)
}

// errFailCalledImproperly is the fallback response used when fail is called
// incorrectly (nil error, or a typed-nil *apperr.HTTPError).
var errFailCalledImproperly = &apperr.HTTPError{Status: http.StatusInternalServerError, Public: "internal server error"}

// fail translates err into an HTTP response, logging any *apperr.HTTPError's
// Internal detail here and never serializing it to the client.
//
// err must be normalized to errFailCalledImproperly (or have Internal
// stripped) before reaching eh.HandleError below: error_handler puts
// Err.Error() directly into the response body, so an unclassified error
// would otherwise leak filesystem paths or subprocess output to the caller.
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
