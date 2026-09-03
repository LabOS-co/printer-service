package httpapi

import (
	"context"
	"io"
	"net"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"printgateway/internal/config"
	"printgateway/internal/printgw"
)

// fullConfig builds a Config the way main.go's real startup path does
// (config.Load with every default applied), rather than the zero-value-heavy
// literal newTestAPI uses elsewhere in this package — this test exists
// specifically to catch a regression to the http.Server zero value (P0-6),
// so it must exercise the same defaults production actually ships with.
func fullConfig(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.Load([]string{"printgateway"}, func(key string) string {
		if key == config.AuthTokenEnv {
			return "test-token"
		}
		return ""
	})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

// TestNewServerSetsEveryTimeoutField is the regression guard named in the
// plan's own test table: a future refactor that stops threading one of
// these through from config (reintroducing a slice of the http.Server zero
// value P0-6 eliminated) fails this immediately instead of only showing up
// as a slowloris report.
func TestNewServerSetsEveryTimeoutField(t *testing.T) {
	t.Parallel()

	cfg := fullConfig(t)
	svc := printgw.NewService(&fakeSubmitter{}, nil, nil, printgw.Timeouts{Submit: time.Second}, 0)
	a := New(cfg, &capturingLogger{}, svc, nil)
	srv := NewServer(a)

	// Compared against cfg's own fields, not just "not the zero value": an
	// Opus review of this stage found isZero alone would miss a field swap
	// (e.g. ReadTimeout: a.cfg.IdleTimeout) or a hardcoded literal, since
	// config.Load's defaults are all distinct, non-zero durations/ints.
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"ReadHeaderTimeout", srv.ReadHeaderTimeout, cfg.ReadHeaderTimeout},
		{"ReadTimeout", srv.ReadTimeout, cfg.ReadTimeout},
		{"WriteTimeout", srv.WriteTimeout, cfg.WriteTimeout},
		{"IdleTimeout", srv.IdleTimeout, cfg.IdleTimeout},
		{"MaxHeaderBytes", srv.MaxHeaderBytes, cfg.MaxHeaderBytes},
	}
	for _, c := range checks {
		if isZero(c.got) {
			t.Errorf("%s is the zero value; every field here must come from config (P0-6)", c.name)
		}
		if c.got != c.want {
			t.Errorf("%s = %v, want cfg's own %v (a field swap or hardcoded literal)", c.name, c.got, c.want)
		}
	}
	if srv.ErrorLog == nil {
		t.Error("ErrorLog is nil; net/http's own error lines would fall through to stderr instead of the labOS log stream")
	}
	if srv.Handler == nil {
		t.Error("Handler is nil")
	}
}

func isZero(v any) bool {
	return reflect.ValueOf(v).IsZero()
}

// TestErrorLogWriterForwardsToTheLogger pins that net/http's own error lines
// (a tripped ReadHeaderTimeout, an internal panic recovered by the stdlib
// server itself) join the same labOS log stream instead of falling through
// to net/http's default os.Stderr logger.
func TestErrorLogWriterForwardsToTheLogger(t *testing.T) {
	t.Parallel()

	logger := &capturingLogger{}
	w := errorLogWriter{logger: logger}

	n, err := w.Write([]byte("http: TLS handshake error from 1.2.3.4: EOF\n"))
	if err != nil {
		t.Fatalf("Write returned an error: %v", err)
	}
	if n != len("http: TLS handshake error from 1.2.3.4: EOF\n") {
		t.Errorf("Write returned n=%d, want the full input length (io.Writer contract)", n)
	}

	errs := logger.snapshotErrors()
	if len(errs) != 1 {
		t.Fatalf("LogError called %d times, want 1", len(errs))
	}
	if strings.Contains(errs[0], "\n") {
		t.Errorf("the trailing newline should be trimmed before logging: %q", errs[0])
	}
	if !strings.Contains(errs[0], "TLS handshake error") {
		t.Errorf("logged message = %q, want it to contain the original text", errs[0])
	}
}

// TestGracefulShutdownLetsAnInFlightRequestFinish drives a real net/http
// server (not httptest.NewServer, which has no exported Shutdown hook) so
// Shutdown is exercised against the exact *http.Server NewServer builds.
func TestGracefulShutdownLetsAnInFlightRequestFinish(t *testing.T) {
	t.Parallel()

	svc := printgw.NewService(&fakeSubmitter{}, nil, nil, printgw.Timeouts{Submit: time.Second}, 0)
	cfg := fullConfig(t)
	cfg.Addr = "127.0.0.1:0"
	a := New(cfg, &capturingLogger{}, svc, nil)
	srv := NewServer(a)

	handlerStarted := make(chan struct{})
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		close(handlerStarted)
		<-release
		w.WriteHeader(http.StatusOK)
	})
	srv.Handler = mux

	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		t.Fatal(err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	reqDone := make(chan *http.Response, 1)
	reqErr := make(chan error, 1)
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String() + "/slow")
		if err != nil {
			reqErr <- err
			return
		}
		reqDone <- resp
	}()

	select {
	case <-handlerStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never started")
	}

	shutdownDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownDone <- srv.Shutdown(ctx)
	}()

	// Poll for Shutdown's real, observable effect - it closes the tracked
	// listener immediately on entry, so a new dial to the same address
	// starts failing right away - rather than sleeping a fixed guess. An
	// Opus review of this stage found the previous fixed 50ms sleep would
	// pass identically whether Shutdown had actually started or not (this
	// test's pass/fail never depended on it), silently degrading to "an
	// in-flight request completes" instead of "...completes *across*
	// Shutdown" under any load that pushed Shutdown's start past 50ms.
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, dialErr := net.DialTimeout("tcp", ln.Addr().String(), 100*time.Millisecond)
		if dialErr != nil {
			break // the listener is closed - Shutdown has genuinely begun
		}
		conn.Close()
		if time.Now().After(deadline) {
			t.Fatal("Shutdown never closed the listener within 5s")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(release)

	select {
	case resp := <-reqDone:
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("in-flight request status = %d, want 200", resp.StatusCode)
		}
	case err := <-reqErr:
		t.Fatalf("in-flight request failed instead of completing: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight request never completed")
	}

	if err := <-shutdownDone; err != nil {
		t.Errorf("Shutdown returned an error: %v", err)
	}
	if err := <-serveErr; err != http.ErrServerClosed {
		t.Errorf("Serve returned %v, want http.ErrServerClosed", err)
	}
}
