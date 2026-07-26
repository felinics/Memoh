package usage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	usagepersistence "github.com/memohai/memoh/domains/agent/chat/usage/persistence"
	agentsqlc "github.com/memohai/memoh/domains/agent/internal/postgres/sqlc"
)

type fakeSQLCQueries struct {
	session          agentsqlc.AgentBotSession
	sessionErr       error
	latestSessionID  pgtype.UUID
	latestSessionErr error
	dayParams        agentsqlc.GetTokenUsageByDayAndTypeParams
	dayRows          []agentsqlc.GetTokenUsageByDayAndTypeRow
	modelParams      agentsqlc.GetTokenUsageByModelParams
	modelRows        []agentsqlc.GetTokenUsageByModelRow
	listParams       agentsqlc.ListTokenUsageRecordsParams
	listRows         []agentsqlc.ListTokenUsageRecordsRow
	countParams      agentsqlc.CountTokenUsageRecordsParams
	count            int64
	countErr         error
}

func (f *fakeSQLCQueries) GetSessionByID(context.Context, pgtype.UUID) (agentsqlc.AgentBotSession, error) {
	return f.session, f.sessionErr
}

func (f *fakeSQLCQueries) GetLatestSessionIDByBot(context.Context, pgtype.UUID) (pgtype.UUID, error) {
	return f.latestSessionID, f.latestSessionErr
}

func (*fakeSQLCQueries) CountMessagesBySession(context.Context, pgtype.UUID) (int64, error) {
	return 0, nil
}

func (*fakeSQLCQueries) GetLatestAssistantUsage(context.Context, pgtype.UUID) (int64, error) {
	return 0, nil
}

func (*fakeSQLCQueries) GetSessionCacheStats(context.Context, pgtype.UUID) (agentsqlc.GetSessionCacheStatsRow, error) {
	return agentsqlc.GetSessionCacheStatsRow{}, nil
}

func (*fakeSQLCQueries) GetSessionUsedSkills(context.Context, pgtype.UUID) ([]string, error) {
	return nil, nil
}

func (f *fakeSQLCQueries) GetTokenUsageByDayAndType(_ context.Context, params agentsqlc.GetTokenUsageByDayAndTypeParams) ([]agentsqlc.GetTokenUsageByDayAndTypeRow, error) {
	f.dayParams = params
	return f.dayRows, nil
}

func (f *fakeSQLCQueries) GetTokenUsageByModel(_ context.Context, params agentsqlc.GetTokenUsageByModelParams) ([]agentsqlc.GetTokenUsageByModelRow, error) {
	f.modelParams = params
	return f.modelRows, nil
}

func (f *fakeSQLCQueries) ListTokenUsageRecords(_ context.Context, params agentsqlc.ListTokenUsageRecordsParams) ([]agentsqlc.ListTokenUsageRecordsRow, error) {
	f.listParams = params
	return f.listRows, nil
}

func (f *fakeSQLCQueries) CountTokenUsageRecords(_ context.Context, params agentsqlc.CountTokenUsageRecordsParams) (int64, error) {
	f.countParams = params
	return f.count, f.countErr
}

func TestReaderMapsNoRows(t *testing.T) {
	t.Parallel()
	reader := newReader(&fakeSQLCQueries{latestSessionErr: pgx.ErrNoRows})

	_, err := reader.GetLatestSessionIDByBot(t.Context(), "11111111-1111-1111-1111-111111111111")
	if !errors.Is(err, usagepersistence.ErrNotFound) {
		t.Fatalf("GetLatestSessionIDByBot() error = %v, want ErrNotFound", err)
	}
}

func TestReaderMapsSession(t *testing.T) {
	t.Parallel()
	fake := &fakeSQLCQueries{
		session: agentsqlc.AgentBotSession{
			ID:              testUUID("11111111-1111-1111-1111-111111111111"),
			BotID:           testUUID("22222222-2222-2222-2222-222222222222"),
			Type:            "chat",
			SessionMode:     "discuss",
			RuntimeType:     "native",
			CreatedByUserID: testUUID("33333333-3333-3333-3333-333333333333"),
		},
	}

	got, err := newReader(fake).GetSession(t.Context(), "11111111-1111-1111-1111-111111111111")
	if err != nil {
		t.Fatalf("GetSession() error = %v", err)
	}
	if got.BotID != "22222222-2222-2222-2222-222222222222" || got.CreatedByUserID != "33333333-3333-3333-3333-333333333333" {
		t.Fatalf("GetSession() = %#v", got)
	}
}

func TestReaderMapsUsageFiltersAndPagination(t *testing.T) {
	t.Parallel()
	from := time.Date(2026, time.July, 1, 2, 3, 4, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	day := time.Date(2026, time.July, 2, 0, 0, 0, 0, time.UTC)
	fake := &fakeSQLCQueries{
		dayRows: []agentsqlc.GetTokenUsageByDayAndTypeRow{{
			SessionType:     "chat",
			Day:             pgtype.Date{Time: day, Valid: true},
			InputTokens:     11,
			OutputTokens:    12,
			CacheReadTokens: 13,
			ReasoningTokens: 14,
		}},
		modelRows: []agentsqlc.GetTokenUsageByModelRow{{
			ModelID:      testUUID("44444444-4444-4444-4444-444444444444"),
			InputTokens:  21,
			OutputTokens: 22,
		}},
		listRows: []agentsqlc.ListTokenUsageRecordsRow{{
			ID:          testUUID("55555555-5555-5555-5555-555555555555"),
			CreatedAt:   pgtype.Timestamptz{Time: from, Valid: true},
			SessionID:   testUUID("66666666-6666-6666-6666-666666666666"),
			SessionType: "acp_agent",
			ModelID:     testUUID("44444444-4444-4444-4444-444444444444"),
		}},
		count: 1,
	}
	reader := newReader(fake)
	filter := usagepersistence.Filter{
		BotID:       "11111111-1111-1111-1111-111111111111",
		From:        from,
		To:          to,
		ModelID:     "44444444-4444-4444-4444-444444444444",
		SessionType: "acp_agent",
	}

	byDay, err := reader.GetDaily(t.Context(), filter)
	if err != nil {
		t.Fatalf("GetDaily() error = %v", err)
	}
	if len(byDay) != 1 || byDay[0].Day != day || byDay[0].CacheReadTokens != 13 {
		t.Fatalf("GetDaily() = %#v", byDay)
	}
	if !fake.dayParams.ModelID.Valid || fake.dayParams.SessionType.String != "acp_agent" {
		t.Fatalf("daily params = %#v", fake.dayParams)
	}

	byModel, err := reader.GetByModel(t.Context(), filter)
	if err != nil {
		t.Fatalf("GetByModel() error = %v", err)
	}
	if len(byModel) != 1 || byModel[0].ModelID != filter.ModelID || byModel[0].InputTokens != 21 {
		t.Fatalf("GetByModel() = %#v", byModel)
	}
	if fake.modelParams.SessionType.String != "acp_agent" {
		t.Fatalf("model params = %#v", fake.modelParams)
	}

	page, err := reader.ListRecords(t.Context(), filter, usagepersistence.Pagination{Limit: 20, Offset: 40})
	if err != nil {
		t.Fatalf("ListRecords() error = %v", err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].CreatedAt != from {
		t.Fatalf("ListRecords() = %#v", page)
	}
	if fake.listParams.PageLimit != 20 || fake.listParams.PageOffset != 40 {
		t.Fatalf("list pagination = %#v", fake.listParams)
	}
	if fake.countParams.ModelID != fake.listParams.ModelID || fake.countParams.SessionType != fake.listParams.SessionType {
		t.Fatalf("count filters = %#v, list filters = %#v", fake.countParams, fake.listParams)
	}
}

func TestReaderClassifiesRecordCountFailure(t *testing.T) {
	t.Parallel()
	fake := &fakeSQLCQueries{countErr: errors.New("count failed")}
	reader := newReader(fake)

	_, err := reader.ListRecords(t.Context(), usagepersistence.Filter{
		BotID: "11111111-1111-1111-1111-111111111111",
	}, usagepersistence.Pagination{Limit: 20})
	if !errors.Is(err, usagepersistence.ErrCountRecords) {
		t.Fatalf("ListRecords() error = %v, want ErrCountRecords", err)
	}
}

func testUUID(value string) pgtype.UUID {
	return pgtype.UUID{Bytes: uuid.MustParse(value), Valid: true}
}
