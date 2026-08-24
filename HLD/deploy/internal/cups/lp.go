// Package cups submits already-spooled files to CUPS via the lp command.
package cups

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"

	"printgateway/internal/apperr"
	"printgateway/internal/printgw"
)

// LPSubmitter hands a spooled file to CUPS via `lp -d <printer> <path>`.
//
// NOTE: straight extraction of the original inline exec.Command call — no
// context cancellation/timeout yet, and the document is still passed as a
// path argument rather than on stdin. Both are hardened in a later step.
type LPSubmitter struct{}

func NewLPSubmitter() *LPSubmitter { return &LPSubmitter{} }

func (s *LPSubmitter) Submit(ctx context.Context, job printgw.SubmitJob) (printgw.SubmitResult, error) {
	cmd := exec.Command("lp", "-d", job.Printer, job.Path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Internal carries the temp-file path and lp's raw stderr — both
		// disclose server-side detail (CUPS queue/backend names in
		// particular) and must never reach the client; see apperr.HTTPError.
		return printgw.SubmitResult{}, &apperr.HTTPError{
			Status: http.StatusInternalServerError,
			Public: "print submission failed",
			Internal: fmt.Errorf("print failed: printer=%q path=%q err=%w output=%s",
				job.Printer, job.Path, err, out),
		}
	}
	return printgw.SubmitResult{Output: string(out)}, nil
}
