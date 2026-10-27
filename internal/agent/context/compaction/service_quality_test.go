package compaction

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/models"
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

	if _, err := newMachineryService(q).compactOnce(context.Background(), cfg); err != nil {
		t.Fatalf("compact: %v", err)
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

	if _, err := newMachineryService(q).compactOnce(context.Background(), cfg); err != nil {
		t.Fatalf("compact: %v", err)
	}
	if stub.maxTokens != 5000 {
		t.Fatalf("summary max tokens = %d, want the model's output allowance in a 20000 window", stub.maxTokens)
	}

	wide := &stubModel{summary: "SUMMARY"}
	cfgWide := machineryConfig(wide, 200)
	cfgWide.SummaryWindowTokens = 200000
	if _, err := newMachineryService(&fakeQueries{uncompacted: qualityRows(t)}).compactOnce(context.Background(), cfgWide); err != nil {
		t.Fatalf("compact wide: %v", err)
	}
	if wide.maxTokens != models.DefaultOutputReserveTokens {
		t.Fatalf("summary max tokens = %d, want the model's output allowance %d", wide.maxTokens, models.DefaultOutputReserveTokens)
	}

	declared := &stubModel{summary: "SUMMARY"}
	cfgDeclared := machineryConfig(declared, 200)
	cfgDeclared.SummaryWindowTokens = 200000
	cfgDeclared.MaxOutputTokens = 64000
	if _, err := newMachineryService(&fakeQueries{uncompacted: qualityRows(t)}).compactOnce(context.Background(), cfgDeclared); err != nil {
		t.Fatalf("compact declared: %v", err)
	}
	if declared.maxTokens != 64000 {
		t.Fatalf("summary max tokens = %d, want the model's declared output cap 64000", declared.maxTokens)
	}
}

func TestRunCompactionRejectsTruncatedSummary(t *testing.T) {
	t.Parallel()

	q := &fakeQueries{uncompacted: qualityRows(t)}
	stub := &stubModel{summary: "SUMMARY", finishReason: "length"}

	_, err := newMachineryService(q).compactOnce(context.Background(), machineryConfig(stub, 200))
	if !errors.Is(err, errIncompleteSummary) {
		t.Fatalf("compact error = %v, want errIncompleteSummary: a truncated summary must never be published", err)
	}
	if q.completed.Status != "error" {
		t.Fatalf("attempt status = %q, want error so the claimed rows stay reclaimable", q.completed.Status)
	}
}

func TestRunCompactionRejectsIneffectiveSummary(t *testing.T) {
	t.Parallel()

	q := &fakeQueries{uncompacted: qualityRows(t)}
	stub := &stubModel{summary: strings.Repeat("a summary longer than everything it replaces ", 200)}

	_, err := newMachineryService(q).compactOnce(context.Background(), machineryConfig(stub, 200))
	if !errors.Is(err, errIneffectiveSummary) {
		t.Fatalf("compact error = %v, want errIneffectiveSummary: a summary must shrink what it replaces", err)
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

	_, err := newMachineryService(q).compactOnce(context.Background(), cfg)
	if !errors.Is(err, ErrSummaryWindowTooSmall) {
		t.Fatalf("compact error = %v, want ErrSummaryWindowTooSmall", err)
	}
	if stub.calls != 0 {
		t.Fatalf("summarizer calls = %d, want 0: a hopeless window must not burn an LLM call", stub.calls)
	}
	if q.created {
		t.Fatal("attempt row was created: fail closed before claiming sources")
	}
}

func TestRunCompactionReadsAsMuchWhateverOutputTheModelDeclares(t *testing.T) {
	t.Parallel()

	for _, declared := range []int{0, 32000, 131072} {
		stub := &stubModel{summary: "SUMMARY"}
		cfg := machineryConfig(stub, 108000)
		cfg.SummaryWindowTokens = 131072
		cfg.MaxOutputTokens = declared
		rows := qualityRows(t)
		rows[0] = mkRow(t, "user", jsonStr(strings.Repeat("large item ", 16000)), 100)
		if _, err := newMachineryService(&fakeQueries{uncompacted: rows}).compactOnce(context.Background(), cfg); err != nil || stub.calls != 1 {
			t.Fatalf("declared output %d: err=%v calls=%d, want the large item summarized", declared, err, stub.calls)
		}
		if limit := statedSummaryLimit(t, stub.prompt); limit > stub.maxTokens {
			t.Fatalf("declared output %d: stated limit %d exceeds the request's max_tokens %d", declared, limit, stub.maxTokens)
		}
	}
}

func TestRunCompactionShowsAnUnsavedRequestAsTheGoal(t *testing.T) {
	t.Parallel()

	stub := &stubModel{summary: "SUMMARY"}
	cfg := machineryConfig(stub, 200)
	cfg.CurrentRequest = "deploy the staging build"
	if _, err := newMachineryService(&fakeQueries{uncompacted: qualityRows(t)}).compactOnce(context.Background(), cfg); err != nil {
		t.Fatalf("compact: %v", err)
	}
	if !strings.Contains(stub.prompt, "<current_request>\nuser: deploy the staging build") {
		t.Fatalf("summarizer prompt lacks the unsaved request:\n%s", stub.prompt)
	}
}
