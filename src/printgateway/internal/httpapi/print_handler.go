package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"

	"printgateway/internal/apperr"
	"printgateway/internal/config"
	"printgateway/internal/printgw"
)

// defaultCopies is used when a caller does not supply one.
const defaultCopies = 1

// maxCopies bounds copies per request, since they consume a physical shared
// resource (paper/toner). Also enforced independently by printgw.Service
// for callers that bypass these HTTP handlers.
const maxCopies = 100

// printHandler accepts a print request as either multipart/form-data (file
// attached directly) or application/json ({"printer","file_url"} or
// {"printer","s3_key"}, fetched server-side).
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
// request. Also used by the maxBytes middleware, which must agree with this
// dispatch on which requests get the larger upload limit.
func isMultipart(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	return strings.EqualFold(mediaType, "multipart/form-data")
}

// handleMultipart handles option 1: the caller attaches the file itself.
// Multipart fields: "printer" (text), "file" (the file part).
func (a *API) handleMultipart(w http.ResponseWriter, r *http.Request) {
	// In-memory threshold only; the hard body-size cap (MaxUploadBytes) is
	// already enforced by the maxBytes middleware.
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

	copiesRaw, copiesPresent := multipartFormValue(r, "copies")
	copies, cerr := parseCopiesFormValue(copiesRaw, copiesPresent)
	if cerr != nil {
		a.fail(w, r, cerr)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		a.fail(w, r, &apperr.HTTPError{Status: http.StatusBadRequest, Public: fmt.Sprintf("missing file part: %v", err)})
		return
	}
	defer file.Close()

	result, err := a.svc.PrintReader(r.Context(), printer, header.Filename, file, copies)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.writeSuccess(w, r, printer, result)
}

// multipartFormValue returns the first value for key from the parsed
// multipart body only (never r.URL.Query(), which r.FormValue would merge
// in — letting a query string silently override the "copies" form field),
// plus whether key was present, so callers can distinguish "absent" from
// "present but empty".
func multipartFormValue(r *http.Request, key string) (value string, present bool) {
	if r.MultipartForm == nil {
		return "", false
	}
	vals, ok := r.MultipartForm.Value[key]
	if !ok || len(vals) == 0 {
		return "", false
	}
	return vals[0], true
}

// parseCopiesFormValue parses the optional "copies" multipart form value,
// defaulting to defaultCopies when absent and validating range when present.
func parseCopiesFormValue(raw string, present bool) (int, *apperr.HTTPError) {
	if !present {
		return defaultCopies, nil
	}
	copies, err := strconv.Atoi(raw)
	if err != nil {
		return 0, &apperr.HTTPError{Status: http.StatusBadRequest, Public: "copies must be a positive integer"}
	}
	if cerr := validateCopiesRange(copies); cerr != nil {
		return 0, cerr
	}
	return copies, nil
}

// validateCopiesRange enforces >=1/<=maxCopies, shared by both intake paths.
func validateCopiesRange(copies int) *apperr.HTTPError {
	if copies < 1 || copies > maxCopies {
		return &apperr.HTTPError{
			Status: http.StatusBadRequest,
			Public: fmt.Sprintf("copies must be between 1 and %d", maxCopies),
		}
	}
	return nil
}

type urlPrintRequest struct {
	Printer string `json:"printer"`
	FileURL string `json:"file_url"` // e.g. a presigned S3/MinIO URL, or any HTTP(S) URL
	S3Key   string `json:"s3_key"`   // a key in the configured object store bucket

	// Copies is a pointer so an omitted field (defaults to defaultCopies) is
	// distinguishable from an explicit 0/negative (rejected below). JSON
	// null is deliberately treated as omitted too, not rejected — encoding/json
	// can't tell the two apart on a *int anyway (see
	// TestPrintHandlerJSONCopiesNullIsAbsentDefault).
	Copies *int `json:"copies"`
}

// handleURLReference handles option 2: the caller sends only a reference —
// a URL the server fetches (file_url, SSRF-guarded) or a key in the
// configured object store (s3_key). Exactly one of the two must be set.
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
	copies := defaultCopies
	if req.Copies != nil {
		if cerr := validateCopiesRange(*req.Copies); cerr != nil {
			a.fail(w, r, cerr)
			return
		}
		copies = *req.Copies
	}

	var (
		result printgw.SubmitResult
		err    error
	)
	if req.S3Key != "" {
		result, err = a.svc.PrintS3Key(r.Context(), req.Printer, req.S3Key, copies)
	} else {
		result, err = a.svc.PrintURL(r.Context(), req.Printer, req.FileURL, copies)
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.writeSuccess(w, r, req.Printer, result)
}

// validObjectKey rejects a key that could escape the configured bucket via
// path traversal (e.g. "../other-bucket/x"). Rejects outright rather than
// normalizing via path.Clean, so what the caller sent and what reaches the
// store never diverge.
func validObjectKey(key string) bool {
	return path.Clean("/"+key) == "/"+key
}

// decodeStrictJSON decodes exactly one JSON value from r.Body into v,
// rejecting an unknown field (catches typo'd field names) and any trailing
// content after that value (catches concatenated bodies).
func decodeStrictJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	// A second Decode call is used rather than Decoder.More, which only
	// answers "more in this array/object", not "more at the top level".
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

// bodyErr classifies an error from reading or decoding a request body. what
// describes what was being parsed (e.g. "invalid JSON body").
func bodyErr(err error, what string) *apperr.HTTPError {
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		// Distinguish "body too large" (413, names the limit) from an
		// ordinary malformed body (400) — both would otherwise look the same.
		return &apperr.HTTPError{
			Status: http.StatusRequestEntityTooLarge,
			Public: fmt.Sprintf("request body exceeds the %d byte limit", maxBytesErr.Limit),
		}
	}
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		// Rebuild the message from Field/Value/Type: the error's own
		// Error() string names the internal Go struct, not just the JSON tag.
		return &apperr.HTTPError{
			Status: http.StatusBadRequest,
			Public: fmt.Sprintf("%s: field %q must be a %s, got %s", what, typeErr.Field, typeErr.Type, typeErr.Value),
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
