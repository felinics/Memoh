package compaction

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

func (*fakeQueries) GetSessionByID(_ context.Context, id pgtype.UUID) (sqlc.BotSession, error) {
	return sqlc.BotSession{ID: id, CompactionEpoch: 7}, nil
}

func TestSummaryBudgetIsStatedNotUsedAsOutputCap(t *testing.T) {
	stub := &stubModel{summary: "short summary", reasoning: 300}
	cfg := machineryConfig(stub, 200)
	cfg.HistoryBudgetTokens = 500
	q := &fakeQueries{uncompacted: qualityRows(t)}
	result, err := newMachineryService(q).RunCompactionSync(t.Context(), cfg)
	if err != nil || result.Status != StatusOK {
		t.Fatalf("reasoning summarizer failed under a small replay budget: result=%+v err=%v", result, err)
	}
	if stub.maxTokens != maxCompactionSummaryTokens {
		t.Fatalf("output cap=%d, want the summarizer cap %d", stub.maxTokens, maxCompactionSummaryTokens)
	}
	if limit := statedSummaryLimit(t, stub.prompt); limit <= 0 || limit > 200 {
		t.Fatalf("stated summary limit=%d, want within the 200-token target", limit)
	}
}

func statedSummaryLimit(t *testing.T, prompt string) int {
	t.Helper()
	at := strings.LastIndex(prompt, "Keep the summary under ")
	var limit int
	if at < 0 {
		t.Fatal("prompt does not state the summary limit")
	}
	if _, err := fmt.Sscanf(prompt[at:], "Keep the summary under %d tokens.", &limit); err != nil {
		t.Fatalf("parse stated summary limit: %v", err)
	}
	return limit
}

func TestSummaryRecoveryReservesRetainedRawReplay(t *testing.T) {
	for _, tailBytes := range []int{1200, 2400} {
		stub := &stubModel{summary: "short summary"}
		cfg := machineryConfig(stub, 200)
		cfg.HistoryBudgetTokens = 500
		rows := qualityRows(t)
		rows[2] = mkRow(t, "user", jsonStr(strings.Repeat("u", tailBytes)), 100)
		q := &fakeQueries{uncompacted: rows}
		result, err := newMachineryService(q).RunCompactionSync(t.Context(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		if tailBytes == 2400 {
			if result.Status != StatusProgress {
				t.Fatalf("reported admission success despite retained overflow: %+v", result)
			}
		} else {
			if limit := statedSummaryLimit(t, stub.prompt); result.Status != StatusOK || limit >= 100 {
				t.Fatalf("failed to reserve retained raw: result=%+v stated limit=%d", result, limit)
			}
		}
	}
}

func TestSummaryRecoveryProtectsCurrentSourcesAndCountsThemOnce(t *testing.T) {
	stub := &stubModel{summary: "short summary"}
	cfg := machineryConfig(stub, 200)
	cfg.HistoryBudgetTokens = 500
	rows := qualityRows(t)
	rows[2] = mkRow(t, "user", jsonStr(strings.Repeat("a", 2400)), 600)
	rows[3] = mkRow(t, "user", jsonStr(strings.Repeat("b", 2400)), 600)
	cfg.ProtectedSources = []turn.ContextMessageSource{{Kind: "history", ID: rows[2].ID.String(), Current: true}, {Kind: "history", ID: rows[3].ID.String(), Current: true}}
	q := &fakeQueries{uncompacted: rows}
	result, err := newMachineryService(q).RunCompactionSync(t.Context(), cfg)
	if err != nil || result.Status != StatusOK {
		t.Fatalf("current input counted twice: result=%+v err=%v", result, err)
	}
	for _, id := range q.markedIDs {
		if id == rows[2].ID || id == rows[3].ID {
			t.Fatal("compacted current input")
		}
	}
	if len(q.markedIDs) != 2 {
		t.Fatalf("old history failed to progress: claims=%v", q.markedIDs)
	}
}

type protectedTruncatedQueries struct{ *fakeQueries }

func (q protectedTruncatedQueries) ListUncompactedMessagesBySessionWithinBytes(ctx context.Context, p sqlc.ListUncompactedMessagesBySessionWithinBytesParams) ([]sqlc.ListUncompactedMessagesBySessionWithinBytesRow, error) {
	rows, e := q.fakeQueries.ListUncompactedMessagesBySessionWithinBytes(ctx, p)
	for i := range rows {
		rows[i].CandidateCount = int64(len(rows) + 1)
	}
	return rows, e
}

func TestProtectedSourcesSurviveTruncatedRead(t *testing.T) {
	first := mkRow(t, "user", jsonStr(strings.Repeat("first unconsumed request ", 80)), 1000)
	second := mkRow(t, "user", jsonStr("second unconsumed request"), 100)
	for _, truncated := range []bool{false, true} {
		q := &fakeQueries{uncompacted: []sqlc.ListUncompactedMessagesBySessionRow{first, second}}
		stub := &stubModel{summary: "short summary"}
		cfg := machineryConfig(stub, 200)
		cfg.HistoryBudgetTokens = 500
		cfg.ProtectedSources = []turn.ContextMessageSource{{Kind: "history", ID: first.ID.String(), Current: true}, {Kind: "history", ID: second.ID.String(), Current: true}}
		svc := newMachineryService(q)
		if truncated {
			svc = newMachineryService(protectedTruncatedQueries{q})
		}
		result, err := svc.RunCompactionSync(t.Context(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("truncated=%v status=%s marked=%v", truncated, result.Status, q.markedIDs)
		if len(q.markedIDs) > 0 {
			t.Fatal("protected batch input was compacted")
		}
	}
}
