package printgw

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"printgateway/internal/apperr"
)

// spoolTo creates a new temp file matching namePattern (os.CreateTemp semantics),
// lets fill write the document into it, and returns its path plus a cleanup func
// that removes it. cleanup is never nil, so callers can always `defer cleanup()`
// unconditionally.
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

	// Sync before Close: on ENOSPC a buffered write can fail silently at Close
	// instead of at the earlier io.Copy, which would otherwise print a truncated file.
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
