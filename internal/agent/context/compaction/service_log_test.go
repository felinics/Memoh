package compaction

import (
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/memohai/memoh/internal/db/postgres/sqlc"
)

func TestToLogIncludesArtifactLineage(t *testing.T) {
	t.Parallel()

	parentID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	log := toLog(sqlc.BotHistoryMessageCompact{
		ArtifactLevel: 2,
		ParentIds: []pgtype.UUID{
			{Bytes: parentID, Valid: true},
			{},
		},
	})
	if log.ArtifactLevel != 2 {
		t.Fatalf("artifact level = %d, want 2", log.ArtifactLevel)
	}
	if len(log.ParentIDs) != 1 || log.ParentIDs[0] != parentID.String() {
		t.Fatalf("parent IDs = %#v, want [%q]", log.ParentIDs, parentID)
	}
}
