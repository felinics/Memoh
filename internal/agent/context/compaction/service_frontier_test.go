package compaction

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

func TestDoCompactionRefusesALegacySummaryWithoutSourceRows(t *testing.T) {
	rows := machineryCorpus(t)
	stub := &stubModel{summary: "S2"}
	cfg := machineryConfig(stub, 450)
	q := &fakeQueries{
		uncompacted: rows,
		priorLogs: []sqlc.BotHistoryMessageCompact{{
			ID:        pgtype.UUID{Bytes: uuid.New(), Valid: true},
			BotID:     pgtype.UUID{Bytes: uuid.MustParse(cfg.BotID), Valid: true},
			SessionID: pgtype.UUID{Bytes: uuid.MustParse(cfg.SessionID), Valid: true},
			Summary:   "earlier-segment-summary",
			Status:    "ok",
		}},
	}
	svc := newMachineryService(q)

	_, err := svc.compactOnce(context.Background(), cfg)
	if !errors.Is(err, errIncompleteAbsorbedCoverage) || stub.calls != 0 || q.created || len(q.backfills) != 0 {
		t.Fatalf("err=%v calls=%d created=%t backfills=%d, want a refusal before any claim", err, stub.calls, q.created, len(q.backfills))
	}
}

func TestDoCompactionAbsorbsOnlyTheActiveArtifactFrontier(t *testing.T) {
	parentID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	activeID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	stub := &stubModel{summary: "S2"}
	cfg := machineryConfig(stub, 450)
	botID := pgtype.UUID{Bytes: uuid.MustParse(cfg.BotID), Valid: true}
	sessionID := pgtype.UUID{Bytes: uuid.MustParse(cfg.SessionID), Valid: true}
	coverage := testCoverageJSON(t, "covered-row")
	q := &fakeQueries{
		uncompacted: machineryCorpus(t),
		priorLogs: []sqlc.BotHistoryMessageCompact{
			{
				ID:           parentID,
				BotID:        botID,
				SessionID:    sessionID,
				Status:       "ok",
				Summary:      "stale-parent-summary",
				Coverage:     coverage,
				SupersededBy: activeID,
				SupersededAt: pgtype.Timestamptz{Time: time.Unix(1, 0), Valid: true},
			},
			{
				ID:        activeID,
				BotID:     botID,
				SessionID: sessionID,
				Status:    "ok",
				Summary:   "active-frontier-summary",
				Coverage:  coverage,
				ParentIds: []pgtype.UUID{parentID},
			},
		},
	}

	if _, err := newMachineryService(q).compactOnce(context.Background(), cfg); err != nil {
		t.Fatalf("compact: %v", err)
	}
	if !strings.Contains(stub.prompt, "active-frontier-summary") || strings.Contains(stub.prompt, "stale-parent-summary") {
		t.Fatalf("absorbed context did not use the active frontier:\n%s", stub.prompt)
	}
	if parents := q.rollupCalls[0].Parents; len(parents) != 1 || parents[0] != activeID {
		t.Fatalf("rollup parents = %v, want only the active artifact", parents)
	}
}

func TestCompactDoesNotAbsorbUnrenderedSummary(t *testing.T) {
	rows := machineryCorpus(t)
	stub := &stubModel{summary: "new-summary"}
	cfg := machineryConfig(stub, 450)
	for _, row := range rows {
		cfg.View = append(cfg.View, turn.ContextMessageSource{Kind: "history", ID: formatUUID(row.ID)})
	}
	hiddenID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	q := &fakeQueries{
		uncompacted: rows,
		priorLogs: []sqlc.BotHistoryMessageCompact{{
			ID:        hiddenID,
			BotID:     pgtype.UUID{Bytes: uuid.MustParse(cfg.BotID), Valid: true},
			SessionID: pgtype.UUID{Bytes: uuid.MustParse(cfg.SessionID), Valid: true},
			Status:    "ok",
			Summary:   "UNRENDERED_SUMMARY_MUST_NOT_ENTER_REQUEST",
			Coverage:  testCoverageJSON(t, "hidden-row"),
		}},
	}
	passes, err := newMachineryService(q).Compact(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(passes) != 1 {
		t.Fatalf("passes=%d want one", len(passes))
	}
	if strings.Contains(stub.prompt, "UNRENDERED_SUMMARY_MUST_NOT_ENTER_REQUEST") || len(passes[0].Absorbed) != 0 {
		t.Fatalf("summarizer saw unrendered summary=%v; absorbed=%v; view contains only %d history IDs", strings.Contains(stub.prompt, "UNRENDERED_SUMMARY_MUST_NOT_ENTER_REQUEST"), passes[0].Absorbed, len(cfg.View))
	}
}

type legacyReadQueries struct {
	*fakeQueries
	loaded int64
	parent pgtype.UUID
}

func (q *legacyReadQueries) ListMessagesByCompactID(ctx context.Context, id pgtype.UUID) ([]sqlc.ListMessagesByCompactIDRow, error) {
	rows, err := q.fakeQueries.ListMessagesByCompactID(ctx, id)
	if id == q.parent {
		for _, row := range rows {
			q.loaded += int64(len(row.Content))
		}
	}
	return rows, err
}

func TestCompactLegacySummaryDoesNotReadUnboundedRawRows(t *testing.T) {
	rows := machineryCorpus(t)
	stub := &stubModel{summary: "new"}
	cfg := machineryConfig(stub, 450)
	cfg.SummaryWindowTokens = 4000
	parentID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	cfg.View = append(cfg.View, turn.ContextMessageSource{Kind: "summary", ID: formatUUID(parentID)})
	for _, row := range rows {
		cfg.View = append(cfg.View, turn.ContextMessageSource{Kind: "history", ID: formatUUID(row.ID)})
	}
	old := mkRow(t, "assistant", `[{"type":"text","text":"`+strings.Repeat("r", 1<<20)+`"}]`, 100)
	q := &legacyReadQueries{parent: parentID, fakeQueries: &fakeQueries{
		uncompacted:  rows,
		priorLogs:    []sqlc.BotHistoryMessageCompact{{ID: parentID, BotID: pgtype.UUID{Bytes: uuid.MustParse(cfg.BotID), Valid: true}, SessionID: pgtype.UUID{Bytes: uuid.MustParse(cfg.SessionID), Valid: true}, Status: "ok", Summary: "legacy summary", Coverage: []byte("[]")}},
		absorbedRows: map[pgtype.UUID][]sqlc.ListMessagesByCompactIDRow{parentID: {sqlc.ListMessagesByCompactIDRow(old)}},
	}}
	passes, err := newMachineryService(q).Compact(context.Background(), cfg)
	readCap := compactionReadMaxBytes(cfg.SummaryWindowTokens - estimateBytesAsTokens(systemPrompt) - compactionPromptFramingTokens)
	if q.loaded > readCap || ReasonOf(err) != ReasonInputOverflow || len(passes) != 0 || stub.calls != 0 || q.completed.Status == "ok" {
		t.Fatalf("legacy coverage: loaded=%d cap=%d passes=%d calls=%d status=%s err=%v", q.loaded, readCap, len(passes), stub.calls, q.completed.Status, err)
	}
}
