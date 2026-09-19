package settings

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// imDisplayQueries serves a stored settings row with the IM display toggles
// set, on top of the shared settings fake.
type imDisplayQueries struct {
	*reasoningPolicyQueries
	showToolCallsInIM        bool
	reuseToolCallMessageInIM bool
}

func (q *imDisplayQueries) GetSettingsByBotID(ctx context.Context, botID pgtype.UUID) (sqlc.GetSettingsByBotIDRow, error) {
	row, err := q.reasoningPolicyQueries.GetSettingsByBotID(ctx, botID)
	row.ShowToolCallsInIm = q.showToolCallsInIM
	row.ReuseToolCallMessageInIm = q.reuseToolCallMessageInIM
	return row, err
}

func TestUpsertBotUnrelatedWriteKeepsIMDisplayToggles(t *testing.T) {
	t.Parallel()

	botID := pgtype.UUID{Bytes: uuid.MustParse("00000000-0000-0000-0000-000000000740"), Valid: true}
	queries := &imDisplayQueries{
		reasoningPolicyQueries:   &reasoningPolicyQueries{botID: botID},
		showToolCallsInIM:        true,
		reuseToolCallMessageInIM: true,
	}
	service := NewService(slog.Default(), queries, nil, nil)
	timezone := "Asia/Tokyo"

	// Autosaving clients send only the field that changed.
	if _, err := service.UpsertBot(context.Background(), uuid.UUID(botID.Bytes).String(), UpsertRequest{Timezone: &timezone}); err != nil {
		t.Fatal(err)
	}
	if got := queries.lastUpsert; !got.ShowToolCallsInIm || !got.ReuseToolCallMessageInIm {
		t.Fatalf("unrelated write reset the IM display toggles: show=%v reuse=%v", got.ShowToolCallsInIm, got.ReuseToolCallMessageInIm)
	}
}

func TestUpsertBotAppliesIMDisplayToggles(t *testing.T) {
	t.Parallel()

	botID := pgtype.UUID{Bytes: uuid.MustParse("00000000-0000-0000-0000-000000000741"), Valid: true}
	queries := &imDisplayQueries{
		reasoningPolicyQueries: &reasoningPolicyQueries{botID: botID},
		showToolCallsInIM:      true,
	}
	service := NewService(slog.Default(), queries, nil, nil)
	off, on := false, true

	if _, err := service.UpsertBot(context.Background(), uuid.UUID(botID.Bytes).String(), UpsertRequest{
		ShowToolCallsInIM:        &off,
		ReuseToolCallMessageInIM: &on,
	}); err != nil {
		t.Fatal(err)
	}
	if got := queries.lastUpsert; got.ShowToolCallsInIm || !got.ReuseToolCallMessageInIm {
		t.Fatalf("requested toggles were not applied: show=%v reuse=%v", got.ShowToolCallsInIm, got.ReuseToolCallMessageInIm)
	}
}

func TestNormalizeBotSettingsRowsCarryReuseToolCallMessageInIM(t *testing.T) {
	t.Parallel()

	if got := normalizeBotSettingsReadRow(sqlc.GetSettingsByBotIDRow{}); got.ReuseToolCallMessageInIM {
		t.Fatalf("expected ReuseToolCallMessageInIM to default to false")
	}
	if got := normalizeBotSettingsReadRow(sqlc.GetSettingsByBotIDRow{ReuseToolCallMessageInIm: true}); !got.ReuseToolCallMessageInIM {
		t.Fatalf("expected ReuseToolCallMessageInIM to propagate from the read row")
	}
	if got := normalizeBotSettingsWriteRow(sqlc.UpsertBotSettingsRow{ReuseToolCallMessageInIm: true}); !got.ReuseToolCallMessageInIM {
		t.Fatalf("expected ReuseToolCallMessageInIM to propagate from the write row")
	}
}
