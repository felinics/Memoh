package application

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/memoh/domains/agent/application/persistence"
	applicationpostgres "github.com/memohai/memoh/domains/agent/internal/postgres/application"
)

// NewPostgresReads constructs Agent-owned application read adapters.
//
// The return type is the persistence port bundle rather than the adapter
// struct: the PostgreSQL adapter stays owner-private, so composition roots
// never name it.
func NewPostgresReads(pool *pgxpool.Pool) persistence.Reads {
	return applicationpostgres.NewReadsFromDB(pool)
}
