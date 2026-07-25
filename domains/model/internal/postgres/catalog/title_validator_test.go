package catalog

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	dbsqlc "github.com/memohai/memoh/domains/model/internal/postgres/sqlc"
)

type titleModelQueriesFake struct {
	model dbsqlc.ModelModel
	err   error
}

func (f titleModelQueriesFake) GetModelByID(context.Context, pgtype.UUID) (dbsqlc.ModelModel, error) {
	return f.model, f.err
}

func TestTitleModelValidator(t *testing.T) {
	const id = "a7b29eb7-6bba-49ed-b18a-d15aa90b430d"
	sentinel := errors.New("query failed")
	tests := map[string]struct {
		id      string
		queries titleModelQueriesFake
		valid   bool
		wantErr error
	}{
		"chat":        {id: id, queries: titleModelQueriesFake{model: dbsqlc.ModelModel{Type: "chat"}}, valid: true},
		"non-chat":    {id: id, queries: titleModelQueriesFake{model: dbsqlc.ModelModel{Type: "embedding"}}},
		"malformed":   {id: "not-a-uuid"},
		"not found":   {id: id, queries: titleModelQueriesFake{err: pgx.ErrNoRows}},
		"query error": {id: id, queries: titleModelQueriesFake{err: sentinel}, wantErr: sentinel},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			valid, err := NewTitleModelValidatorWithQueries(tc.queries).IsValidTitleModel(context.Background(), tc.id)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("IsValidTitleModel() error = %v, want %v", err, tc.wantErr)
			}
			if valid != tc.valid {
				t.Fatalf("IsValidTitleModel() = %v, want %v", valid, tc.valid)
			}
		})
	}
}
