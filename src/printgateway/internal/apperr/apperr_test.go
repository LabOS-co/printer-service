package apperr

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// typedNilHTTPError returns a non-nil error interface whose concrete value
// is a nil *HTTPError pointer — the classic Go footgun, built explicitly so
// TestStatusCodeOf can pin the guard against it.
func typedNilHTTPError() error {
	var e *HTTPError
	return e
}

func TestStatusCodeOf(t *testing.T) {
	t.Parallel()

	plain := errors.New("boom")

	tests := []struct {
		name string
		err  error
		want int
	}{
		{
			// Scoped to StatusCodeOf itself: errors.As on nil is well-defined
			// and false, so this returns the default. It does NOT mean
			// API.fail(w, r, nil) is safe — that call reaches
			// error_handler.HandleError, which invokes Error() on the nil
			// APIError.Err and panics (error_handler@v1.2.4). Whether fail
			// should guard against nil is a question for the httpapi stage;
			// this row pins only the one function under test.
			name: "nil error defaults to 500",
			err:  nil,
			want: http.StatusInternalServerError,
		},
		{
			name: "unclassified error defaults to 500",
			err:  plain,
			want: http.StatusInternalServerError,
		},
		{
			name: "bare HTTPError reports its own status",
			err:  &HTTPError{Status: http.StatusNotFound, Public: "not found"},
			want: http.StatusNotFound,
		},
		{
			name: "found through one layer of wrapping",
			err:  fmt.Errorf("spooling: %w", &HTTPError{Status: http.StatusBadRequest, Public: "bad"}),
			want: http.StatusBadRequest,
		},
		{
			// Nothing in the type bounds how deep the chain may get, and
			// errors.As walks it to the end, so the contract is pinned at
			// more than one level rather than assumed from the single-level
			// case above. (No current call site nests this deeply — httpapi
			// does not wrap at all, it hands err straight to StatusCodeOf —
			// but the guarantee is the type's, not the caller's.)
			name: "found through several layers of wrapping",
			err: fmt.Errorf("handler: %w",
				fmt.Errorf("service: %w", &HTTPError{Status: http.StatusGatewayTimeout, Public: "timed out"})),
			want: http.StatusGatewayTimeout,
		},
		{
			name: "reached through Internal via Unwrap",
			err: fmt.Errorf("outer: %w",
				&HTTPError{Status: http.StatusBadGateway, Public: "upstream", Internal: plain}),
			want: http.StatusBadGateway,
		},
		{
			// errors.As stops at the OUTERMOST match, and two call sites
			// depend on that: printgw.PrintURL (service.go:99-101) and
			// Service.getObject (service.go:158-161) both detect an
			// already-classified error and return it *untouched*, precisely
			// so no outer layer re-classifies a 404 into its own guess. That
			// pass-through is only correct while the outermost status is the
			// one that wins — hence pinning the semantics here. No current
			// code nests an HTTPError inside an HTTPError.
			name: "outermost HTTPError wins over a nested one",
			err: &HTTPError{
				Status:   http.StatusForbidden,
				Public:   "outer",
				Internal: &HTTPError{Status: http.StatusNotFound, Public: "inner"},
			},
			want: http.StatusForbidden,
		},
		{
			// An HTTPError built without a Status is reported as 0 rather
			// than coerced to 500 here, because error_handler.handleAPIError
			// already substitutes 500 for a zero status. Coercing in both
			// places would just hide which one is responsible.
			name: "zero status is reported verbatim, not coerced",
			err:  &HTTPError{Public: "no status set"},
			want: 0,
		},
		{
			// A non-nil error interface whose concrete value is a nil
			// *HTTPError pointer (constructible by e.g. `var e *HTTPError;
			// return e` from a function returning error) still satisfies
			// errors.As, since it matches on the dynamic TYPE, not the
			// value. Without StatusCodeOf's own `httpErr != nil` guard,
			// httpErr.Status below would dereference that nil pointer
			// instead of falling through to the documented 500 default. No
			// production call site constructs one today, but this pins the
			// guard directly rather than resting on that being permanent.
			name: "typed-nil HTTPError falls through to the default, not a panic",
			err:  typedNilHTTPError(),
			want: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := StatusCodeOf(tt.err); got != tt.want {
				t.Errorf("StatusCodeOf(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}

// TestHTTPErrorErrorReturnsPublicOnly guards the reason this type exists.
// error_handler.handleAPIError copies Error() verbatim into the client-facing
// JSON body, so if Error() ever returned Internal — or both — every spool
// path and every line of lp's stderr would be disclosed to the caller.
func TestHTTPErrorErrorReturnsPublicOnly(t *testing.T) {
	t.Parallel()

	const (
		public   = "print submission failed"
		secret   = "/tmp/print-upload-3141592"
		lpStderr = "lp: Error - unknown printer or class"
	)
	err := &HTTPError{
		Status:   http.StatusInternalServerError,
		Public:   public,
		Internal: fmt.Errorf("print failed: path=%q: %s", secret, lpStderr),
	}

	if got := err.Error(); got != public {
		t.Errorf("Error() = %q, want %q", got, public)
	}
	// Deliberately redundant: the equality above already implies this, so
	// this loop can never be the only assertion that fires. It stays because
	// it names what "leak" concretely means here — a spool path and lp's
	// stderr — which the equality check alone does not communicate. Do not
	// "simplify" by deleting the equality check and keeping only this.
	for _, leaked := range []string{secret, lpStderr} {
		if strings.Contains(err.Error(), leaked) {
			t.Errorf("Error() = %q, must not disclose %q", err.Error(), leaked)
		}
	}
}

func TestHTTPErrorUnwrap(t *testing.T) {
	t.Parallel()

	t.Run("errors.Is reaches a sentinel under Internal", func(t *testing.T) {
		t.Parallel()
		// fetch.Fetch classifies its failures exactly this way: the sentinel
		// (errBlockedTarget) is buried under a formatted message, and the
		// caller matches on it rather than on the text.
		sentinel := errors.New("file_url resolved to a disallowed address")
		err := &HTTPError{
			Status:   http.StatusBadRequest,
			Public:   "blocked",
			Internal: fmt.Errorf("dial control: %w", sentinel),
		}
		if !errors.Is(err, sentinel) {
			t.Error("errors.Is did not reach the sentinel through Internal")
		}
	})

	t.Run("nil Internal unwraps to nil", func(t *testing.T) {
		t.Parallel()
		// Most HTTPErrors in this service carry no Internal at all (a pure
		// caller mistake has no server-side detail). errors.Is/As must
		// terminate on those rather than dereference a nil.
		err := &HTTPError{Status: http.StatusBadRequest, Public: "printer is required"}
		if got := errors.Unwrap(err); got != nil {
			t.Errorf("Unwrap() = %v, want nil", got)
		}
		// errors.As must still reach the HTTPError itself and find Internal
		// empty — this is the shape every pure caller-mistake error in the
		// service has, and it is what fail() branches on before deciding
		// whether there is anything to log.
		var httpErr *HTTPError
		if !errors.As(err, &httpErr) {
			t.Fatal("errors.As did not match the HTTPError itself")
		}
		if httpErr.Internal != nil {
			t.Errorf("Internal = %v, want nil", httpErr.Internal)
		}
	})
}
