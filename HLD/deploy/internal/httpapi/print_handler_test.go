package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"printgateway/internal/apperr"
	"printgateway/internal/printgw"
)

// newMultipartBody builds a multipart/form-data body with the given parts in
// exactly the given order — mime/multipart.Writer emits parts in call order,
// which is what TestPrintHandlerMultipartFieldOrderIndependence relies on to
// prove the handler doesn't care which part arrives first.
func newMultipartBody(t *testing.T, fileFirst bool, printer, filename, fileContent string) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	writeField := func() {
		if printer != "" {
			if err := w.WriteField("printer", printer); err != nil {
				t.Fatal(err)
			}
		}
	}
	writeFile := func() {
		if filename != "" {
			fw, err := w.CreateFormFile("file", filename)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fw.Write([]byte(fileContent)); err != nil {
				t.Fatal(err)
			}
		}
	}

	if fileFirst {
		writeFile()
		writeField()
	} else {
		writeField()
		writeFile()
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, w.FormDataContentType()
}

func doPrint(t *testing.T, srv *httptest.Server, token string, body io.Reader, contentType string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/print", body)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set(authTokenHeader, token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestPrintHandlerMultipartHappyPath(t *testing.T) {
	t.Parallel()

	submitter := &fakeSubmitter{result: printgw.SubmitResult{Output: "request id is q-1 (1 file(s))\n"}}
	a, _ := newTestAPI(testAPIOpts{submitter: submitter})
	srv := httptest.NewServer(NewServer(a).Handler)
	defer srv.Close()

	body, ct := newMultipartBody(t, false, "q-hp-laserjet", "doc.pdf", "%PDF-1.4 fake")
	resp := doPrint(t, srv, "test-token", body, ct)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, b)
	}
	var got map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["status"] != "submitted" {
		t.Errorf("status field = %q, want %q", got["status"], "submitted")
	}
	if got["output"] != "request id is q-1 (1 file(s))\n" {
		t.Errorf("output field = %q", got["output"])
	}
	if len(got) != 2 {
		t.Errorf("response has %d keys, want exactly 2 (status, output): %v", len(got), got)
	}

	// Pins what actually got printed, not just that *something* did - an
	// Opus review of this stage found no test in this package ever checked
	// this, so a mutant substituting the printer, title, or spooled body
	// passed the whole suite.
	jobs := submitter.snapshotJobs()
	if len(jobs) != 1 {
		t.Fatalf("Submit called %d times, want 1", len(jobs))
	}
	if jobs[0].Printer != "q-hp-laserjet" {
		t.Errorf("Printer = %q, want %q", jobs[0].Printer, "q-hp-laserjet")
	}
	if jobs[0].Title != "doc.pdf" {
		t.Errorf("Title = %q, want %q", jobs[0].Title, "doc.pdf")
	}
	bodies := submitter.snapshotSpooledBodies()
	if len(bodies) != 1 || string(bodies[0]) != "%PDF-1.4 fake" {
		t.Errorf("spooled body = %q, want %q", bodies, "%PDF-1.4 fake")
	}
}

// TestPrintHandlerMultipartFieldOrderIndependence pins that the handler
// doesn't assume "printer" arrives before "file" in the multipart body —
// ParseMultipartForm reads the whole body before FormValue/FormFile are
// ever called, so both orders must behave identically.
func TestPrintHandlerMultipartFieldOrderIndependence(t *testing.T) {
	t.Parallel()

	a, _ := newTestAPI(testAPIOpts{})
	srv := httptest.NewServer(NewServer(a).Handler)
	defer srv.Close()

	body, ct := newMultipartBody(t, true, "q-hp-laserjet", "doc.pdf", "%PDF-1.4 fake")
	resp := doPrint(t, srv, "test-token", body, ct)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200 (file part before printer field); body=%s", resp.StatusCode, b)
	}
}

func TestPrintHandlerMultipartMissingPrinter(t *testing.T) {
	t.Parallel()

	a, _ := newTestAPI(testAPIOpts{})
	srv := httptest.NewServer(NewServer(a).Handler)
	defer srv.Close()

	body, ct := newMultipartBody(t, false, "", "doc.pdf", "content")
	resp := doPrint(t, srv, "test-token", body, ct)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 400; body=%s", resp.StatusCode, b)
	}
}

func TestPrintHandlerMultipartMissingFilePartIs400(t *testing.T) {
	t.Parallel()

	a, _ := newTestAPI(testAPIOpts{})
	srv := httptest.NewServer(NewServer(a).Handler)
	defer srv.Close()

	body, ct := newMultipartBody(t, false, "q1", "", "")
	resp := doPrint(t, srv, "test-token", body, ct)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 400 (no file part); body=%s", resp.StatusCode, b)
	}
}

func TestPrintHandlerJSONMissingPrinterIs400(t *testing.T) {
	t.Parallel()

	a, _ := newTestAPI(testAPIOpts{})
	srv := httptest.NewServer(NewServer(a).Handler)
	defer srv.Close()

	resp := doPrint(t, srv, "test-token", strings.NewReader(`{"file_url":"http://example.invalid/x.pdf"}`), "application/json")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestPrintHandlerJSONBothFileURLAndS3KeyIs400(t *testing.T) {
	t.Parallel()

	a, _ := newTestAPI(testAPIOpts{})
	srv := httptest.NewServer(NewServer(a).Handler)
	defer srv.Close()

	resp := doPrint(t, srv, "test-token", strings.NewReader(`{"printer":"q1","file_url":"http://x","s3_key":"a.pdf"}`), "application/json")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (exactly one of file_url/s3_key required)", resp.StatusCode)
	}
}

func TestPrintHandlerJSONNeitherFileURLNorS3KeyIs400(t *testing.T) {
	t.Parallel()

	a, _ := newTestAPI(testAPIOpts{})
	srv := httptest.NewServer(NewServer(a).Handler)
	defer srv.Close()

	resp := doPrint(t, srv, "test-token", strings.NewReader(`{"printer":"q1"}`), "application/json")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (exactly one of file_url/s3_key required)", resp.StatusCode)
	}
}

func TestPrintHandlerJSONS3KeyPathTraversalIs400(t *testing.T) {
	t.Parallel()

	a, _ := newTestAPI(testAPIOpts{})
	srv := httptest.NewServer(NewServer(a).Handler)
	defer srv.Close()

	resp := doPrint(t, srv, "test-token", strings.NewReader(`{"printer":"q1","s3_key":"../other-bucket/x"}`), "application/json")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (path traversal in s3_key)", resp.StatusCode)
	}
}

// TestPrintHandlerJSONConcatenatedDocumentsIs400 exercises decodeStrictJSON's
// "err == nil" branch specifically: two syntactically valid JSON values back
// to back, as opposed to TestPrintHandlerJSONTrailingGarbageIs400's
// not-valid-JSON tail (a different branch of the same function).
func TestPrintHandlerJSONConcatenatedDocumentsIs400(t *testing.T) {
	t.Parallel()

	a, _ := newTestAPI(testAPIOpts{})
	srv := httptest.NewServer(NewServer(a).Handler)
	defer srv.Close()

	resp := doPrint(t, srv, "test-token", strings.NewReader(`{"printer":"q1","file_url":"http://x"}{"printer":"q2"}`), "application/json")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (a second concatenated JSON value must be rejected, not silently discarded)", resp.StatusCode)
	}
}

func TestPrintHandlerMultipartOversizeIs413(t *testing.T) {
	t.Parallel()

	a, _ := newTestAPI(testAPIOpts{maxUpload: 16})
	srv := httptest.NewServer(NewServer(a).Handler)
	defer srv.Close()

	body, ct := newMultipartBody(t, false, "q-hp-laserjet", "doc.pdf", strings.Repeat("x", 1000))
	resp := doPrint(t, srv, "test-token", body, ct)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 413; body=%s", resp.StatusCode, b)
	}
}

func TestPrintHandlerJSONHappyPath(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFetcher{body: []byte("%PDF-1.4 fake")}
	submitter := &fakeSubmitter{}
	a, _ := newTestAPI(testAPIOpts{fetcher: fetcher, submitter: submitter})
	srv := httptest.NewServer(NewServer(a).Handler)
	defer srv.Close()

	resp := doPrint(t, srv, "test-token", strings.NewReader(`{"printer":"q1","file_url":"http://example.invalid/x.pdf"}`), "application/json")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, b)
	}
	// Pins that the URL the handler decoded is the one actually fetched,
	// and that the printer/spooled body reaching Submit match - an Opus
	// review of this stage found fakeFetcher discarded rawURL entirely, so
	// a mutant hardcoding a different file_url before calling PrintURL
	// passed the whole suite.
	if fetcher.rawURL != "http://example.invalid/x.pdf" {
		t.Errorf("fetcher received rawURL = %q, want %q", fetcher.rawURL, "http://example.invalid/x.pdf")
	}
	jobs := submitter.snapshotJobs()
	if len(jobs) != 1 || jobs[0].Printer != "q1" {
		t.Fatalf("jobs = %+v, want exactly one job with Printer=q1", jobs)
	}
	if bodies := submitter.snapshotSpooledBodies(); len(bodies) != 1 || string(bodies[0]) != "%PDF-1.4 fake" {
		t.Errorf("spooled body = %q, want %q", bodies, "%PDF-1.4 fake")
	}
}

func TestPrintHandlerJSONUnknownFieldIs400(t *testing.T) {
	t.Parallel()

	a, _ := newTestAPI(testAPIOpts{fetcher: &fakeFetcher{body: []byte("%PDF-1.4 fake")}})
	srv := httptest.NewServer(NewServer(a).Handler)
	defer srv.Close()

	resp := doPrint(t, srv, "test-token", strings.NewReader(`{"printer":"q1","file_url":"http://example.invalid/x.pdf","bogus":"field"}`), "application/json")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 400 for an unknown JSON field; body=%s", resp.StatusCode, b)
	}
}

func TestPrintHandlerJSONTrailingGarbageIs400(t *testing.T) {
	t.Parallel()

	a, _ := newTestAPI(testAPIOpts{})
	srv := httptest.NewServer(NewServer(a).Handler)
	defer srv.Close()

	resp := doPrint(t, srv, "test-token", strings.NewReader(`{"printer":"q1","file_url":"http://example.invalid/x.pdf"} trailing garbage`), "application/json")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 400 for trailing garbage after the JSON value; body=%s", resp.StatusCode, b)
	}
}

func TestPrintHandlerS3KeyHappyPath(t *testing.T) {
	t.Parallel()

	objectStore := &fakeObjectStore{body: []byte("%PDF fake"), size: 9}
	submitter := &fakeSubmitter{}
	a, _ := newTestAPI(testAPIOpts{objectStore: objectStore, submitter: submitter})
	srv := httptest.NewServer(NewServer(a).Handler)
	defer srv.Close()

	resp := doPrint(t, srv, "test-token", strings.NewReader(`{"printer":"q1","s3_key":"docs/a.pdf"}`), "application/json")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, b)
	}
	// Pins that the key the handler decoded is the one actually fetched
	// from the store, and that the printer/spooled body reaching Submit
	// match - see TestPrintHandlerJSONHappyPath's comment for why this
	// matters (an Opus review of this stage found none of this recorded).
	if objectStore.key != "docs/a.pdf" {
		t.Errorf("store received key = %q, want %q", objectStore.key, "docs/a.pdf")
	}
	jobs := submitter.snapshotJobs()
	if len(jobs) != 1 || jobs[0].Printer != "q1" {
		t.Fatalf("jobs = %+v, want exactly one job with Printer=q1", jobs)
	}
	if bodies := submitter.snapshotSpooledBodies(); len(bodies) != 1 || string(bodies[0]) != "%PDF fake" {
		t.Errorf("spooled body = %q, want %q", bodies, "%PDF fake")
	}
}

func TestPrintHandlerUnknownS3KeyIs404(t *testing.T) {
	t.Parallel()

	a, _ := newTestAPI(testAPIOpts{
		objectStore: &fakeObjectStore{err: &apperr.HTTPError{Status: http.StatusNotFound, Public: "not found"}},
	})
	srv := httptest.NewServer(NewServer(a).Handler)
	defer srv.Close()

	resp := doPrint(t, srv, "test-token", strings.NewReader(`{"printer":"q1","s3_key":"docs/missing.pdf"}`), "application/json")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 404; body=%s", resp.StatusCode, b)
	}
}

func TestPrintHandlerWrongContentTypeIs415(t *testing.T) {
	t.Parallel()

	a, _ := newTestAPI(testAPIOpts{})
	srv := httptest.NewServer(NewServer(a).Handler)
	defer srv.Close()

	resp := doPrint(t, srv, "test-token", strings.NewReader("printer=q1"), "text/plain")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", resp.StatusCode)
	}
}

func TestPrintHandlerGetIs405(t *testing.T) {
	t.Parallel()

	a, _ := newTestAPI(testAPIOpts{})
	srv := httptest.NewServer(NewServer(a).Handler)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/print", nil)
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

func TestPrintHandlerBadTokenIs401(t *testing.T) {
	t.Parallel()

	a, _ := newTestAPI(testAPIOpts{})
	srv := httptest.NewServer(NewServer(a).Handler)
	defer srv.Close()

	body, ct := newMultipartBody(t, false, "q1", "doc.pdf", "content")
	resp := doPrint(t, srv, "wrong-token", body, ct)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

// TestPrintHandlerDoesNotLeakInternalDetailToTheClient is the leak-regression
// case from the plan's own test table: an unclassified Submitter error whose
// text names a temp file path and lp's own stderr must reach the log (an
// operator needs it) but never the HTTP response body (an authenticated
// caller is not automatically trusted with the server's filesystem layout or
// subprocess internals).
func TestPrintHandlerDoesNotLeakInternalDetailToTheClient(t *testing.T) {
	t.Parallel()

	const leakedPath = "/tmp/print-upload-xyz123"
	const leakedStderr = "lp: unknown printer q-does-not-exist"
	sensitive := fmt.Errorf("running lp: exit status 1: %s (spool file %s)", leakedStderr, leakedPath)

	a, logger := newTestAPI(testAPIOpts{
		submitter: &fakeSubmitter{err: sensitive},
	})
	srv := httptest.NewServer(NewServer(a).Handler)
	defer srv.Close()

	body, ct := newMultipartBody(t, false, "q-does-not-exist", "doc.pdf", "content")
	resp := doPrint(t, srv, "test-token", body, ct)
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(respBody, []byte(leakedPath)) {
		t.Errorf("response body leaked the spool path: %s", respBody)
	}
	if bytes.Contains(respBody, []byte(leakedStderr)) {
		t.Errorf("response body leaked lp's stderr: %s", respBody)
	}

	errs := waitForErrors(t, logger, 1)
	joined := strings.Join(errs, "\n")
	if !strings.Contains(joined, leakedPath) {
		t.Errorf("log does not contain the spool path (an operator needs it): %v", errs)
	}
	if !strings.Contains(joined, leakedStderr) {
		t.Errorf("log does not contain lp's stderr (an operator needs it): %v", errs)
	}
}
