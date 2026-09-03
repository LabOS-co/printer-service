package httpapi

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- requestContext / correlation id --------------------------------------

// TestSanitizeRequestIDByteClasses unit-tests sanitizeRequestID directly for
// the byte classes its own doc comment names. A real end-to-end HTTP
// request can't carry most of these: a literal LF/CR can't appear in an
// HTTP header value at all without breaking request framing (there is no
// wire-level way to send one — net/http's client validates and refuses,
// and there is no way around that short of a raw socket producing a
// response no HTTP parser would accept either), so the only way to test the
// byte-wise rejection logic itself is to call the function directly. The
// end-to-end replace-and-log behavior for bytes a real client CAN send
// (over-128-bytes, a single invalid-UTF-8 byte) is covered separately below
// by TestRequestContextReplacesAMalformedID.
func TestSanitizeRequestIDByteClasses(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		id   string
		want string
	}{
		{"plain ascii", "abc-123", "abc-123"},
		{"embedded LF", "abc\ndef", ""},
		{"embedded CR", "abc\rdef", ""},
		{"C1 control NEL, UTF-8 encoded (U+0085)", "abc\xc2\x85def", ""},
		{"line separator, UTF-8 encoded (U+2028)", "abc\xe2\x80\xa8def", ""},
		{"paragraph separator, UTF-8 encoded (U+2029)", "abc\xe2\x80\xa9def", ""},
		{"invalid UTF-8 byte 0xff", "abc\xffdef", ""},
		{"DEL 0x7f", "abc\x7fdef", ""},
		{"empty", "", ""},
		{"exactly 128 bytes", strings.Repeat("x", 128), strings.Repeat("x", 128)},
		{"129 bytes, over the limit", strings.Repeat("x", 129), ""},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := sanitizeRequestID(tt.id); got != tt.want {
				t.Errorf("sanitizeRequestID(%q) = %q, want %q", tt.id, got, tt.want)
			}
		})
	}
}

func TestRequestContextHonorsAndEchoesAWellFormedID(t *testing.T) {
	t.Parallel()

	a, _ := newTestAPI(testAPIOpts{})
	mux := http.NewServeMux()
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	srv := httptest.NewServer(a.handlerChain(mux))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/echo", nil)
	req.Header.Set(requestIDHeader, "caller-supplied-id-123")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if got := resp.Header.Get(requestIDHeader); got != "caller-supplied-id-123" {
		t.Errorf("%s = %q, want the caller-supplied id unchanged", requestIDHeader, got)
	}
}

// TestRequestContextReplacesAMalformedID covers the malformed-id cases a
// real HTTP client can actually send on the wire (net/http's own client
// validates header values for CR/LF before sending, so those bytes can't
// reach this end-to-end path — see TestSanitizeRequestIDByteClasses for
// that part of the contract instead).
func TestRequestContextReplacesAMalformedID(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		id   string
		// wantTruncatedFrom, when non-zero, is the byte length
		// logSafeRequestID's log line must cite for this id - and the full
		// id must NOT appear in the log verbatim. Zero means "too short to
		// trigger logSafeRequestID's own truncation, don't check."
		wantTruncatedFrom int
	}{
		{"over 128 bytes", strings.Repeat("x", 500), 500},
		{"non-ASCII byte", "abc\xffdef", 0},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			a, logger := newTestAPI(testAPIOpts{})
			mux := http.NewServeMux()
			mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
			srv := httptest.NewServer(a.handlerChain(mux))
			defer srv.Close()

			req, _ := http.NewRequest(http.MethodGet, srv.URL+"/echo", nil)
			req.Header.Set(requestIDHeader, tt.id)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()

			got := resp.Header.Get(requestIDHeader)
			if got == tt.id {
				t.Errorf("%s echoed the malformed id unchanged: %q", requestIDHeader, got)
			}
			if got == "" {
				t.Errorf("%s is empty; a substitute id should have been generated", requestIDHeader)
			}

			infos := logger.snapshotInfos()
			found := false
			for _, msg := range infos {
				if strings.Contains(msg, "rejected caller-supplied") {
					found = true
				}
			}
			if !found {
				t.Errorf("no log line recorded the rejected id; infos=%v", infos)
			}

			// An Opus review of this stage found the truncation branch
			// (logSafeRequestID, over maxLogged bytes) was covered by this
			// same 500-byte case but never actually asserted: mutating
			// `if len(raw) > maxLogged` to `if false` still passed every
			// existing check here, since "rejected caller-supplied" appears
			// on both the truncated and untruncated message shapes.
			if tt.wantTruncatedFrom > 0 {
				wantSubstr := fmt.Sprintf("truncated from %d bytes", tt.wantTruncatedFrom)
				truncated := false
				for _, msg := range infos {
					if strings.Contains(msg, wantSubstr) {
						truncated = true
					}
					if strings.Contains(msg, tt.id) {
						t.Errorf("log line contains the full %d-byte id verbatim, should be truncated: %q", len(tt.id), msg)
					}
				}
				if !truncated {
					t.Errorf("no log line contains %q; infos=%v", wantSubstr, infos)
				}
			}
		})
	}
}

// TestFiftyConcurrentRequestsEachGetOwnJobID is the permanent guard on the
// P0-5 globals fix: each of 50 concurrent requests supplies its own
// well-formed id and must get exactly that id back, with exactly one
// completion log recorded per id and no cross-contamination between
// goroutines. A reintroduced shared *logs.LogMetaData would fail this
// deterministically (ids would visibly swap between concurrent requests)
// even without -race; run with -race (WSL — see the plan's own
// environment-limits note) to also confirm no data race in getting there.
func TestFiftyConcurrentRequestsEachGetOwnJobID(t *testing.T) {
	a, logger := newTestAPI(testAPIOpts{})
	mux := http.NewServeMux()
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(time.Millisecond) // widen the window a shared-state race would need to hit
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(a.handlerChain(mux))
	defer srv.Close()

	const n = 50
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			want := fmt.Sprintf("req-%d", i)
			req, _ := http.NewRequest(http.MethodGet, srv.URL+"/echo", nil)
			req.Header.Set(requestIDHeader, want)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Errorf("request %d: %v", i, err)
				return
			}
			defer resp.Body.Close()
			if got := resp.Header.Get(requestIDHeader); got != want {
				t.Errorf("request %d: echoed %q, want %q", i, got, want)
			}
		}(i)
	}
	wg.Wait()

	// wg.Wait() only synchronizes with each CLIENT goroutine's Done() call —
	// it proves nothing about whether the SERVER-side goroutine that handles
	// each request (a completely different goroutine, on httptest.Server's
	// side) has finished its own deferred accessLog bookkeeping by the time
	// the client sees the response. waitForCompletions polls through the
	// logger's own mutex instead of assuming that's already true.
	completions := waitForCompletions(t, logger, n)
	if len(completions) != n {
		t.Fatalf("LogAPICompletion called %d times, want %d", len(completions), n)
	}
	seen := make(map[string]int, n)
	for _, md := range completions {
		seen[md.JobId]++
	}
	for i := range n {
		want := fmt.Sprintf("req-%d", i)
		if seen[want] != 1 {
			t.Errorf("JobId %q recorded %d times, want exactly 1", want, seen[want])
		}
	}
}

// --- accessLog / LogAPICompletion -----------------------------------------

func TestAccessLogRecordsExactlyOnceWithFields(t *testing.T) {
	t.Parallel()

	a, logger := newTestAPI(testAPIOpts{})
	mux := http.NewServeMux()
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(15 * time.Millisecond)
		w.WriteHeader(http.StatusTeapot)
	})
	srv := httptest.NewServer(a.handlerChain(mux))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/slow", nil)
	req.Header.Set(requestIDHeader, "the-job-id")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	completions := waitForCompletions(t, logger, 1)
	if len(completions) != 1 {
		t.Fatalf("LogAPICompletion called %d times, want 1", len(completions))
	}
	md := completions[0]
	if md.JobId != "the-job-id" {
		t.Errorf("JobId = %q, want %q", md.JobId, "the-job-id")
	}
	if md.Status != "418" {
		t.Errorf("Status = %q, want 418", md.Status)
	}
	// A window, not just >0: an Opus review of this stage found >0 alone
	// doesn't pin the unit (ms) - mutating Duration's Milliseconds() call to
	// Microseconds() (middleware.go) still passes ">0", so a 15ms request
	// could report 15000 unnoticed. 15ms..5s comfortably covers the sleep
	// without being sensitive to ordinary scheduling jitter.
	if md.Duration < 15 || md.Duration >= 5000 {
		t.Errorf("Duration = %d ms, want in [15, 5000) (handler slept 15ms)", md.Duration)
	}
	// logs v1.5.2's formatters render the completion message from
	// ServiceDuration, not Duration (verified live against a real logstash
	// listener) — both must be set the same way or the message text reads
	// "Duration: 0 ms" regardless of the correct structured field.
	if md.ServiceDuration != md.Duration {
		t.Errorf("ServiceDuration = %d, want equal to Duration (%d)", md.ServiceDuration, md.Duration)
	}
}

func TestAccessLogRecordsA401WithDuration(t *testing.T) {
	t.Parallel()

	a, logger := newTestAPI(testAPIOpts{})
	srv := httptest.NewServer(NewServer(a).Handler)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/print", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	completions := waitForCompletions(t, logger, 1)
	if len(completions) != 1 {
		t.Fatalf("LogAPICompletion called %d times, want 1", len(completions))
	}
	if completions[0].Status != "401" {
		t.Errorf("Status = %q, want 401", completions[0].Status)
	}
}

// TestAccessLogDefaultsStatusTo200WhenHandlerNeverCallsWriteHeader pins
// statusRecorder's documented default: net/http itself sends 200 when a
// handler writes a body without ever calling WriteHeader, so a recorder
// that never observed a call must report the same thing net/http actually
// sends - not, say, its zero value. An Opus review of this stage found no
// test in this package exercised this path (every other test's handler
// either panics or calls WriteHeader explicitly).
func TestAccessLogDefaultsStatusTo200WhenHandlerNeverCallsWriteHeader(t *testing.T) {
	t.Parallel()

	a, logger := newTestAPI(testAPIOpts{})
	mux := http.NewServeMux()
	mux.HandleFunc("/implicit", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok")) // no WriteHeader call
	})
	srv := httptest.NewServer(a.handlerChain(mux))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/implicit")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("client saw status %d, want 200", resp.StatusCode)
	}
	completions := waitForCompletions(t, logger, 1)
	if len(completions) != 1 || completions[0].Status != "200" {
		t.Errorf("logged Status = %+v, want exactly one completion with Status=200", completions)
	}
}

// --- maxBytes ---------------------------------------------------------

func TestMaxBytesEnforcesLimitByContentType(t *testing.T) {
	t.Parallel()

	a, _ := newTestAPI(testAPIOpts{maxUpload: 10, maxJSON: 5})

	var readErr error
	probe := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
	})

	cases := []struct {
		name        string
		contentType string
		bodyLen     int
		wantErr     bool
	}{
		{"multipart under cap", "multipart/form-data; boundary=x", 10, false},
		{"multipart over cap", "multipart/form-data; boundary=x", 11, true},
		{"multipart case-insensitive Content-Type", "Multipart/Form-Data; boundary=x", 10, false},
		{"json under cap", "application/json", 5, false},
		{"json over cap", "application/json", 6, true},
		{"unrecognized content-type uses the tighter JSON cap", "text/plain", 6, true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			readErr = nil
			body := bytes.Repeat([]byte("x"), tt.bodyLen)
			req := httptest.NewRequest(http.MethodPost, "/print", bytes.NewReader(body))
			req.Header.Set("Content-Type", tt.contentType)
			w := httptest.NewRecorder()
			a.maxBytes(probe).ServeHTTP(w, req)
			if tt.wantErr && readErr == nil {
				t.Errorf("expected a MaxBytesReader error, got none")
			}
			if !tt.wantErr && readErr != nil {
				t.Errorf("unexpected error under the cap: %v", readErr)
			}
		})
	}
}

// TestMaxBytesWrapsAccessLogForConnectionClose pins the ordering fix from
// the Opus review: maxBytes must wrap accessLog (not the reverse), because
// http.MaxBytesReader signals net/http via an unexported interface that
// accessLog's statusRecorder wrapper cannot implement. Nesting them the
// other way silently defeats the size limit's connection-handling — the
// server would keep the connection open and drain part of the rejected
// body instead of closing it, verified live before this fix existed.
func TestMaxBytesWrapsAccessLogForConnectionClose(t *testing.T) {
	t.Parallel()

	a, logger := newTestAPI(testAPIOpts{maxUpload: 10, maxJSON: 8 << 10})
	mux := http.NewServeMux()
	mux.HandleFunc("/print", func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(a.handlerChain(mux))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/print", bytes.NewReader(bytes.Repeat([]byte("x"), 1000)))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if !resp.Close {
		t.Errorf("resp.Close = false, want true: MaxBytesReader's requestTooLarge signal did not reach net/http")
	}
	if got := waitForCompletions(t, logger, 1); len(got) != 1 {
		t.Errorf("LogAPICompletion called %d times, want 1 (an oversized-body rejection must still be recorded)", len(got))
	}
}

// --- panicRecovery ------------------------------------------------------

func TestPanicRecoveryBeforeWriteReturns500(t *testing.T) {
	t.Parallel()

	a, logger := newTestAPI(testAPIOpts{})
	mux := http.NewServeMux()
	mux.HandleFunc("/boom", func(w http.ResponseWriter, r *http.Request) { panic("clean panic") })
	srv := httptest.NewServer(a.handlerChain(mux))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/boom")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
	if bytes.Contains(body, []byte("clean panic")) || bytes.Contains(body, []byte("goroutine")) {
		t.Fatalf("response body leaked panic/stack detail: %s", body)
	}
	completions := waitForCompletions(t, logger, 1)
	if len(completions) != 1 {
		t.Fatalf("LogAPICompletion called %d times, want 1", len(completions))
	}
	if completions[0].Status != "500" {
		t.Errorf("Status = %q, want 500", completions[0].Status)
	}
}

// TestPanicRecoveryAfterWriteAbortsConnection pins the other Opus-review
// fix: a panic after a handler already wrote part of a response must not
// try to layer a fresh 500 over it (net/http would append it, producing a
// corrupted-but-parseable response — verified live to reach a client as a
// clean 200 with two concatenated JSON documents before this fix). The
// connection is aborted instead, which surfaces to a client as a request
// error, never as success.
func TestPanicRecoveryAfterWriteAbortsConnection(t *testing.T) {
	t.Parallel()

	a, logger := newTestAPI(testAPIOpts{})
	mux := http.NewServeMux()
	mux.HandleFunc("/boom", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"submi`))
		panic("mid-write panic")
	})
	srv := httptest.NewServer(a.handlerChain(mux))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/boom")
	if err == nil {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if bytes.Contains(body, []byte("errorCode")) {
			t.Fatalf("response contains a second, concatenated JSON document: %s", body)
		}
	}
	// Deterministic in practice (confirmed stable over 20 consecutive
	// runs), not merely the common case: net/http never flushes this
	// sub-4KiB buffered partial response before http.ErrAbortHandler closes
	// the raw connection, so the client always observes a connection error
	// rather than a truncated-but-parseable body. Asserted explicitly per
	// an Opus review of this stage, which found the `if err == nil` body
	// above never actually ran on a healthy machine — a property this
	// test's own design relied on without stating it.
	if err == nil {
		t.Error("expected the connection to be aborted (a client-visible error), got a successful response instead")
	}

	if errs := waitForErrors(t, logger, 1); len(errs) == 0 {
		t.Errorf("expected a LogError call recording the mid-write panic")
	}
	if completions := waitForCompletions(t, logger, 1); len(completions) != 1 {
		t.Errorf("LogAPICompletion called %d times, want 1 (accessLog's bookkeeping is deferred so it survives this unwind)", len(completions))
	}
}

func TestPanicRecoveryReRaisesErrAbortHandler(t *testing.T) {
	t.Parallel()

	a, _ := newTestAPI(testAPIOpts{})
	mux := http.NewServeMux()
	mux.HandleFunc("/boom", func(w http.ResponseWriter, r *http.Request) { panic(http.ErrAbortHandler) })
	srv := httptest.NewServer(a.handlerChain(mux))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/boom")
	if err == nil {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if bytes.Contains(body, []byte("internal server error")) {
			t.Fatalf("http.ErrAbortHandler was converted into a fabricated 500 instead of re-panicking: %s", body)
		}
	}
	// Same determinism as TestPanicRecoveryAfterWriteAbortsConnection: the
	// connection is aborted before any response is written at all here, so
	// the client always sees a connection error, never a response to
	// inspect at all.
	if err == nil {
		t.Error("expected the connection to be aborted (a client-visible error), got a successful response instead")
	}
}
