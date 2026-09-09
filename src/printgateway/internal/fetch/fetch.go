// Package fetch downloads a caller-supplied file_url, guarded against SSRF.
// The address actually dialed is checked post-DNS-resolution, immediately
// before connect(2) — not just the URL string — to defeat DNS rebinding.
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
// loopback/private/link-local block (false in production, true only for
// tests dialing httptest.Server). allowedHosts is the optional host-suffix
// allowlist (empty means any public host); maxBytes bounds the response size.
func NewSafeFetcher(allowPrivateTargets bool, allowedHosts []string, maxBytes int64) *SafeFetcher {
	dialer := &net.Dialer{Control: newDialControl(allowPrivateTargets)}
	// DisableKeepAlives forces a fresh dial, and so a fresh Control check,
	// per request — defense in depth against a reused connection bypassing it.
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
// into dst, returning the byte count. ctx bounds the whole call.
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

	// Belt-and-suspenders re-check of the actually-connected address, in
	// case Control is ever mis-wired in a future refactor. cancel() alone
	// isn't sufficient: net/http can still return (resp, nil) after GotConn
	// fires, so blocked is also checked unconditionally after Do returns.
	dialCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// blocked is read/written with no lock: safe only because this
	// Transport never negotiates HTTP/2, so GotConn always fires
	// synchronously on this goroutine. Enabling HTTP/2 here would race it.
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

	// Success-path recheck: Do can return a non-nil resp even after GotConn
	// flagged and cancelled it (see above).
	if blocked.IsValid() {
		return 0, &apperr.HTTPError{Status: http.StatusBadRequest, Public: errBlockedTarget.Error(), Internal: fmt.Errorf("post-connect check blocked %s (dial control did not)", blocked)}
	}

	if resp.StatusCode != http.StatusOK {
		return 0, &apperr.HTTPError{Status: http.StatusBadGateway, Public: "file_url returned an error", Internal: fmt.Errorf("file_url returned HTTP %d", resp.StatusCode)}
	}
	if resp.ContentLength > f.maxBytes {
		return 0, &apperr.HTTPError{Status: http.StatusRequestEntityTooLarge, Public: "file_url response is too large", Internal: fmt.Errorf("content-length %d exceeds max %d bytes", resp.ContentLength, f.maxBytes)}
	}

	// LimitReader regardless of Content-Length, since a chunked or lying
	// body can't be trusted to stop on its own. Clamp maxBytes+1 to avoid
	// int64 overflow, which would otherwise wrap to a negative limit and
	// make io.LimitReader return an immediate (blank-page) EOF.
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
