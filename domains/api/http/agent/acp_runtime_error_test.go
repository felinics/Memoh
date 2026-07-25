package agent

import (
	"errors"
	"fmt"
	"testing"

	acpagent "github.com/memohai/memoh/domains/agent/acp"
	acpclient "github.com/memohai/memoh/domains/agent/acp/client"
	"github.com/memohai/memoh/internal/apperror"
)

func TestRuntimePoolConfigFailureUsesApplicationError(t *testing.T) {
	cause := fmt.Errorf("%w: transport closed", acpagent.ErrRuntimeConfigUpdateFailed)
	err := runtimePoolError(cause)
	if got := apperror.CodeOf(err); got != apperror.CodeACPConfigUpdateFailed {
		t.Fatalf("runtimePoolError() code = %q, want %q", got, apperror.CodeACPConfigUpdateFailed)
	}
	if got := apperror.CauseOf(err); !errors.Is(got, cause) {
		t.Fatalf("runtimePoolError() cause = %v, want private cause", got)
	}
}

func TestRuntimePoolSelectionErrorsUseApplicationErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		code apperror.Code
	}{
		{
			name: "unsupported",
			err:  acpclient.ErrModelSelectionUnsupported,
			code: apperror.CodeACPModelSelectionUnsupported,
		},
		{
			name: "unavailable",
			err:  fmt.Errorf("%w: stale-model", acpclient.ErrModelUnavailable),
			code: apperror.CodeACPModelUnavailable,
		},
		{
			name: "missing",
			err:  acpclient.ErrModelIDRequired,
			code: apperror.CodeACPModelIDRequired,
		},
		{
			name: "reasoning unsupported",
			err:  acpclient.ErrReasoningSelectionUnsupported,
			code: apperror.CodeACPReasoningUnsupported,
		},
		{
			name: "reasoning unavailable",
			err:  fmt.Errorf("%w: stale-effort", acpclient.ErrReasoningEffortUnavailable),
			code: apperror.CodeACPReasoningUnavailable,
		},
		{
			name: "reasoning missing",
			err:  acpclient.ErrReasoningEffortRequired,
			code: apperror.CodeACPReasoningEffortRequired,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := apperror.CodeOf(runtimePoolError(tt.err)); got != tt.code {
				t.Fatalf("runtimePoolError() code = %q, want %q", got, tt.code)
			}
		})
	}
}
