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

// Service owns the temp-file spool lifecycle for a print request: get the
// document onto local disk (from an already-open reader, by fetching a URL,
// or by downloading an S3/MinIO key), hand it to a Submitter, then always
// clean up.
type Service struct {
	submitter   Submitter
	fetcher     Fetcher
	objectStore ObjectStore // nil when S3 is not configured; see PrintS3Key
	timeouts    Timeouts
	s3MaxBytes  int64
}

// Timeouts bounds Service's operations. A named struct rather than adjacent
// time.Duration parameters on NewService: config.go's own env-var-override
// table exists specifically because two same-typed values next to each
// other is a swap that compiles, vets clean, and is invisible in review —
// the same hazard applies here, one call site away.
type Timeouts struct {
	Submit time.Duration // bounds the Submit call; see ports.go (P0-1)
	Fetch  time.Duration // bounds the Fetch call; see ports.go (P0-4)
	S3     time.Duration // bounds the ObjectStore.Get call
}

// objectStore may be nil: S3 is an additive capability behind
// config.S3Endpoint, not a required one (multipart upload is always the
// primary intake path — see the HLD's own constraint), so a Service built
// without S3 configured must still work for everything else. PrintS3Key is
// the only method that checks for nil.
func NewService(submitter Submitter, fetcher Fetcher, objectStore ObjectStore, timeouts Timeouts, s3MaxBytes int64) *Service {
	return &Service{submitter: submitter, fetcher: fetcher, objectStore: objectStore, timeouts: timeouts, s3MaxBytes: s3MaxBytes}
}

// submit bounds ctx to submitTimeout before handing job to the Submitter, so
// a wedged CUPS queue fails the request instead of hanging the handler
// goroutine forever (P0-1).
//
// A Submitter error that isn't already an *apperr.HTTPError is re-wrapped
// into a generic one here, the same way fetch/getObject below re-wrap an
// unclassified Fetcher/ObjectStore error — this is the one call site that
// didn't, before this. The concrete implementation (cups.LPSubmitter)
// always classifies its own errors, so this was unreachable through it, but
// an unclassified error's raw text (a temp path, a subprocess's stderr) is
// never safe to hand upward as-is: httpapi.fail passes Err.Error() straight
// into error_handler's response body for anything it doesn't recognize as
// an *apperr.HTTPError, and this package — not the HTTP layer two calls
// away — is where a Submitter's own failure detail is known well enough to
// classify correctly.
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

	return s.submit(ctx, SubmitJob{Printer: printer, Path: spoolPath, Title: sanitizeName(filename)})
}

// PrintURL downloads rawURL via the configured Fetcher, spools it, and
// submits it to printer.
func (s *Service) PrintURL(ctx context.Context, printer, rawURL string) (SubmitResult, error) {
	spoolPath, cleanup, err := spoolTo("print-download-*.pdf", func(w io.Writer) error {
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

	return s.submit(ctx, SubmitJob{Printer: printer, Path: spoolPath, Title: "download"})
}

// PrintS3Key downloads key from the configured ObjectStore, spools it, and
// submits it to printer. Unlike PrintURL, there is no SSRF surface to guard:
// objectStore talks to one fixed, server-side-credentialed bucket — the
// caller controls only the key, never the destination.
func (s *Service) PrintS3Key(ctx context.Context, printer, key string) (SubmitResult, error) {
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

	return s.submit(ctx, SubmitJob{Printer: printer, Path: spoolPath, Title: sanitizeName(key)})
}

// getObject bounds ctx to timeouts.S3 before calling ObjectStore.Get, the
// same pattern as fetch/submit above, then rejects an object bigger than
// s3MaxBytes before copying a single byte — unlike a file_url response's
// Content-Length (at best a claim until the read catches a lie), Get's size
// is the store's own authoritative metadata, so this check alone is
// sufficient; no LimitReader belt-and-suspenders needed on top of it.
func (s *Service) getObject(ctx context.Context, key string, dst io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, s.timeouts.S3)
	defer cancel()

	rc, size, err := s.objectStore.Get(ctx, key)
	if err != nil {
		// objstore.MinIO already classifies its own failures (missing key ->
		// 404, everything else -> 502) into the right *apperr.HTTPError —
		// pass those through as-is, same reasoning as PrintURL's fetch error
		// handling above.
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

	// size is documented (ports.go) as the store's own authoritative
	// metadata, unlike a file_url response's spoofable Content-Length — but
	// that is a contract on the ObjectStore interface, not something this
	// code can verify by itself, and ObjectStore has exactly one production
	// implementation today. LimitReader plus the copied-vs-reported check
	// below make the contract enforced rather than merely assumed: an
	// implementation (present or future) that under-reports size, reports
	// 0/-1 for "unknown", or whose stream ends early can no longer spool a
	// truncated file and have it printed while this reports success — the
	// exact P0-3 failure class, in a new guise.
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
