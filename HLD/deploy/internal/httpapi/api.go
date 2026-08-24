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
//
// NOTE: logger/metaData/errorHandler are still constructed once and reused
// across all requests — the same pattern main.go used as package globals,
// just moved onto a struct. The per-request metadata fix (so a correlation
// id can be added without a data race) is a separate, later step.
type API struct {
	cfg          config.Config
	logger       logs.Logger
	metaData     *logs.LogMetaData
	errorHandler error_handler.ErrorHandler
	svc          *printgw.Service
}

func New(cfg config.Config, logger logs.Logger, svc *printgw.Service) *API {
	metaData := &logs.LogMetaData{Service: config.ServiceName}
	return &API{
		cfg:          cfg,
		logger:       logger,
		metaData:     metaData,
		errorHandler: error_handler.NewErrorHandler(logger, metaData),
		svc:          svc,
	}
}

// MetaData is exposed so main's startup/shutdown log lines can reuse it
// instead of constructing a second logs.LogMetaData.
func (a *API) MetaData() *logs.LogMetaData { return a.metaData }

// fail translates err into an HTTP response. If err is (or wraps) an
// *apperr.HTTPError, its Internal detail — which may contain filesystem
// paths, subprocess stderr, or a downstream response body — is logged here
// and never serialized; only its Public text reaches the client.
// error_handler.HandleError logs again internally, but by then Err.Error()
// is already just the public text, so the sensitive detail is logged
// exactly once, by us.
func (a *API) fail(w http.ResponseWriter, err error) {
	var httpErr *apperr.HTTPError
	if errors.As(err, &httpErr) && httpErr.Internal != nil {
		a.logger.LogError(httpErr.Internal.Error(), a.metaData)
	}
	a.errorHandler.HandleError(error_handler.APIError{
		StatusCode: apperr.StatusCodeOf(err),
		Err:        err,
	}, w)
}
