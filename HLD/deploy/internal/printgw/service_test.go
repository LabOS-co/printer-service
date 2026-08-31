package printgw

import (
	"context"
	"errors"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"printgateway/internal/apperr"
)

// testTimeouts are long enough never to fire incidentally — the cases that
// are actually about a deadline set their own short value, so a slow machine
// can never turn a behavior test into a timeout test.
//
// The three are deliberately far APART, not merely non-zero. With
// assertDeadlineWithin's CI slack, three equal values make a Fetch/S3/Submit
// mispairing in service.go invisible (confirmed by mutation: getObject using
// timeouts.Fetch, and fetch using timeouts.S3, both survived a suite where
// these were all 30s) — which is precisely the swap the named Timeouts struct
// was introduced to prevent.
var testTimeouts = Timeouts{
	Submit: 30 * time.Second,
	Fetch:  10 * time.Minute,
	S3:     30 * time.Minute,
}

const testS3Max = 1 << 20

// uniqueName derives a spool-file name component unique to the calling test,
// so assertNoSpoolFilesMatching can glob for this test's files alone. The
// package's temp directory is shared with every other parallel subtest, and
// PrintURL's own spool pattern is a fixed string, so this is the handle that
// makes leak assertions safe to parallelize.
func uniqueName(t *testing.T) string {
	t.Helper()
	name := sanitizeName(strings.ReplaceAll(t.Name(), "/", "-")) + ".pdf"
	// A glob metacharacter surviving into the name would make
	// assertNoSpoolFilesMatching either match nothing (silently vacuous
	// forever) or fail with ErrBadPattern. Subtest names come from table rows,
	// so that is one rename away; fail loudly instead of quietly.
	if strings.ContainsAny(name, `*?[]`) {
		t.Fatalf("test name %q yields a glob-unsafe spool name %q; rename the subtest", t.Name(), name)
	}
	return name
}

// assertNoSpoolFilesMatching fails if any file matching pattern survives in
// the temp directory. pattern must be unique to the calling test — see
// uniqueName — which is also what makes watchSpoolFiles' up-front sweep safe.
func assertNoSpoolFilesMatching(t *testing.T, pattern string) {
	t.Helper()
	matches := spoolFilesMatching(t, pattern)
	if len(matches) != 0 {
		t.Errorf("spool files left behind: %v", matches)
	}
}

func spoolFilesMatching(t *testing.T, pattern string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), pattern))
	if err != nil {
		// Only ErrBadPattern is possible here; uniqueName's guard should have
		// prevented it, so reaching this means that guard has a hole.
		t.Fatalf("globbing %q: %v", pattern, err)
	}
	return matches
}

// watchSpoolFiles makes a leak assertion survive a previously crashed run.
//
// The temp directory is shared and persistent, so a run that panicked or hit
// the go test timeout leaves spool files behind — and because the name is
// unique per test, every LATER run of that test then fails on files it did
// not create. That was not hypothetical: mutation-testing this package left
// exactly such orphans (a mutant that removed submit's timeout made a
// blocking test hang to the test binary's deadline), and the next clean run
// reported four spurious leaks.
//
// Anything already matching is stale by definition — only one instance of a
// given test runs at a time — so it is swept first, and the assertion is
// registered via t.Cleanup so it still runs if the body returns early.
func watchSpoolFiles(t *testing.T, pattern string) {
	t.Helper()
	for _, stale := range spoolFilesMatching(t, pattern) {
		if err := os.Remove(stale); err != nil {
			t.Fatalf("could not clear the stale spool file %q from an earlier run: %v", stale, err)
		}
	}
	t.Cleanup(func() { assertNoSpoolFilesMatching(t, pattern) })
}

// assertDeadlineWithin checks that a deadline observed by a fake was derived
// from want, without depending on how long the test took to get there. The
// deadline is set to time.Now()+want inside Service, at some point after
// start, so the gap can only be >= want; the generous upper bound keeps a
// loaded CI machine from failing this.
func assertDeadlineWithin(t *testing.T, label string, start time.Time, got time.Time, want time.Duration) {
	t.Helper()
	gap := got.Sub(start)
	if gap < want {
		t.Errorf("%s deadline is %s after the call started, want at least %s", label, gap, want)
	}
	if gap > want+30*time.Second {
		t.Errorf("%s deadline is %s after the call started, want roughly %s", label, gap, want)
	}
}

func requireHTTPError(t *testing.T, err error, wantStatus int) *apperr.HTTPError {
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
	return httpErr
}

// ---------------------------------------------------------------- PrintReader

func TestPrintReaderSubmitsTheSpooledUpload(t *testing.T) {
	t.Parallel()

	const content = "%PDF-1.4 uploaded body"
	sub := &fakeSubmitter{result: SubmitResult{Output: "request id is q-1 (1 file(s))"}}

	var seenPath, seenContent string
	sub.inspect = func(job SubmitJob) {
		// Read the file at submission time: this is the only moment it exists,
		// and it is what proves the document really reached disk before lp
		// would have been handed the path.
		seenPath = job.Path
		b, err := os.ReadFile(job.Path)
		if err != nil {
			t.Errorf("spooled file unreadable at submit time: %v", err)
			return
		}
		seenContent = string(b)
	}

	svc := NewService(sub, nil, nil, testTimeouts, testS3Max)
	res, err := svc.PrintReader(context.Background(), "q-hp", "invoice.pdf", strings.NewReader(content))
	if err != nil {
		t.Fatalf("PrintReader returned an unexpected error: %v", err)
	}

	if res.Output != "request id is q-1 (1 file(s))" {
		t.Errorf("Output = %q, want the submitter's output verbatim", res.Output)
	}
	if seenContent != content {
		t.Errorf("spooled content = %q, want %q", seenContent, content)
	}

	job := sub.lastJob()
	if job.Printer != "q-hp" {
		t.Errorf("Printer = %q, want %q", job.Printer, "q-hp")
	}
	if job.Title != "invoice.pdf" {
		t.Errorf("Title = %q, want %q", job.Title, "invoice.pdf")
	}
	if job.Path == "" {
		t.Error("Path is empty; the submitter has nothing to open")
	}

	// The temp file is gone once PrintReader returns. Cleanup on the SUCCESS
	// path is not itself P0-1 — that defect was `defer cleanup()` never
	// running because a wedged lp hung the handler forever (see
	// TestPrintReaderBoundsTheSubmitCall for the leak under timeout) — but it
	// is the same obligation, and a per-request file that is never reclaimed
	// fills the disk either way.
	assertNotExist(t, seenPath, "after PrintReader returned")
}

func TestPrintReaderSanitizesTheFilename(t *testing.T) {
	t.Parallel()

	sub := &fakeSubmitter{}
	svc := NewService(sub, nil, nil, testTimeouts, testS3Max)

	// A caller-supplied name with separators must not reach os.CreateTemp's
	// pattern (it would fail outright) nor lp's -t argument unsanitized.
	if _, err := svc.PrintReader(context.Background(), "q", `../../etc/my file.pdf`, strings.NewReader("x")); err != nil {
		t.Fatalf("PrintReader returned an unexpected error: %v", err)
	}

	job := sub.lastJob()
	if want := ".._.._etc_my_file.pdf"; job.Title != want {
		t.Errorf("Title = %q, want %q", job.Title, want)
	}
	if strings.ContainsAny(filepath.Base(job.Path), `/\`) {
		t.Errorf("spool file name %q contains a path separator", filepath.Base(job.Path))
	}
	// Load-bearing for other tests, not cosmetic: every
	// assertNoSpoolFilesMatching in this file globs on the sanitized name
	// being part of the spool pattern. If PrintReader's pattern ever dropped
	// it, those globs would match nothing and pass forever — four leak
	// assertions going vacuous at once, with no test failing. Confirmed by
	// mutation: without this line, changing the pattern to "print-upload-*"
	// survives the whole suite.
	if base := filepath.Base(job.Path); !strings.HasSuffix(base, ".._.._etc_my_file.pdf") {
		t.Errorf("spool file name = %q, want it to end in the sanitized filename", base)
	}
}

func TestPrintReaderSpoolFailureNeverReachesTheSubmitter(t *testing.T) {
	t.Parallel()

	name := uniqueName(t)
	watchSpoolFiles(t, "print-upload-*-"+name)
	sub := &fakeSubmitter{}
	svc := NewService(sub, nil, nil, testTimeouts, testS3Max)

	readErr := errors.New("connection reset mid-upload")
	_, err := svc.PrintReader(context.Background(), "q", name, &failingReader{err: readErr})

	httpErr := requireHTTPError(t, err, http.StatusInternalServerError)
	if httpErr.Public != "internal server error" {
		t.Errorf("public message = %q, want a generic one", httpErr.Public)
	}
	if !errors.Is(err, readErr) {
		t.Error("the underlying read error is not reachable through Internal")
	}
	// The reader's error text could name anything about the transport; it must
	// stay out of what the client sees.
	if strings.Contains(httpErr.Public, "connection reset") {
		t.Errorf("public message %q leaks the internal detail", httpErr.Public)
	}

	if sub.called() {
		t.Error("the submitter was called even though spooling failed")
	}
}

func TestPrintReaderPropagatesTheSubmitterError(t *testing.T) {
	t.Parallel()

	// cups.LPSubmitter classifies its own failures; Service must hand them
	// back untouched rather than re-wrapping and re-classifying.
	submitErr := &apperr.HTTPError{
		Status:   http.StatusGatewayTimeout,
		Public:   "print submission timed out",
		Internal: errors.New("signal: killed"),
	}
	sub := &fakeSubmitter{err: submitErr}
	svc := NewService(sub, nil, nil, testTimeouts, testS3Max)

	_, err := svc.PrintReader(context.Background(), "q", "x.pdf", strings.NewReader("x"))
	if err != submitErr { //nolint:errorlint // identity is the property under test
		t.Errorf("error = %#v, want the submitter's own error value", err)
	}
}

// TestPrintReaderBoundsTheSubmitCall is the P0-1 headline test: a wedged CUPS
// queue must fail the request at SubmitTimeout instead of hanging the handler
// goroutine forever, leaking the goroutine, the lp process, the temp file and
// the client connection, permanently, per request.
func TestPrintReaderBoundsTheSubmitCall(t *testing.T) {
	t.Parallel()

	const submitTimeout = 80 * time.Millisecond
	name := uniqueName(t)
	watchSpoolFiles(t, "print-upload-*-"+name)
	sub := &fakeSubmitter{block: true}
	svc := NewService(sub, nil, nil, Timeouts{Submit: submitTimeout, Fetch: time.Minute, S3: time.Minute}, testS3Max)

	start := time.Now()
	_, err := svc.PrintReader(context.Background(), "q", name, strings.NewReader("x"))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("PrintReader returned nil for a submitter that never completes")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want it to carry the deadline-exceeded cause", err)
	}
	if elapsed < submitTimeout {
		t.Errorf("returned after %s, want at least the %s submit timeout", elapsed, submitTimeout)
	}
	// Generous: the point is that it returns at all, in the order of the
	// timeout rather than of the (unbounded) submitter.
	if elapsed > 10*time.Second {
		t.Errorf("returned after %s, want roughly %s", elapsed, submitTimeout)
	}

	if len(sub.deadlines) != 1 {
		t.Fatalf("submitter saw %d deadlines, want 1 — Service must bound every Submit call", len(sub.deadlines))
	}
	assertDeadlineWithin(t, "submit", start, sub.deadlines[0], submitTimeout)

	// The spool file is reclaimed even though the submission failed.
}

func TestPrintReaderHonorsACancelledCallerContext(t *testing.T) {
	t.Parallel()

	name := uniqueName(t)
	watchSpoolFiles(t, "print-upload-*-"+name)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	sub := &fakeSubmitter{}
	svc := NewService(sub, nil, nil, testTimeouts, testS3Max)

	// Spooling still happens (it does not consult ctx), but the submit must
	// see the cancellation — Service derives its bounded context FROM the
	// caller's, so a client that disconnected does not get a print anyway.
	if _, err := svc.PrintReader(ctx, "q", name, strings.NewReader("x")); err == nil {
		t.Error("PrintReader returned nil for a cancelled context")
	}
}

// ------------------------------------------------------------------- PrintURL

func TestPrintURLSubmitsTheFetchedDocument(t *testing.T) {
	t.Parallel()

	const body = "%PDF-1.4 downloaded body"
	fetcher := &fakeFetcher{body: []byte(body)}
	sub := &fakeSubmitter{result: SubmitResult{Output: "request id is q-2 (1 file(s))"}}

	var seenContent string
	sub.inspect = func(job SubmitJob) {
		b, err := os.ReadFile(job.Path)
		if err != nil {
			t.Errorf("spooled file unreadable at submit time: %v", err)
			return
		}
		seenContent = string(b)
	}

	svc := NewService(sub, fetcher, nil, testTimeouts, testS3Max)
	start := time.Now()
	res, err := svc.PrintURL(context.Background(), "q-canon", "https://example.com/a.pdf")
	if err != nil {
		t.Fatalf("PrintURL returned an unexpected error: %v", err)
	}

	if res.Output != "request id is q-2 (1 file(s))" {
		t.Errorf("Output = %q, want the submitter's output verbatim", res.Output)
	}
	if seenContent != body {
		t.Errorf("spooled content = %q, want %q", seenContent, body)
	}
	if len(fetcher.urls) != 1 || fetcher.urls[0] != "https://example.com/a.pdf" {
		t.Errorf("fetcher saw %v, want the caller's URL exactly once", fetcher.urls)
	}

	job := sub.lastJob()
	if job.Printer != "q-canon" {
		t.Errorf("Printer = %q, want %q", job.Printer, "q-canon")
	}
	// A fixed title, not the URL: the URL can be arbitrarily long and carry a
	// query string full of credentials (a presigned link), neither of which
	// belongs in lpstat.
	if job.Title != "download" {
		t.Errorf("Title = %q, want %q", job.Title, "download")
	}

	if len(fetcher.deadlines) != 1 {
		t.Fatalf("fetcher saw %d deadlines, want 1 — Service must bound every Fetch call", len(fetcher.deadlines))
	}
	assertDeadlineWithin(t, "fetch", start, fetcher.deadlines[0], testTimeouts.Fetch)

	assertNotExist(t, fetcher.dstPath(), "after PrintURL returned")
}

func TestPrintURLClassifiesFetchFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		fetchErr   error
		wantStatus int
		wantPublic string
	}{
		{
			// A transport failure carries no status of its own, so Service
			// supplies 502: the gateway could not reach the upstream.
			name:       "an unclassified error becomes 502",
			fetchErr:   errors.New("dial tcp 93.184.216.34:443: i/o timeout"),
			wantStatus: http.StatusBadGateway,
			wantPublic: "failed to download file_url",
		},
		{
			// The A5 behavior: fetch.SafeFetcher already classified this as a
			// caller mistake. Collapsing it to 502 would tell the caller the
			// gateway is broken when in fact their URL was rejected.
			name: "a blocked SSRF target stays a 400",
			fetchErr: &apperr.HTTPError{
				Status:   http.StatusBadRequest,
				Public:   "file_url resolved to a disallowed address",
				Internal: errors.New("dial control blocked 169.254.169.254"),
			},
			wantStatus: http.StatusBadRequest,
			wantPublic: "file_url resolved to a disallowed address",
		},
		{
			name: "an oversize response stays a 413",
			fetchErr: &apperr.HTTPError{
				Status: http.StatusRequestEntityTooLarge,
				Public: "file_url response is too large",
			},
			wantStatus: http.StatusRequestEntityTooLarge,
			wantPublic: "file_url response is too large",
		},
		{
			name: "an upstream error response stays a 502 with its own message",
			fetchErr: &apperr.HTTPError{
				Status: http.StatusBadGateway,
				Public: "file_url returned an error",
			},
			wantStatus: http.StatusBadGateway,
			wantPublic: "file_url returned an error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fetcher := &fakeFetcher{err: tt.fetchErr}
			sub := &fakeSubmitter{}
			svc := NewService(sub, fetcher, nil, testTimeouts, testS3Max)

			_, err := svc.PrintURL(context.Background(), "q", "https://example.com/a.pdf")
			httpErr := requireHTTPError(t, err, tt.wantStatus)
			if httpErr.Public != tt.wantPublic {
				t.Errorf("public message = %q, want %q", httpErr.Public, tt.wantPublic)
			}
			if sub.called() {
				t.Error("the submitter was called even though the fetch failed")
			}
			assertNotExist(t, fetcher.dstPath(), "after a failed fetch")
		})
	}
}

// TestPrintURLPartialFetchLeavesNothingBehind is the invariant the plan names
// explicitly: a fetch that fails AFTER writing some bytes must remove the
// half-written file and never invoke lp on it.
func TestPrintURLPartialFetchLeavesNothingBehind(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFetcher{
		body:    []byte("half a document"),
		err:     errors.New("unexpected EOF"),
		partial: true,
	}
	sub := &fakeSubmitter{}
	svc := NewService(sub, fetcher, nil, testTimeouts, testS3Max)

	_, err := svc.PrintURL(context.Background(), "q", "https://example.com/a.pdf")
	requireHTTPError(t, err, http.StatusBadGateway)

	if sub.called() {
		t.Error("the submitter was called with a partially written file")
	}
	assertNotExist(t, fetcher.dstPath(), "after a partial fetch failed")
}

// TestPrintURLHonorsACancelledCallerContext is PrintReader's twin for the
// download path. Without it, Service.fetch deriving its bounded context from
// context.Background() instead of the caller's survives the whole suite
// (confirmed by mutation) — and the consequence is concrete: a client that
// disconnects leaves a 64 MiB file_url download running to completion, per
// abandoned request.
func TestPrintURLHonorsACancelledCallerContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	fetcher := &fakeFetcher{body: []byte("should never be written")}
	sub := &fakeSubmitter{}
	svc := NewService(sub, fetcher, nil, testTimeouts, testS3Max)

	_, err := svc.PrintURL(ctx, "q", "https://example.com/a.pdf")
	if err == nil {
		t.Fatal("PrintURL returned nil for a cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want it to carry context.Canceled", err)
	}
	if !fetcher.called() {
		t.Error("the fetcher was never called; the cancellation must reach it, not short-circuit before it")
	}
	if sub.called() {
		t.Error("the submitter was called for a cancelled request")
	}
	assertNotExist(t, fetcher.dstPath(), "after a cancelled fetch")
}

// TestPrintURLAcceptsAZeroByteDocument pins current behavior rather than
// endorsing it: an empty response is spooled and submitted, so lp prints a
// blank page and the API answers 200. Nothing in printgw rejects it today.
// Recorded so that adding an emptiness check later is a deliberate, visible
// decision instead of an accidental behavior change.
func TestPrintURLAcceptsAZeroByteDocument(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFetcher{body: nil}
	sub := &fakeSubmitter{}

	var size int64 = -1
	sub.inspect = func(job SubmitJob) {
		info, err := os.Stat(job.Path)
		if err != nil {
			t.Errorf("spooled file unreadable at submit time: %v", err)
			return
		}
		size = info.Size()
	}

	svc := NewService(sub, fetcher, nil, testTimeouts, testS3Max)
	if _, err := svc.PrintURL(context.Background(), "q", "https://example.com/empty.pdf"); err != nil {
		t.Fatalf("PrintURL returned an unexpected error: %v", err)
	}
	if !sub.called() {
		t.Error("the submitter was not called for a zero-byte document")
	}
	if size != 0 {
		t.Errorf("spooled size = %d, want 0", size)
	}
}

// ----------------------------------------------------------------- PrintS3Key

func TestPrintS3KeyWithoutAnObjectStore(t *testing.T) {
	t.Parallel()

	name := uniqueName(t)
	watchSpoolFiles(t, "print-s3-*-"+name)
	sub := &fakeSubmitter{}
	// S3 is additive: a Service built without it must still serve everything
	// else, and answer 503 only for this one intake.
	svc := NewService(sub, &fakeFetcher{}, nil, testTimeouts, testS3Max)

	_, err := svc.PrintS3Key(context.Background(), "q", name)
	httpErr := requireHTTPError(t, err, http.StatusServiceUnavailable)
	if httpErr.Public != "object storage is not configured" {
		t.Errorf("public message = %q, want %q", httpErr.Public, "object storage is not configured")
	}
	if httpErr.Internal != nil {
		t.Errorf("Internal = %v, want nil: this is a configuration state, not a failure", httpErr.Internal)
	}
	if sub.called() {
		t.Error("the submitter was called with no object store configured")
	}
	// Nothing was spooled at all — the nil check happens before spoolTo.
}

func TestPrintS3KeySubmitsTheDownloadedObject(t *testing.T) {
	t.Parallel()

	const body = "%PDF-1.4 object body"
	store := &fakeObjectStore{body: []byte(body), size: int64(len(body))}
	sub := &fakeSubmitter{result: SubmitResult{Output: "request id is q-3 (1 file(s))"}}

	var seenPath, seenContent string
	sub.inspect = func(job SubmitJob) {
		seenPath = job.Path
		b, err := os.ReadFile(job.Path)
		if err != nil {
			t.Errorf("spooled file unreadable at submit time: %v", err)
			return
		}
		seenContent = string(b)
	}

	svc := NewService(sub, nil, store, testTimeouts, testS3Max)
	start := time.Now()
	res, err := svc.PrintS3Key(context.Background(), "q-brother", "docs/2026/invoice.pdf")
	if err != nil {
		t.Fatalf("PrintS3Key returned an unexpected error: %v", err)
	}

	if res.Output != "request id is q-3 (1 file(s))" {
		t.Errorf("Output = %q, want the submitter's output verbatim", res.Output)
	}
	if seenContent != body {
		t.Errorf("spooled content = %q, want %q", seenContent, body)
	}
	if len(store.keys) != 1 || store.keys[0] != "docs/2026/invoice.pdf" {
		t.Errorf("store saw %v, want the caller's key exactly once and unmodified", store.keys)
	}

	job := sub.lastJob()
	// The whole key is sanitized into the title (so the folder is still
	// visible in lpstat), while the spool file name uses only path.Base.
	if want := "docs_2026_invoice.pdf"; job.Title != want {
		t.Errorf("Title = %q, want %q", job.Title, want)
	}

	if len(store.deadlines) != 1 {
		t.Fatalf("store saw %d deadlines, want 1 — Service must bound every Get call", len(store.deadlines))
	}
	assertDeadlineWithin(t, "s3", start, store.deadlines[0], testTimeouts.S3)

	if store.closes() != 1 {
		t.Errorf("object was closed %d times, want exactly 1", store.closes())
	}
	assertNotExist(t, seenPath, "after PrintS3Key returned")
}

func TestPrintS3KeyClassifiesGetFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		getErr     error
		wantStatus int
		wantPublic string
	}{
		{
			// objstore.MinIO already turned cloud_storage.ErrNotFound into a
			// 404; passing it through is what lets a caller tell "your key is
			// wrong" from "our storage is broken".
			name: "a missing key stays a 404",
			getErr: &apperr.HTTPError{
				Status:   http.StatusNotFound,
				Public:   `object "docs/nope.pdf" not found`,
				Internal: errors.New("cloud_storage: key not found"),
			},
			wantStatus: http.StatusNotFound,
			wantPublic: `object "docs/nope.pdf" not found`,
		},
		{
			name: "a storage failure stays a 502",
			getErr: &apperr.HTTPError{
				Status: http.StatusBadGateway,
				Public: "failed to fetch object from storage",
			},
			wantStatus: http.StatusBadGateway,
			wantPublic: "failed to fetch object from storage",
		},
		{
			name:       "an unclassified error becomes 502",
			getErr:     errors.New("dial tcp 10.0.0.5:9000: connect: connection refused"),
			wantStatus: http.StatusBadGateway,
			wantPublic: "failed to fetch object from storage",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			name := uniqueName(t)
			watchSpoolFiles(t, "print-s3-*-"+name)
			store := &fakeObjectStore{err: tt.getErr}
			sub := &fakeSubmitter{}
			svc := NewService(sub, nil, store, testTimeouts, testS3Max)

			_, err := svc.PrintS3Key(context.Background(), "q", name)
			httpErr := requireHTTPError(t, err, tt.wantStatus)
			if httpErr.Public != tt.wantPublic {
				t.Errorf("public message = %q, want %q", httpErr.Public, tt.wantPublic)
			}
			if sub.called() {
				t.Error("the submitter was called even though Get failed")
			}
		})
	}
}

// TestPrintS3KeyEnforcesTheSizeLimit covers both halves of getObject's size
// handling, which exist because a store's reported size is a contract on the
// ObjectStore interface, not something this code can verify: the up-front
// check on the reported size, and the LimitReader that catches a store which
// under-reports. Without the second, a lying or truncated stream could spool
// and print while this reported success — the P0-3 failure class in a new
// guise.
func TestPrintS3KeyEnforcesTheSizeLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		maxBytes     int64
		body         string
		reportedSize int64
		wantStatus   int // 0 means the call must succeed
		wantInPublic string
		// wantNoRead asserts the object was rejected before a single byte was
		// copied — the property that separates the up-front metadata check
		// from the downstream copy limit.
		wantNoRead bool
	}{
		{
			name:         "an object at the limit is accepted",
			maxBytes:     10,
			body:         "0123456789",
			reportedSize: 10,
		},
		{
			// The ONLY case that reaches the up-front `size > s.s3MaxBytes`
			// check: an over-limit BODY is caught identically by the copy
			// limit downstream, so without a store that over-REPORTS (a stale
			// HEAD, a multipart manifest) that check is untested. Confirmed by
			// mutation: with only the over-body case below, replacing the
			// up-front check with `if false` survives the suite.
			name:         "a store that over-reports is rejected before any byte is copied",
			maxBytes:     10,
			body:         "abc",
			reportedSize: 1000,
			wantStatus:   http.StatusRequestEntityTooLarge,
			wantInPublic: "maximum allowed size of 10 bytes",
			wantNoRead:   true,
		},
		{
			name:         "a body over the limit is rejected",
			maxBytes:     10,
			body:         "0123456789A",
			reportedSize: 11,
			wantStatus:   http.StatusRequestEntityTooLarge,
			wantInPublic: "maximum allowed size of 10 bytes",
			wantNoRead:   true,
		},
		{
			// The store claims a small object and then streams a large one.
			// Only the LimitReader catches this.
			name:         "a store that under-reports its size is caught by the copy limit",
			maxBytes:     10,
			body:         "0123456789ABCDEF",
			reportedSize: 5,
			wantStatus:   http.StatusRequestEntityTooLarge,
			wantInPublic: "maximum allowed size of 10 bytes",
		},
		{
			// Fewer bytes than promised: a truncated stream. Reporting success
			// here would physically print a truncated document.
			name:         "a stream shorter than the reported size is rejected",
			maxBytes:     100,
			body:         "abc",
			reportedSize: 10,
			wantStatus:   http.StatusBadGateway,
			wantInPublic: "failed to fetch object from storage",
		},
		{
			// A negative size is NOT part of the ObjectStore contract —
			// ports.go says "claimed size" and objstore/minio.go says "exact
			// size", and the one production implementation gets it from a
			// successful minio Stat, so it is never negative. This pins the
			// defensive `size >= 0` guard: a future ObjectStore returning a
			// sentinel must not make the call fail spuriously. Note the guard
			// also means such a store SKIPS the copied-vs-reported
			// verification entirely — a real gap, recorded rather than
			// asserted away.
			name:         "a negative size is tolerated when the body fits",
			maxBytes:     100,
			body:         "abc",
			reportedSize: -1,
		},
		{
			// Pinned, not endorsed: a zero-byte object is spooled and
			// submitted, so lp prints a blank page and the API answers 200.
			// Adding an emptiness check later should be a visible decision.
			name:         "a zero-byte object is accepted and prints a blank page",
			maxBytes:     100,
			body:         "",
			reportedSize: 0,
		},
		{
			// A maxBytes near math.MaxInt64 would overflow maxBytes+1 into a
			// negative LimitReader bound, which io.LimitReader treats as
			// "already at the limit" — an immediate EOF that reads as a
			// genuine zero-byte success and prints a blank page.
			name:         "a limit at math.MaxInt64 does not overflow into an empty read",
			maxBytes:     math.MaxInt64,
			body:         "not empty",
			reportedSize: 9,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			name := uniqueName(t)
			watchSpoolFiles(t, "print-s3-*-"+name)
			store := &fakeObjectStore{body: []byte(tt.body), size: tt.reportedSize}
			sub := &fakeSubmitter{}

			var spooled string
			sub.inspect = func(job SubmitJob) {
				b, err := os.ReadFile(job.Path)
				if err != nil {
					t.Errorf("spooled file unreadable at submit time: %v", err)
					return
				}
				spooled = string(b)
			}

			svc := NewService(sub, nil, store, testTimeouts, tt.maxBytes)
			_, err := svc.PrintS3Key(context.Background(), "q", name)

			if tt.wantStatus == 0 {
				if err != nil {
					t.Fatalf("PrintS3Key returned an unexpected error: %v", err)
				}
				if spooled != tt.body {
					t.Errorf("spooled content = %q, want %q", spooled, tt.body)
				}
			} else {
				httpErr := requireHTTPError(t, err, tt.wantStatus)
				if !strings.Contains(httpErr.Public, tt.wantInPublic) {
					t.Errorf("public message = %q, want it to contain %q", httpErr.Public, tt.wantInPublic)
				}
				if sub.called() {
					t.Error("the submitter was called for a rejected object")
				}
			}

			if tt.wantNoRead && store.read() {
				t.Error("the object was read; an over-reported size must be rejected before any byte is copied")
			}

			// Closed on every path, including the ones that reject the object
			// without reading it — otherwise the store's connection leaks per
			// request.
			if store.closes() != 1 {
				t.Errorf("object was closed %d times, want exactly 1", store.closes())
			}
		})
	}
}

func TestPrintS3KeyCopyFailure(t *testing.T) {
	t.Parallel()

	t.Run("a read error with a live context is a 502", func(t *testing.T) {
		t.Parallel()

		name := uniqueName(t)
		watchSpoolFiles(t, "print-s3-*-"+name)
		store := &fakeObjectStore{
			body:   []byte("abc"),
			size:   3,
			object: &fakeObject{readErr: errors.New("unexpected stream reset")},
		}
		sub := &fakeSubmitter{}
		svc := NewService(sub, nil, store, testTimeouts, testS3Max)

		_, err := svc.PrintS3Key(context.Background(), "q", name)
		httpErr := requireHTTPError(t, err, http.StatusBadGateway)
		if httpErr.Public != "failed to fetch object from storage" {
			t.Errorf("public message = %q, want %q", httpErr.Public, "failed to fetch object from storage")
		}
		if !strings.Contains(httpErr.Internal.Error(), "unexpected stream reset") {
			t.Errorf("Internal = %v, want it to carry the underlying read error", httpErr.Internal)
		}
		if sub.called() {
			t.Error("the submitter was called after a failed copy")
		}
		if store.closes() != 1 {
			t.Errorf("object was closed %d times, want exactly 1: a real store's connection leaks otherwise", store.closes())
		}
	})

	t.Run("a read error with a done context is a 504", func(t *testing.T) {
		t.Parallel()

		name := uniqueName(t)
		watchSpoolFiles(t, "print-s3-*-"+name)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Cancel the caller's context from inside the first Read. getObject
		// derives its own bounded context from this one, so ctx.Err() is
		// non-nil by the time io.Copy reports the failure — which is what
		// separates "storage broke" (502) from "we ran out of time" (504).
		//
		// No fake read error is needed: fakeObject is ctx-bound the way the
		// real CloudStorageObject is, so the cancellation itself is what
		// fails the read. An invented error here would have made the test
		// look like the classification keys off the error text; it does not,
		// it keys off ctx.Err().
		store := &fakeObjectStore{
			body:   []byte("abc"),
			size:   3,
			object: &fakeObject{beforeRead: cancel},
		}
		sub := &fakeSubmitter{}
		svc := NewService(sub, nil, store, testTimeouts, testS3Max)

		_, err := svc.PrintS3Key(ctx, "q", name)
		httpErr := requireHTTPError(t, err, http.StatusGatewayTimeout)
		if httpErr.Public != "print submission timed out" {
			t.Errorf("public message = %q, want %q", httpErr.Public, "print submission timed out")
		}
		if !strings.Contains(httpErr.Internal.Error(), "ctxErr=") {
			t.Errorf("Internal = %v, want it to name the context error", httpErr.Internal)
		}
		if sub.called() {
			t.Error("the submitter was called after a cancelled copy")
		}
		if store.closes() != 1 {
			t.Errorf("object was closed %d times, want exactly 1: a real store's connection leaks otherwise", store.closes())
		}
	})
}

// TestPrintS3KeySpoolNameUsesOnlyTheBasename documents the difference between
// the two uses of the key: the spool file name takes path.Base (a full key
// would otherwise blow past filename length limits), while the job title
// keeps the whole sanitized key so an operator can still tell jobs apart.
func TestPrintS3KeySpoolNameUsesOnlyTheBasename(t *testing.T) {
	t.Parallel()

	store := &fakeObjectStore{body: []byte("x"), size: 1}
	sub := &fakeSubmitter{}

	var spoolBase string
	sub.inspect = func(job SubmitJob) { spoolBase = filepath.Base(job.Path) }

	svc := NewService(sub, nil, store, testTimeouts, testS3Max)
	if _, err := svc.PrintS3Key(context.Background(), "q", "a/deep/nested/report.pdf"); err != nil {
		t.Fatalf("PrintS3Key returned an unexpected error: %v", err)
	}

	if !strings.HasSuffix(spoolBase, "report.pdf") {
		t.Errorf("spool file name = %q, want it to end in the key's basename", spoolBase)
	}
	if strings.Contains(spoolBase, "deep") {
		t.Errorf("spool file name = %q, want only the basename, not the whole key", spoolBase)
	}
	if want := "a_deep_nested_report.pdf"; sub.lastJob().Title != want {
		t.Errorf("Title = %q, want the whole sanitized key %q", sub.lastJob().Title, want)
	}
}

// failingReader fails on the first Read, standing in for a client connection
// that drops mid-upload.
type failingReader struct{ err error }

func (r *failingReader) Read([]byte) (int, error) { return 0, r.err }

// TestNewServiceAcceptsANilObjectStore pins the constructor contract its doc
// comment states: only PrintS3Key checks for nil, so building a Service
// without object storage must not panic anywhere else.
func TestNewServiceAcceptsANilObjectStore(t *testing.T) {
	t.Parallel()

	svc := NewService(&fakeSubmitter{}, &fakeFetcher{body: []byte("x")}, nil, testTimeouts, testS3Max)
	if svc == nil {
		t.Fatal("NewService returned nil")
	}
	for i, call := range []func() error{
		func() error {
			_, err := svc.PrintReader(context.Background(), "q", "a.pdf", strings.NewReader("x"))
			return err
		},
		func() error {
			_, err := svc.PrintURL(context.Background(), "q", "https://example.com/a.pdf")
			return err
		},
	} {
		if err := call(); err != nil {
			t.Errorf("call %d failed with a nil object store: %v", i, err)
		}
	}
}
