package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strings"

	"printgateway/internal/apperr"
	"printgateway/internal/config"
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
	case isMultipart(contentType):
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

// isMultipart reports whether contentType names a multipart/form-data
// request, matching on the parsed media type rather than a literal prefix.
// mime.ParseMediaType already lowercases the returned type, so the
// strings.EqualFold below is defensive rather than load-bearing for
// case-insensitivity — the real reason a literal-prefix check would be
// wrong is RFC 9110 case-insensitivity of the token itself, which
// ParseMediaType already normalizes. Shared with the maxBytes middleware,
// which must agree with this dispatch on which requests get the larger
// upload limit rather than the tighter JSON one — two independent checks
// that happened to agree today would silently diverge the moment either
// one were reimplemented differently.
func isMultipart(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	return strings.EqualFold(mediaType, "multipart/form-data")
}

// Option 1: the caller attaches the file itself.
// multipart fields: "printer" (text), "file" (the file part).
//
// The multipart/JSON parsing errors below are the caller's own mistake, not
// server-internal detail, so their full text is safe as the Public message
// — unlike printgw's errors, which come from the filesystem/subprocess/
// network and must stay out of the response body (see apperr.HTTPError).
func (a *API) handleMultipart(w http.ResponseWriter, r *http.Request) {
	// In-memory threshold only; the hard cap on the whole body is
	// a.cfg.MaxUploadBytes, already enforced by the maxBytes middleware
	// before this handler ever runs (see config.DefaultMultipartMemoryBytes
	// for why these are deliberately different values).
	if err := r.ParseMultipartForm(config.DefaultMultipartMemoryBytes); err != nil {
		a.fail(w, r, bodyErr(err, "invalid multipart body"))
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
	if err := decodeStrictJSON(r, &req); err != nil {
		a.fail(w, r, bodyErr(err, "invalid JSON body"))
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

// decodeStrictJSON decodes exactly one JSON value from r.Body into v,
// rejecting an unknown field and any trailing content after that value.
// Both intakes that take a JSON body (handleURLReference, presignHandler)
// use this rather than a bare json.Decode: a caller who mistypes "file_url"
// as "fiel_url" would otherwise have the typo silently dropped and the
// request fail downstream on the confusing-sounding "exactly one of
// file_url or s3_key is required" instead of the actual mistake, and a
// caller who accidentally concatenates two JSON bodies would have the
// second one silently discarded instead of rejected.
//
// Two properties this does NOT enforce, verified empirically rather than
// assumed: field-name matching stays case-insensitive ({"Printer":...}
// still matches Printer string `json:"printer"`) since DisallowUnknownFields
// inherits encoding/json's own case-insensitive matching rather than adding
// stricter rules of its own, and a duplicate key ({"printer":"a","printer":
// "b"}) is not rejected — encoding/json applies the last occurrence and
// this function does nothing to change that. Only a genuinely unrecognized
// field name and trailing content are rejected.
//
// The trailing-content check decodes a second value into a throwaway
// json.RawMessage rather than calling Decoder.More (which only answers
// "is there a next array/object element", not "is there more input at the
// top level"): a second Decode call returns io.EOF when nothing but
// whitespace remains, returns nil when it successfully parsed another JSON
// value (reject), and returns any other error when what follows is present
// but not valid JSON on its own (also reject, via that same error).
func decodeStrictJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	var extra json.RawMessage
	switch err := dec.Decode(&extra); {
	case errors.Is(err, io.EOF):
		return nil
	case err == nil:
		return errors.New("body must contain exactly one JSON value")
	default:
		return err
	}
}

// bodyErr classifies an error from reading or decoding a request body.
// what describes what was being parsed (e.g. "invalid JSON body"), matching
// the message shape every call site already used before this existed.
//
// A *http.MaxBytesError — produced when the maxBytes middleware's
// http.MaxBytesReader cuts a read short — means the client's own body
// exceeded the configured limit, not that it was malformed; that is a 413
// naming the limit, not the generic 400 every other parsing mistake here
// gets. Without this, a caller hitting the size limit saw the same 400 as a
// caller who sent garbage, with no way to tell the two apart from the
// response alone.
func bodyErr(err error, what string) *apperr.HTTPError {
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		return &apperr.HTTPError{
			Status: http.StatusRequestEntityTooLarge,
			Public: fmt.Sprintf("request body exceeds the %d byte limit", maxBytesErr.Limit),
		}
	}
	return &apperr.HTTPError{Status: http.StatusBadRequest, Public: fmt.Sprintf("%s: %v", what, err)}
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
