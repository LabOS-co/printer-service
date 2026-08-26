// Package cups submits already-spooled files to CUPS via the lp command.
package cups

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"

	"printgateway/internal/apperr"
	"printgateway/internal/printgw"
)

// LPSubmitter hands a spooled file to CUPS via `lp -d <printer> -t <title>`,
// with the document piped on stdin rather than passed as a path argument.
//
// Passing ctx through exec.CommandContext (P0-1) means a wedged queue no
// longer hangs the handler forever: on expiry/cancellation the child is
// killed and the goroutine, process, and temp file (removed by the caller's
// cleanup) are all reclaimed instead of leaking.
//
// Dropping the path operand from argv makes filename-based argument
// injection structurally impossible rather than merely guarded against — the
// temp path never appears in argv, so it can't be mistaken for a flag and
// never shows up in `ps` or an lp error string either.
type LPSubmitter struct{}

func NewLPSubmitter() *LPSubmitter { return &LPSubmitter{} }

func (s *LPSubmitter) Submit(ctx context.Context, job printgw.SubmitJob) (printgw.SubmitResult, error) {
	f, err := os.Open(job.Path)
	if err != nil {
		return printgw.SubmitResult{}, &apperr.HTTPError{
			Status:   http.StatusInternalServerError,
			Public:   "print submission failed",
			Internal: fmt.Errorf("opening spooled file %q: %w", job.Path, err),
		}
	}
	defer f.Close()

	cmd := exec.CommandContext(ctx, "lp", "-d", job.Printer, "-t", job.Title)
	// exec.Command's default is to hand the child the WHOLE process
	// environment. That used to only leak PRINT_GATEWAY_TOKEN; since F1
	// (Vault secrets) it would also leak VAULT_TOKEN/SECRET_STORE_PASSWORD —
	// materially more valuable credentials — to lp and every CUPS filter it
	// spawns, visible via e.g. /proc/<pid>/environ. lp needs none of that:
	// PATH to find its own helpers, HOME for CUPS client-side config lookups
	// (~/.cups). Nothing else is passed through.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
	cmd.Stdin = f
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Internal carries the temp-file path and lp's raw stderr — both
		// disclose server-side detail (CUPS queue/backend names in
		// particular) and must never reach the client; see apperr.HTTPError.
		if ctx.Err() != nil {
			return printgw.SubmitResult{}, &apperr.HTTPError{
				Status: http.StatusGatewayTimeout,
				Public: "print submission timed out",
				Internal: fmt.Errorf("print timed out/cancelled: printer=%q path=%q ctxErr=%v err=%w output=%s",
					job.Printer, job.Path, ctx.Err(), err, out),
			}
		}
		return printgw.SubmitResult{}, &apperr.HTTPError{
			Status: http.StatusInternalServerError,
			Public: "print submission failed",
			Internal: fmt.Errorf("print failed: printer=%q path=%q err=%w output=%s",
				job.Printer, job.Path, err, out),
		}
	}
	return printgw.SubmitResult{Output: string(out)}, nil
}
