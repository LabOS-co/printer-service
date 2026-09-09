package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStatusHandlerNoTokenRequired(t *testing.T) {
	t.Parallel()

	a, _ := newTestAPI(testAPIOpts{})
	srv := httptest.NewServer(NewServer(a).Handler)
	defer srv.Close()

	// Deliberately no X-Labos-Print-Token header: the network-proxy health
	// check has no token to send.
	resp, err := http.Get(srv.URL + "/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "Running. Label: printgateway") {
		t.Errorf("body = %q, want it to start with the running/label line", body)
	}
}

func TestStatusHandlerMethodNotAllowed(t *testing.T) {
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
}
