package compaction

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

func qualityRows(t *testing.T) []sqlc.ListUncompactedMessagesBySessionRow {
	t.Helper()
	return []sqlc.ListUncompactedMessagesBySessionRow{
		mkRow(t, "user", jsonStr(strings.Repeat("old question ", 40)), 100),
		mkRow(t, "assistant", jsonStr(strings.Repeat("old answer ", 40)), 100),
		mkRow(t, "user", `"current question"`, 100),
		mkRow(t, "assistant", `"current answer"`, 100),
	}
}

func TestRunCompactionRecordsModelRecordProvenance(t *testing.T) {
	t.Parallel()

	q := &fakeQueries{uncompacted: qualityRows(t)}
	stub := &stubModel{summary: "SUMMARY"}
	cfg := machineryConfig(stub, 200)
	cfg.ModelID = "openai/gpt-test"
	recordID := testUUID(t)
	cfg.ModelRecordID = recordID.String()

	if _, err := newMachineryService(q).RunCompactionSync(context.Background(), cfg); err != nil {
		t.Fatalf("RunCompactionSync: %v", err)
	}
	if q.completed.ModelID != recordID {
		t.Fatalf("artifact model provenance = %v, want the models row UUID %v: the runtime slug must not be parsed as a database id", q.completed.ModelID, recordID)
	}
}

func TestRunCompactionCapsSummaryOutputTokens(t *testing.T) {
	t.Parallel()

	q := &fakeQueries{uncompacted: qualityRows(t)}
	stub := &stubModel{summary: "SUMMARY"}
	cfg := machineryConfig(stub, 200)
	cfg.SummaryWindowTokens = 20000

	if _, err := newMachineryService(q).RunCompactionSync(context.Background(), cfg); err != nil {
		t.Fatalf("RunCompactionSync: %v", err)
	}
	if stub.maxTokens != 2000 {
		t.Fatalf("summary max tokens = %d, want window/10 = 2000", stub.maxTokens)
	}

	wide := &stubModel{summary: "SUMMARY"}
	cfgWide := machineryConfig(wide, 200)
	cfgWide.SummaryWindowTokens = 200000
	if _, err := newMachineryService(&fakeQueries{uncompacted: qualityRows(t)}).RunCompactionSync(context.Background(), cfgWide); err != nil {
		t.Fatalf("RunCompactionSync wide: %v", err)
	}
	if wide.maxTokens != maxCompactionSummaryTokens {
		t.Fatalf("summary max tokens = %d, want the %d cap", wide.maxTokens, maxCompactionSummaryTokens)
	}
}

func TestRunCompactionRejectsTruncatedSummary(t *testing.T) {
	t.Parallel()

	q := &fakeQueries{uncompacted: qualityRows(t)}
	stub := &stubModel{summary: "SUMMARY", finishReason: "length"}

	_, err := newMachineryService(q).RunCompactionSync(context.Background(), machineryConfig(stub, 200))
	if !errors.Is(err, errIncompleteSummary) {
		t.Fatalf("RunCompactionSync error = %v, want errIncompleteSummary: a truncated summary must never be published", err)
	}
	if q.completed.Status != "error" {
		t.Fatalf("attempt status = %q, want error so the claimed rows stay reclaimable", q.completed.Status)
	}
}

func TestRunCompactionRejectsIneffectiveSummary(t *testing.T) {
	t.Parallel()

	q := &fakeQueries{uncompacted: qualityRows(t)}
	stub := &stubModel{summary: strings.Repeat("a summary longer than everything it replaces ", 200)}

	_, err := newMachineryService(q).RunCompactionSync(context.Background(), machineryConfig(stub, 200))
	if !errors.Is(err, errIneffectiveSummary) {
		t.Fatalf("RunCompactionSync error = %v, want errIneffectiveSummary: a summary must shrink what it replaces", err)
	}
	if q.completed.Status != "error" {
		t.Fatalf("attempt status = %q, want error", q.completed.Status)
	}
}

func TestRunCompactionFailsClosedOnTinySummarizerWindow(t *testing.T) {
	t.Parallel()

	q := &fakeQueries{uncompacted: qualityRows(t)}
	stub := &stubModel{summary: "SUMMARY"}
	cfg := machineryConfig(stub, 200)
	cfg.SummaryWindowTokens = 512

	_, err := newMachineryService(q).RunCompactionSync(context.Background(), cfg)
	if !errors.Is(err, ErrSummaryWindowTooSmall) {
		t.Fatalf("RunCompactionSync error = %v, want ErrSummaryWindowTooSmall", err)
	}
	if stub.calls != 0 {
		t.Fatalf("summarizer calls = %d, want 0: a hopeless window must not burn an LLM call", stub.calls)
	}
	if q.created {
		t.Fatal("attempt row was created: fail closed before claiming sources")
	}
}
