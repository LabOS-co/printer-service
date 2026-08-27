package printgw

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"printgateway/internal/apperr"
)

// Service owns the temp-file spool lifecycle for a print request: get the
// document onto local disk (from an already-open reader or by fetching a
// URL), hand it to a Submitter, then always clean up.
type Service struct {
	submitter Submitter
	fetcher   Fetcher
	timeouts  Timeouts
}

// Timeouts bounds Service's two operations. A named struct rather than two
// adjacent time.Duration parameters on NewService: config.go's own
// env-var-override table exists specifically because two same-typed values
// next to each other is a swap that compiles, vets clean, and is invisible
// in review — the same hazard applies here, one call site away.
type Timeouts struct {
	Submit time.Duration // bounds the Submit call; see ports.go (P0-1)
	Fetch  time.Duration // bounds the Fetch call; see ports.go (P0-4)
}

func NewService(submitter Submitter, fetcher Fetcher, timeouts Timeouts) *Service {
	return &Service{submitter: submitter, fetcher: fetcher, timeouts: timeouts}
}

// submit bounds ctx to submitTimeout before handing job to the Submitter, so
// a wedged CUPS queue fails the request instead of hanging the handler
// goroutine forever (P0-1).
func (s *Service) submit(ctx context.Context, job SubmitJob) (SubmitResult, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeouts.Submit)
	defer cancel()
	return s.submitter.Submit(ctx, job)
}

// fetch bounds ctx to timeouts.Fetch before calling the Fetcher, the same
// pattern as submit above: a file_url host that accepts the connection and
// then never answers must not hang the handler goroutine forever.
func (s *Service) fetch(ctx context.Context, rawURL string, dst io.Writer) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeouts.Fetch)
	defer cancel()
	return s.fetcher.Fetch(ctx, rawURL, dst)
}

// PrintReader spools src — an already-open uploaded file named filename,
// used only to build a readable temp-file pattern — and submits it to
// printer.
func (s *Service) PrintReader(ctx context.Context, printer, filename string, src io.Reader) (SubmitResult, error) {
	path, cleanup, err := spoolTo("print-upload-*-"+sanitizeName(filename), func(w io.Writer) error {
		if _, err := io.Copy(w, src); err != nil {
			return &apperr.HTTPError{
				Status:   http.StatusInternalServerError,
				Public:   "internal server error",
				Internal: fmt.Errorf("spooling uploaded document: %w", err),
			}
		}
		return nil
	})
	defer cleanup()
	if err != nil {
		return SubmitResult{}, err
	}

	return s.submit(ctx, SubmitJob{Printer: printer, Path: path, Title: sanitizeName(filename)})
}

// PrintURL downloads rawURL via the configured Fetcher, spools it, and
// submits it to printer.
func (s *Service) PrintURL(ctx context.Context, printer, rawURL string) (SubmitResult, error) {
	path, cleanup, err := spoolTo("print-download-*.pdf", func(w io.Writer) error {
		if _, err := s.fetch(ctx, rawURL, w); err != nil {
			// fetch.SafeFetcher classifies its own failures (bad file_url,
			// blocked SSRF target, oversize response, ...) into the right
			// *apperr.HTTPError status already — pass those through as-is
			// rather than force-collapsing everything to 502, which would
			// turn a caller mistake (e.g. a blocked address) into a
			// misleading "gateway" failure.
			var httpErr *apperr.HTTPError
			if errors.As(err, &httpErr) {
				return err
			}
			return &apperr.HTTPError{
				Status:   http.StatusBadGateway,
				Public:   "failed to download file_url",
				Internal: err,
			}
		}
		return nil
	})
	defer cleanup()
	if err != nil {
		return SubmitResult{}, err
	}

	return s.submit(ctx, SubmitJob{Printer: printer, Path: path, Title: "download"})
}
