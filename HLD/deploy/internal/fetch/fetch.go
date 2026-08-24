// Package fetch downloads a caller-supplied file_url.
package fetch

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

// HTTPFetcher downloads a document from a caller-supplied URL.
//
// NOT SSRF-safe yet: no scheme/host/private-IP checks, no timeout, and it
// follows redirects. Do not treat this type as safe against a
// caller-supplied URL pointing at an internal address — that guard lands
// in a dedicated later hardening step.
type HTTPFetcher struct{}

func NewHTTPFetcher() *HTTPFetcher { return &HTTPFetcher{} }

// Fetch performs an HTTP GET against rawURL and copies the response body
// into dst, returning the byte count.
func (f *HTTPFetcher) Fetch(ctx context.Context, rawURL string, dst io.Writer) (int64, error) {
	resp, err := http.Get(rawURL)
	if err != nil {
		return 0, fmt.Errorf("failed downloading file_url: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("file_url returned HTTP %d", resp.StatusCode)
	}

	return io.Copy(dst, resp.Body)
}
