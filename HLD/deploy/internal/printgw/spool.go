package printgw

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"printgateway/internal/apperr"
)

// spoolTo creates a new temp file matching namePattern (same semantics as
// os.CreateTemp), lets fill write the document into it, and returns its
// path plus a cleanup func that removes it. cleanup is never nil: the
// caller can always `defer cleanup()` unconditionally, whether or not err
// is nil.
func spoolTo(namePattern string, fill func(io.Writer) error) (path string, cleanup func(), err error) {
	tmp, err := os.CreateTemp("", namePattern)
	if err != nil {
		return "", func() {}, &apperr.HTTPError{
			Status:   http.StatusInternalServerError,
			Public:   "internal server error",
			Internal: fmt.Errorf("creating temp file: %w", err),
		}
	}
	cleanup = func() { os.Remove(tmp.Name()) }

	if fillErr := fill(tmp); fillErr != nil {
		tmp.Close()
		cleanup()
		return "", cleanup, fillErr
	}

	// The Close() error is intentionally ignored here: on ENOSPC a
	// truncated file can be handed to the printer while this reports
	// success. This is the single site for that gap — both PrintReader and
	// PrintURL spool through here — fixed in a later hardening step (P0-3).
	tmp.Close()

	return tmp.Name(), cleanup, nil
}

// sanitizeName strips characters from a caller-supplied filename that would
// otherwise be interpreted as path separators by os.CreateTemp's pattern.
func sanitizeName(name string) string {
	replacer := strings.NewReplacer("/", "_", "\\", "_", " ", "_")
	return replacer.Replace(name)
}
