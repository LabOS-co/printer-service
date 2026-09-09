package apperr

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// typedNilHTTPError returns a non-nil error interface whose concrete value
// is a nil *HTTPError pointer, to exercise StatusCodeOf's nil guard.
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
			// errors.As stops at the outermost match; printgw.PrintURL and
			// Service.getObject both rely on this to pass an
			// already-classified error through untouched.
			name: "outermost HTTPError wins over a nested one",
			err: &HTTPError{
				Status:   http.StatusForbidden,
				Public:   "outer",
				Internal: &HTTPError{Status: http.StatusNotFound, Public: "inner"},
			},
			want: http.StatusForbidden,
		},
		{
			// A zero Status is reported verbatim; error_handler.handleAPIError
			// is responsible for substituting 500, not this function.
			name: "zero status is reported verbatim, not coerced",
			err:  &HTTPError{Public: "no status set"},
			want: 0,
		},
		{
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

// TestHTTPErrorErrorReturnsPublicOnly guards against Internal ever leaking
// into the client-facing JSON body via error_handler.handleAPIError.
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
		err := &HTTPError{Status: http.StatusBadRequest, Public: "printer is required"}
		if got := errors.Unwrap(err); got != nil {
			t.Errorf("Unwrap() = %v, want nil", got)
		}
		var httpErr *HTTPError
		if !errors.As(err, &httpErr) {
			t.Fatal("errors.As did not match the HTTPError itself")
		}
		if httpErr.Internal != nil {
			t.Errorf("Internal = %v, want nil", httpErr.Internal)
		}
	})
}
