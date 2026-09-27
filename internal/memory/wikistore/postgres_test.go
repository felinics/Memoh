package wikistore

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
)

type rowErrDB struct{ err error }

func (rowErrDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("unexpected exec")
}

func (rowErrDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unexpected query")
}

func (d rowErrDB) QueryRow(context.Context, string, ...any) pgx.Row { return errRow(d) }

type errRow struct{ err error }

func (r errRow) Scan(...any) error { return r.err }

func TestGetNodeReportsMissingRowAsNotFound(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		notFound bool
	}{
		{"no rows", pgx.ErrNoRows, true},
		{"wrapped no rows", fmt.Errorf("scan: %w", pgx.ErrNoRows), true},
		{"other", errors.New("no rows in result set"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := NewPostgres(dbsqlc.New(rowErrDB{err: tc.err}))
			_, err := store.GetNode(context.Background(), "00000000-0000-0000-0000-000000000001", "node")
			if got := errors.Is(err, ErrNodeNotFound); got != tc.notFound {
				t.Fatalf("errors.Is(%v, ErrNodeNotFound) = %v, want %v", err, got, tc.notFound)
			}
		})
	}
}
