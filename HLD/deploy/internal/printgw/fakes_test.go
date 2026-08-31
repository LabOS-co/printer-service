package printgw

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// The fakes below carry no mutex. Service runs a request entirely on the
// calling goroutine — it contains no `go` statement — and every subtest builds
// its own fakes, so nothing here is ever touched from two goroutines at once.
// The one place that is not obvious is fakeSubmitter.block, which parks on
// ctx.Done() inside the caller's goroutine rather than a new one.

// fakeSubmitter stands in for cups.LPSubmitter. lp does not exist on the
// Windows dev box, so without this seam none of these tests could run at all;
// more importantly, block is the only way to exercise the wedged-queue
// deadline that P0-1 is about.
type fakeSubmitter struct {
	result SubmitResult
	err    error

	// block parks until ctx is done, standing in for a wedged CUPS queue. It
	// wraps ctx's error with %w so a test can assert with errors.Is. Note it
	// does NOT return a 504 *apperr.HTTPError: classifying a timeout is
	// cups.LPSubmitter's job (lp.go), not Service's, so a test asserting on
	// Service must see the raw context cause here, not a status.
	block bool

	// inspect runs at submission time, while the spooled file still exists.
	inspect func(job SubmitJob)

	jobs      []SubmitJob
	deadlines []time.Time
}

func (f *fakeSubmitter) Submit(ctx context.Context, job SubmitJob) (SubmitResult, error) {
	f.jobs = append(f.jobs, job)
	if d, ok := ctx.Deadline(); ok {
		f.deadlines = append(f.deadlines, d)
	}
	if f.inspect != nil {
		f.inspect(job)
	}
	if f.block {
		<-ctx.Done()
		return SubmitResult{}, fmt.Errorf("submit aborted: %w", ctx.Err())
	}
	if err := ctx.Err(); err != nil {
		// A caller-cancelled context must not be silently ignored: the real
		// submitter's exec.CommandContext would fail immediately too.
		return SubmitResult{}, fmt.Errorf("submit not attempted: %w", err)
	}
	return f.result, f.err
}

func (f *fakeSubmitter) called() bool { return len(f.jobs) > 0 }

func (f *fakeSubmitter) lastJob() SubmitJob {
	if len(f.jobs) == 0 {
		return SubmitJob{}
	}
	return f.jobs[len(f.jobs)-1]
}

// fakeFetcher stands in for fetch.SafeFetcher. Service tests must not open
// sockets, and the "fetch fails after a partial write, so the temp file is
// removed and lp is never invoked" invariant needs a fake to provoke.
//
// dstPaths records the *os.File it was handed — spoolTo passes the real file
// as the io.Writer, which is what lets a test assert the spool file was
// removed even on paths where Service returns no path at all.
type fakeFetcher struct {
	body []byte
	err  error

	// partial makes it write body and THEN fail, the case that leaves a
	// half-written spool file behind if cleanup is wrong.
	partial bool

	urls      []string
	dstPaths  []string
	deadlines []time.Time
}

func (f *fakeFetcher) Fetch(ctx context.Context, rawURL string, dst io.Writer) (int64, error) {
	f.urls = append(f.urls, rawURL)
	if file, ok := dst.(interface{ Name() string }); ok {
		f.dstPaths = append(f.dstPaths, file.Name())
	}
	if d, ok := ctx.Deadline(); ok {
		f.deadlines = append(f.deadlines, d)
	}

	// The real SafeFetcher dials through this ctx, so a caller that has
	// already gone away fails here before any byte is written. Modelling that
	// is what makes a cancellation test meaningful rather than decorative.
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("fetch not attempted: %w", err)
	}

	if f.err != nil && !f.partial {
		return 0, f.err
	}
	n, writeErr := dst.Write(f.body)
	if writeErr != nil {
		return int64(n), writeErr
	}
	if f.err != nil {
		return int64(n), f.err
	}
	return int64(n), nil
}

func (f *fakeFetcher) called() bool { return len(f.urls) > 0 }

func (f *fakeFetcher) dstPath() string {
	if len(f.dstPaths) == 0 {
		return ""
	}
	return f.dstPaths[len(f.dstPaths)-1]
}

// fakeObject is the io.ReadCloser ObjectStore.Get returns. closes counts Close
// calls: Service must close the store's stream on every path, including the
// ones where it rejects the object without reading it.
type fakeObject struct {
	r       io.Reader
	readErr error // returned once r is drained, if set

	// ctx is the context Get was called with. The real CloudStorageObject is
	// bound to its originating ctx for its ENTIRE read lifetime — a Read after
	// ctx is done fails with ctx's error, not merely the call that produced it
	// (see objstore/minio.go's Get doc comment). Modelling that here is what
	// lets the 504 case be provoked the way production would produce it,
	// rather than with an invented read error whose text only looks the part.
	ctx context.Context

	// beforeRead runs once, on the first Read. Used to cancel the caller's
	// context mid-copy.
	beforeRead func()

	closes  int
	didRead bool
}

func (o *fakeObject) Read(p []byte) (int, error) {
	if !o.didRead {
		o.didRead = true
		if o.beforeRead != nil {
			o.beforeRead()
		}
	}
	if o.ctx != nil {
		if err := o.ctx.Err(); err != nil {
			return 0, err
		}
	}
	n, err := o.r.Read(p)
	if errors.Is(err, io.EOF) && o.readErr != nil {
		return n, o.readErr
	}
	return n, err
}

func (o *fakeObject) Close() error {
	o.closes++
	return nil
}

// fakeObjectStore stands in for objstore.MinIO. There is no MinIO on the dev
// box or in CI, and size is deliberately settable independently of len(body)
// so a store that under-reports, over-reports, or answers with a sentinel can
// be exercised — that divergence is the whole reason getObject verifies the
// copied count instead of trusting the metadata.
type fakeObjectStore struct {
	body []byte
	size int64
	err  error

	object *fakeObject

	keys      []string
	deadlines []time.Time
}

func (s *fakeObjectStore) Get(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	s.keys = append(s.keys, key)
	if d, ok := ctx.Deadline(); ok {
		s.deadlines = append(s.deadlines, d)
	}
	if s.err != nil {
		// The real Get returns a nil ReadCloser alongside its error
		// (objstore/minio.go), so callers must never defer Close on it.
		return nil, 0, s.err
	}
	if s.object == nil {
		s.object = &fakeObject{}
	}
	if s.object.r == nil {
		s.object.r = bytes.NewReader(s.body)
	}
	s.object.ctx = ctx
	return s.object, s.size, nil
}

func (s *fakeObjectStore) called() bool { return len(s.keys) > 0 }

func (s *fakeObjectStore) closes() int {
	if s.object == nil {
		return 0
	}
	return s.object.closes
}

// read reports whether anything was ever read from the object — the handle for
// asserting that a rejection happened BEFORE any byte was copied.
func (s *fakeObjectStore) read() bool {
	return s.object != nil && s.object.didRead
}
