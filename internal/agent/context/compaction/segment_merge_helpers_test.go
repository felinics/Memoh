package compaction

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

func fusionParentLogs(t *testing.T, cfg TriggerConfig, summaries ...string) []sqlc.BotHistoryMessageCompact {
	t.Helper()
	botID := pgtype.UUID{Bytes: uuid.MustParse(cfg.BotID), Valid: true}
	sessionID := pgtype.UUID{Bytes: uuid.MustParse(cfg.SessionID), Valid: true}
	parents := make([]sqlc.BotHistoryMessageCompact, 0, len(summaries))
	for i, summary := range summaries {
		createdAtMs := int64(i+1) * 1000
		coverage := strictTestCoveredSource(fmt.Sprintf("fusion-parent-%d", i), createdAtMs)
		parents = append(parents, sqlc.BotHistoryMessageCompact{
			ID:              pgtype.UUID{Bytes: uuid.New(), Valid: true},
			BotID:           botID,
			SessionID:       sessionID,
			Status:          "ok",
			Summary:         summary,
			Coverage:        mustMarshalCoverage(t, coverage),
			AnchorStartMs:   createdAtMs,
			AnchorEndMs:     createdAtMs,
			ArtifactLevel:   int32(i),
			CompactionEpoch: 0,
		})
	}
	return parents
}
