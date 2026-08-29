package searchproviders

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/felinics/memoh/internal/apperror"
)

func TestMapSearchProviderWriteError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		err         error
		wantCode    apperror.Code
		wantWrapped bool
	}{
		{
			name: "provider type conflict",
			err: &pgconn.PgError{
				Code:           "23505",
				ConstraintName: "search_providers_team_provider_unique",
			},
			wantCode: apperror.CodeSearchProviderTypeConflict,
		},
		{
			name: "canonical provider type conflict",
			err: fmt.Errorf("wrapped: %w", &pgconn.PgError{
				Code:           "23505",
				ConstraintName: "search_providers_provider_unique",
			}),
			wantCode: apperror.CodeSearchProviderTypeConflict,
		},
		{
			name: "provider name conflict",
			err: &pgconn.PgError{
				Code:           "23505",
				ConstraintName: "search_providers_name_unique",
			},
			wantCode: apperror.CodeProviderNameTaken,
		},
		{
			name:        "infrastructure error",
			err:         errors.New("database unavailable"),
			wantWrapped: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := mapSearchProviderWriteError(tt.err, "write search provider")
			if tt.wantWrapped {
				if !errors.Is(got, tt.err) {
					t.Fatalf("error %v does not wrap %v", got, tt.err)
				}
				return
			}
			if code := apperror.CodeOf(got); code != tt.wantCode {
				t.Fatalf("code = %q, want %q", code, tt.wantCode)
			}
			if cause := apperror.CauseOf(got); !errors.Is(cause, tt.err) {
				t.Fatalf("private cause = %v, want %v", cause, tt.err)
			}
		})
	}
}
