package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestStatusIsUnauthenticatedJSON guards system_api.Status's contract: GET
// /status needs no X-Labos-Print-Token (the network-proxy's health check has
// none to send) and answers the status/version/build/label JSON shape, every
// field populated (version/build/label read "unknown" rather than empty when
// the test binary itself isn't built with -ldflags, so asserting non-empty —
// not a specific value — is what actually exercises the wiring).
func TestStatusIsUnauthenticatedJSON(t *testing.T) {
	t.Parallel()

	a, _ := newTestAPI(testAPIOpts{})
	srv := httptest.NewServer(NewServer(a).Handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var body struct {
		Status  string `json:"status"`
		Version string `json:"version"`
		Build   string `json:"build"`
		Label   string `json:"label"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Status == "" || body.Version == "" || body.Build == "" || body.Label == "" {
		t.Errorf("status payload has an empty field: %+v", body)
	}
}

// TestStatusMethodNotAllowedUsesTheLabosEnvelope guards the a.methodNotAllowed
// wiring: chi's own default 405 response is an empty body, which would
// otherwise break the "every failure is the same JSON envelope" contract
// documented in README.md (a.notFound guards chi's matching 404 default).
func TestStatusMethodNotAllowedUsesTheLabosEnvelope(t *testing.T) {
	t.Parallel()

	a, _ := newTestAPI(testAPIOpts{})
	srv := httptest.NewServer(NewServer(a).Handler)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/status", "text/plain", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}
	if allow := resp.Header.Get("Allow"); allow != http.MethodGet {
		t.Errorf("Allow header = %q, want %q (RFC 9110 §15.5.6 requires it on a 405)", allow, http.MethodGet)
	}

	var body struct {
		ErrorMessage string `json:"errorMessage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v (want the labOS error envelope, not chi's empty default)", err)
	}
	if body.ErrorMessage == "" {
		t.Error("errorMessage field is empty")
	}
}

// TestStatusNotFoundUsesTheLabosEnvelope guards the a.notFound wiring: chi's
// own default 404 response is a plain-text "404 page not found", which would
// otherwise break the "every failure is the same JSON envelope" contract
// documented in README.md (a.methodNotAllowed guards chi's matching 405 default).
func TestStatusNotFoundUsesTheLabosEnvelope(t *testing.T) {
	t.Parallel()

	a, _ := newTestAPI(testAPIOpts{})
	srv := httptest.NewServer(NewServer(a).Handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/no-such-route")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}

	var body struct {
		ErrorMessage string `json:"errorMessage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v (want the labOS error envelope, not chi's plain-text default)", err)
	}
	if body.ErrorMessage == "" {
		t.Error("errorMessage field is empty")
	}
}
