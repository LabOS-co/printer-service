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

// capturePath records the real spool file handed to fill. spoolTo passes the
// *os.File itself as the io.Writer, so a test can learn the path even on the
// paths where spoolTo returns "" — which is exactly the case that matters,
// since that is when the file must have been removed.
// It returns an error rather than calling t.Fatalf, and every caller inside a
// fill closure returns that error: t.Fatalf runs runtime.Goexit, which would
// unwind straight out of spoolTo — a function with no defers — leaving the
// temp file open and unremoved (on Windows the handle is held until the test
// binary exits). A failing assertion should not also leak.
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

	// The file must still be there when spoolTo returns: its whole purpose is
	// to hand a readable path to the Submitter.
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("spooled file is not readable after spoolTo: %v", readErr)
	}
	if string(got) != content {
		t.Errorf("spooled content = %q, want %q", got, content)
	}

	// The name honors os.CreateTemp's pattern semantics — the "*" is where the
	// random part goes, so the caller-supplied suffix survives and a job stays
	// identifiable.
	if base := filepath.Base(path); !strings.HasPrefix(base, "spool-success-") || !strings.HasSuffix(base, ".pdf") {
		t.Errorf("spooled file name = %q, want the pattern's prefix and suffix preserved", base)
	}

	cleanup()
	assertNotExist(t, path, "after cleanup")

	// cleanup must tolerate a second call: PrintReader/PrintURL/PrintS3Key all
	// `defer cleanup()` and some paths call it eagerly too, so it runs twice on
	// every failure.
	cleanup()
	assertNotExist(t, path, "after a second cleanup")
}

// TestSpoolToRemovesTheFileWhenFillFails is the P0-3-adjacent invariant: a
// document that could not be written completely must never be left on disk
// where a later change might hand it to lp.
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
		// Write something first: the interesting case is a PARTIALLY written
		// file, not an empty one.
		if _, wErr := io.WriteString(w, "half a document"); wErr != nil {
			return fmt.Errorf("writing to the spool file failed: %w", wErr)
		}
		return fillErr
	})
	defer cleanup()

	// fill's error is returned verbatim, not re-wrapped: the callers rely on
	// this to pass an already-classified *apperr.HTTPError (a blocked SSRF
	// target, a 404 object key) through with its own status intact.
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

// TestSpoolToRejectsAPatternWithASeparator covers the os.CreateTemp failure
// branch, and documents why sanitizeName exists: os.CreateTemp refuses any
// pattern containing a path separator (os.IsPathSeparator accepts both "/"
// and "\" on Windows), so an unsanitized caller-supplied filename reaching
// the pattern is a 500, not a traversal.
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
	// The pattern reaches Internal (it may embed a caller-supplied filename)
	// and must not reach the client.
	if httpErr.Internal == nil {
		t.Error("Internal is nil; the CreateTemp failure detail is what makes this diagnosable")
	}
	if strings.Contains(httpErr.Public, "escape") {
		t.Errorf("public message %q leaks the pattern", httpErr.Public)
	}
}

// TestSpoolToFailsWhenTheSpooledFileCannotBeSynced covers the Sync-error
// branch, which is the whole reason spoolTo checks Sync at all (P0-3): on
// ENOSPC a buffered write's failure often surfaces only here, never at the
// earlier io.Copy, and an unchecked Sync means a truncated document gets
// physically printed while the API answers 200 {"status":"submitted"}.
//
// ENOSPC cannot be provoked portably from a test. What CAN be provoked is the
// same failure mode through a different cause: fill receives the real
// *os.File, so a fill that closes it leaves Sync to fail with EBADF. The
// cause differs; the branch, the status, and the cleanup obligation under
// test are identical.
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
		// Leave the descriptor unusable, so the Sync that follows fails.
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
	// The half-written file is removed rather than left where a later change
	// might pick it up.
	assertNotExist(t, innerPath, "after sync failed")
}

// requireSpoolHTTPError is the local form of the classification assertion.
// It differs from service_test.go's requireHTTPError by also pinning the
// generic public message: every failure spoolTo produces is a server-side
// fault whose detail (a filesystem path, a caller-supplied pattern) must stay
// internal, so there is exactly one right answer here. The service-level
// helper cannot assert that, because its callers legitimately surface a
// caller-facing message that varies per case.
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

// spoolTo's tmp.Close() error branch (the one AFTER a successful Sync) is the
// one statement pair in this package left uncovered. It is NOT unreachable —
// an earlier draft of this comment claimed that, and it is wrong in a way
// that could get a correct branch deleted. close(2) can return EIO or ENOSPC
// after a successful fsync on NFS and CIFS, where delayed write-back errors
// are reported at close; that is live whenever TMPDIR is a network mount,
// which is not exotic for a print spooler.
//
// What is true is that it is not provokable PORTABLY from a test: every
// locally-producible state that makes Close fail (a closed or invalid
// descriptor) also makes the preceding Sync fail, so control lands in the
// Sync branch instead — verified, that is exactly where the
// fill-closes-the-file route above goes. Closing the gap would need a seam in
// production code (an overridable syncFile/closeFile func var), which is a
// change to make deliberately, not as a side effect of chasing a coverage
// number.

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
		// Dot segments are left alone on purpose: with every separator gone
		// they cannot form a traversal, and os.CreateTemp treats them as
		// ordinary name characters.
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

// TestSanitizeNameOutputIsAlwaysAUsableTempPattern is the property that
// matters, asserted against the real os.CreateTemp rather than by inspecting
// the string: whatever a caller names their upload, the sanitized form must be
// a legal pattern. sanitizeName replaces both "/" and "\" specifically because
// os.IsPathSeparator accepts both on Windows.
//
// Creation happens in t.TempDir(), not os.TempDir(): os.CreateTemp's pattern
// validation is directory-independent, so the property under test is
// unchanged, and the testing package then removes everything afterwards. That
// matters more than tidiness here — see the Windows note below, where one of
// these names produces a file this test could not reliably remove on its own.
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
			// Same shape PrintReader builds.
			pattern := "print-upload-*-" + sanitizeName(name)
			f, err := os.CreateTemp(dir, pattern)
			if err != nil {
				t.Fatalf("os.CreateTemp rejected the sanitized pattern for %q: %v", name, err)
			}
			defer f.Close()

			// The file really landed in the directory given, not anywhere the
			// original name pointed at.
			if got := filepath.Dir(f.Name()); got != filepath.Clean(dir) {
				t.Errorf("spool file for %q landed in %q, want %q", name, got, dir)
			}
		})
	}
}

// Windows note, observed while writing the test above and worth recording
// because it is invisible on the deployment platform: sanitizeName does NOT
// replace ":", so on Windows `C:\Program Files\evil.pdf` sanitizes to
// "C:_Program_Files_evil.pdf" and os.CreateTemp then creates an NTFS
// *alternate data stream* — a base file "print-upload-<rand>-C" carrying a
// stream named "_Program_Files_evil.pdf". os.Remove on the full name deletes
// only the stream, so spoolTo's cleanup would leave a zero-byte base file
// behind on every such request.
//
// Deliberately NOT treated as a defect to fix here: printgateway runs where
// CUPS runs (Linux/WSL — see CLAUDE.md), and on Linux ":" is an ordinary
// filename character with none of this behavior. Recorded as a follow-up
// rather than papered over, since it would become real if this service ever
// spooled on Windows.
