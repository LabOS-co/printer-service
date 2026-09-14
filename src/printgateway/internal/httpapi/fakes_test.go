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
// on. Mutex-guarded since it's shared across the concurrent middleware test.
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

// snapshotCompletions/snapshotErrors/snapshotInfos are the race-safe way to
// read these slices from a test: an HTTP round trip completing gives no
// happens-before guarantee that the server-side goroutine's deferred
// bookkeeping has finished, so reads must go through the same mutex as the
// writers.
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
// completions, or gives up after two seconds.
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

// fakeSubmitter stands in for cups.LPSubmitter. Mutex-guarded since the
// concurrency test drives many goroutines through one shared Service/Submitter.
type fakeSubmitter struct {
	mu     sync.Mutex
	result printgw.SubmitResult
	err    error
	jobs   []printgw.SubmitJob

	// spooledBodies captures the bytes at job.Path read DURING Submit, since
	// printgw.Service deletes the spool file as soon as Submit returns.
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

// snapshotJobs returns a copy of every job Submit has recorded so far.
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

// fakeFetcher stands in for fetch.SafeFetcher, recording rawURL so a test
// can assert the handler passed through the URL it actually received.
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

// fakeObjectStore stands in for objstore.MinIO. err should normally already
// be an *apperr.HTTPError per ports.go's contract; a plain error is used
// only to exercise Service's own re-wrap of an unclassified error.
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
// httpapi's own narrow Presigner interface. getURL/putURL are kept separate
// so a test can tell GET and PUT results apart, and every call is recorded
// so a test can assert which method/key/ttl was actually sent.
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

// testAPIOpts configures newTestAPI; every field defaults sensibly in
// newTestAPI itself, so a test only sets what it cares about.
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

	// requireAuth: nil defaults to true, matching production's mandatory-auth behavior before
	// RequireAuth existed, so existing token-enforcement tests don't have to opt back in. Pass a
	// non-nil false to test the auth-disabled path.
	requireAuth *bool
}

// newTestAPI builds a real *API over a real printgw.Service, wired to fakes
// at the lowest layer (Submitter/Fetcher/ObjectStore/Presigner).
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

	requireAuth := true
	if opts.requireAuth != nil {
		requireAuth = *opts.requireAuth
	}

	logger := &capturingLogger{}
	svc := printgw.NewService(opts.submitter, opts.fetcher, opts.objectStore, opts.timeouts, opts.s3MaxBytes)
	cfg := config.Config{
		AuthToken:      opts.authToken,
		RequireAuth:    requireAuth,
		MaxUploadBytes: opts.maxUpload,
		MaxJSONBytes:   opts.maxJSON,
		PresignTTL:     15 * time.Minute,
		S3Timeout:      5 * time.Second,
	}
	return New(cfg, logger, svc, opts.presigner), logger
}
