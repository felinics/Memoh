//nolint:errorlint // the tests compare chain nodes by identity, as Analyze reports them.
package errs

import (
	"context"
	stderrors "errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/apperror"
)

// Catalog codes the tests use: a 4xx with an allowed arg, a 4xx without, and
// a 5xx.
const (
	codeClient     = apperror.CodeBotNameTaken         // 409, args: field
	codeClientBare = apperror.CodeCapabilityNotFound   // 404
	codeServer     = apperror.CodeWorkspaceUnreachable // 503
)

// causedError stands in for apperror.Error once it implements Cause(): the
// cause is reachable through Cause() only, never through Unwrap.
type causedError struct {
	code  apperror.Code
	cause error
}

func (e causedError) Error() string { return string(e.code) }

func (e causedError) Cause() error { return e.cause }

type causeAndUnwrap struct{ cause error }

func (e causeAndUnwrap) Error() string { return "both: " + e.cause.Error() }

func (e causeAndUnwrap) Cause() error { return e.cause }

func (e causeAndUnwrap) Unwrap() error { return e.cause }

func TestWrapAndFrame(t *testing.T) {
	base := stderrors.New("base")
	wrapped := Wrap(base, "outer", slog.String("id", "outer"))
	wrapped = Wrap(wrapped, "again", slog.String("id", "inner"))
	if !stderrors.Is(wrapped, base) {
		t.Fatal("errors.Is did not penetrate")
	}
	r := Analyze(context.Background(), wrapped)
	if r.Source == nil || !strings.HasSuffix(r.Source.File, "errs_test.go") {
		t.Fatalf("source=%+v", r.Source)
	}
	if r.Attrs[0].Value.String() != "inner" || r.Attrs[0].Key != "id" {
		t.Fatalf("attrs=%v", r.Attrs)
	}
	if r.Fault != apperror.FaultServer || r.Unlocated {
		t.Fatalf("report=%+v", r)
	}
}

func TestWithTimeout(t *testing.T) {
	ctx, cancel := WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	<-ctx.Done()
	if got := Analyze(ctx, context.DeadlineExceeded); got.Fault == apperror.FaultCanceled {
		t.Fatalf("got canceled: %+v", got)
	}
}

func TestJoinAndAs(t *testing.T) {
	pub := apperror.New(codeClientBare, nil)
	err := stderrors.Join(Wrap(stderrors.New("a"), "one"), fmt.Errorf("other: %w", pub))
	var got *apperror.Error
	if !stderrors.As(err, &got) || got != pub {
		t.Fatal("errors.As failed")
	}
	if Analyze(context.Background(), err).answer != pub {
		t.Fatal("analysis failed")
	}
}
