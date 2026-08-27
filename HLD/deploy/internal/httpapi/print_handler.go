package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"strings"

	"printgateway/internal/apperr"
	"printgateway/internal/printgw"
)

// printHandler accepts a print request over HTTP in one of two ways:
//  1. multipart/form-data — the file is attached directly in the request.
//  2. application/json    — {"printer": "...", "file_url": "..."} or
//     {"printer": "...", "s3_key": "..."}: the server fetches the file
//     itself, either from any HTTP(S) URL (file_url, SSRF-guarded) or from
//     the configured object store bucket (s3_key, no SSRF surface — the
//     bucket is fixed and server-credentialed, not caller-controlled).
func (a *API) printHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		a.fail(w, r, &apperr.HTTPError{Status: http.StatusMethodNotAllowed, Public: "use POST"})
		return
	}

	contentType := r.Header.Get("Content-Type")

	switch {
	case strings.HasPrefix(contentType, "multipart/form-data"):
		a.handleMultipart(w, r)
	case strings.HasPrefix(contentType, "application/json"):
		a.handleURLReference(w, r)
	default:
		a.fail(w, r, &apperr.HTTPError{
			Status: http.StatusUnsupportedMediaType,
			Public: "Content-Type must be multipart/form-data (direct file) or application/json (file_url or s3_key)",
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
		a.fail(w, r, &apperr.HTTPError{Status: http.StatusBadRequest, Public: fmt.Sprintf("invalid multipart body: %v", err)})
		return
	}
	printer := r.FormValue("printer")
	if printer == "" {
		_, eh := a.requestMeta(r)
		eh.ThrowMissingParameterError(w, "printer")
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		a.fail(w, r, &apperr.HTTPError{Status: http.StatusBadRequest, Public: fmt.Sprintf("missing file part: %v", err)})
		return
	}
	defer file.Close()

	result, err := a.svc.PrintReader(r.Context(), printer, header.Filename, file)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.writeSuccess(w, r, printer, result)
}

type urlPrintRequest struct {
	Printer string `json:"printer"`
	FileURL string `json:"file_url"` // e.g. a presigned S3/MinIO URL, or any HTTP(S) URL
	S3Key   string `json:"s3_key"`   // a key in the configured object store bucket
}

// Option 2: the caller sends only a reference — either a URL the server
// fetches itself (file_url) or a key in the configured object store
// (s3_key). Exactly one of the two must be set: file_url goes through
// PrintURL's SSRF-guarded fetch, s3_key goes through PrintS3Key's
// fixed-bucket download, and mixing them would leave one silently ignored.
func (a *API) handleURLReference(w http.ResponseWriter, r *http.Request) {
	var req urlPrintRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		a.fail(w, r, &apperr.HTTPError{Status: http.StatusBadRequest, Public: fmt.Sprintf("invalid JSON body: %v", err)})
		return
	}
	if req.Printer == "" {
		a.fail(w, r, &apperr.HTTPError{Status: http.StatusBadRequest, Public: "printer is required"})
		return
	}
	if (req.FileURL == "") == (req.S3Key == "") {
		a.fail(w, r, &apperr.HTTPError{Status: http.StatusBadRequest, Public: "exactly one of file_url or s3_key is required"})
		return
	}
	if req.S3Key != "" && !validObjectKey(req.S3Key) {
		a.fail(w, r, &apperr.HTTPError{Status: http.StatusBadRequest, Public: "s3_key must not contain path traversal segments"})
		return
	}

	var (
		result printgw.SubmitResult
		err    error
	)
	if req.S3Key != "" {
		result, err = a.svc.PrintS3Key(r.Context(), req.Printer, req.S3Key)
	} else {
		result, err = a.svc.PrintURL(r.Context(), req.Printer, req.FileURL)
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.writeSuccess(w, r, req.Printer, result)
}

// validObjectKey rejects a key that could address something outside the
// one configured bucket via path traversal (e.g. "../other-bucket/x").
// Shared by handleURLReference (s3_key) and presignHandler (key): both use
// a caller-supplied key verbatim against the same fixed, server-credentialed
// bucket.
//
// Verified live against a real MinIO instance that it independently rejects
// this server-side (XMinioInvalidResourceName) — Go's net/http client sends
// ".." in a URL path unnormalized, over the wire, exactly as given — but
// that is one backend's behavior this service should not depend on for a
// security property its own README claims ("a caller can at most read/write
// within that one bucket, not escape it"). path.Clean resolves ".."/"."
// segments the same way a filesystem would; a key that isn't already in
// that clean form is rejected outright rather than silently normalized,
// so there is no discrepancy between what a caller thinks they asked for
// and what request actually reaches the store.
func validObjectKey(key string) bool {
	return path.Clean("/"+key) == "/"+key
}

func (a *API) writeSuccess(w http.ResponseWriter, r *http.Request, printer string, result printgw.SubmitResult) {
	md, _ := a.requestMeta(r)
	a.logger.LogInfo(fmt.Sprintf("print submitted: printer=%q output=%s", printer, result.Output), md)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "submitted",
		"output": result.Output,
	})
}
