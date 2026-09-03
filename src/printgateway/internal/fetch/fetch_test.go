package fetch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"printgateway/internal/apperr"
)

// These tests drive the real SafeFetcher over httptest, so the transport,
// the redirect policy and the dial Control are all the production ones.
// httptest.Server always binds loopback on an ephemeral port, so every case
// that needs to reach it passes allowPrivateTargets=true — the escape hatch
// designed in for exactly this. The cases that must prove a target is
// REFUSED deliberately do not use a server at all: newDialControl rejects
// before connect(2), so a bare address is both faithful and free of any real
// network traffic.

// One block in Fetch is deliberately left uncovered, recorded here so a
// later reader does not conclude it is dead code and delete it:
//
//   - GotConn's own ParseAddrPort error return. Unreachable portably: a TCP
//     RemoteAddr() always parses as ip:port, so provoking this needs a
//     non-IP net.Addr (a net.Pipe or unix-socket conn), which this
//     production transport never produces.
//
// The rest of the post-connect "belt and suspenders" layer needs NO
// production seam to reach, even though its three branches only fire
// depending on which side of a specific race against ctx cancellation
// http.Client.Do lands on: TestFetchPostConnectRecheckCatchesAMisWiredControl,
// TestFetchSucceedsAgainstAPublicPeerWithPrivateTargetsBlocked, and
// TestFetchPostConnectRecheckCatchesACompletedResponseFromABlockedPeer below
// drive all three directly, by substituting f.client.Transport from inside
// this package. An earlier version of this comment claimed an injectable
// Control would be needed and left the layer untested — that claim was
// wrong, and dangerous: a mutant making GotConn reject every peer (a 100%
// outage of file_url in production) shipped green under it, because no test
// ever ran Fetch to a successful download with allowPrivate=false. A LATER
// version of this same comment then claimed the success-path
// blocked.IsValid() arm specifically could not be provoked deterministically
// — also wrong: a fake http.RoundTripper that fires GotConn and then returns
// a completed response regardless of ctx (exactly the real net/http race
// fetch.go's own comment describes) reaches it every time. Two wrong
// "unreachable" claims in a row on the same paragraph is itself the lesson:
// verify by trying, not by re-reading the reasoning that produced the last
// claim.
//
// The http.NewRequestWithContext error branch is separately unreachable:
// verified by probe that for every input url.Parse accepts, u.String()
// round-trips and NewRequest succeeds; the method is a constant. Unreachable
// for any URL that has already cleared validateURL.
const testMaxBytes = 100

// requireHTTPError asserts err carries the expected status, a non-empty
// Public (or error_handler would return a blank message to the caller), and
// a non-nil Internal (every construction site in this package sets one; a
// nil here would mean a failure with no diagnosable detail in the log).
//
// It deliberately does NOT assert Public != Internal.Error() as a blanket
// rule: several call sites (a bad scheme, a disallowed port) set Public to
// exactly the internal message on purpose, because that detail is itself
// safe to disclose. The places where leaking internal detail would matter
// (the allowlist rejection, an upstream URL, an upstream status code) assert
// that distinction individually — see
// TestFetchDoesNotLeakTheRejectedHostToTheCaller and its neighbors.
func requireHTTPError(t *testing.T, err error, wantStatus int) *apperr.HTTPError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error with status %d, got nil", wantStatus)
	}
	var httpErr *apperr.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error is %T (%v), want *apperr.HTTPError", err, err)
	}
	if httpErr.Status != wantStatus {
		t.Fatalf("status = %d (%v), want %d", httpErr.Status, err, wantStatus)
	}
	if httpErr.Public == "" {
		t.Errorf("Public is empty; error_handler would return a blank message to the caller")
	}
	if httpErr.Internal == nil {
		t.Errorf("Internal is nil; the failure would be undiagnosable from the log")
	}
	return httpErr
}

// newTestServer starts an httptest server and returns it with a fetcher
// already permitted to reach it.
func newTestServer(t *testing.T, maxBytes int64, handler http.HandlerFunc) (*httptest.Server, *SafeFetcher) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, NewSafeFetcher(true, nil, maxBytes)
}

func TestFetchCopiesTheBody(t *testing.T) {
	t.Parallel()

	const body = "%PDF-1.7 not really a pdf"
	srv, f := newTestServer(t, testMaxBytes, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		io.WriteString(w, body)
	})

	var dst bytes.Buffer
	n, err := f.Fetch(t.Context(), srv.URL+"/doc.pdf", &dst)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if n != int64(len(body)) {
		t.Errorf("n = %d, want %d", n, len(body))
	}
	if dst.String() != body {
		t.Errorf("dst = %q, want %q", dst.String(), body)
	}
}

// TestFetchAcceptsABodyExactlyAtTheLimit is the other half of the oversize
// cases: without it, a limit off by one in the wrong direction (rejecting at
// exactly maxBytes) would go unnoticed.
func TestFetchAcceptsABodyExactlyAtTheLimit(t *testing.T) {
	t.Parallel()

	body := strings.Repeat("a", testMaxBytes)
	srv, f := newTestServer(t, testMaxBytes, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, body)
	})

	var dst bytes.Buffer
	n, err := f.Fetch(t.Context(), srv.URL+"/doc.pdf", &dst)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if n != testMaxBytes {
		t.Errorf("n = %d, want %d", n, testMaxBytes)
	}
}

// TestFetchRejectsAnOversizeContentLength pins the cheap pre-check: the body
// must never be read at all when the header already says it is too big.
func TestFetchRejectsAnOversizeContentLength(t *testing.T) {
	t.Parallel()

	body := strings.Repeat("a", testMaxBytes*2)
	srv, f := newTestServer(t, testMaxBytes, func(w http.ResponseWriter, r *http.Request) {
		// Set it explicitly rather than relying on Go's automatic sizing, so
		// the case keeps testing the Content-Length path even if the body
		// size or the server's buffering behavior changes.
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		io.WriteString(w, body)
	})

	var dst bytes.Buffer
	n, err := f.Fetch(t.Context(), srv.URL+"/doc.pdf", &dst)
	httpErr := requireHTTPError(t, err, http.StatusRequestEntityTooLarge)
	if n != 0 {
		t.Errorf("n = %d, want 0: the body must not be read once the header is over the limit", n)
	}
	if dst.Len() != 0 {
		t.Errorf("dst got %d bytes, want 0", dst.Len())
	}
	if !strings.Contains(fmt.Sprint(httpErr.Internal), "content-length") {
		t.Errorf("Internal = %v, want it to name content-length (the pre-check, not the copy limit)", httpErr.Internal)
	}
}

// TestFetchRejectsAnOversizeChunkedBody is the case that matters more: a
// chunked response declares no length, so the header pre-check cannot fire
// and only the LimitReader stops it. A body that lies about its size takes
// this same path.
func TestFetchRejectsAnOversizeChunkedBody(t *testing.T) {
	t.Parallel()

	srv, f := newTestServer(t, testMaxBytes, func(w http.ResponseWriter, r *http.Request) {
		// Flushing before the response is complete forces chunked encoding,
		// which is what leaves ContentLength at -1 on the client side.
		io.WriteString(w, strings.Repeat("a", testMaxBytes))
		w.(http.Flusher).Flush()
		io.WriteString(w, "over the limit")
	})

	var dst bytes.Buffer
	_, err := f.Fetch(t.Context(), srv.URL+"/doc.pdf", &dst)
	httpErr := requireHTTPError(t, err, http.StatusRequestEntityTooLarge)
	if !strings.Contains(fmt.Sprint(httpErr.Internal), "body exceeded") {
		t.Errorf("Internal = %v, want the copy-limit message, not the content-length one", httpErr.Internal)
	}
	// The overshoot is bounded to one byte past the limit by LimitReader —
	// an unbounded read is the resource-exhaustion half of P0-4.
	if int64(dst.Len()) > testMaxBytes+1 {
		t.Errorf("dst got %d bytes, want at most %d", dst.Len(), testMaxBytes+1)
	}
}

// TestFetchWithAHugeMaxBytesStillCopies is the regression guard for the
// overflow found in A5's review: maxBytes near math.MaxInt64 wrapped
// maxBytes+1 negative, and io.LimitReader treats N<=0 as "already at the
// limit" — an immediate EOF that reads as a genuine zero-byte success, so a
// blank page printed and was reported as submitted. That is P0-3's failure
// class, reached through a config value rather than a disk error.
func TestFetchWithAHugeMaxBytesStillCopies(t *testing.T) {
	t.Parallel()

	const body = "not blank"
	srv, f := newTestServer(t, math.MaxInt64, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, body)
	})

	var dst bytes.Buffer
	n, err := f.Fetch(t.Context(), srv.URL+"/doc.pdf", &dst)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if n != int64(len(body)) || dst.String() != body {
		t.Fatalf("got %d bytes %q, want %d bytes %q", n, dst.String(), len(body), body)
	}
}

func TestFetchRejectsANonOKStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status int
	}{
		{name: "404", status: http.StatusNotFound},
		{name: "500", status: http.StatusInternalServerError},
		{name: "204 is not OK either", status: http.StatusNoContent},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv, f := newTestServer(t, testMaxBytes, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				io.WriteString(w, "an error page, not a document")
			})

			var dst bytes.Buffer
			n, err := f.Fetch(t.Context(), srv.URL+"/doc.pdf", &dst)
			// 502, not the upstream's own status: the caller asked this
			// gateway for a print, and the upstream's status is a detail of
			// how that failed.
			httpErr := requireHTTPError(t, err, http.StatusBadGateway)
			if n != 0 || dst.Len() != 0 {
				t.Errorf("wrote %d bytes (%q), want nothing: an error page must never be spooled", n, dst.String())
			}
			if !strings.Contains(fmt.Sprint(httpErr.Internal), strconv.Itoa(tt.status)) {
				t.Errorf("Internal = %v, want it to name the upstream status %d", httpErr.Internal, tt.status)
			}
			if strings.Contains(httpErr.Public, strconv.Itoa(tt.status)) {
				t.Errorf("Public = %q leaks the upstream status", httpErr.Public)
			}
		})
	}
}

// TestFetchRefusesRedirects pins the no-redirect rule. Following even one
// redirect would let a caller name an allowed public host and be handed
// straight to a private one, bypassing every pre-flight check — the dial
// Control would still fire, but the host allowlist would not.
func TestFetchRefusesRedirects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status int
	}{
		{name: "302", status: http.StatusFound},
		{name: "301", status: http.StatusMovedPermanently},
		{name: "307", status: http.StatusTemporaryRedirect},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv, f := newTestServer(t, testMaxBytes, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/target.pdf" {
					io.WriteString(w, "reached the redirect target")
					return
				}
				http.Redirect(w, r, "/target.pdf", tt.status)
			})

			var dst bytes.Buffer
			_, err := f.Fetch(t.Context(), srv.URL+"/doc.pdf", &dst)
			httpErr := requireHTTPError(t, err, http.StatusBadRequest)
			if httpErr.Public != errRedirectNotAllowed.Error() {
				t.Errorf("Public = %q, want %q", httpErr.Public, errRedirectNotAllowed.Error())
			}
			if dst.Len() != 0 {
				t.Errorf("dst = %q, want nothing: the redirect target must not be read", dst.String())
			}
		})
	}
}

// TestFetchHonorsTheContextDeadline is the P0-1-shaped case for the fetch
// side: without it a wedged upstream parks the request goroutine for as long
// as it likes. printgw.Service.fetch is what applies config.FetchTimeout to
// this ctx in production.
func TestFetchHonorsTheContextDeadline(t *testing.T) {
	t.Parallel()

	srv, f := newTestServer(t, testMaxBytes, func(w http.ResponseWriter, r *http.Request) {
		// Park until the client goes away rather than sleeping a fixed
		// duration: httptest.Server.Close waits for outstanding handlers, so
		// a sleeping handler would hold up the whole test binary.
		<-r.Context().Done()
	})

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	// Run Fetch off the test goroutine and race it against a bound, rather
	// than calling it inline and checking elapsed afterward: if ctx
	// propagation ever regresses, Fetch blocks on <-r.Context().Done()
	// forever (the handler only unblocks when the client goes away), and an
	// inline call would hang the whole test binary to its default 30s/10m
	// timeout instead of failing this one test with a clear message.
	//
	// requireHTTPError (and any other t.Fatal*) is deliberately NOT called
	// from the goroutine below: Fatal's runtime.Goexit unwinds only the
	// calling goroutine, not the test, so a failure there would silently
	// leak this goroutine rather than fail the test. The raw error and
	// elapsed time are sent back and asserted from the test goroutine
	// instead.
	type result struct {
		err     error
		elapsed time.Duration
	}
	done := make(chan result, 1)
	start := time.Now()
	var dst bytes.Buffer
	go func() {
		_, err := f.Fetch(ctx, srv.URL+"/doc.pdf", &dst)
		done <- result{err: err, elapsed: time.Since(start)}
	}()

	select {
	case r := <-done:
		httpErr := requireHTTPError(t, r.err, http.StatusBadGateway)
		if !errors.Is(httpErr.Internal, context.DeadlineExceeded) {
			t.Errorf("Internal = %v, want it to wrap context.DeadlineExceeded", httpErr.Internal)
		}
		// Generous slack: this asserts "bounded by the deadline", not the
		// scheduler's precision.
		if r.elapsed > 5*time.Second {
			t.Errorf("Fetch took %v, want it to return at the deadline", r.elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Fetch did not return within 5s of its 50ms context deadline")
	}
}

// TestFetchReportsACancelledContext covers the caller-cancelled path
// (a disconnected client), distinct from a deadline.
func TestFetchReportsACancelledContext(t *testing.T) {
	t.Parallel()

	srv, f := newTestServer(t, testMaxBytes, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	defer cancel()

	var dst bytes.Buffer
	_, err := f.Fetch(ctx, srv.URL+"/doc.pdf", &dst)
	httpErr := requireHTTPError(t, err, http.StatusBadGateway)
	if !errors.Is(httpErr.Internal, context.Canceled) {
		t.Errorf("Internal = %v, want it to wrap context.Canceled", httpErr.Internal)
	}
}

// TestFetchReportsATruncatedBody covers the copy-error branch: the response
// started fine and failed partway through. It must be an error, not a short
// success, or a truncated document reaches lp and prints (P0-3 again).
func TestFetchReportsATruncatedBody(t *testing.T) {
	t.Parallel()

	srv, f := newTestServer(t, testMaxBytes, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "the first half")
		w.(http.Flusher).Flush()
		// ErrAbortHandler drops the connection mid-body without the server
		// printing a stack trace.
		panic(http.ErrAbortHandler)
	})

	var dst bytes.Buffer
	n, err := f.Fetch(t.Context(), srv.URL+"/doc.pdf", &dst)
	httpErr := requireHTTPError(t, err, http.StatusBadGateway)
	// The partial count is returned deliberately — the caller (printgw)
	// discards the spool file on error, and the count is diagnostic.
	if n != int64(dst.Len()) {
		t.Errorf("n = %d but dst holds %d bytes; the count must describe what was written", n, dst.Len())
	}
	if strings.Contains(httpErr.Public, srv.URL) {
		t.Errorf("Public = %q leaks the upstream URL", httpErr.Public)
	}
}

// TestFetchRejectsBadURLs covers the pre-flight URL policy reached through
// Fetch. No server and no dial: every one of these is refused before a
// connection is attempted, which is the property worth pinning.
func TestFetchRejectsBadURLs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		rawURL     string
		wantStatus int
		wantPublic string
		allowlist  []string
	}{
		{
			name: "an ftp scheme", rawURL: "ftp://example.invalid/doc.pdf",
			wantStatus: http.StatusBadRequest, wantPublic: "scheme must be http or https",
		},
		{
			name: "a file scheme", rawURL: "file:///etc/passwd",
			wantStatus: http.StatusBadRequest, wantPublic: "scheme must be http or https",
		},
		{
			name: "embedded credentials", rawURL: "http://user:pass@example.invalid/doc.pdf",
			wantStatus: http.StatusBadRequest, wantPublic: "embedded credentials",
		},
		{
			name: "the CUPS admin port", rawURL: "http://example.invalid:631/doc.pdf",
			wantStatus: http.StatusBadRequest, wantPublic: "port 631 is not allowed",
		},
		{
			name: "ssh", rawURL: "http://example.invalid:22/doc.pdf",
			wantStatus: http.StatusBadRequest, wantPublic: "port 22 is not allowed",
		},
		{
			name: "a host outside the allowlist", rawURL: "http://example.invalid/doc.pdf",
			allowlist: []string{"docs.internal"}, wantStatus: http.StatusForbidden,
			wantPublic: "host is not allowed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := NewSafeFetcher(false, tt.allowlist, testMaxBytes)
			var dst bytes.Buffer
			n, err := f.Fetch(t.Context(), tt.rawURL, &dst)
			httpErr := requireHTTPError(t, err, tt.wantStatus)
			if !strings.Contains(httpErr.Public, tt.wantPublic) {
				t.Errorf("Public = %q, want it to contain %q", httpErr.Public, tt.wantPublic)
			}
			if n != 0 || dst.Len() != 0 {
				t.Errorf("wrote %d bytes, want 0: nothing may be fetched from a refused URL", n)
			}
		})
	}
}

// TestFetchDoesNotLeakTheRejectedHostToTheCaller: the allowlist rejection is
// the one refusal whose Public string is deliberately vaguer than its
// Internal one — naming the allowlist's contents back to an unauthenticated
// caller would turn the error into a probe for what the gateway can reach.
func TestFetchDoesNotLeakTheRejectedHostToTheCaller(t *testing.T) {
	t.Parallel()

	f := NewSafeFetcher(false, []string{"docs.internal"}, testMaxBytes)
	var dst bytes.Buffer
	_, err := f.Fetch(t.Context(), "http://secret-name.example.invalid/doc.pdf", &dst)

	httpErr := requireHTTPError(t, err, http.StatusForbidden)
	if strings.Contains(httpErr.Public, "secret-name") {
		t.Errorf("Public = %q echoes the requested host back", httpErr.Public)
	}
	if !strings.Contains(fmt.Sprint(httpErr.Internal), "secret-name") {
		t.Errorf("Internal = %v, want it to name the host for the log", httpErr.Internal)
	}
}

// TestFetchBlocksPrivateTargetsAtTheDial is the end-to-end proof that
// newDialControl is actually wired into the transport, not merely correct in
// isolation (guard_test.go covers it in isolation). Each address is on an
// allowed port, so the pre-flight checks all pass and the ONLY thing that can
// refuse it is Control — which fires before connect(2), so nothing is dialed
// even though no server is listening.
func TestFetchBlocksPrivateTargetsAtTheDial(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		rawURL string
	}{
		{name: "loopback", rawURL: "http://127.0.0.1/doc.pdf"},
		{
			name: "a hostname that resolves to loopback", rawURL: "http://localhost/doc.pdf",
			// The rows around this one are all IP literals, which never
			// exercise DNS resolution — validateURL's own hostname check and
			// Control's post-resolution check could trade places without any
			// of them noticing. "localhost" resolves via the OS hosts file
			// (hermetic, no real network) and proves Control is actually
			// invoked with the resolved address, not the literal string.
		},
		{name: "cloud metadata", rawURL: "http://169.254.169.254/latest/meta-data/"},
		{name: "RFC1918", rawURL: "https://10.1.2.3/doc.pdf"},
		{name: "IPv6 loopback", rawURL: "http://[::1]/doc.pdf"},
		{name: "IPv4-compatible IPv6 loopback", rawURL: "http://[::127.0.0.1]/doc.pdf"},
		{
			name: "a zoned IPv6 loopback", rawURL: "http://[::127.0.0.1%25eth0]/doc.pdf",
			// The 08778a0 regression, end to end: %25 is the percent-encoded
			// zone separator a caller would actually put in a JSON file_url.
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := NewSafeFetcher(false, nil, testMaxBytes)
			var dst bytes.Buffer
			_, err := f.Fetch(t.Context(), tt.rawURL, &dst)

			httpErr := requireHTTPError(t, err, http.StatusBadRequest)
			if httpErr.Public != errBlockedTarget.Error() {
				t.Errorf("Public = %q, want %q", httpErr.Public, errBlockedTarget.Error())
			}
			if !errors.Is(httpErr.Internal, errBlockedTarget) {
				t.Errorf("Internal = %v, want it to wrap errBlockedTarget", httpErr.Internal)
			}
			if dst.Len() != 0 {
				t.Errorf("dst got %d bytes from a blocked target", dst.Len())
			}
		})
	}
}

// TestFetchPostConnectRecheckCatchesAMisWiredControl provokes the
// ERROR-path arm of the belt-and-suspenders layer: a connection reached a
// peer that isBlockedAddr rejects, because Control did not stop it, and
// cancel() (called from GotConn) wins its race with http.Client.Do, so Do
// returns a non-nil error. That is exactly the "Control mis-wired in some
// future refactor" scenario the layer exists to survive, and it needs no
// production seam — this test lives in package fetch, so it can swap the
// transport on the fetcher's own client, which is precisely what a
// mis-wiring looks like from the trace's viewpoint.
//
// Its sibling, TestFetchPostConnectRecheckCatchesACompletedResponseFromABlockedPeer
// below, provokes the other arm: the SUCCESS path, where Do wins that race
// instead. The two are asserted on their branch-specific message tails
// (rather than the "post-connect check blocked" prefix they share) precisely
// so that a mutant which disables cancel() — shifting this test's real
// control flow from the error arm onto the success arm while it keeps
// passing on the shared prefix alone — is caught here rather than silently
// changing which branch this test exercises.
//
// The URL names a public IP literal so validateURL, the port check and the
// host allowlist all pass with allowPrivate=false (which is what lets the
// GotConn body run at all), while the substituted DialContext actually
// connects to the loopback httptest server regardless of the address given —
// nothing here performs a real DNS lookup or dials off-box.
func TestFetchPostConnectRecheckCatchesAMisWiredControl(t *testing.T) {
	t.Parallel()

	const secret = "content from a target that should never have been reached"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, secret)
	}))
	t.Cleanup(srv.Close)

	f := NewSafeFetcher(false, nil, testMaxBytes)
	f.client.Transport = &http.Transport{ // the mis-wiring: no Control at all
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
		},
	}

	var dst bytes.Buffer
	_, err := f.Fetch(t.Context(), "http://93.184.216.34/doc.pdf", &dst)

	httpErr := requireHTTPError(t, err, http.StatusBadRequest)
	if httpErr.Public != errBlockedTarget.Error() {
		t.Errorf("Public = %q, want %q", httpErr.Public, errBlockedTarget.Error())
	}
	if !strings.Contains(fmt.Sprint(httpErr.Internal), "post-connect check blocked") {
		t.Errorf("Internal = %v, want the post-connect layer to be the one that refused", httpErr.Internal)
	}
	if strings.Contains(fmt.Sprint(httpErr.Internal), "(dial control did not)") {
		t.Errorf("Internal = %v, matches the SUCCESS-path arm's tail; want the error-path arm (cancel() should have won its race with Do)", httpErr.Internal)
	}
	if dst.String() != "" {
		t.Errorf("dst = %q, want nothing spooled from a blocked peer", dst.String())
	}
}

// blockedPeerRoundTripper simulates the real net/http race fetch.go's own
// comment describes: net/http's transport prefers a completed round trip
// over honoring ctx.Done(), so cancel() winning the GotConn race does not
// guarantee Do returns an error. It fires GotConn (so isBlockedAddr sees the
// configured, blocked address and calls cancel(), exactly like production),
// then unconditionally returns a normal 200 response — deliberately never
// checking req.Context() — so the success-path blocked.IsValid() recheck in
// fetch.go is the ONLY thing standing between this response and the caller.
type blockedPeerRoundTripper struct {
	blockedAddr net.Addr
	body        string
}

func (rt blockedPeerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if trace := httptrace.ContextClientTrace(req.Context()); trace != nil && trace.GotConn != nil {
		trace.GotConn(httptrace.GotConnInfo{Conn: blockedPeerConn{addr: rt.blockedAddr}})
	}
	return &http.Response{
		StatusCode:    http.StatusOK,
		Status:        "200 OK",
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        make(http.Header),
		Body:          io.NopCloser(strings.NewReader(rt.body)),
		ContentLength: int64(len(rt.body)),
		Request:       req,
	}, nil
}

// blockedPeerConn supplies only the RemoteAddr GotConn reads; nothing else
// on net.Conn is ever called by fetch.go, so the rest is left nil deliberately.
type blockedPeerConn struct {
	net.Conn
	addr net.Addr
}

func (c blockedPeerConn) RemoteAddr() net.Addr { return c.addr }

// TestFetchPostConnectRecheckCatchesACompletedResponseFromABlockedPeer
// provokes the SUCCESS-path arm of the belt-and-suspenders layer (fetch.go's
// blocked.IsValid() check after Do returns (resp, nil)) — the sibling of
// TestFetchPostConnectRecheckCatchesAMisWiredControl above, and the one
// branch an earlier version of this file's package comment incorrectly
// claimed could not be provoked deterministically. blockedPeerRoundTripper
// makes the race fetch.go's comment describes non-optional: GotConn always
// fires and Do always "wins" it by returning a completed response anyway.
func TestFetchPostConnectRecheckCatchesACompletedResponseFromABlockedPeer(t *testing.T) {
	t.Parallel()

	const secret = "content from a target that should never have been reached"
	f := NewSafeFetcher(false, nil, testMaxBytes)
	f.client.Transport = blockedPeerRoundTripper{
		blockedAddr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 6310},
		body:        secret,
	}

	var dst bytes.Buffer
	_, err := f.Fetch(t.Context(), "http://93.184.216.34/doc.pdf", &dst)

	httpErr := requireHTTPError(t, err, http.StatusBadRequest)
	if httpErr.Public != errBlockedTarget.Error() {
		t.Errorf("Public = %q, want %q", httpErr.Public, errBlockedTarget.Error())
	}
	if !strings.Contains(fmt.Sprint(httpErr.Internal), "(dial control did not)") {
		t.Errorf("Internal = %v, want the success-path recheck's own tail — this arm must be the one that refused, not the error-path arm", httpErr.Internal)
	}
	if dst.String() != "" {
		t.Errorf("dst = %q, want nothing spooled from a blocked peer even though the transport completed the response", dst.String())
	}
}

// publicPeerConn reports a public RemoteAddr for a connection that is really
// loopback, so a test can drive Fetch's PRODUCTION configuration
// (allowPrivate=false) all the way to a successful download. Without it, no
// test in this package ever runs the GotConn body to completion, and a
// post-connect layer that rejected every peer — breaking every fetch in
// production — would pass the entire suite while looking exhaustively
// tested.
type publicPeerConn struct{ net.Conn }

func (publicPeerConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(93, 184, 216, 34), Port: 80}
}

func TestFetchSucceedsAgainstAPublicPeerWithPrivateTargetsBlocked(t *testing.T) {
	t.Parallel()

	const body = "%PDF-1.7 from a public peer"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	f := NewSafeFetcher(false, nil, testMaxBytes)
	f.client.Transport = &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			c, err := (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
			if err != nil {
				return nil, err
			}
			return publicPeerConn{c}, nil
		},
	}

	var dst bytes.Buffer
	n, err := f.Fetch(t.Context(), "http://93.184.216.34/doc.pdf", &dst)
	if err != nil {
		t.Fatalf("Fetch against a public peer with allowPrivate=false: %v", err)
	}
	if n != int64(len(body)) || dst.String() != body {
		t.Errorf("got %d bytes %q, want %d bytes %q", n, dst.String(), len(body), body)
	}
}

// TestFetchAllowsAHostOnTheAllowlist is the positive half of the allowlist:
// without it, an allowlist that rejects everything would pass every other
// allowlist case in this file.
func TestFetchAllowsAHostOnTheAllowlist(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok")
	}))
	t.Cleanup(srv.Close)

	host := mustHostname(t, srv.URL)
	f := NewSafeFetcher(true, []string{host}, testMaxBytes)

	var dst bytes.Buffer
	if _, err := f.Fetch(t.Context(), srv.URL+"/doc.pdf", &dst); err != nil {
		t.Fatalf("Fetch with %q allowlisted: %v", host, err)
	}
	if dst.String() != "ok" {
		t.Errorf("dst = %q, want %q", dst.String(), "ok")
	}
}

func mustHostname(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parsing test server URL %q: %v", rawURL, err)
	}
	return u.Hostname()
}

// TestNewSafeFetcherDisablesKeepAlives pins a property no black-box case can
// observe: connection reuse is keyed on scheme+host:port, so a reused
// connection can only reach a peer Control already approved — but a fresh
// dial per request means a fresh Control check per request, which is the
// defense-in-depth the comment in NewSafeFetcher claims. Asserting it here
// keeps that claim from quietly becoming false.
func TestNewSafeFetcherDisablesKeepAlives(t *testing.T) {
	t.Parallel()

	f := NewSafeFetcher(false, nil, testMaxBytes)
	transport, ok := f.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport is %T, want *http.Transport", f.client.Transport)
	}
	if !transport.DisableKeepAlives {
		t.Error("DisableKeepAlives = false, want true (a fresh dial means a fresh Control check)")
	}
	if transport.DialContext == nil {
		t.Error("DialContext is nil: the guarded dialer is not wired in at all")
	}
	// HTTP/2 must stay off by every path Go 1.25 offers to turn it on:
	// Fetch's `blocked` variable is read and written with no lock, which is
	// safe only because GotConn fires synchronously on the calling
	// goroutine. http2's transport invokes trace callbacks from its own
	// goroutine, which would make that a real race. ForceAttemptHTTP2 is the
	// pre-1.24 switch; Transport.Protocols (added in 1.24) and a manually
	// registered TLSNextProto["h2"] both also enable it and would leave this
	// property silently false if only ForceAttemptHTTP2 were checked.
	if transport.ForceAttemptHTTP2 {
		t.Error("ForceAttemptHTTP2 = true; see the data-race note on `blocked` in Fetch")
	}
	if transport.Protocols != nil {
		t.Errorf("Protocols = %v, want nil (HTTP/1 only); see the data-race note on `blocked` in Fetch", transport.Protocols)
	}
	if _, ok := transport.TLSNextProto["h2"]; ok {
		t.Error("TLSNextProto has an \"h2\" entry; see the data-race note on `blocked` in Fetch")
	}
}
