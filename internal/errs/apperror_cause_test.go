//nolint:errorlint // the test inspects the outermost node by identity, as Analyze reports it.
package errs

import (
	"context"
	"fmt"
	"log/slog"
	"testing"

	"github.com/felinics/memoh/internal/apperror"
)

// The real *apperror.Error, not a stand-in, exposes its private cause to
// diagnostics through Cause().
func TestAppErrorCauseIsTraversed(t *testing.T) {
	t.Parallel()
	source := New("source", slog.String("id", "source"))
	err := fmt.Errorf("handler: %w", apperror.Wrap(apperror.CodeHTTPConflict, fmt.Errorf("use case: %w", source), nil))

	r := Analyze(context.Background(), err)
	if r.answer == nil || apperror.CodeOf(r.answer) != apperror.CodeHTTPConflict || r.Fault != apperror.FaultClient {
		t.Fatalf("public error not recognized: %+v", r)
	}
	if r.Source == nil || len(r.Attrs) != 1 || r.Attrs[0].Value.String() != "source" {
		t.Fatalf("Cause() not traversed: %+v", r)
	}
	if !hasStack(err) {
		t.Fatal("hasStack did not follow Cause()")
	}
	outer := Wrap(err, "outer")
	if e, ok := outer.(*faultError); !ok || len(e.stack) != 0 {
		t.Fatal("Wrap recorded a stack although one exists under Cause()")
	}
}
