package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"printgateway/internal/apperr"
	"printgateway/internal/printgw"
)

// newBareAPI builds an *API directly (not through NewServer/httptest), for
// unit-testing fail's own branches without needing a full HTTP round trip.
func newBareAPI() (*API, *capturingLogger) {
	return newTestAPI(testAPIOpts{})
}

// containsSubstring reports whether any entry in errs contains substr.
func containsSubstring(errs []string, substr string) bool {
	for _, e := range errs {
		if strings.Contains(e, substr) {
			return true
		}
	}
	return false
}

func TestFailOrdinaryHTTPErrorLogsInternalOnce(t *testing.T) {
	t.Parallel()

	a, logger := newBareAPI()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/x", nil)

	a.fail(w, r, &apperr.HTTPError{Status: http.StatusBadGateway, Public: "bad gateway", Internal: errors.New("upstream dial failed: 10.0.0.1:9999")})

	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", w.Code)
	}
	if strings.Contains(w.Body.String(), "10.0.0.1") {
		t.Errorf("Internal detail leaked into the response: %s", w.Body.String())
	}
	errs := logger.snapshotErrors()
	if !containsSubstring(errs, "10.0.0.1") {
		t.Errorf("Internal detail was not logged: %v", errs)
	}
}

func TestFailHTTPErrorWithNoInternalLogsNothing(t *testing.T) {
	t.Parallel()

	a, logger := newBareAPI()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/x", nil)

	a.fail(w, r, &apperr.HTTPError{Status: http.StatusBadRequest, Public: "printer is required"})

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if errs := logger.snapshotErrors(); len(errs) != 1 || errs[0] != "printer is required" {
		t.Errorf("expected only error_handler's own line, got %v", errs)
	}
}

func TestFailNilErrorIsGuarded(t *testing.T) {
	t.Parallel()

	a, logger := newBareAPI()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/x", nil)

	a.fail(w, r, nil)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
	if !containsSubstring(logger.snapshotErrors(), "nil error") {
		t.Errorf("expected a nil-error caller-bug LogError call, got %v", logger.snapshotErrors())
	}
	if body := w.Body.String(); !strings.Contains(body, "internal server error") {
		t.Errorf("response body = %q, want it to contain errFailCalledImproperly's pinned Public text", body)
	}
}

func TestFailTypedNilHTTPErrorIsGuarded(t *testing.T) {
	t.Parallel()

	a, logger := newBareAPI()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/x", nil)

	var typedNil *apperr.HTTPError
	a.fail(w, r, typedNil)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
	if !containsSubstring(logger.snapshotErrors(), "typed-nil") {
		t.Errorf("expected a typed-nil caller-bug LogError call, got %v", logger.snapshotErrors())
	}
}

// TestFailUnclassifiedErrorIsNeverSerialized asserts a plain error (not an
// *apperr.HTTPError) never reaches the client verbatim.
func TestFailUnclassifiedErrorIsNeverSerialized(t *testing.T) {
	t.Parallel()

	a, logger := newBareAPI()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/x", nil)

	a.fail(w, r, errors.New("raw filesystem error: /etc/shadow permission denied"))

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
	if strings.Contains(w.Body.String(), "/etc/shadow") {
		t.Fatalf("an unclassified error's raw text leaked into the response: %s", w.Body.String())
	}
	if !containsSubstring(logger.snapshotErrors(), "/etc/shadow") {
		t.Errorf("expected the unclassified error's text to be logged, got %v", logger.snapshotErrors())
	}
}

// TestRequireTokenEmptyExpectedFailsClosed asserts that with no token
// configured, an unauthenticated request is still refused (fail closed).
func TestRequireTokenEmptyExpectedFailsClosed(t *testing.T) {
	t.Parallel()

	svc := printgw.NewService(&fakeSubmitter{}, nil, nil, printgw.Timeouts{Submit: 1}, 0)
	logger := &capturingLogger{}
	cfg := fullConfig(t)
	cfg.AuthToken = ""
	a := New(cfg, logger, svc, nil)
	srv := httptest.NewServer(NewServer(a).Handler)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/print", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 (server not configured for auth, must fail closed)", resp.StatusCode)
	}
}

// TestRequireTokenIsNoOpWhenAuthNotRequired asserts that with RequireAuth
// false (this deployment's current default), a request with no token header
// at all reaches the handler instead of being rejected.
func TestRequireTokenIsNoOpWhenAuthNotRequired(t *testing.T) {
	t.Parallel()

	svc := printgw.NewService(&fakeSubmitter{}, nil, nil, printgw.Timeouts{Submit: 1}, 0)
	logger := &capturingLogger{}
	cfg := fullConfig(t)
	cfg.RequireAuth = false
	cfg.AuthToken = ""
	a := New(cfg, logger, svc, nil)
	srv := httptest.NewServer(NewServer(a).Handler)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/print", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusServiceUnavailable {
		t.Errorf("status = %d, want the request to reach the handler unauthenticated (not 401/503)", resp.StatusCode)
	}
}
