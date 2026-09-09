package printgw

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"path"
	"time"

	"printgateway/internal/apperr"
)

// Service owns the temp-file spool lifecycle for a print request: get the document
// onto local disk (from an already-open reader, by fetching a URL, or by downloading
// an S3/MinIO key), hand it to a Submitter, then always clean up.
type Service struct {
	submitter   Submitter
	fetcher     Fetcher
	objectStore ObjectStore // nil when S3 is not configured; see PrintS3Key
	timeouts    Timeouts
	s3MaxBytes  int64
}

// Timeouts bounds Service's operations. A named struct rather than adjacent
// time.Duration parameters, so a same-typed argument swap at a call site fails to
// compile instead of silently mispairing a timeout with the wrong operation.
type Timeouts struct {
	Submit time.Duration // bounds the Submit call; see ports.go
	Fetch  time.Duration // bounds the Fetch call; see ports.go
	S3     time.Duration // bounds the ObjectStore.Get call
}

// NewService builds a Service. objectStore may be nil: S3 is an additive capability
// behind config.S3Endpoint, and only PrintS3Key checks for nil.
func NewService(submitter Submitter, fetcher Fetcher, objectStore ObjectStore, timeouts Timeouts, s3MaxBytes int64) *Service {
	return &Service{submitter: submitter, fetcher: fetcher, objectStore: objectStore, timeouts: timeouts, s3MaxBytes: s3MaxBytes}
}

// maxCopies mirrors httpapi.maxCopies. Copies governs consumption of a physical,
// shared resource (paper/toner), so it is enforced again here rather than trusting
// the HTTP edge to be every caller's only path into this package.
const maxCopies = 100

// validateCopies enforces copies is in [1, maxCopies].
func validateCopies(copies int) error {
	if copies < 1 || copies > maxCopies {
		return &apperr.HTTPError{
			Status: http.StatusBadRequest,
			Public: fmt.Sprintf("copies must be between 1 and %d", maxCopies),
		}
	}
	return nil
}

// submit bounds ctx to Timeouts.Submit and hands job to the Submitter, re-wrapping
// any error that isn't already a classified *apperr.HTTPError so no raw internal
// detail (a temp path, a subprocess's stderr) reaches httpapi's response body.
func (s *Service) submit(ctx context.Context, job SubmitJob) (SubmitResult, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeouts.Submit)
	defer cancel()
	result, err := s.submitter.Submit(ctx, job)
	if err != nil {
		var httpErr *apperr.HTTPError
		if errors.As(err, &httpErr) {
			return result, err
		}
		return result, &apperr.HTTPError{
			Status:   http.StatusInternalServerError,
			Public:   "print submission failed",
			Internal: err,
		}
	}
	return result, nil
}

// fetch bounds ctx to Timeouts.Fetch before calling the Fetcher.
func (s *Service) fetch(ctx context.Context, rawURL string, dst io.Writer) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeouts.Fetch)
	defer cancel()
	return s.fetcher.Fetch(ctx, rawURL, dst)
}

// PrintReader spools src — an already-open uploaded file named filename — and
// submits it to printer.
func (s *Service) PrintReader(ctx context.Context, printer, filename string, src io.Reader, copies int) (SubmitResult, error) {
	if err := validateCopies(copies); err != nil {
		return SubmitResult{}, err
	}

	spoolPath, cleanup, err := spoolTo("print-upload-*-"+sanitizeName(filename), func(w io.Writer) error {
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

	return s.submit(ctx, SubmitJob{Printer: printer, Path: spoolPath, Title: sanitizeName(filename), Copies: copies})
}

// PrintURL downloads rawURL via the configured Fetcher, spools it, and submits it
// to printer.
func (s *Service) PrintURL(ctx context.Context, printer, rawURL string, copies int) (SubmitResult, error) {
	if err := validateCopies(copies); err != nil {
		return SubmitResult{}, err
	}

	spoolPath, cleanup, err := spoolTo("print-download-*.pdf", func(w io.Writer) error {
		if _, err := s.fetch(ctx, rawURL, w); err != nil {
			// fetch.SafeFetcher classifies its own failures already; pass a classified
			// error through as-is instead of collapsing it to 502.
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

	return s.submit(ctx, SubmitJob{Printer: printer, Path: spoolPath, Title: "download", Copies: copies})
}

// PrintS3Key downloads key from the configured ObjectStore, spools it, and submits
// it to printer. Unlike PrintURL, there is no SSRF surface to guard: objectStore
// targets one fixed, server-side-credentialed bucket.
func (s *Service) PrintS3Key(ctx context.Context, printer, key string, copies int) (SubmitResult, error) {
	if err := validateCopies(copies); err != nil {
		return SubmitResult{}, err
	}
	if s.objectStore == nil {
		return SubmitResult{}, &apperr.HTTPError{
			Status: http.StatusServiceUnavailable,
			Public: "object storage is not configured",
		}
	}

	spoolPath, cleanup, err := spoolTo("print-s3-*-"+sanitizeName(path.Base(key)), func(w io.Writer) error {
		return s.getObject(ctx, key, w)
	})
	defer cleanup()
	if err != nil {
		return SubmitResult{}, err
	}

	return s.submit(ctx, SubmitJob{Printer: printer, Path: spoolPath, Title: sanitizeName(key), Copies: copies})
}

// getObject bounds ctx to Timeouts.S3, fetches key, and rejects it if it exceeds
// s3MaxBytes — checked both against the store's reported size up front and against
// the actual bytes copied, since a store's reported size is a contract on the
// ObjectStore interface, not something provably true of every implementation.
func (s *Service) getObject(ctx context.Context, key string, dst io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, s.timeouts.S3)
	defer cancel()

	rc, size, err := s.objectStore.Get(ctx, key)
	if err != nil {
		// objstore.MinIO classifies its own failures already; pass a classified
		// error through as-is, same reasoning as PrintURL's fetch handling above.
		var httpErr *apperr.HTTPError
		if errors.As(err, &httpErr) {
			return err
		}
		return &apperr.HTTPError{
			Status:   http.StatusBadGateway,
			Public:   "failed to fetch object from storage",
			Internal: err,
		}
	}
	defer rc.Close()

	if size > s.s3MaxBytes {
		return &apperr.HTTPError{
			Status: http.StatusRequestEntityTooLarge,
			Public: fmt.Sprintf("object exceeds the maximum allowed size of %d bytes", s.s3MaxBytes),
		}
	}

	limit := s.s3MaxBytes
	if limit > math.MaxInt64-1 {
		limit = math.MaxInt64 - 1
	}
	n, err := io.Copy(dst, io.LimitReader(rc, limit+1))
	if err != nil {
		if ctx.Err() != nil {
			return &apperr.HTTPError{
				Status:   http.StatusGatewayTimeout,
				Public:   "print submission timed out",
				Internal: fmt.Errorf("object %q: fetch timed out/cancelled: ctxErr=%v err=%w", key, ctx.Err(), err),
			}
		}
		return &apperr.HTTPError{
			Status:   http.StatusBadGateway,
			Public:   "failed to fetch object from storage",
			Internal: fmt.Errorf("copying object %q from storage: %w", key, err),
		}
	}
	if n > s.s3MaxBytes {
		return &apperr.HTTPError{
			Status: http.StatusRequestEntityTooLarge,
			Public: fmt.Sprintf("object exceeds the maximum allowed size of %d bytes", s.s3MaxBytes),
		}
	}
	if size >= 0 && n != size {
		return &apperr.HTTPError{
			Status:   http.StatusBadGateway,
			Public:   "failed to fetch object from storage",
			Internal: fmt.Errorf("object %q: storage reported size %d but delivered %d bytes", key, size, n),
		}
	}
	return nil
}
