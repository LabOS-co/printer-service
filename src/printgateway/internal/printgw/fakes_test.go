package printgw

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// The fakes below carry no mutex: Service never spawns a goroutine, and every
// subtest builds its own fakes.

// fakeSubmitter stands in for cups.LPSubmitter.
type fakeSubmitter struct {
	result SubmitResult
	err    error

	// block parks on ctx.Done(), standing in for a wedged CUPS queue. Returns the
	// raw context cause rather than a classified 504: that classification is
	// cups.LPSubmitter's job, not Service's.
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

// fakeFetcher stands in for fetch.SafeFetcher. dstPaths records the *os.File it
// was handed, letting a test assert the spool file was removed even when Service
// returns no path at all.
type fakeFetcher struct {
	body []byte
	err  error

	// partial makes it write body and then fail, leaving a half-written spool
	// file behind if cleanup is wrong.
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

// fakeObject is the io.ReadCloser ObjectStore.Get returns.
type fakeObject struct {
	r       io.Reader
	readErr error // returned once r is drained, if set

	// ctx is the context Get was called with. Reads fail with ctx.Err() once ctx is
	// done, matching objstore's real CloudStorageObject (bound to ctx for its whole
	// read lifetime, not just the call that produced it).
	ctx context.Context

	// beforeRead runs once, on the first Read — used to cancel the caller's context
	// mid-copy.
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

// fakeObjectStore stands in for objstore.MinIO. size is settable independently of
// len(body) so a store that under- or over-reports can be exercised.
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

// read reports whether anything was ever read from the object.
func (s *fakeObjectStore) read() bool {
	return s.object != nil && s.object.didRead
}
