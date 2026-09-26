package compaction

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

func (*fakeQueries) GetSessionByID(_ context.Context, id pgtype.UUID) (sqlc.BotSession, error) {
	return sqlc.BotSession{ID: id, CompactionEpoch: 7}, nil
}

func TestSummaryRecoveryPreservesCurrentRawMessage(t *testing.T) {
	stub := &stubModel{summary: "short replacement"}
	cfg := machineryConfig(stub, 800)
	cfg.AllowFrontierFusion = true
	cfg.ContextWindowTokens = 2000
	parents := fusionParentLogs(t, cfg, strings.Repeat("s", 2040), strings.Repeat("t", 2040))
	rows := fusionQualityRows(t, cfg)
	rows = rows[len(rows)-1:]
	q := &fakeQueries{uncompacted: rows, priorLogs: parents}
	res, err := newMachineryService(q).RunCompactionSync(t.Context(), cfg)
	if err != nil || res.Status != StatusOK || len(q.markedIDs) != 0 || res.MessageCount != 0 {
		t.Fatalf("recovery consumed current source: result=%+v err=%v claims=%v", res, err, q.markedIDs)
	}
}

func TestSummaryRecoveryFailureKeepsParentsActive(t *testing.T) {
	for _, failure := range []string{"empty", "ineffective", "incomplete", "epoch", "parent", "cancelled", "missing_coverage"} {
		t.Run(failure, func(t *testing.T) {
			stub := &stubModel{summary: "short replacement"}
			cfg := machineryConfig(stub, 800)
			cfg.AllowFrontierFusion = true
			cfg.ContextWindowTokens = 2000
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
			res, err := newMachineryService(q).RunCompactionSync(ctx, cfg)
			if res.Status == StatusOK {
				t.Fatalf("unsafe replacement accepted: %+v", res)
			}
			if failure != "missing_coverage" && err == nil {
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
			cfg.AllowFrontierFusion = true
			cfg.ContextWindowTokens = 2000
			cfg.MaxCompactTokens = 50000
			summaries := make([]string, parentCount)
			for i := range summaries {
				summaries[i] = strings.Repeat("s", 4080/parentCount)
			}
			parents := fusionParentLogs(t, cfg, summaries...)
			q := &fakeQueries{priorLogs: parents}
			result, err := newMachineryService(q).RunCompactionSync(t.Context(), cfg)
			if err != nil || result.Status != StatusOK {
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
		cfg := machineryConfig(stub, 200)
		cfg.HistoryBudgetTokens = 500
		cfg.SummaryWindowTokens = 128000
		cfg.AllowFrontierFusion = true
		parents := fusionParentLogs(t, cfg, strings.Repeat("p", 8000))
		q := &fakeQueries{priorLogs: parents}
		result, err := newMachineryService(q).RunCompactionSync(t.Context(), cfg)
		if stub.maxTokens > 200 {
			t.Fatalf("output cap=%d exceeds target=200", stub.maxTokens)
		}
		if len(summary) > 200 {
			if err == nil || result.Status == StatusOK || len(q.rollupCalls) > 0 {
				t.Fatalf("oversized replay accepted: result=%+v err=%v", result, err)
			}
			assertFusionParentsActive(t, q, parents)
		} else if err != nil || result.Status != StatusOK {
			t.Fatalf("fitting summary rejected: result=%+v err=%v", result, err)
		}
	}
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
			if result.Status != StatusOK || stub.maxTokens >= 100 {
				t.Fatalf("failed to reserve retained raw: result=%+v cap=%d", result, stub.maxTokens)
			}
		}
	}
}
