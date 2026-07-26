package runtime

import (
	"errors"
	"testing"

	userruntime "github.com/memohai/memoh/domains/runtime/client"
	"github.com/memohai/memoh/internal/apperror"
)

func TestRuntimeErrorUsesDomainErrors(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantKind apperror.Kind
	}{
		{name: "not found", err: userruntime.ErrRuntimeNotFound, wantKind: apperror.KindNotFound},
		{name: "duplicate name", err: userruntime.ErrRuntimeNameTaken, wantKind: apperror.KindConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := runtimeError(nil, "create user runtime", tt.err)
			if kind := apperror.KindOf(err); kind != tt.wantKind {
				t.Fatalf("kind = %q, want %q", kind, tt.wantKind)
			}
			if !errors.Is(apperror.CauseOf(err), tt.err) {
				t.Fatalf("cause = %v, want %v", apperror.CauseOf(err), tt.err)
			}
		})
	}
}
