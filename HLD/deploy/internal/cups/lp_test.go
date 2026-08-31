package cups

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"printgateway/internal/apperr"
	"printgateway/internal/printgw"
)

// TestMain builds the fakelp helper (testdata/fakelp) once for the whole
// package run and puts it on PATH under the literal name lp/lp.exe, since
// exec.CommandContext(ctx, "lp", ...) resolves that name via PATH lookup —
// there is no seam inside LPSubmitter itself to inject a different command.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "cups-fakelp-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakelp setup:", err)
		os.Exit(1)
	}
	exeName := "lp"
	if runtime.GOOS == "windows" {
		exeName = "lp.exe"
	}
	out, err := exec.Command("go", "build", "-o", filepath.Join(dir, exeName), "./testdata/fakelp").CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "building fakelp: %v\n%s", err, out)
		os.RemoveAll(dir)
		os.Exit(1)
	}

	origPath := os.Getenv("PATH")
	os.Setenv("PATH", dir+string(os.PathListSeparator)+origPath)

	code := m.Run()

	os.Setenv("PATH", origPath)
	os.RemoveAll(dir)
	os.Exit(code)
}

func writeSpoolFile(t *testing.T, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "spool")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("writing spool file: %v", err)
	}
	return path
}

// TestSubmitSuccessHoldsInvariants exercises the "ok" fakelp mode, which
// dumps argv, env, and a stdin hash back to the caller — one live process
// round trip pins three separate invariants that were previously only
// checked by hand (A4's smoke tests): no spool path in argv (the whole
// point of moving the document to stdin), only PATH/HOME reach the child
// (the F1 credential-leak fix — VAULT_TOKEN/SECRET_STORE_PASSWORD must not
// be inheritable), and the exact spooled bytes are what the child receives.
func TestSubmitSuccessHoldsInvariants(t *testing.T) {
	t.Setenv("PRINTGATEWAY_TEST_CANARY", "leak-me-if-you-can")

	content := []byte("this is the spooled document body\n")
	path := writeSpoolFile(t, content)

	sub := NewLPSubmitter()
	result, err := sub.Submit(context.Background(), printgw.SubmitJob{
		Printer: "ok",
		Path:    path,
		Title:   "job-title-42",
	})
	if err != nil {
		t.Fatalf("Submit returned error: %v", err)
	}

	got := map[string]string{}
	var envLines []string
	for _, line := range strings.Split(strings.TrimRight(result.Output, "\n"), "\n") {
		if strings.HasPrefix(line, "ENV:") {
			envLines = append(envLines, strings.TrimPrefix(line, "ENV:"))
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			t.Fatalf("unparseable fakelp output line %q (full output:\n%s)", line, result.Output)
		}
		got[key] = value
	}

	if want := "-d|ok|-t|job-title-42"; got["ARGV"] != want {
		t.Errorf("argv = %q, want %q — a leaked spool path or extra flag would show up here", got["ARGV"], want)
	}

	// LPSubmitter.Submit sets cmd.Env to exactly PATH+HOME (the F1
	// credential-leak fix — VAULT_TOKEN/SECRET_STORE_PASSWORD must not be
	// inheritable). On Windows, go's os/exec itself unconditionally appends
	// SYSTEMROOT if missing (os/exec.addCriticalEnv) — undocumented by this
	// package, not a leak (SYSTEMROOT carries no secret), and outside
	// Submit's control, so it's allowed here without loosening the check for
	// anything else, in particular the canary below.
	allowed := map[string]bool{"PATH": true, "HOME": true}
	if runtime.GOOS == "windows" {
		allowed["SYSTEMROOT"] = true
	}
	seen := map[string]bool{}
	for _, e := range envLines {
		key, _, _ := strings.Cut(e, "=")
		key = strings.ToUpper(key)
		if !allowed[key] {
			t.Errorf("unexpected env var reached the child: %q — Submit must scrub the process "+
				"environment, including VAULT_TOKEN/SECRET_STORE_PASSWORD since F1", e)
		}
		seen[key] = true
	}
	for key := range allowed {
		if !seen[key] {
			t.Errorf("expected env var %s did not reach the child", key)
		}
	}

	if wantLen := fmt.Sprintf("%d", len(content)); got["STDIN_LEN"] != wantLen {
		t.Errorf("stdin length = %s, want %s — the spooled file must be piped on stdin whole", got["STDIN_LEN"], wantLen)
	}
	sum := sha256.Sum256(content)
	if wantSum := hex.EncodeToString(sum[:]); got["STDIN_SHA256"] != wantSum {
		t.Errorf("stdin sha256 = %s, want %s — child did not receive the exact spooled bytes", got["STDIN_SHA256"], wantSum)
	}
}

func TestSubmitOpenFailure(t *testing.T) {
	t.Parallel()

	sub := NewLPSubmitter()
	_, err := sub.Submit(context.Background(), printgw.SubmitJob{
		Printer: "ok",
		Path:    filepath.Join(t.TempDir(), "does-not-exist.pdf"),
		Title:   "t",
	})

	var httpErr *apperr.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("err = %v, want *apperr.HTTPError", err)
	}
	if httpErr.Status != http.StatusInternalServerError {
		t.Errorf("Status = %d, want %d", httpErr.Status, http.StatusInternalServerError)
	}
	if httpErr.Public != "print submission failed" {
		t.Errorf("Public = %q, want a generic message — the real path must never reach the client", httpErr.Public)
	}
	if httpErr.Internal == nil {
		t.Error("Internal is nil, want the os.Open error preserved for the log")
	}
}

func TestSubmitLPFailure(t *testing.T) {
	t.Parallel()

	path := writeSpoolFile(t, []byte("doc"))
	sub := NewLPSubmitter()
	_, err := sub.Submit(context.Background(), printgw.SubmitJob{Printer: "fail", Path: path, Title: "t"})

	var httpErr *apperr.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("err = %v, want *apperr.HTTPError", err)
	}
	if httpErr.Status != http.StatusInternalServerError {
		t.Errorf("Status = %d, want 500 (a non-timeout lp failure)", httpErr.Status)
	}
	if httpErr.Public != "print submission failed" {
		t.Errorf("Public = %q, want a generic message — lp's raw stderr must never reach the client", httpErr.Public)
	}
	if httpErr.Internal == nil || !strings.Contains(httpErr.Internal.Error(), `printer="fail"`) {
		t.Errorf("Internal = %v, want it to name the printer for diagnosis", httpErr.Internal)
	}
	// lp's own stderr is the real CUPS error (e.g. client-error-not-found) an
	// operator needs to diagnose a failure — pin that it survives into
	// Internal's output=%s field, not just that Submit used CombinedOutput.
	if httpErr.Internal == nil || !strings.Contains(httpErr.Internal.Error(), "unable to print (simulated)") {
		t.Errorf("Internal = %v, want it to contain lp's stderr text", httpErr.Internal)
	}
}

// TestSubmitTimeoutKillsTheChild covers P0-1's headline claim: a wedged lp
// process no longer hangs the handler. fakelp's "hang" mode never exits on
// its own, so returning at all — and returning close to the deadline, not
// after it — is only possible if exec.CommandContext actually killed the
// child.
func TestSubmitTimeoutKillsTheChild(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		ctxErrWant string
		makeCtx    func() (context.Context, context.CancelFunc)
	}{
		{
			name:       "deadline exceeded",
			ctxErrWant: "context deadline exceeded",
			makeCtx: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 300*time.Millisecond)
			},
		},
		{
			name:       "caller cancels",
			ctxErrWant: "context canceled",
			makeCtx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				go func() {
					time.Sleep(300 * time.Millisecond)
					cancel()
				}()
				return ctx, cancel
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			path := writeSpoolFile(t, []byte("doc"))
			ctx, cancel := tc.makeCtx()
			defer cancel()

			// Submit runs in a goroutine so a genuine P0-1 regression (the
			// child not actually getting killed) fails HERE, with this
			// message, in 2s — instead of wedging until go test's own
			// 10-minute default timeout panics, which would skip TestMain's
			// cleanup and leave the fakelp "hang" child (a 24h time.Sleep)
			// orphaned on the machine holding its temp dir open.
			//
			// "returned within the bound" is a genuine proof the child died,
			// not just that Submit unblocked: CombinedOutput gives cmd a
			// non-*os.File stdout/stderr, so os/exec allocates an os.Pipe
			// plus copying goroutines, and Wait (WaitDelay unset, i.e. no
			// limit of its own) cannot return until those pipes hit EOF —
			// which requires every write end, all held only by the child, to
			// be closed. That proof breaks silently if Stdout/Stderr are ever
			// changed to *os.File or a WaitDelay is added — see lp.go before
			// doing either.
			type result struct {
				err error
			}
			done := make(chan result, 1)
			go func() {
				_, err := NewLPSubmitter().Submit(ctx, printgw.SubmitJob{Printer: "hang", Path: path, Title: "t"})
				done <- result{err}
			}()

			var err error
			select {
			case r := <-done:
				err = r.err
			case <-time.After(2 * time.Second):
				// Measured spread is ~0.3s across repeated runs against the
				// 300ms deadline used above, so 2s leaves ample margin.
				t.Fatalf("Submit did not return within 2s of a 300ms deadline — the wedged child was not killed (P0-1)")
			}

			var httpErr *apperr.HTTPError
			if !errors.As(err, &httpErr) {
				t.Fatalf("err = %v, want *apperr.HTTPError", err)
			}
			if httpErr.Status != http.StatusGatewayTimeout {
				t.Errorf("Status = %d, want %d", httpErr.Status, http.StatusGatewayTimeout)
			}
			if httpErr.Public != "print submission timed out" {
				t.Errorf("Public = %q", httpErr.Public)
			}
			if httpErr.Internal == nil || !strings.Contains(httpErr.Internal.Error(), tc.ctxErrWant) {
				t.Errorf("Internal = %v, want it to contain %q", httpErr.Internal, tc.ctxErrWant)
			}
		})
	}
}
