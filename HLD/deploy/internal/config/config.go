// Package config loads the Print Gateway's runtime configuration.
package config

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

// Config holds the service's runtime configuration.
type Config struct {
	Addr string

	// AuthToken is empty when PRINT_GATEWAY_TOKEN is not set. The caller
	// decides how to react to that (see httpapi.requireToken).
	AuthToken string
}

// Load builds Config from argv (an address override, matching the original
// main()'s os.Args[1] convention) and the environment.
func Load(args []string, getenv func(string) string) Config {
	addr := DefaultAddr
	if len(args) > 1 {
		addr = args[1]
	}

	return Config{
		Addr:      addr,
		AuthToken: getenv(AuthTokenEnv),
	}
}
