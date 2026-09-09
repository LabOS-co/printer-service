// Package printgw is the print-request business logic: it owns the
// temp-file spool lifecycle and orchestrates a Fetcher (optional) and a
// Submitter to get a document to the printer.
package printgw

import (
	"context"
	"io"
)

// SubmitJob is a document ready to be handed to the underlying print system.
type SubmitJob struct {
	Printer string
	Path    string

	// Title is a sanitized, human-readable job name (lp's -t).
	Title string

	// Copies is the number of copies lp should print (lp's -n). Service.validateCopies
	// enforces [1, maxCopies] before a SubmitJob is built, independently of the HTTP edge.
	Copies int
}

// SubmitResult is returned after a successful submission.
type SubmitResult struct {
	Output string // raw stdout+stderr from the print command, verbatim
}

// Submitter hands a spooled file to the underlying print system. ctx is bounded by
// Service to config.SubmitTimeout, and cups.LPSubmitter uses exec.CommandContext so
// expiry or cancellation actually kills the child process.
type Submitter interface {
	Submit(ctx context.Context, job SubmitJob) (SubmitResult, error)
}

// Fetcher downloads a document from a caller-supplied URL, copying it into dst and
// returning the byte count. ctx is bounded by Service to config.FetchTimeout; the
// production implementation (fetch.SafeFetcher) also enforces SSRF defense before dialing.
type Fetcher interface {
	Fetch(ctx context.Context, rawURL string, dst io.Writer) (int64, error)
}

// ObjectStore is a narrow port onto S3/MinIO covering only the one operation this
// package needs, kept separate from httpapi.Presigner's own narrow interface onto the
// same client. PrintS3Key needs no SSRF-style guard the way PrintURL does: the
// production implementation (objstore.MinIO) targets a fixed, server-side-credentialed
// bucket, so the caller controls only the key, never the destination.
type ObjectStore interface {
	// Get returns key's content and its claimed size. The size is treated as
	// authoritative but still verified against the actual bytes copied (see
	// Service.getObject). Returns an error wrapping apperr.HTTPError{Status: 404}
	// when key does not exist.
	Get(ctx context.Context, key string) (io.ReadCloser, int64, error)
}
