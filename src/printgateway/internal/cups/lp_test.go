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

// TestMain builds the fakelp helper and puts it on PATH as lp/lp.exe, since
// LPSubmitter has no seam to inject a different command name.
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

// TestSubmitSuccessHoldsInvariants checks argv has no spool path, only
// PATH/HOME reach the child, and the exact spooled bytes arrive on stdin.
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

	// os/exec on Windows always appends SYSTEMROOT itself (addCriticalEnv);
	// that's not a secret leak, so allow it here without loosening the check.
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
				"environment, including VAULT_TOKEN/SECRET_STORE_PASSWORD", e)
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

// TestSubmitCopiesFlag checks Copies 0/1 omit -n and only >1 appends it.
func TestSubmitCopiesFlag(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		copies   int
		wantArgv string
	}{
		{name: "zero (unset) omits -n", copies: 0, wantArgv: "-d|ok|-t|t"},
		{name: "one omits -n", copies: 1, wantArgv: "-d|ok|-t|t"},
		{name: "two appends -n 2", copies: 2, wantArgv: "-d|ok|-t|t|-n|2"},
		{name: "five appends -n 5", copies: 5, wantArgv: "-d|ok|-t|t|-n|5"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			path := writeSpoolFile(t, []byte("doc"))
			sub := NewLPSubmitter()
			result, err := sub.Submit(context.Background(), printgw.SubmitJob{
				Printer: "ok",
				Path:    path,
				Title:   "t",
				Copies:  tc.copies,
			})
			if err != nil {
				t.Fatalf("Submit returned error: %v", err)
			}

			var argv string
			for _, line := range strings.Split(strings.TrimRight(result.Output, "\n"), "\n") {
				if v, ok := strings.CutPrefix(line, "ARGV:"); ok {
					argv = v
					break
				}
			}
			if argv != tc.wantArgv {
				t.Errorf("argv = %q, want %q", argv, tc.wantArgv)
			}
		})
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
	if httpErr.Internal == nil || !strings.Contains(httpErr.Internal.Error(), "unable to print (simulated)") {
		t.Errorf("Internal = %v, want it to contain lp's stderr text", httpErr.Internal)
	}
}

// TestSubmitTimeoutKillsTheChild checks a wedged lp process (fakelp's "hang"
// mode never exits on its own) doesn't hang the handler past ctx's deadline.
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

			// Run in a goroutine with an explicit bound rather than inline,
			// so a regression fails this test in 2s instead of wedging to
			// go test's own default timeout and orphaning the hung child.
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
				t.Fatalf("Submit did not return within 2s of a 300ms deadline — the wedged child was not killed")
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
