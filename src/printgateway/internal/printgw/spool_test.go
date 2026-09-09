package printgw

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"printgateway/internal/apperr"
)

// capturePath records the real spool file handed to fill, letting a test learn
// the path even when spoolTo returns "". Returns an error rather than calling
// t.Fatalf: Fatalf's runtime.Goexit would unwind out of spoolTo (which has no
// defers) leaving the temp file open on Windows.
func capturePath(t *testing.T, w io.Writer) (string, error) {
	t.Helper()
	f, ok := w.(*os.File)
	if !ok {
		return "", fmt.Errorf("spoolTo passed fill a %T, want *os.File", w)
	}
	return f.Name(), nil
}

func assertNotExist(t *testing.T, path, what string) {
	t.Helper()
	if path == "" {
		t.Fatalf("%s: no path was captured, cannot assert removal", what)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s: %q still exists (stat err = %v), want it removed", what, path, err)
	}
}

func TestSpoolToSuccess(t *testing.T) {
	t.Parallel()

	const content = "%PDF-1.4 spooled bytes"
	var innerPath string

	path, cleanup, err := spoolTo("spool-success-*.pdf", func(w io.Writer) error {
		var capErr error
		if innerPath, capErr = capturePath(t, w); capErr != nil {
			return capErr
		}
		_, writeErr := io.WriteString(w, content)
		return writeErr
	})
	defer cleanup()

	if err != nil {
		t.Fatalf("spoolTo returned an unexpected error: %v", err)
	}
	if cleanup == nil {
		t.Fatal("cleanup is nil; callers defer it unconditionally")
	}
	if path != innerPath {
		t.Errorf("returned path %q differs from the file fill was given (%q)", path, innerPath)
	}

	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("spooled file is not readable after spoolTo: %v", readErr)
	}
	if string(got) != content {
		t.Errorf("spooled content = %q, want %q", got, content)
	}

	if base := filepath.Base(path); !strings.HasPrefix(base, "spool-success-") || !strings.HasSuffix(base, ".pdf") {
		t.Errorf("spooled file name = %q, want the pattern's prefix and suffix preserved", base)
	}

	cleanup()
	assertNotExist(t, path, "after cleanup")

	// cleanup must tolerate a second call: it runs twice on every failure path.
	cleanup()
	assertNotExist(t, path, "after a second cleanup")
}

// TestSpoolToRemovesTheFileWhenFillFails: a document that could not be written
// completely must never be left on disk.
func TestSpoolToRemovesTheFileWhenFillFails(t *testing.T) {
	t.Parallel()

	fillErr := &apperr.HTTPError{
		Status:   http.StatusBadGateway,
		Public:   "failed to download file_url",
		Internal: errors.New("connection reset"),
	}
	var innerPath string

	path, cleanup, err := spoolTo("spool-fill-fails-*.pdf", func(w io.Writer) error {
		var capErr error
		if innerPath, capErr = capturePath(t, w); capErr != nil {
			return capErr
		}
		if _, wErr := io.WriteString(w, "half a document"); wErr != nil {
			return fmt.Errorf("writing to the spool file failed: %w", wErr)
		}
		return fillErr
	})
	defer cleanup()

	// fill's error is returned verbatim, not re-wrapped: callers rely on this to
	// pass an already-classified *apperr.HTTPError through with its status intact.
	if err != fillErr { //nolint:errorlint // identity is the property under test
		t.Errorf("spoolTo returned %#v, want fill's own error value", err)
	}
	if path != "" {
		t.Errorf("path = %q on the failure path, want empty", path)
	}
	if cleanup == nil {
		t.Fatal("cleanup is nil on the failure path; the caller still defers it")
	}
	assertNotExist(t, innerPath, "after fill failed")
}

// TestSpoolToRejectsAPatternWithASeparator: os.CreateTemp refuses a pattern
// containing a path separator, so an unsanitized filename reaching it is a 500,
// not a traversal.
func TestSpoolToRejectsAPatternWithASeparator(t *testing.T) {
	t.Parallel()

	filled := false
	path, cleanup, err := spoolTo("spool-"+string(os.PathSeparator)+"escape-*.pdf", func(io.Writer) error {
		filled = true
		return nil
	})
	defer cleanup()

	if filled {
		t.Error("fill was called even though no temp file could be created")
	}
	if path != "" {
		t.Errorf("path = %q, want empty", path)
	}
	if cleanup == nil {
		t.Fatal("cleanup is nil; callers defer it unconditionally, including on this path")
	}

	var httpErr *apperr.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error is %T (%v), want *apperr.HTTPError", err, err)
	}
	if httpErr.Status != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", httpErr.Status, http.StatusInternalServerError)
	}
	if httpErr.Public != "internal server error" {
		t.Errorf("public message = %q, want a generic one", httpErr.Public)
	}
	if httpErr.Internal == nil {
		t.Error("Internal is nil; the CreateTemp failure detail is what makes this diagnosable")
	}
	if strings.Contains(httpErr.Public, "escape") {
		t.Errorf("public message %q leaks the pattern", httpErr.Public)
	}
}

// TestSpoolToFailsWhenTheSpooledFileCannotBeSynced covers the Sync-error branch
// (see spoolTo's comment on why Sync is checked). ENOSPC can't be provoked
// portably, so this triggers the same branch via a different cause: fill closes
// the real *os.File first, leaving Sync to fail with EBADF.
func TestSpoolToFailsWhenTheSpooledFileCannotBeSynced(t *testing.T) {
	t.Parallel()

	var innerPath string
	path, cleanup, err := spoolTo("spool-sync-fails-*.pdf", func(w io.Writer) error {
		f, capErr := capturePath(t, w)
		if capErr != nil {
			return capErr
		}
		innerPath = f
		file := w.(*os.File)
		if _, wErr := io.WriteString(file, "a document"); wErr != nil {
			return fmt.Errorf("writing to the spool file failed: %w", wErr)
		}
		if cErr := file.Close(); cErr != nil {
			return fmt.Errorf("closing the spool file failed: %w", cErr)
		}
		return nil
	})
	defer cleanup()

	if path != "" {
		t.Errorf("path = %q, want empty: a file that could not be synced must never be handed to lp", path)
	}
	if cleanup == nil {
		t.Fatal("cleanup is nil on the sync-failure path")
	}

	httpErr := requireSpoolHTTPError(t, err, http.StatusInternalServerError)
	if !strings.Contains(httpErr.Internal.Error(), "syncing spooled file") {
		t.Errorf("Internal = %v, want it to name the sync failure", httpErr.Internal)
	}
	assertNotExist(t, innerPath, "after sync failed")
}

// requireSpoolHTTPError also pins the generic public message: every failure
// spoolTo produces is a server-side fault, unlike service_test.go's
// requireHTTPError whose callers legitimately vary their public message.
func requireSpoolHTTPError(t *testing.T, err error, wantStatus int) *apperr.HTTPError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error with status %d, got nil", wantStatus)
	}
	var httpErr *apperr.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error is %T (%v), want *apperr.HTTPError", err, err)
	}
	if httpErr.Status != wantStatus {
		t.Fatalf("status = %d (%v), want %d", httpErr.Status, err, wantStatus)
	}
	if httpErr.Public != "internal server error" {
		t.Errorf("public message = %q, want a generic one", httpErr.Public)
	}
	if httpErr.Internal == nil {
		t.Fatal("Internal is nil; the failure detail is what makes this diagnosable")
	}
	return httpErr
}

// spoolTo's tmp.Close() error branch (after a successful Sync) is left uncovered
// deliberately: it is reachable in production (close(2) can report a delayed
// write-back error via EIO/ENOSPC on NFS/CIFS after a successful fsync), but not
// provokable portably from a test — every locally-producible Close failure also
// fails the preceding Sync. Closing the gap needs a production seam (an
// overridable syncFile/closeFile func var), a deliberate change, not a side effect
// of chasing coverage.

func TestSanitizeName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"already clean", "invoice.pdf", "invoice.pdf"},
		{"empty", "", ""},
		{"forward slash", "a/b.pdf", "a_b.pdf"},
		{"backslash", `a\b.pdf`, "a_b.pdf"},
		{"space", "my invoice.pdf", "my_invoice.pdf"},
		{"mixed separators and spaces", `dir/sub\my file.pdf`, "dir_sub_my_file.pdf"},
		{"consecutive", "a//  b.pdf", "a____b.pdf"},
		{"leading and trailing", " /x/ ", "__x__"},
		{"absolute posix path", "/etc/passwd", "_etc_passwd"},
		{"absolute windows path", `C:\Windows\System32\x`, "C:_Windows_System32_x"},
		// Dot segments are left alone: with every separator gone they cannot form
		// a traversal.
		{"dot segments are harmless once separators are gone", "../../etc/passwd", ".._.._etc_passwd"},
		{"non-ASCII is preserved", "חשבון.pdf", "חשבון.pdf"},
		{"tab and newline are not touched", "a\tb\nc", "a\tb\nc"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := sanitizeName(tt.in); got != tt.want {
				t.Errorf("sanitizeName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestSanitizeNameOutputIsAlwaysAUsableTempPattern asserts against the real
// os.CreateTemp, in t.TempDir() rather than os.TempDir() so the testing package
// cleans up afterwards — see the Windows note below, where one of these names
// produces a file this test could not reliably remove on its own.
func TestSanitizeNameOutputIsAlwaysAUsableTempPattern(t *testing.T) {
	t.Parallel()

	hostile := []string{
		"/etc/passwd",
		`..\..\..\Windows\System32\config\SAM`,
		"../../../../root/.ssh/id_rsa",
		`C:\Program Files\evil.pdf`,
		"with space/and slash",
		"",
		"חשבון/2026.pdf",
	}

	for i, name := range hostile {
		t.Run(fmt.Sprintf("%d", i), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			pattern := "print-upload-*-" + sanitizeName(name) // same shape PrintReader builds
			f, err := os.CreateTemp(dir, pattern)
			if err != nil {
				t.Fatalf("os.CreateTemp rejected the sanitized pattern for %q: %v", name, err)
			}
			defer f.Close()

			if got := filepath.Dir(f.Name()); got != filepath.Clean(dir) {
				t.Errorf("spool file for %q landed in %q, want %q", name, got, dir)
			}
		})
	}
}

// Windows note: sanitizeName does not replace ":", so on Windows
// `C:\Program Files\evil.pdf` sanitizes to "C:_Program_Files_evil.pdf" and
// os.CreateTemp creates an NTFS alternate data stream — spoolTo's cleanup then
// leaves a zero-byte base file behind. Not fixed here: printgateway runs on
// Linux/WSL (see CLAUDE.md), where ":" is an ordinary filename character; this
// would only become real if the service ever spooled on Windows.
