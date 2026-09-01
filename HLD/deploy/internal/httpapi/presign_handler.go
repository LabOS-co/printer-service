package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"printgateway/internal/apperr"
)

// Presigner returns time-limited URLs a third party can use directly
// against the configured object store, without ever holding our S3
// credentials. Declared here rather than in printgw.ObjectStore: this
// handler is its only consumer, printgw.Service never presigns, and each
// package depending on only the slice of objstore.MinIO's method set it
// actually calls is the point of splitting them (see printgw.ObjectStore's
// doc comment).
type Presigner interface {
	PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)
	PresignPut(ctx context.Context, key string, ttl time.Duration) (string, error)
}

type presignRequest struct {
	Key    string `json:"key"`
	Method string `json:"method"` // "GET" or "PUT"

	// TTLSeconds is optional; a zero or omitted value uses cfg.PresignTTL.
	// A value greater than cfg.PresignTTL is clamped down to it, never
	// rejected outright — a client asking for "as long as possible" is not
	// a caller error. See config.DefaultPresignTTL.
	TTLSeconds int `json:"ttl_seconds"`
}

type presignResponse struct {
	URL       string    `json:"url"`
	Key       string    `json:"key"`
	ExpiresAt time.Time `json:"expires_at"`
}

// presignHandler returns a time-limited URL a caller can GET (to fetch a
// document this service will also print by s3_key) or PUT (to upload one it
// will then print by key) directly against the configured object store,
// without ever holding our S3 credentials.
//
// key is used exactly as given — no server-assigned prefixing or
// caller-basename sanitizing (unlike printgw's own spool filenames). The
// object store's bucket is fixed and server-credentialed, and validObjectKey
// rejects any key containing a "../" (or similar) traversal segment, so a
// caller naming an arbitrary key can at most read/write within that one
// bucket, not escape it — verified live against a real MinIO instance,
// which independently rejects an unclean key server-side, but that backend
// behavior is not what this guarantee should rest on.
func (a *API) presignHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		a.fail(w, r, &apperr.HTTPError{Status: http.StatusMethodNotAllowed, Public: "use POST"})
		return
	}
	if a.objectStore == nil {
		a.fail(w, r, &apperr.HTTPError{Status: http.StatusServiceUnavailable, Public: "object storage is not configured"})
		return
	}

	var req presignRequest
	if err := decodeStrictJSON(r, &req); err != nil {
		a.fail(w, r, bodyErr(err, "invalid JSON body"))
		return
	}
	if req.Key == "" {
		a.fail(w, r, &apperr.HTTPError{Status: http.StatusBadRequest, Public: "key is required"})
		return
	}
	if !validObjectKey(req.Key) {
		a.fail(w, r, &apperr.HTTPError{Status: http.StatusBadRequest, Public: "key must not contain path traversal segments"})
		return
	}

	ttl := a.cfg.PresignTTL
	if req.TTLSeconds > 0 {
		// Compared in seconds, before ever converting the caller-supplied
		// value to a time.Duration: req.TTLSeconds * time.Second overflows
		// int64 well before any plausible legitimate value (e.g.
		// ttl_seconds=10000000000, a plausible "milliseconds by mistake"
		// input), which would silently produce a negative ttl instead of
		// the documented clamp. capSeconds is always small (bounded by
		// cfg.PresignTTL), so this comparison can never overflow, and the
		// only value ever multiplied afterward is one already known to be
		// smaller than that safe bound.
		capSeconds := int64(ttl / time.Second)
		if int64(req.TTLSeconds) < capSeconds {
			ttl = time.Duration(req.TTLSeconds) * time.Second
		}
	}

	method := strings.ToUpper(req.Method)
	if method == "" {
		method = "GET"
	}
	if method != "GET" && method != "PUT" {
		a.fail(w, r, &apperr.HTTPError{Status: http.StatusBadRequest, Public: "method must be GET or PUT"})
		return
	}

	// Bounded the same way Service.getObject/fetch/submit are: with
	// S3Region empty, presigning performs a live bucket-location round trip
	// (see main.go's region warning), and r.Context() alone carries no
	// deadline — an S3 endpoint that accepts the connection and then goes
	// silent would otherwise park this goroutine indefinitely.
	ctx, cancel := context.WithTimeout(r.Context(), a.cfg.S3Timeout)
	defer cancel()

	// Captured before the presign call, not after: with the same live
	// round trip in play, computing this from time.Now() after the call
	// returns would overstate the URL's actual remaining validity by
	// however long that call took.
	issuedAt := time.Now()

	var (
		url string
		err error
	)
	if method == "PUT" {
		url, err = a.objectStore.PresignPut(ctx, req.Key, ttl)
	} else {
		url, err = a.objectStore.PresignGet(ctx, req.Key, ttl)
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}

	// A presigned URL grants bucket read (GET) or write (PUT) access to
	// whoever holds it — the one operation in this service that hands out
	// object-store credentials by proxy — so who requested it, for which
	// key and method, is worth a line on its own rather than only the
	// generic access log the accessLog middleware already adds. Never the
	// URL itself: it IS the credential.
	md, _ := a.requestMeta(r)
	a.logger.LogInfo(fmt.Sprintf("presigned %s issued: key=%q ttl=%s", method, req.Key, ttl), md)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(presignResponse{
		URL:       url,
		Key:       req.Key,
		ExpiresAt: issuedAt.Add(ttl),
	})
}
