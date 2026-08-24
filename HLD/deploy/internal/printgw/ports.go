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
}

// SubmitResult is returned after a successful submission.
type SubmitResult struct {
	Output string // raw stdout+stderr from the print command, verbatim
}

// Submitter hands a spooled file to the underlying print system.
//
// ctx is accepted for interface stability but not yet wired to cancellation
// by the production implementation — that lands in a later hardening step
// together with the exec.CommandContext timeout.
type Submitter interface {
	Submit(ctx context.Context, job SubmitJob) (SubmitResult, error)
}

// Fetcher downloads a document from a caller-supplied URL, copying it into
// dst and returning the byte count.
//
// ctx is accepted for interface stability but not yet wired to the
// production implementation's request — SSRF protection and timeouts land
// in a later hardening step.
type Fetcher interface {
	Fetch(ctx context.Context, rawURL string, dst io.Writer) (int64, error)
}
