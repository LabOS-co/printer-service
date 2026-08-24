package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"printgateway/internal/apperr"
	"printgateway/internal/printgw"
)

// printHandler accepts a print request over HTTP in one of two ways:
//  1. multipart/form-data — the file is attached directly in the request.
//  2. application/json    — {"printer": "...", "file_url": "..."}: the
//     server downloads the file itself from the given URL first. Built for
//     a presigned S3/MinIO URL, but works with any plain HTTP(S) URL.
func (a *API) printHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		a.fail(w, &apperr.HTTPError{Status: http.StatusMethodNotAllowed, Public: "use POST"})
		return
	}

	contentType := r.Header.Get("Content-Type")

	switch {
	case strings.HasPrefix(contentType, "multipart/form-data"):
		a.handleMultipart(w, r)
	case strings.HasPrefix(contentType, "application/json"):
		a.handleURLReference(w, r)
	default:
		a.fail(w, &apperr.HTTPError{
			Status: http.StatusUnsupportedMediaType,
			Public: "Content-Type must be multipart/form-data (direct file) or application/json (file_url)",
		})
	}
}

// Option 1: the caller attaches the file itself.
// multipart fields: "printer" (text), "file" (the file part).
//
// The multipart/JSON parsing errors below are the caller's own mistake, not
// server-internal detail, so their full text is safe as the Public message
// — unlike printgw's errors, which come from the filesystem/subprocess/
// network and must stay out of the response body (see apperr.HTTPError).
func (a *API) handleMultipart(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(64 << 20); err != nil { // 64MB in-memory threshold, rest spills to disk
		a.fail(w, &apperr.HTTPError{Status: http.StatusBadRequest, Public: fmt.Sprintf("invalid multipart body: %v", err)})
		return
	}
	printer := r.FormValue("printer")
	if printer == "" {
		a.errorHandler.ThrowMissingParameterError(w, "printer")
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		a.fail(w, &apperr.HTTPError{Status: http.StatusBadRequest, Public: fmt.Sprintf("missing file part: %v", err)})
		return
	}
	defer file.Close()

	result, err := a.svc.PrintReader(r.Context(), printer, header.Filename, file)
	if err != nil {
		a.fail(w, err)
		return
	}
	a.writeSuccess(w, printer, result)
}

type urlPrintRequest struct {
	Printer string `json:"printer"`
	FileURL string `json:"file_url"` // e.g. a presigned S3/MinIO URL, or any HTTP(S) URL
}

// Option 2: the caller sends only a reference; the server fetches the file.
func (a *API) handleURLReference(w http.ResponseWriter, r *http.Request) {
	var req urlPrintRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		a.fail(w, &apperr.HTTPError{Status: http.StatusBadRequest, Public: fmt.Sprintf("invalid JSON body: %v", err)})
		return
	}
	if req.Printer == "" || req.FileURL == "" {
		a.fail(w, &apperr.HTTPError{Status: http.StatusBadRequest, Public: "both printer and file_url are required"})
		return
	}

	result, err := a.svc.PrintURL(r.Context(), req.Printer, req.FileURL)
	if err != nil {
		a.fail(w, err)
		return
	}
	a.writeSuccess(w, req.Printer, result)
}

func (a *API) writeSuccess(w http.ResponseWriter, printer string, result printgw.SubmitResult) {
	a.logger.LogInfo(fmt.Sprintf("print submitted: printer=%q output=%s", printer, result.Output), a.metaData)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "submitted",
		"output": result.Output,
	})
}
