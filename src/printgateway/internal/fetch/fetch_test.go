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
// redirect policy, and dial Control are all the production ones.
// allowPrivateTargets=true is the escape hatch for reaching the loopback
// httptest server; cases proving a target is REFUSED use no server at all,
// since newDialControl rejects before connect(2).
//
// Two branches in Fetch are deliberately left uncovered:
//   - GotConn's ParseAddrPort error return: unreachable with a real TCP
//     RemoteAddr(), which always parses as ip:port.
//   - http.NewRequestWithContext's error branch: unreachable for any URL
//     that already cleared validateURL.
const testMaxBytes = 100

// requireHTTPError asserts err carries the expected status, a non-empty
// Public, and a non-nil Internal. It does not assert Public != Internal: a
// few call sites deliberately expose the same detail in both; the places
// where that distinction matters assert it individually (e.g.
// TestFetchDoesNotLeakTheRejectedHostToTheCaller).
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

// TestFetchAcceptsABodyExactlyAtTheLimit pins against an off-by-one that
// would reject a body of exactly maxBytes.
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

// TestFetchRejectsAnOversizeChunkedBody covers a chunked response, which
// declares no length, so only the LimitReader (not the header pre-check)
// stops it.
func TestFetchRejectsAnOversizeChunkedBody(t *testing.T) {
	t.Parallel()

	srv, f := newTestServer(t, testMaxBytes, func(w http.ResponseWriter, r *http.Request) {
		// Flushing before the response completes forces chunked encoding.
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
	// LimitReader bounds the overshoot to one byte past the limit.
	if int64(dst.Len()) > testMaxBytes+1 {
		t.Errorf("dst got %d bytes, want at most %d", dst.Len(), testMaxBytes+1)
	}
}

// TestFetchWithAHugeMaxBytesStillCopies is the regression guard for
// maxBytes near math.MaxInt64 overflowing maxBytes+1 negative.
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

// TestFetchRefusesRedirects pins the no-redirect rule: following one would
// let an allowed public host redirect to a private one, bypassing the host
// allowlist (though not the dial Control).
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

// TestFetchHonorsTheContextDeadline pins that a wedged upstream cannot park
// the request goroutine forever.
func TestFetchHonorsTheContextDeadline(t *testing.T) {
	t.Parallel()

	srv, f := newTestServer(t, testMaxBytes, func(w http.ResponseWriter, r *http.Request) {
		// Park until the client disconnects, not a fixed sleep: Close waits
		// for outstanding handlers and would hold up the whole test binary.
		<-r.Context().Done()
	})

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	// Run off the test goroutine with an explicit bound: if ctx propagation
	// regresses, Fetch blocks forever and an inline call would hang the
	// whole test binary instead of failing just this test.
	//
	// t.Fatal must not be called from the goroutine below (Goexit only
	// unwinds that goroutine, not the test) — results are sent back and
	// asserted from the test goroutine instead.
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

// TestFetchReportsATruncatedBody covers a response that starts fine and
// fails partway through: it must be an error, not a short success.
func TestFetchReportsATruncatedBody(t *testing.T) {
	t.Parallel()

	srv, f := newTestServer(t, testMaxBytes, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "the first half")
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler) // drops the connection without a stack trace
	})

	var dst bytes.Buffer
	n, err := f.Fetch(t.Context(), srv.URL+"/doc.pdf", &dst)
	httpErr := requireHTTPError(t, err, http.StatusBadGateway)
	if n != int64(dst.Len()) {
		t.Errorf("n = %d but dst holds %d bytes; the count must describe what was written", n, dst.Len())
	}
	if strings.Contains(httpErr.Public, srv.URL) {
		t.Errorf("Public = %q leaks the upstream URL", httpErr.Public)
	}
}

// TestFetchRejectsBadURLs covers the pre-flight URL policy: no server and no
// dial, since every case is refused before a connection is attempted.
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

// TestFetchDoesNotLeakTheRejectedHostToTheCaller: naming the allowlist back
// to an unauthenticated caller would turn the error into a network probe.
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
// newDialControl is actually wired into the transport (guard_test.go covers
// it in isolation). Each address is on an allowed port, so only Control can
// refuse it, before connect(2) — no server needs to be listening.
func TestFetchBlocksPrivateTargetsAtTheDial(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		rawURL string
	}{
		{name: "loopback", rawURL: "http://127.0.0.1/doc.pdf"},
		{
			// The other rows are IP literals; this one resolves via the OS
			// hosts file, proving Control runs on the resolved address.
			name: "a hostname that resolves to loopback", rawURL: "http://localhost/doc.pdf",
		},
		{name: "cloud metadata", rawURL: "http://169.254.169.254/latest/meta-data/"},
		{name: "RFC1918", rawURL: "https://10.1.2.3/doc.pdf"},
		{name: "IPv6 loopback", rawURL: "http://[::1]/doc.pdf"},
		{name: "IPv4-compatible IPv6 loopback", rawURL: "http://[::127.0.0.1]/doc.pdf"},
		{name: "a zoned IPv6 loopback (%25 is the percent-encoded zone separator)", rawURL: "http://[::127.0.0.1%25eth0]/doc.pdf"},
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
// ERROR-path arm of the belt-and-suspenders layer, simulating "Control
// mis-wired in a future refactor" by swapping the transport to one with no
// Control at all: GotConn flags the blocked peer and cancel() wins its race
// with http.Client.Do, so Do returns an error. Its sibling,
// TestFetchPostConnectRecheckCatchesACompletedResponseFromABlockedPeer,
// provokes the SUCCESS-path arm instead. The two assert on their
// branch-specific message tails (not just the shared "post-connect check
// blocked" prefix) so a mutant that disables cancel() is still caught.
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

// blockedPeerRoundTripper simulates net/http returning a completed response
// even though GotConn already flagged the peer and called cancel() — the
// success-path race fetch.go's own comment describes — by never checking
// req.Context() before responding.
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

// blockedPeerConn supplies only the RemoteAddr GotConn reads.
type blockedPeerConn struct {
	net.Conn
	addr net.Addr
}

func (c blockedPeerConn) RemoteAddr() net.Addr { return c.addr }

// TestFetchPostConnectRecheckCatchesACompletedResponseFromABlockedPeer
// provokes the SUCCESS-path arm of the belt-and-suspenders layer, the
// sibling of TestFetchPostConnectRecheckCatchesAMisWiredControl above.
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
		t.Errorf("Internal = %v, want the success-path recheck's own tail", httpErr.Internal)
	}
	if dst.String() != "" {
		t.Errorf("dst = %q, want nothing spooled from a blocked peer even though the transport completed the response", dst.String())
	}
}

// publicPeerConn reports a public RemoteAddr for a connection that is really
// loopback, letting a test drive Fetch's production configuration
// (allowPrivate=false) to a successful download.
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

// TestFetchAllowsAHostOnTheAllowlist is the positive half of the allowlist,
// catching an allowlist that rejects everything.
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

// TestNewSafeFetcherDisablesKeepAlives pins the defense-in-depth claimed in
// NewSafeFetcher: a fresh dial per request means a fresh Control check.
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
	// HTTP/2 must stay off by every path that could enable it (Fetch's
	// `blocked` is read/written without a lock, safe only under HTTP/1's
	// synchronous GotConn); check all three switches, not just the pre-1.24 one.
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
