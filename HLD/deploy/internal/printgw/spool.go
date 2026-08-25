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

	// Sync before Close: on ENOSPC a buffered write's failure often only
	// surfaces here (or at Close), not at the earlier io.Copy — and both
	// PrintReader and PrintURL spool through this one function, so this is
	// the single site that must catch it (P0-3). Left unchecked, a
	// truncated file gets physically printed while this reports success.
	if syncErr := tmp.Sync(); syncErr != nil {
		tmp.Close()
		cleanup()
		return "", cleanup, &apperr.HTTPError{
			Status:   http.StatusInternalServerError,
			Public:   "internal server error",
			Internal: fmt.Errorf("syncing spooled file: %w", syncErr),
		}
	}

	if closeErr := tmp.Close(); closeErr != nil {
		cleanup()
		return "", cleanup, &apperr.HTTPError{
			Status:   http.StatusInternalServerError,
			Public:   "internal server error",
			Internal: fmt.Errorf("closing spooled file: %w", closeErr),
		}
	}

	return tmp.Name(), cleanup, nil
}

// sanitizeName strips characters from a caller-supplied filename that would
// otherwise be interpreted as path separators by os.CreateTemp's pattern.
func sanitizeName(name string) string {
	replacer := strings.NewReplacer("/", "_", "\\", "_", " ", "_")
	return replacer.Replace(name)
}
