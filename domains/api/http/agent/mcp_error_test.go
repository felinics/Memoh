package agent

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	mcppersistence "github.com/memohai/memoh/domains/agent/mcp/persistence"
	"github.com/memohai/memoh/internal/apperror"
)

func TestMCPConnectionServiceError(t *testing.T) {
	tests := []struct {
		name           string
		err            error
		fallbackStatus int
		wantKind       apperror.Kind
	}{
		{
			name:           "not found",
			err:            fmt.Errorf("get connection: %w", mcppersistence.ErrNotFound),
			fallbackStatus: http.StatusInternalServerError,
			wantKind:       apperror.KindNotFound,
		},
		{
			name:           "bad request fallback",
			err:            errors.New("name is required"),
			fallbackStatus: http.StatusBadRequest,
			wantKind:       apperror.KindInvalid,
		},
		{
			name:           "internal error fallback",
			err:            errors.New("storage unavailable"),
			fallbackStatus: http.StatusInternalServerError,
			wantKind:       apperror.KindInternal,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := mcpConnectionServiceError(test.err, test.fallbackStatus)
			if kind := apperror.KindOf(got); kind != test.wantKind {
				t.Fatalf("mcpConnectionServiceError() kind = %s, want %s", kind, test.wantKind)
			}
			if cause := apperror.CauseOf(got); !errors.Is(cause, test.err) {
				t.Fatalf("mcpConnectionServiceError() cause = %v, want %v", cause, test.err)
			}
		})
	}
}
