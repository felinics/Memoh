package searchproviders

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/felinics/memoh/internal/apperror"
)

func TestFirecrawlProviderMetadata(t *testing.T) {
	service := &Service{}
	if !isValidProviderName(ProviderFirecrawl) {
		t.Fatal("Firecrawl must be accepted as a search provider")
	}
	for _, meta := range service.ListMeta(context.Background()) {
		if meta.Provider != string(ProviderFirecrawl) {
			continue
		}
		if meta.DisplayName != "Firecrawl" {
			t.Fatalf("display name = %q", meta.DisplayName)
		}
		fields := meta.ConfigSchema.Fields
		if fields["api_key"].Type != "secret" || !fields["api_key"].Required {
			t.Fatalf("api_key schema = %#v", fields["api_key"])
		}
		if fields["base_url"].Example != "https://api.firecrawl.dev/v2/search" {
			t.Fatalf("base_url schema = %#v", fields["base_url"])
		}
		return
	}
	t.Fatal("Firecrawl metadata not found")
}

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
