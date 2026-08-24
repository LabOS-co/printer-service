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
// operator can tune it without a rebuild; a malformed value is a startup
// error rather than a silently-ignored override.
const (
	DefaultReadHeaderTimeout = 10 * time.Second
	DefaultReadTimeout       = 5 * time.Minute // uploads are large; the one value where too tight breaks real clients
	DefaultWriteTimeout      = 1 * time.Minute
	DefaultIdleTimeout       = 60 * time.Second
	DefaultMaxHeaderBytes    = 64 << 10 // 64 KiB
	DefaultShutdownGrace     = 2 * time.Minute

	ReadHeaderTimeoutEnv = "PRINT_GATEWAY_READ_HEADER_TIMEOUT"
	ReadTimeoutEnv       = "PRINT_GATEWAY_READ_TIMEOUT"
	WriteTimeoutEnv      = "PRINT_GATEWAY_WRITE_TIMEOUT"
	IdleTimeoutEnv       = "PRINT_GATEWAY_IDLE_TIMEOUT"
	MaxHeaderBytesEnv    = "PRINT_GATEWAY_MAX_HEADER_BYTES"
	ShutdownGraceEnv     = "PRINT_GATEWAY_SHUTDOWN_GRACE"
)

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
	}

	var err error
	if cfg.ReadHeaderTimeout, err = overrideDuration(getenv, ReadHeaderTimeoutEnv, cfg.ReadHeaderTimeout); err != nil {
		return Config{}, err
	}
	if cfg.ReadTimeout, err = overrideDuration(getenv, ReadTimeoutEnv, cfg.ReadTimeout); err != nil {
		return Config{}, err
	}
	if cfg.WriteTimeout, err = overrideDuration(getenv, WriteTimeoutEnv, cfg.WriteTimeout); err != nil {
		return Config{}, err
	}
	if cfg.IdleTimeout, err = overrideDuration(getenv, IdleTimeoutEnv, cfg.IdleTimeout); err != nil {
		return Config{}, err
	}
	if cfg.ShutdownGrace, err = overrideDuration(getenv, ShutdownGraceEnv, cfg.ShutdownGrace); err != nil {
		return Config{}, err
	}
	if cfg.MaxHeaderBytes, err = overrideBytes(getenv, MaxHeaderBytesEnv, cfg.MaxHeaderBytes); err != nil {
		return Config{}, err
	}

	return cfg, nil
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
	return d, nil
}

func overrideBytes(getenv func(string) string, name string, def int) (int, error) {
	raw := getenv(name)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s: invalid byte size %q, want a positive integer", name, raw)
	}
	return n, nil
}
