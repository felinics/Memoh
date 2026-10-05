package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/searchproviders"
)

type searchProviderRowQueries struct {
	dbstore.Queries
}

func (searchProviderRowQueries) GetSearchProviderByID(context.Context, pgtype.UUID) (sqlc.SearchProvider, error) {
	return sqlc.SearchProvider{Name: "Brave", Provider: string(searchproviders.ProviderBrave), Config: []byte(`{}`)}, nil
}

func TestSearchProviderWritesAnswerInvalidProviderForAnUnknownType(t *testing.T) {
	h := NewSearchProvidersHandler(slog.New(slog.DiscardHandler), searchproviders.NewService(slog.New(slog.DiscardHandler), searchProviderRowQueries{}))
	cases := []struct {
		name, method, target, body string
	}{
		{"create", http.MethodPost, "/search-providers", `{"name":"mine","provider":"bogus"}`},
		{"update", http.MethodPut, "/search-providers/00000000-0000-0000-0000-000000000001", `{"provider":"bogus"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, problem := serveProblem(t, h.Register, tc.method, tc.target, tc.body)
			if status != http.StatusBadRequest || problem.Code != string(apperror.CodeSearchProviderInvalidProvider) || problem.Fault != "client" {
				t.Fatalf("answer = %d %+v, want 400 %s client", status, problem, apperror.CodeSearchProviderInvalidProvider)
			}
		})
	}
}

// uniqueViolationQueries fails every search provider write on constraint.
type uniqueViolationQueries struct {
	searchProviderRowQueries
	constraint string
}

func (q uniqueViolationQueries) CreateSearchProvider(context.Context, sqlc.CreateSearchProviderParams) (sqlc.SearchProvider, error) {
	return sqlc.SearchProvider{}, &pgconn.PgError{Code: "23505", ConstraintName: q.constraint}
}

func (q uniqueViolationQueries) UpdateSearchProvider(context.Context, sqlc.UpdateSearchProviderParams) (sqlc.SearchProvider, error) {
	return sqlc.SearchProvider{}, &pgconn.PgError{Code: "23505", ConstraintName: q.constraint}
}

func TestSearchProviderWritesAnswerTheConstraintTheyViolated(t *testing.T) {
	cases := []struct {
		name, constraint, method, target, body string
		code                                   apperror.Code
	}{
		{"create with a configured type", "search_providers_provider_unique", http.MethodPost, "/search-providers", `{"name":"mine","provider":"brave"}`, apperror.CodeSearchProviderTypeConflict},
		{"update to a configured type", "search_providers_provider_unique", http.MethodPut, "/search-providers/00000000-0000-0000-0000-000000000001", `{"provider":"bing"}`, apperror.CodeSearchProviderTypeConflict},
		{"create with a taken name", "search_providers_name_unique", http.MethodPost, "/search-providers", `{"name":"mine","provider":"brave"}`, apperror.CodeProviderNameTaken},
		{"update to a taken name", "search_providers_name_unique", http.MethodPut, "/search-providers/00000000-0000-0000-0000-000000000001", `{"name":"theirs"}`, apperror.CodeProviderNameTaken},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			log := slog.New(slog.DiscardHandler)
			h := NewSearchProvidersHandler(log, searchproviders.NewService(log, uniqueViolationQueries{constraint: tc.constraint}))
			status, problem := serveProblem(t, h.Register, tc.method, tc.target, tc.body)
			if status != http.StatusConflict || problem.Code != string(tc.code) || problem.Fault != "client" {
				t.Fatalf("answer = %d %s %s, want 409 %s client", status, problem.Code, problem.Fault, tc.code)
			}
		})
	}
}

func TestSearchProviderErrorTranslatesEachSentinel(t *testing.T) {
	cases := []struct {
		err  error
		want apperror.Code
	}{
		{fmt.Errorf("%w: bogus", searchproviders.ErrInvalidProvider), apperror.CodeSearchProviderInvalidProvider},
		{fmt.Errorf("create search provider: %w", searchproviders.ErrTypeConflict), apperror.CodeSearchProviderTypeConflict},
		{fmt.Errorf("update search provider: %w", searchproviders.ErrNameTaken), apperror.CodeProviderNameTaken},
	}
	for _, tc := range cases {
		got := searchProviderError(tc.err)
		if apperror.CodeOf(got) != tc.want {
			t.Fatalf("searchProviderError(%v) = %q, want %q", tc.err, apperror.CodeOf(got), tc.want)
		}
		if !errors.Is(apperror.CauseOf(got), tc.err) {
			t.Fatalf("searchProviderError(%v) dropped its cause", tc.err)
		}
	}
	coded := apperror.New(apperror.CodeProviderNameTaken, nil)
	other := errors.New("create search provider: connection reset")
	for _, err := range []error{coded, other, nil} {
		if got := searchProviderError(err); got != err { //nolint:errorlint // Identity: an error it does not translate is returned as it is.
			t.Fatalf("searchProviderError(%v) = %v, want it unchanged", err, got)
		}
	}
}
