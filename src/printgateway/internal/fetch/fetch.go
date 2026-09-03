// Package fetch downloads a caller-supplied file_url, guarded against SSRF
// (HLD §11.3): the address that will actually be dialed is checked after DNS
// resolution and immediately before connect(2), not just the URL string the
// caller supplied, which is what defeats DNS rebinding.
package fetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/netip"

	"printgateway/internal/apperr"
)

// SafeFetcher downloads a document from a caller-supplied URL, refusing any
// target that isn't a direct http(s) link on port 80/443 to a public
// address. See guard.go for the checks themselves.
type SafeFetcher struct {
	client       *http.Client
	allowPrivate bool
	allowlist    []string
	maxBytes     int64
}

// NewSafeFetcher builds a fetcher. allowPrivateTargets lifts the
// loopback/private/link-local block — false in production, true only for
// tests that must dial httptest.Server. allowedHosts is the optional
// host-suffix allowlist (empty means any public host). maxBytes bounds the
// downloaded response, independent of and in addition to whatever the
// eventual print-side limit is, since this is disk written from a
// potentially-untrusted response before printgw ever sees it.
func NewSafeFetcher(allowPrivateTargets bool, allowedHosts []string, maxBytes int64) *SafeFetcher {
	dialer := &net.Dialer{Control: newDialControl(allowPrivateTargets)}
	// DisableKeepAlives forces a fresh dial (and so a fresh Control check)
	// per request; it is defense-in-depth; connection reuse is keyed on
	// scheme+host:port, so a *reused* connection could only ever reach a
	// peer Control already validated. No ResponseHeaderTimeout: the caller's
	// ctx (see printgw.Service.fetch) already bounds the whole call, and a
	// second, independent timeout knob here would be one more value to keep
	// in sync with FetchTimeout for no benefit.
	transport := &http.Transport{
		DialContext:       dialer.DialContext,
		DisableKeepAlives: true,
	}
	return &SafeFetcher{
		client: &http.Client{
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errRedirectNotAllowed
			},
		},
		allowPrivate: allowPrivateTargets,
		allowlist:    allowedHosts,
		maxBytes:     maxBytes,
	}
}

// Fetch performs an HTTP GET against rawURL and copies the response body
// into dst, returning the byte count. ctx bounds the whole call — the
// caller (printgw.Service) applies config.FetchTimeout to it, the same way
// it applies config.SubmitTimeout around Submitter.Submit.
func (f *SafeFetcher) Fetch(ctx context.Context, rawURL string, dst io.Writer) (int64, error) {
	u, err := validateURL(rawURL)
	if err != nil {
		return 0, &apperr.HTTPError{Status: http.StatusBadRequest, Public: err.Error(), Internal: err}
	}
	port := u.Port()
	if port == "" {
		port = defaultPort(u.Scheme)
	}
	if !portAllowed(port, f.allowPrivate) {
		err := fmt.Errorf("file_url port %s is not allowed (only 80/443)", port)
		return 0, &apperr.HTTPError{Status: http.StatusBadRequest, Public: err.Error(), Internal: err}
	}
	if !hostAllowed(u.Hostname(), f.allowlist) {
		err := fmt.Errorf("file_url host %q is not in the allowed host list", u.Hostname())
		return 0, &apperr.HTTPError{Status: http.StatusForbidden, Public: "file_url host is not allowed", Internal: err}
	}

	// Belt-and-suspenders per HLD §11.3: newDialControl is the real gate
	// (it runs pre-connect, on the resolved IP), but re-check the address
	// actually connected to, so a Control mis-wired in some future refactor
	// still fails safe instead of silently fetching from wherever the
	// connection landed.
	//
	// cancel() here is NOT sufficient on its own: net/http's transport
	// prefers a response racing a context cancellation over the
	// cancellation itself (it re-checks for a completed round trip before
	// honoring ctx.Done), so a fast local target can still return
	// (resp, nil) after GotConn fired. blocked is therefore checked again,
	// unconditionally, once Do returns — see below — not only in the error
	// branch.
	dialCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// blocked is read and written with no lock: safe only because this
	// Transport never negotiates HTTP/2 (no ForceAttemptHTTP2, no Protocols
	// set — DialContext alone keeps h2 off), so GotConn always fires
	// synchronously from the same goroutine that will read blocked below.
	// Enabling HTTP/2 on this client in the future would turn this into a
	// real data race, since http2's own transport invokes trace callbacks
	// from its own goroutine.
	var blocked netip.Addr
	trace := &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			if f.allowPrivate {
				return
			}
			addrPort, err := netip.ParseAddrPort(info.Conn.RemoteAddr().String())
			if err != nil {
				return
			}
			if isBlockedAddr(addrPort.Addr()) {
				blocked = addrPort.Addr()
				cancel()
			}
		},
	}

	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(dialCtx, trace), http.MethodGet, u.String(), nil)
	if err != nil {
		return 0, &apperr.HTTPError{Status: http.StatusInternalServerError, Public: "internal server error", Internal: fmt.Errorf("building fetch request: %w", err)}
	}

	resp, err := f.client.Do(req)
	if err != nil {
		switch {
		case blocked.IsValid():
			return 0, &apperr.HTTPError{Status: http.StatusBadRequest, Public: errBlockedTarget.Error(), Internal: fmt.Errorf("post-connect check blocked %s: %w", blocked, err)}
		case errors.Is(err, errBlockedTarget):
			return 0, &apperr.HTTPError{Status: http.StatusBadRequest, Public: errBlockedTarget.Error(), Internal: err}
		case errors.Is(err, errRedirectNotAllowed):
			return 0, &apperr.HTTPError{Status: http.StatusBadRequest, Public: errRedirectNotAllowed.Error(), Internal: err}
		default:
			return 0, &apperr.HTTPError{Status: http.StatusBadGateway, Public: "failed to download file_url", Internal: fmt.Errorf("fetching file_url: %w", err)}
		}
	}
	defer resp.Body.Close()

	// Do can return a non-nil resp even though GotConn flagged the address
	// and called cancel() — see the comment above. Check here too, on the
	// success path, or this whole second layer only ever fires when the
	// request would have failed anyway, i.e. never when it matters.
	if blocked.IsValid() {
		return 0, &apperr.HTTPError{Status: http.StatusBadRequest, Public: errBlockedTarget.Error(), Internal: fmt.Errorf("post-connect check blocked %s (dial control did not)", blocked)}
	}

	if resp.StatusCode != http.StatusOK {
		return 0, &apperr.HTTPError{Status: http.StatusBadGateway, Public: "file_url returned an error", Internal: fmt.Errorf("file_url returned HTTP %d", resp.StatusCode)}
	}
	if resp.ContentLength > f.maxBytes {
		return 0, &apperr.HTTPError{Status: http.StatusRequestEntityTooLarge, Public: "file_url response is too large", Internal: fmt.Errorf("content-length %d exceeds max %d bytes", resp.ContentLength, f.maxBytes)}
	}

	// LimitReader regardless of Content-Length: a chunked or lying body
	// must not be trusted to stop on its own. Guard maxBytes+1 against
	// overflow: an operator-set FetchMaxBytes near math.MaxInt64 would
	// otherwise wrap to a negative limit, and io.LimitReader treats N<=0 as
	// "already at limit" — an immediate EOF that reads as a genuine
	// zero-byte success, spooling and printing a blank page.
	limit := f.maxBytes
	if limit > math.MaxInt64-1 {
		limit = math.MaxInt64 - 1
	}
	n, err := io.Copy(dst, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return n, &apperr.HTTPError{Status: http.StatusBadGateway, Public: "failed to download file_url", Internal: fmt.Errorf("reading file_url response: %w", err)}
	}
	if n > f.maxBytes {
		return n, &apperr.HTTPError{Status: http.StatusRequestEntityTooLarge, Public: "file_url response is too large", Internal: fmt.Errorf("body exceeded max %d bytes", f.maxBytes)}
	}

	return n, nil
}
