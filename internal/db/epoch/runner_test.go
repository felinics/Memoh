package epoch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"testing"
)

func TestBootstrapOwnerSchemaUsesValidatedOwnerSchema(t *testing.T) {
	_, manifest := validAssets(t)

	for _, owner := range manifest.Owners {
		t.Run(owner.Name, func(t *testing.T) {
			execer := &recordingSchemaExecer{}
			if err := bootstrapOwnerSchema(t.Context(), execer, owner); err != nil {
				t.Fatalf("bootstrapOwnerSchema() error = %v", err)
			}
			want := []string{"CREATE SCHEMA IF NOT EXISTS " + owner.Schema}
			if owner.Name != "iam" {
				want = append(want,
					"ALTER DEFAULT PRIVILEGES IN SCHEMA "+owner.Schema+" GRANT ALL ON TABLES TO memoh_migrate",
					"ALTER DEFAULT PRIVILEGES IN SCHEMA "+owner.Schema+" GRANT ALL ON SEQUENCES TO memoh_migrate",
				)
			}
			if !slices.Equal(execer.queries, want) {
				t.Fatalf("bootstrapOwnerSchema() queries = %q, want %q", execer.queries, want)
			}
		})
	}
}

func TestBootstrapOwnerSchemaReturnsExecError(t *testing.T) {
	for _, failAt := range []int{0, 1, 2} {
		t.Run(fmt.Sprintf("query_%d", failAt), func(t *testing.T) {
			want := errors.New("exec")
			execer := &recordingSchemaExecer{failAt: failAt, err: want}

			err := bootstrapOwnerSchema(t.Context(), execer, Owner{Name: "channel", Schema: "channel"})
			if !errors.Is(err, want) {
				t.Fatalf("bootstrapOwnerSchema() error = %v, want %v", err, want)
			}
		})
	}
}

type recordingSchemaExecer struct {
	queries []string
	failAt  int
	err     error
}

func (e *recordingSchemaExecer) ExecContext(
	_ context.Context,
	query string,
	_ ...any,
) (sql.Result, error) {
	e.queries = append(e.queries, query)
	if e.err != nil && len(e.queries)-1 == e.failAt {
		return nil, e.err
	}
	return nil, nil
}
