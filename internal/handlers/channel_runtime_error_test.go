package handlers

import (
	"errors"
	"testing"

	"github.com/felinics/memoh/internal/apperror"
	runtimeRpc "github.com/felinics/memoh/internal/rpc/runtime"
)

func TestMapChannelRuntimeErrorKeepsCausePrivate(t *testing.T) {
	cause := errors.Join(runtimeRpc.ErrUnavailable, errors.New("dial tcp channel:9091: secret detail"))
	err := mapChannelRuntimeError(cause)
	if got := apperror.CodeOf(err); got != apperror.CodeChannelRuntimeUnavailable {
		t.Fatalf("code = %q", got)
	}
	if got := apperror.CauseOf(err); !errors.Is(got, runtimeRpc.ErrUnavailable) {
		t.Fatalf("cause = %v", got)
	}
	problem, ok := apperror.ProblemFrom(err, "req-1")
	if !ok {
		t.Fatal("expected public problem")
	}
	if problem.Status != 503 || problem.Code != string(apperror.CodeChannelRuntimeUnavailable) {
		t.Fatalf("problem = %#v", problem)
	}
}
