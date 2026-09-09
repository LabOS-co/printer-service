package httpapi

import (
	"fmt"
	"net/http"
	"runtime"

	"printgateway/internal/apperr"
	"printgateway/internal/config"
)

const bytesPerMB = 1 << 20

// statusHandler is the network-proxy health check: GET /status, unauthenticated
// (registered outside requireToken) so the proxy can probe liveness without a
// token, mirroring ApplicationHealthCheck's GET /status in the VC++ services.
func (a *API) statusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		a.fail(w, r, &apperr.HTTPError{Status: http.StatusMethodNotAllowed, Public: "use GET"})
		return
	}

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "Running. Label: %s\n Working set memory usage (MB): %d\n Virtual memory usage (MB): %d\n",
		config.ServiceName, mem.HeapAlloc/bytesPerMB, mem.Sys/bytesPerMB)
}
