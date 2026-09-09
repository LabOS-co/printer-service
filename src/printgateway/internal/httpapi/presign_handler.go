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
// credentials.
type Presigner interface {
	PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)
	PresignPut(ctx context.Context, key string, ttl time.Duration) (string, error)
}

type presignRequest struct {
	Key    string `json:"key"`
	Method string `json:"method"` // "GET" or "PUT"

	// TTLSeconds is optional (0/omitted uses cfg.PresignTTL); a value above
	// cfg.PresignTTL is clamped down to it rather than rejected.
	TTLSeconds int `json:"ttl_seconds"`
}

type presignResponse struct {
	URL       string    `json:"url"`
	Key       string    `json:"key"`
	ExpiresAt time.Time `json:"expires_at"`
}

// presignHandler returns a time-limited URL a caller can GET (to fetch a
// document) or PUT (to upload one) directly against the configured object
// store, without ever holding our S3 credentials.
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
		// Compare in seconds before converting to time.Duration: a large
		// caller-supplied value (e.g. milliseconds sent by mistake) would
		// overflow int64 on *time.Second first and silently yield a negative ttl.
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

	// Presigning can perform a live round trip to S3; bound it so a silent
	// endpoint can't park this goroutine indefinitely.
	ctx, cancel := context.WithTimeout(r.Context(), a.cfg.S3Timeout)
	defer cancel()

	// Captured before the presign call so ExpiresAt isn't overstated by
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

	// Log who requested this and for which key/method, but never the URL
	// itself — it is a bearer credential for the bucket.
	md, _ := a.requestMeta(r)
	a.logger.LogInfo(fmt.Sprintf("presigned %s issued: key=%q ttl=%s", method, req.Key, ttl), md)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(presignResponse{
		URL:       url,
		Key:       req.Key,
		ExpiresAt: issuedAt.Add(ttl),
	})
}
