package application

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	applicationpersistence "github.com/memohai/memoh/domains/agent/application/persistence"
	dbsqlc "github.com/memohai/memoh/domains/agent/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

type queries interface {
	GetLatestSessionModelID(context.Context, pgtype.UUID) (pgtype.UUID, error)
	ListMessageRefsByCompactID(context.Context, pgtype.UUID) ([]dbsqlc.ListMessageRefsByCompactIDRow, error)
}

// Reads adapts Agent-owner SQLC statements to application read ports.
type Reads struct {
	queries queries
}

func NewReads(queries queries) *Reads {
	return &Reads{queries: queries}
}

// NewReadsFromDB constructs application reads bound to Agent-owner SQLC.
func NewReadsFromDB(db dbsqlc.DBTX) *Reads {
	return NewReads(dbsqlc.New(db))
}

func (r *Reads) LatestSessionModelID(ctx context.Context, sessionID string) (string, error) {
	id, err := db.ParseUUID(sessionID)
	if err != nil {
		return "", fmt.Errorf("parse session id: %w", err)
	}
	modelID, err := r.queries.GetLatestSessionModelID(ctx, id)
	if err != nil {
		return "", err
	}
	if !modelID.Valid {
		return "", nil
	}
	return modelID.String(), nil
}

func (r *Reads) ListCompactionMessageRefs(ctx context.Context, compactID string) ([]string, error) {
	id, err := db.ParseUUID(compactID)
	if err != nil {
		return nil, fmt.Errorf("parse compact id: %w", err)
	}
	rows, err := r.queries.ListMessageRefsByCompactID(ctx, id)
	if err != nil {
		return nil, err
	}
	refs := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.ID.Valid {
			refs = append(refs, row.ID.String())
		}
	}
	return refs, nil
}

var (
	_ applicationpersistence.LatestSessionModelReader   = (*Reads)(nil)
	_ applicationpersistence.CompactionMessageRefReader = (*Reads)(nil)
)
