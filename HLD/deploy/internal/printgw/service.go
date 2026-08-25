package printgw

import (
	"context"
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
	submitter     Submitter
	fetcher       Fetcher
	submitTimeout time.Duration // bounds the Submit call; see ports.go (P0-1)
}

func NewService(submitter Submitter, fetcher Fetcher, submitTimeout time.Duration) *Service {
	return &Service{submitter: submitter, fetcher: fetcher, submitTimeout: submitTimeout}
}

// submit bounds ctx to submitTimeout before handing job to the Submitter, so
// a wedged CUPS queue fails the request instead of hanging the handler
// goroutine forever (P0-1).
func (s *Service) submit(ctx context.Context, job SubmitJob) (SubmitResult, error) {
	ctx, cancel := context.WithTimeout(ctx, s.submitTimeout)
	defer cancel()
	return s.submitter.Submit(ctx, job)
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
		if _, err := s.fetcher.Fetch(ctx, rawURL, w); err != nil {
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
