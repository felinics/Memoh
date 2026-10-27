package compaction

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/models"
)

func (*fakeQueries) GetSessionByID(_ context.Context, id pgtype.UUID) (sqlc.BotSession, error) {
	return sqlc.BotSession{ID: id, CompactionEpoch: 7}, nil
}

func TestSummaryRecoveryPreservesCurrentRawMessage(t *testing.T) {
	stub := &stubModel{summary: "short replacement"}
	cfg := machineryConfig(stub, 800)
	parents := fusionParentLogs(t, cfg, strings.Repeat("s", 2040), strings.Repeat("t", 2040))
	rows := fusionQualityRows(t, cfg)
	rows = rows[len(rows)-1:]
	cfg.ProtectedSources = historySources(rows[0])
	q := &fakeQueries{uncompacted: rows, priorLogs: parents}
	res, err := newMachineryService(q).compactOnce(t.Context(), cfg)
	if err != nil || res.Status != passOK || len(q.markedIDs) != 0 || res.MessageCount != 0 {
		t.Fatalf("recovery consumed current source: result=%+v err=%v claims=%v", res, err, q.markedIDs)
	}
}

func TestSummaryRecoveryFailureKeepsParentsActive(t *testing.T) {
	for _, failure := range []string{"empty", "ineffective", "incomplete", "epoch", "parent", "cancelled", "missing_coverage"} {
		t.Run(failure, func(t *testing.T) {
			stub := &stubModel{summary: "short replacement"}
			cfg := machineryConfig(stub, 800)
			parents := fusionParentLogs(t, cfg, strings.Repeat("s", 2040), strings.Repeat("t", 2040))
			q := &fakeQueries{priorLogs: parents}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch failure {
			case "empty":
				stub.summary = ""
			case "ineffective":
				stub.summary = strings.Repeat("x", 8000)
			case "incomplete":
				stub.finishReason = "length"
			case "epoch":
				q.createErr = pgx.ErrNoRows
			case "parent":
				q.rollupErr = pgx.ErrNoRows
			case "cancelled":
				cancel()
			case "missing_coverage":
				q.priorLogs[0].Coverage = []byte("[]")
			}
			res, err := newMachineryService(q).compactOnce(ctx, cfg)
			if res.Status == passOK {
				t.Fatalf("unsafe replacement accepted: %+v", res)
			}
			if err == nil {
				t.Fatal("expected failed recovery")
			}
			if failure == "cancelled" && (!errors.Is(err, context.Canceled) || stub.calls != 0 || q.created) {
				t.Fatalf("cancelled recovery performed work: err=%v calls=%d created=%v", err, stub.calls, q.created)
			}
			assertFusionParentsActive(t, q, parents)
			if len(q.markedIDs) != 0 {
				t.Fatal("summary-only recovery claimed raw messages")
			}
		})
	}
}

func TestCompactionRecoversSummaryOnlyFrontier(t *testing.T) {
	for _, parentCount := range []int{1, 2} {
		t.Run(strings.Repeat("parent", parentCount), func(t *testing.T) {
			stub := &stubModel{summary: "short replacement"}
			cfg := machineryConfig(stub, 800)
			summaries := make([]string, parentCount)
			for i := range summaries {
				summaries[i] = strings.Repeat("s", 4080/parentCount)
			}
			parents := fusionParentLogs(t, cfg, summaries...)
			q := &fakeQueries{priorLogs: parents}
			result, err := newMachineryService(q).compactOnce(t.Context(), cfg)
			if err != nil || result.Status != passOK {
				t.Fatalf("summary-only recovery: result=%+v err=%v", result, err)
			}
			if stub.calls != 1 || result.MessageCount != 0 || len(q.markedIDs) != 0 {
				t.Fatalf("calls=%d count=%d claims=%d", stub.calls, result.MessageCount, len(q.markedIDs))
			}
			if q.createArg.ExpectedEpoch != 7 || len(q.rollupCalls) != 1 || len(q.rollupCompleted.Parents) != parentCount {
				t.Fatalf("recovery must fence and replace the complete frontier: create=%+v rollup=%+v", q.createArg, q.rollupCompleted)
			}
			coverage, err := DecodeArtifactCoverage(q.rollupCompleted.Coverage)
			if err != nil || len(coverage) != parentCount {
				t.Fatalf("inherited coverage=%+v err=%v", coverage, err)
			}
		})
	}
}

func TestSummaryRecoveryBoundsActualReplay(t *testing.T) {
	for _, summary := range []string{"short replacement", strings.Repeat("s", 2400)} {
		stub := &stubModel{summary: summary}
		cfg := machineryConfig(stub, 500)
		parents := fusionParentLogs(t, cfg, strings.Repeat("p", 8000))
		q := &fakeQueries{priorLogs: parents}
		result, err := newMachineryService(q).compactOnce(t.Context(), cfg)
		if limit := statedSummaryLimit(t, stub.prompt); limit > 500 {
			t.Fatalf("stated summary limit=%d exceeds the 500-token room", limit)
		}
		if len(summary) > 2000 {
			if err == nil || result.Status == passOK || len(q.rollupCalls) > 0 {
				t.Fatalf("oversized replay accepted: result=%+v err=%v", result, err)
			}
			assertFusionParentsActive(t, q, parents)
		} else if err != nil || result.Status != passOK {
			t.Fatalf("fitting summary rejected: result=%+v err=%v", result, err)
		}
	}
}

func TestSummaryBudgetIsStatedNotUsedAsOutputCap(t *testing.T) {
	stub := &stubModel{summary: "short summary", reasoning: 300}
	cfg := machineryConfig(stub, 500)
	q := &fakeQueries{uncompacted: qualityRows(t)}
	result, err := newMachineryService(q).compactOnce(t.Context(), cfg)
	if err != nil || result.Status != passOK {
		t.Fatalf("reasoning summarizer failed under a small replay budget: result=%+v err=%v", result, err)
	}
	if stub.maxTokens != models.DefaultOutputReserveTokens {
		t.Fatalf("output cap=%d, want the model's output allowance %d", stub.maxTokens, models.DefaultOutputReserveTokens)
	}
	if limit := statedSummaryLimit(t, stub.prompt); limit <= 0 || limit > 500 {
		t.Fatalf("stated summary limit=%d, want within the 500-token room", limit)
	}
}

func statedSummaryLimit(t *testing.T, prompt string) int {
	t.Helper()
	at := strings.LastIndex(prompt, "must stay under ")
	var limit int
	if at < 0 {
		t.Fatal("prompt does not state the summary limit")
	}
	if _, err := fmt.Sscanf(prompt[at:], "must stay under %d tokens", &limit); err != nil {
		t.Fatalf("parse stated summary limit: %v", err)
	}
	return limit
}

func TestSummaryRecoveryReservesKeptRawReplay(t *testing.T) {
	for _, keptBytes := range []int{1200, 2400} {
		stub := &stubModel{summary: "short summary"}
		cfg := machineryConfig(stub, 500)
		rows := qualityRows(t)
		rows[3] = mkRow(t, "assistant", jsonStr(strings.Repeat("u", keptBytes)), 0)
		cfg.ProtectedSources = historySources(rows[2])
		q := &fakeQueries{uncompacted: rows}
		result, err := newMachineryService(q).compactOnce(t.Context(), cfg)
		if keptBytes == 2400 {
			if !errors.Is(err, errNoSummaryRoom) || stub.calls != 0 || q.created {
				t.Fatalf("kept history over the room: result=%+v err=%v calls=%d, want no room before any claim", result, err, stub.calls)
			}
			continue
		}
		if limit := statedSummaryLimit(t, stub.prompt); err != nil || result.Status != passOK || limit >= 200 {
			t.Fatalf("failed to reserve kept raw: result=%+v err=%v stated limit=%d", result, err, limit)
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
	result, err := newMachineryService(q).compactOnce(t.Context(), cfg)
	if err != nil || result.Status != passOK {
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

	for i := range cfg.ProtectedSources {
		cfg.ProtectedSources[i].Current = false
	}
	stub.calls = 0
	if _, err := newMachineryService(&fakeQueries{uncompacted: rows}).compactOnce(t.Context(), cfg); !errors.Is(err, errNoSummaryRoom) || stub.calls != 0 {
		t.Fatalf("err=%v calls=%d, want protected history inside the budget to leave no summary room", err, stub.calls)
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
		result, err := svc.compactOnce(t.Context(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("truncated=%v status=%s marked=%v", truncated, result.Status, q.markedIDs)
		if len(q.markedIDs) > 0 {
			t.Fatal("protected batch input was compacted")
		}
	}
}
