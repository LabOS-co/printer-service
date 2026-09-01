package httpapi

import (
	"bytes"
	"context"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/LabOS-co/go-packages/logs"

	"printgateway/internal/config"
	"printgateway/internal/printgw"
)

// capturingLogger records every call this package's tests need to assert
// on. Guarded by a mutex — unlike the single-request fakes below, this one
// is shared across the 50-concurrent middleware test, and logs.LoggerMock's
// embedded no-op methods cover everything else the logs.Logger interface
// requires.
type capturingLogger struct {
	logs.LoggerMock

	mu          sync.Mutex
	infos       []string
	errors      []string
	completions []*logs.LogMetaData
}

func (c *capturingLogger) LogInfo(msg string, _ *logs.LogMetaData) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.infos = append(c.infos, msg)
	return nil
}

func (c *capturingLogger) LogError(msg string, _ *logs.LogMetaData) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.errors = append(c.errors, msg)
	return nil
}

func (c *capturingLogger) LogAPICompletion(md *logs.LogMetaData) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.completions = append(c.completions, md)
	return nil
}

// snapshotCompletions/snapshotErrors/snapshotInfos are the only race-safe
// way to read these slices from a test. An HTTP round trip establishes no
// happens-before edge the race detector recognizes between "the client
// received a response" and "the server-side goroutine finished its
// deferred bookkeeping" — verified live: a test reading c.completions
// directly right after http.Get returns raced against accessLog's own
// deferred append, most visibly on the panic-after-write path (the
// connection closes as soon as net/http reacts to http.ErrAbortHandler,
// which can happen before this goroutine's own defers finish). Locking
// through the same mutex the writers use is the fix, not a longer sleep.
func (c *capturingLogger) snapshotCompletions() []*logs.LogMetaData {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*logs.LogMetaData, len(c.completions))
	copy(out, c.completions)
	return out
}

func (c *capturingLogger) snapshotErrors() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.errors))
	copy(out, c.errors)
	return out
}

func (c *capturingLogger) snapshotInfos() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.infos))
	copy(out, c.infos)
	return out
}

// waitForCompletions polls logger until it has recorded at least want
// completions, or gives up after two seconds. A snapshot alone proves
// nothing about whether the server-side goroutine has *finished* logging
// yet — most visible on a request whose connection is aborted, where the
// client can observe the drop before the server-side goroutine's own
// defers run. Polling a short, bounded amount rather than sleeping a fixed
// duration keeps the common case (already done by the time this is called)
// fast while still tolerating the rare slow case.
func waitForCompletions(t *testing.T, logger *capturingLogger, want int) []*logs.LogMetaData {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := logger.snapshotCompletions()
		if len(got) >= want || time.Now().After(deadline) {
			return got
		}
		time.Sleep(time.Millisecond)
	}
}

func waitForErrors(t *testing.T, logger *capturingLogger, want int) []string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := logger.snapshotErrors()
		if len(got) >= want || time.Now().After(deadline) {
			return got
		}
		time.Sleep(time.Millisecond)
	}
}

// fakeSubmitter stands in for cups.LPSubmitter. Guarded by a mutex for the
// same reason as capturingLogger: the concurrency test drives many
// goroutines through the same *API, and every request shares one Service,
// hence one Submitter. result/err are read under the same lock as jobs is
// written — an Opus review of this stage found the original version read
// them unlocked, which happened to be safe only because every test sets
// them once at construction and never mutates them mid-run; guarding all
// three uniformly removes that as an assumption a future test could
// silently violate.
type fakeSubmitter struct {
	mu     sync.Mutex
	result printgw.SubmitResult
	err    error
	jobs   []printgw.SubmitJob

	// spooledBodies holds, per call and in order, the actual bytes at
	// job.Path read DURING Submit — printgw.Service's own `defer cleanup()`
	// deletes the spool file the instant Submit returns, so this is the
	// only point at which a test can observe what was actually spooled,
	// not just which Printer/Title Submit was called with. An Opus review
	// of this stage found that no test in this package ever read jobs at
	// all, so a mutant that spooled a completely different printer, file,
	// or body passed the whole suite.
	spooledBodies [][]byte
}

func (f *fakeSubmitter) Submit(ctx context.Context, job printgw.SubmitJob) (printgw.SubmitResult, error) {
	body, _ := os.ReadFile(job.Path)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jobs = append(f.jobs, job)
	f.spooledBodies = append(f.spooledBodies, body)
	return f.result, f.err
}

// jobs returns a copy of every job Submit has recorded so far, safe to read
// from a test goroutine that isn't the one driving requests.
func (f *fakeSubmitter) snapshotJobs() []printgw.SubmitJob {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]printgw.SubmitJob, len(f.jobs))
	copy(out, f.jobs)
	return out
}

func (f *fakeSubmitter) snapshotSpooledBodies() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]byte, len(f.spooledBodies))
	copy(out, f.spooledBodies)
	return out
}

// fakeFetcher stands in for fetch.SafeFetcher. rawURL is recorded (not just
// used) so a test can assert the handler passed through the URL it actually
// received, rather than merely that *some* fetch happened — an Opus review
// of this stage found the original version discarded rawURL entirely, which
// let a mutant that fetched a hardcoded wrong URL pass the whole suite.
type fakeFetcher struct {
	body   []byte
	err    error
	rawURL string
}

func (f *fakeFetcher) Fetch(ctx context.Context, rawURL string, dst io.Writer) (int64, error) {
	f.rawURL = rawURL
	if f.err != nil {
		return 0, f.err
	}
	n, err := io.Copy(dst, bytes.NewReader(f.body))
	return n, err
}

// fakeObjectStore stands in for objstore.MinIO. err, when set, should
// normally already be an *apperr.HTTPError (matching ports.go's documented
// contract — Get wraps a missing key as {Status: 404}) since that is what a
// real ObjectStore implementation does; tests exercising Service's own
// re-wrap of an unclassified error use a plain error instead. key is
// recorded for the same reason fakeFetcher now records rawURL — see that
// type's comment.
type fakeObjectStore struct {
	body []byte
	size int64
	err  error
	key  string
}

func (f *fakeObjectStore) Get(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	f.key = key
	if f.err != nil {
		return nil, 0, f.err
	}
	return io.NopCloser(bytes.NewReader(f.body)), f.size, nil
}

// fakePresigner stands in for objstore.MinIO's presign methods, behind
// httpapi's own narrow Presigner interface. getURL/putURL are deliberately
// distinct (not one shared url field) so a test can tell GET and PUT apart
// by which one comes back, and every call is recorded — an Opus review of
// this stage found the original one-url, no-recording version let three
// separate mutants (GET/PUT dispatch swapped, the wrong key passed through,
// the wrong ttl passed through) all pass the whole suite, since nothing
// ever observed which method/key/ttl the handler actually sent.
type fakePresigner struct {
	mu     sync.Mutex
	getURL string
	putURL string
	err    error
	calls  []presignCall
}

type presignCall struct {
	method string
	key    string
	ttl    time.Duration
}

func (f *fakePresigner) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, presignCall{method: "GET", key: key, ttl: ttl})
	f.mu.Unlock()
	return f.getURL, f.err
}

func (f *fakePresigner) PresignPut(ctx context.Context, key string, ttl time.Duration) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, presignCall{method: "PUT", key: key, ttl: ttl})
	f.mu.Unlock()
	return f.putURL, f.err
}

func (f *fakePresigner) snapshotCalls() []presignCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]presignCall, len(f.calls))
	copy(out, f.calls)
	return out
}

// testAPIOpts configures newTestAPI. Every field has a zero-value-friendly
// default applied by newTestAPI itself, so a test only sets what it cares
// about.
type testAPIOpts struct {
	submitter   printgw.Submitter
	fetcher     printgw.Fetcher
	objectStore printgw.ObjectStore
	presigner   Presigner

	authToken  string
	maxUpload  int64
	maxJSON    int64
	s3MaxBytes int64
	timeouts   printgw.Timeouts
}

// newTestAPI builds a real *API over real printgw.Service, wired to fakes
// at the lowest layer (Submitter/Fetcher/ObjectStore/Presigner) — "full
// stack over the real chain with fakes", per the plan's own description of
// this stage. A submitter defaulting to one that always succeeds means a
// test only supplies the fakes it actually cares about varying.
func newTestAPI(opts testAPIOpts) (*API, *capturingLogger) {
	if opts.authToken == "" {
		opts.authToken = "test-token"
	}
	if opts.maxUpload == 0 {
		opts.maxUpload = 10 << 20
	}
	if opts.maxJSON == 0 {
		opts.maxJSON = 8 << 10
	}
	if opts.s3MaxBytes == 0 {
		opts.s3MaxBytes = 10 << 20
	}
	if opts.timeouts == (printgw.Timeouts{}) {
		opts.timeouts = printgw.Timeouts{Submit: 5 * time.Second, Fetch: 5 * time.Second, S3: 5 * time.Second}
	}
	if opts.submitter == nil {
		opts.submitter = &fakeSubmitter{result: printgw.SubmitResult{Output: "request id is q-1 (1 file(s))\n"}}
	}

	logger := &capturingLogger{}
	svc := printgw.NewService(opts.submitter, opts.fetcher, opts.objectStore, opts.timeouts, opts.s3MaxBytes)
	cfg := config.Config{
		AuthToken:      opts.authToken,
		MaxUploadBytes: opts.maxUpload,
		MaxJSONBytes:   opts.maxJSON,
		PresignTTL:     15 * time.Minute,
		S3Timeout:      5 * time.Second,
	}
	return New(cfg, logger, svc, opts.presigner), logger
}
