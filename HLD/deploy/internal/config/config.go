// Package config loads the Print Gateway's runtime configuration.
package config

import (
	"fmt"
	"strconv"
	"time"
)

const (
	// DefaultAddr is used when no address is given on the command line.
	// Loopback-only: the server is reachable only from the machine it runs
	// on unless an address is passed deliberately.
	DefaultAddr = "127.0.0.1:8090"

	// AuthTokenEnv is the environment variable carrying the shared secret
	// compared against the X-Labos-Print-Token header on every request.
	AuthTokenEnv = "PRINT_GATEWAY_TOKEN"

	// ServiceName identifies this service in logs.LogMetaData.
	ServiceName = "printgateway"
)

// Server timeouts and limits (P0-6). A zero-value *http.Server has none of
// these, which is what let a slow or silent client hold a connection open
// forever with nothing to notice or reclaim it. Each has an env var so an
// operator can tune it without a rebuild; a malformed, non-positive, or
// mutually inconsistent value is a startup error rather than a
// silently-ignored override (see Load and validate).
const (
	DefaultReadHeaderTimeout = 10 * time.Second
	DefaultReadTimeout       = 5 * time.Minute // uploads are large; the one value where too tight breaks real clients

	// DefaultWriteTimeout must dominate DefaultReadTimeout, and by enough to
	// cover the work the handler does after the body is in.
	//
	// net/http does NOT start the write deadline when the response starts. In
	// conn.readRequest (net/http/server.go, Go 1.25) the deadline is armed in
	// a defer that fires as soon as the *headers* are parsed:
	//
	//	if d := c.server.WriteTimeout; d > 0 {
	//		defer func() { c.rwc.SetWriteDeadline(time.Now().Add(d)) }()
	//	}
	//
	// so WriteTimeout is the budget for reading the body, spooling it to
	// disk, downloading file_url, running lp, AND writing the response — not
	// just the last of those. At the 1m this originally shipped with, any
	// request slower than a minute was read successfully and submitted to
	// CUPS, and then failed on the response write: the caller saw a reset
	// connection, retried, and the document printed twice. Keep the
	// WriteTimeout > ReadTimeout invariant that validate enforces.
	DefaultWriteTimeout = 6 * time.Minute

	DefaultIdleTimeout    = 60 * time.Second
	DefaultMaxHeaderBytes = 64 << 10 // 64 KiB
	DefaultShutdownGrace  = 2 * time.Minute

	ReadHeaderTimeoutEnv = "PRINT_GATEWAY_READ_HEADER_TIMEOUT"
	ReadTimeoutEnv       = "PRINT_GATEWAY_READ_TIMEOUT"
	WriteTimeoutEnv      = "PRINT_GATEWAY_WRITE_TIMEOUT"
	IdleTimeoutEnv       = "PRINT_GATEWAY_IDLE_TIMEOUT"
	MaxHeaderBytesEnv    = "PRINT_GATEWAY_MAX_HEADER_BYTES"
	ShutdownGraceEnv     = "PRINT_GATEWAY_SHUTDOWN_GRACE"
)

// DefaultSubmitTimeout bounds how long a single `lp` invocation may run
// (P0-1). Without it, a wedged CUPS queue hangs the handler goroutine
// forever: defer cleanup() never runs, leaking the goroutine, the lp
// process, the spooled temp file, and the client connection, permanently,
// per request. printgw.Service applies this as a context.WithTimeout around
// the Submit call, and cups.LPSubmitter's exec.CommandContext is what turns
// that expiry into the child process actually being killed.
//
// NOTE: not yet included in a ShutdownGrace-vs-budget startup assertion —
// that needs FetchTimeout too, which isn't wired to real cancellation until
// a later step (SSRF hardening).
const DefaultSubmitTimeout = 30 * time.Second

const SubmitTimeoutEnv = "PRINT_GATEWAY_SUBMIT_TIMEOUT"

// Config holds the service's runtime configuration.
type Config struct {
	Addr string

	// AuthToken is empty when PRINT_GATEWAY_TOKEN is not set. The caller
	// decides how to react to that (see httpapi.requireToken).
	AuthToken string

	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	MaxHeaderBytes    int

	// ShutdownGrace bounds how long Shutdown waits for in-flight requests
	// to finish before main forces an exit.
	ShutdownGrace time.Duration

	// SubmitTimeout bounds a single lp invocation (P0-1).
	SubmitTimeout time.Duration
}

// Load builds Config from argv (an address override, matching the original
// main()'s os.Args[1] convention) and the environment. It fails on a
// present-but-malformed override rather than silently keeping the default,
// naming the offending variable.
func Load(args []string, getenv func(string) string) (Config, error) {
	addr := DefaultAddr
	if len(args) > 1 {
		addr = args[1]
	}

	cfg := Config{
		Addr:      addr,
		AuthToken: getenv(AuthTokenEnv),

		ReadHeaderTimeout: DefaultReadHeaderTimeout,
		ReadTimeout:       DefaultReadTimeout,
		WriteTimeout:      DefaultWriteTimeout,
		IdleTimeout:       DefaultIdleTimeout,
		MaxHeaderBytes:    DefaultMaxHeaderBytes,
		ShutdownGrace:     DefaultShutdownGrace,
		SubmitTimeout:     DefaultSubmitTimeout,
	}

	// Table rather than one if-block per value: the repeated form made the
	// env-var/field pairing a copy-paste field, where writing ReadTimeoutEnv
	// into &cfg.WriteTimeout would compile, vet clean, and be invisible in
	// review. Here each pairing appears exactly once, on one line.
	for _, d := range []struct {
		name string
		dst  *time.Duration
	}{
		{ReadHeaderTimeoutEnv, &cfg.ReadHeaderTimeout},
		{ReadTimeoutEnv, &cfg.ReadTimeout},
		{WriteTimeoutEnv, &cfg.WriteTimeout},
		{IdleTimeoutEnv, &cfg.IdleTimeout},
		{ShutdownGraceEnv, &cfg.ShutdownGrace},
		{SubmitTimeoutEnv, &cfg.SubmitTimeout},
	} {
		v, err := overrideDuration(getenv, d.name, *d.dst)
		if err != nil {
			return Config{}, err
		}
		*d.dst = v
	}

	n, err := overrideBytes(getenv, MaxHeaderBytesEnv, cfg.MaxHeaderBytes)
	if err != nil {
		return Config{}, err
	}
	cfg.MaxHeaderBytes = n

	if err := validate(cfg); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// validate enforces the relationships *between* values. Each one is
// individually plausible and only the combination is wrong, which is
// exactly the class of mistake nothing downstream reports: net/http simply
// applies whichever deadline expires first, leaving an operator to work out
// from a reset connection why a request that was clearly inside the read
// budget died anyway.
func validate(cfg Config) error {
	// A header deadline that outlives the whole-request deadline can never
	// be the one that fires, so setting it is a no-op the operator will
	// believe took effect.
	if cfg.ReadHeaderTimeout > cfg.ReadTimeout {
		return fmt.Errorf("%s (%s) must not exceed %s (%s)",
			ReadHeaderTimeoutEnv, cfg.ReadHeaderTimeout, ReadTimeoutEnv, cfg.ReadTimeout)
	}

	// See DefaultWriteTimeout: the write deadline is armed at header-parse
	// time, so it has to cover the body read too. If it does not, a slow
	// upload is accepted and printed and then fails on the response write —
	// the caller sees failure for a job that succeeded, and retries it.
	if cfg.WriteTimeout <= cfg.ReadTimeout {
		return fmt.Errorf("%s (%s) must exceed %s (%s): the write deadline starts when request headers are parsed, so it must cover reading the body as well as sending the response",
			WriteTimeoutEnv, cfg.WriteTimeout, ReadTimeoutEnv, cfg.ReadTimeout)
	}

	return nil
}

func overrideDuration(getenv func(string) string, name string, def time.Duration) (time.Duration, error) {
	raw := getenv(name)
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid duration %q: %w", name, raw, err)
	}
	// A non-positive duration is not a tuning choice. net/http guards every
	// timeout with `if d > 0`, so "0" or "-5s" — a plausible typo — does not
	// mean "very short", it means *no timeout at all*, silently restoring the
	// exact exposure these values exist to close. Reject it the same way a
	// non-positive byte size is rejected, rather than inheriting net/http's
	// semantics as an undocumented escape hatch.
	if d <= 0 {
		return 0, fmt.Errorf("%s: %q must be positive; net/http reads a non-positive timeout as no timeout at all", name, raw)
	}
	return d, nil
}

func overrideBytes(getenv func(string) string, name string, def int) (int, error) {
	raw := getenv(name)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s: invalid byte size %q, want a positive integer number of bytes", name, raw)
	}
	return n, nil
}
