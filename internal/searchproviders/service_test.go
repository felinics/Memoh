package searchproviders

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestMapSearchProviderWriteError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want error
	}{
		{
			name: "provider type conflict",
			err: &pgconn.PgError{
				Code:           "23505",
				ConstraintName: "search_providers_team_provider_unique",
			},
			want: ErrTypeConflict,
		},
		{
			name: "canonical provider type conflict",
			err: fmt.Errorf("wrapped: %w", &pgconn.PgError{
				Code:           "23505",
				ConstraintName: "search_providers_provider_unique",
			}),
			want: ErrTypeConflict,
		},
		{
			name: "provider name conflict",
			err: &pgconn.PgError{
				Code:           "23505",
				ConstraintName: "search_providers_name_unique",
			},
			want: ErrNameTaken,
		},
		{
			name: "infrastructure error",
			err:  errors.New("database unavailable"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := mapSearchProviderWriteError(tt.err, "write search provider")
			if !errors.Is(got, tt.err) {
				t.Fatalf("error %v does not wrap %v", got, tt.err)
			}
			for _, sentinel := range []error{ErrTypeConflict, ErrNameTaken} {
				if want := tt.want != nil && errors.Is(tt.want, sentinel); errors.Is(got, sentinel) != want {
					t.Fatalf("errors.Is(%v, %v) = %t, want %t", got, sentinel, !want, want)
				}
			}
		})
	}
}
