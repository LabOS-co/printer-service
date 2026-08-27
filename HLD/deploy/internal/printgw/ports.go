// Package printgw is the print-request business logic: it owns the
// temp-file spool lifecycle and orchestrates a Fetcher (optional) and a
// Submitter to get a document to the printer. It depends on nothing beyond
// the standard library (plus the sibling apperr package for HTTP status
// classification), which is what makes it testable without a real CUPS
// install or network access.
package printgw

import (
	"context"
	"io"
)

// SubmitJob is a document ready to be handed to the underlying print
// system: already spooled to local disk.
type SubmitJob struct {
	Printer string
	Path    string

	// Title is a sanitized, human-readable job name (lp's -t). It exists so
	// an operator can tell jobs apart in `lpstat`/logs now that Path is no
	// longer passed as a lp argument (see cups.LPSubmitter).
	Title string
}

// SubmitResult is returned after a successful submission.
type SubmitResult struct {
	Output string // raw stdout+stderr from the print command, verbatim
}

// Submitter hands a spooled file to the underlying print system. ctx is
// bounded by Service to config.SubmitTimeout (P0-1) and the production
// implementation (cups.LPSubmitter) uses exec.CommandContext, so expiry or
// caller cancellation actually kills the child process.
type Submitter interface {
	Submit(ctx context.Context, job SubmitJob) (SubmitResult, error)
}

// Fetcher downloads a document from a caller-supplied URL, copying it into
// dst and returning the byte count.
//
// ctx is bounded by Service to config.FetchTimeout, the same way Submitter's
// is bounded to config.SubmitTimeout (see Service.fetch) — a wedged or
// silent file_url host now fails the request instead of hanging it forever.
// The production implementation (fetch.SafeFetcher) also enforces SSRF
// defense (HLD §11.3) before ever using ctx to dial.
type Fetcher interface {
	Fetch(ctx context.Context, rawURL string, dst io.Writer) (int64, error)
}

// ObjectStore is a narrow port onto S3/MinIO — just the one operation this
// package actually calls, not the full cloud_storage.CloudStorageStreamingClient
// surface (no Put/Delete/Stat/presign: nothing here calls them, and printgw
// stays free of any dependency beyond what its own callers need). Presigning
// is a separate capability with a separate, unrelated consumer — see
// httpapi.Presigner — deliberately not folded into this interface: printgw
// and httpapi each depend on only the slice of objstore.MinIO's method set
// they actually use, rather than both carrying the other's half. The
// production implementation (objstore.MinIO) adapts a *fixed*,
// server-side-credentialed bucket, which is why PrintS3Key needs no
// SSRF-style guard the way PrintURL does — there is no caller-controlled
// destination, only a caller-controlled key.
type ObjectStore interface {
	// Get returns key's content and its claimed size — treated as
	// authoritative by Service.getObject in the sense that it's checked
	// against the configured max up front, but still verified against the
	// actual byte count copied, not merely trusted (see getObject). Returns
	// an error wrapping apperr.HTTPError{Status: 404} when key does not
	// exist.
	Get(ctx context.Context, key string) (io.ReadCloser, int64, error)
}
