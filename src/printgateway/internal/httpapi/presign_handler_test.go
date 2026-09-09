package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func doPresign(t *testing.T, srv *httptest.Server, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/files/presign", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set(authTokenHeader, token)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestPresignHandlerGetHappyPath(t *testing.T) {
	t.Parallel()

	presigner := &fakePresigner{getURL: "https://minio.example/get-url", putURL: "https://minio.example/put-url"}
	a, logger := newTestAPI(testAPIOpts{presigner: presigner})
	srv := httptest.NewServer(NewServer(a).Handler)
	defer srv.Close()

	resp := doPresign(t, srv, "test-token", `{"key":"docs/a.pdf"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got struct {
		URL       string    `json:"url"`
		Key       string    `json:"key"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.URL != presigner.getURL {
		t.Errorf("url = %q, want the GET url %q", got.URL, presigner.getURL)
	}
	if got.Key != "docs/a.pdf" {
		t.Errorf("key = %q", got.Key)
	}
	if got.ExpiresAt.Before(time.Now()) {
		t.Errorf("expires_at %v is already in the past", got.ExpiresAt)
	}

	calls := presigner.snapshotCalls()
	if len(calls) != 1 {
		t.Fatalf("presigner called %d times, want 1", len(calls))
	}
	if calls[0].method != "GET" {
		t.Errorf("dispatched %s, want GET", calls[0].method)
	}
	if calls[0].key != "docs/a.pdf" {
		t.Errorf("store received key %q, want %q", calls[0].key, "docs/a.pdf")
	}

	infos := logger.snapshotInfos()
	found := false
	for _, msg := range infos {
		if strings.Contains(msg, "presigned GET issued") {
			found = true
		}
		if strings.Contains(msg, got.URL) {
			t.Fatalf("the presigned URL itself must never be logged (it is the credential): %q", msg)
		}
	}
	if !found {
		t.Errorf("no log line recorded the presign issuance; infos=%v", infos)
	}
}

func TestPresignHandlerPutHappyPath(t *testing.T) {
	t.Parallel()

	presigner := &fakePresigner{getURL: "https://minio.example/get-url", putURL: "https://minio.example/put-url"}
	a, _ := newTestAPI(testAPIOpts{presigner: presigner})
	srv := httptest.NewServer(NewServer(a).Handler)
	defer srv.Close()

	resp := doPresign(t, srv, "test-token", `{"key":"docs/a.pdf","method":"PUT"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.URL != presigner.putURL {
		t.Errorf("url = %q, want the PUT url %q (a GET/PUT dispatch bug would return the GET url instead)", got.URL, presigner.putURL)
	}

	calls := presigner.snapshotCalls()
	if len(calls) != 1 || calls[0].method != "PUT" {
		t.Fatalf("presigner calls = %+v, want exactly one PUT call", calls)
	}
}

// TestPresignHandlerLowercaseMethodIsNormalized asserts "put" dispatches to
// PresignPut rather than being rejected as an invalid method.
func TestPresignHandlerLowercaseMethodIsNormalized(t *testing.T) {
	t.Parallel()

	presigner := &fakePresigner{getURL: "https://minio.example/get-url", putURL: "https://minio.example/put-url"}
	a, _ := newTestAPI(testAPIOpts{presigner: presigner})
	srv := httptest.NewServer(NewServer(a).Handler)
	defer srv.Close()

	resp := doPresign(t, srv, "test-token", `{"key":"docs/a.pdf","method":"put"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if calls := presigner.snapshotCalls(); len(calls) != 1 || calls[0].method != "PUT" {
		t.Fatalf("presigner calls = %+v, want exactly one PUT call", calls)
	}
}

func TestPresignHandlerDefaultsToGETOnEmptyMethod(t *testing.T) {
	t.Parallel()

	a, _ := newTestAPI(testAPIOpts{presigner: &fakePresigner{getURL: "https://minio.example/x", putURL: "https://minio.example/x"}})
	srv := httptest.NewServer(NewServer(a).Handler)
	defer srv.Close()

	resp := doPresign(t, srv, "test-token", `{"key":"docs/a.pdf","method":""}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (empty method defaults to GET)", resp.StatusCode)
	}
}

func TestPresignHandlerInvalidMethodIs400(t *testing.T) {
	t.Parallel()

	a, _ := newTestAPI(testAPIOpts{presigner: &fakePresigner{getURL: "https://minio.example/x", putURL: "https://minio.example/x"}})
	srv := httptest.NewServer(NewServer(a).Handler)
	defer srv.Close()

	resp := doPresign(t, srv, "test-token", `{"key":"docs/a.pdf","method":"DELETE"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestPresignHandlerMissingKeyIs400(t *testing.T) {
	t.Parallel()

	a, _ := newTestAPI(testAPIOpts{presigner: &fakePresigner{getURL: "https://minio.example/x", putURL: "https://minio.example/x"}})
	srv := httptest.NewServer(NewServer(a).Handler)
	defer srv.Close()

	resp := doPresign(t, srv, "test-token", `{}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestPresignHandlerPathTraversalKeyIs400(t *testing.T) {
	t.Parallel()

	a, _ := newTestAPI(testAPIOpts{presigner: &fakePresigner{getURL: "https://minio.example/x", putURL: "https://minio.example/x"}})
	srv := httptest.NewServer(NewServer(a).Handler)
	defer srv.Close()

	resp := doPresign(t, srv, "test-token", `{"key":"../other-bucket/x"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestPresignHandlerNotConfiguredIs503(t *testing.T) {
	t.Parallel()

	a, _ := newTestAPI(testAPIOpts{}) // no presigner set: objectStore is nil
	srv := httptest.NewServer(NewServer(a).Handler)
	defer srv.Close()

	resp := doPresign(t, srv, "test-token", `{"key":"docs/a.pdf"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}

// TestPresignHandlerStoreErrorIsGenericized asserts an unclassified
// Presigner error never reaches the client's response body verbatim.
func TestPresignHandlerStoreErrorIsGenericized(t *testing.T) {
	t.Parallel()

	a, _ := newTestAPI(testAPIOpts{presigner: &fakePresigner{err: fmt.Errorf("bucket unreachable")}})
	srv := httptest.NewServer(NewServer(a).Handler)
	defer srv.Close()

	resp := doPresign(t, srv, "test-token", `{"key":"docs/a.pdf"}`)
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (unclassified Presigner error re-wrapped generically)", resp.StatusCode)
	}
	if bytes.Contains(body, []byte("bucket unreachable")) {
		t.Errorf("response body leaked the store's raw error text: %s", body)
	}
}

func TestPresignHandlerGetIs405(t *testing.T) {
	t.Parallel()

	a, _ := newTestAPI(testAPIOpts{presigner: &fakePresigner{getURL: "https://minio.example/x", putURL: "https://minio.example/x"}})
	srv := httptest.NewServer(NewServer(a).Handler)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/files/presign", nil)
	req.Header.Set(authTokenHeader, "test-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}
}

// TestPresignHandlerCustomTTLUnderTheCapIsHonored asserts a ttl_seconds
// smaller than cfg.PresignTTL is used as given, not widened to the cap.
func TestPresignHandlerCustomTTLUnderTheCapIsHonored(t *testing.T) {
	t.Parallel()

	presigner := &fakePresigner{getURL: "https://minio.example/x", putURL: "https://minio.example/x"}
	a, _ := newTestAPI(testAPIOpts{presigner: presigner})
	srv := httptest.NewServer(NewServer(a).Handler)
	defer srv.Close()

	before := time.Now()
	resp := doPresign(t, srv, "test-token", `{"key":"docs/a.pdf","ttl_seconds":30}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got struct {
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.ExpiresAt.After(before.Add(60 * time.Second)) {
		t.Errorf("expires_at %v was not honored as the requested 30s ttl", got.ExpiresAt)
	}
	if calls := presigner.snapshotCalls(); len(calls) != 1 || calls[0].ttl != 30*time.Second {
		t.Errorf("presigner calls = %+v, want exactly one call with ttl=30s", calls)
	}
}

// TestPresignHandlerTTLIsClampedNotRejected asserts a caller asking for
// longer than cfg.PresignTTL gets the capped value, not an error.
func TestPresignHandlerTTLIsClampedNotRejected(t *testing.T) {
	t.Parallel()

	a, _ := newTestAPI(testAPIOpts{presigner: &fakePresigner{getURL: "https://minio.example/x", putURL: "https://minio.example/x"}})
	srv := httptest.NewServer(NewServer(a).Handler)
	defer srv.Close()

	before := time.Now()
	// newTestAPI's cfg.PresignTTL is 15 minutes; ask for a day.
	resp := doPresign(t, srv, "test-token", `{"key":"docs/a.pdf","ttl_seconds":86400}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got struct {
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.ExpiresAt.After(before.Add(16 * time.Minute)) {
		t.Errorf("expires_at %v was not clamped to cfg.PresignTTL (15m)", got.ExpiresAt)
	}
}

// TestPresignHandlerTTLOverflowDoesNotProduceANegativeDuration guards
// against int64 overflow on a "milliseconds by mistake" ttl_seconds value.
func TestPresignHandlerTTLOverflowDoesNotProduceANegativeDuration(t *testing.T) {
	t.Parallel()

	a, _ := newTestAPI(testAPIOpts{presigner: &fakePresigner{getURL: "https://minio.example/x", putURL: "https://minio.example/x"}})
	srv := httptest.NewServer(NewServer(a).Handler)
	defer srv.Close()

	resp := doPresign(t, srv, "test-token", `{"key":"docs/a.pdf","ttl_seconds":10000000000}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (an oversized ttl_seconds must clamp, not error)", resp.StatusCode)
	}
	var got struct {
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.ExpiresAt.Before(time.Now()) {
		t.Errorf("expires_at %v is in the past; ttl_seconds overflow produced a negative duration", got.ExpiresAt)
	}
}

func TestPresignHandlerJSONDecodeErrorIs400(t *testing.T) {
	t.Parallel()

	a, _ := newTestAPI(testAPIOpts{presigner: &fakePresigner{getURL: "https://minio.example/x", putURL: "https://minio.example/x"}})
	srv := httptest.NewServer(NewServer(a).Handler)
	defer srv.Close()

	resp := doPresign(t, srv, "test-token", `not json`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestPresignHandlerBadTokenIs401(t *testing.T) {
	t.Parallel()

	a, _ := newTestAPI(testAPIOpts{presigner: &fakePresigner{getURL: "https://minio.example/x", putURL: "https://minio.example/x"}})
	srv := httptest.NewServer(NewServer(a).Handler)
	defer srv.Close()

	resp := doPresign(t, srv, "wrong-token", `{"key":"docs/a.pdf"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}
