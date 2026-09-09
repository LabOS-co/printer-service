// Package cups submits already-spooled files to CUPS via the lp command.
package cups

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"

	"printgateway/internal/apperr"
	"printgateway/internal/printgw"
)

// lp counts documents named as file arguments on its command line, so
// piping the document on stdin (see LPSubmitter's doc comment) makes it
// report "(0 file(s))" even though it printed the piped document — this
// corrects that count back to the one document actually submitted.
var lpFileCountRe = regexp.MustCompile(`\(\d+ file\(s\)\)`)

// LPSubmitter hands a spooled file to CUPS via `lp -d <printer> -t <title>`,
// with the document piped on stdin rather than passed as a path argument
// (so the spool path can't be mistaken for a flag or leak via `ps`/lp errors).
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

	argv := []string{"-d", job.Printer, "-t", job.Title}
	if job.Copies > 1 {
		argv = append(argv, "-n", strconv.Itoa(job.Copies))
	}
	cmd := exec.CommandContext(ctx, "lp", argv...)
	// Don't inherit the full process env (exec.Command's default): that
	// would leak PRINT_GATEWAY_TOKEN/VAULT_TOKEN/SECRET_STORE_PASSWORD to lp
	// and every CUPS filter it spawns. lp only needs PATH and HOME.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
	cmd.Stdin = f
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Internal (temp path + lp's raw stderr) must never reach the
		// client — see apperr.HTTPError.
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
	corrected := lpFileCountRe.ReplaceAll(out, []byte("(1 file(s))"))
	return printgw.SubmitResult{Output: string(corrected)}, nil
}
