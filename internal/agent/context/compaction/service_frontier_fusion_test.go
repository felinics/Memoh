package compaction

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

func TestDoCompactionFusionIneffectiveGateCountsAllReplacedParentSummaries(t *testing.T) {
	t.Parallel()

	stub := &stubModel{}
	cfg := machineryConfig(stub, 200)
	cfg.AllowFrontierFusion = true
	cfg.MaxCompactTokens = 4000
	rows := fusionQualityRows(t, cfg)
	parents := fusionParentLogs(t, cfg, strings.Repeat("a", 2400), strings.Repeat("b", 2400))

	items, _ := itemsFromRows(rows)
	entries, _ := buildEntriesAndIDs(splitByTarget(items, cfg.TargetTokens))
	artifacts := fusionArtifacts(t, parents)
	partialReplacement := summaryReplacementTokens(entries, artifacts[:1])
	wholeReplacement := summaryReplacementTokens(entries, artifacts)
	stub.summary = strings.Repeat("s", partialReplacement*4)
	summaryTokens := estimateSummaryReplayTokens(stub.summary)
	if summaryTokens < partialReplacement || summaryTokens >= wholeReplacement {
		t.Fatalf("test setup invalid: summary=%d partial=%d whole=%d", summaryTokens, partialReplacement, wholeReplacement)
	}

	q := &fakeQueries{uncompacted: rows, priorLogs: parents}
	res, err := newMachineryService(q).RunCompactionSync(context.Background(), cfg)
	if err != nil {
		t.Fatalf("RunCompactionSync: %v", err)
	}
	if res.Status != StatusOK || len(q.rollupCalls) != 1 {
		t.Fatalf("fusion result = %#v rollup_calls=%d, want ok with one rollup completion", res, len(q.rollupCalls))
	}
	if len(q.completeCalls) != 0 {
		t.Fatalf("ordinary completion calls = %d, want none on successful fusion", len(q.completeCalls))
	}
	for _, parent := range parents {
		if successor := q.parentSuccessors[parent.ID]; successor != q.createdLogID {
			t.Fatalf("replaced parent %v successor = %v, want rollup %v", parent.ID, successor, q.createdLogID)
		}
	}
}

func TestDoCompactionFusionAbsorbsWholeFrontierWithEmptyPriorContext(t *testing.T) {
	t.Parallel()

	stub := &stubModel{summary: "concise whole frontier replacement"}
	cfg := machineryConfig(stub, 200)
	cfg.AllowFrontierFusion = true
	cfg.MaxCompactTokens = 50000
	cfg.ContextWindowTokens = 10000
	cfg.SummaryWindowTokens = 50000

	absorbedOldest := "absorbed-oldest:" + strings.Repeat("a", 18800)
	absorbedMiddle := "absorbed-middle:" + strings.Repeat("b", 4000)
	absorbedNewest := "absorbed-newest:" + strings.Repeat("c", 1200)
	parents := fusionParentLogs(t, cfg, absorbedOldest, absorbedMiddle, absorbedNewest)
	artifacts := fusionArtifacts(t, parents)
	retainBudget := frontierRetainBudget(cfg, cfg.MaxCompactTokens)
	if !shouldFuseFrontier(cfg, artifacts, cfg.MaxCompactTokens) {
		t.Fatalf("test setup did not cross retain budget: frontier=%d retain=%d", frontierSummaryTokens(artifacts), retainBudget)
	}
	expectedInputBudget, err := boundedCompactionInputTokens(cfg, cfg.MaxCompactTokens, maxCompactionSummaryTokens, fusionSystemPrompt)
	if err != nil {
		t.Fatalf("bounded fusion input fixture: %v", err)
	}

	q := &fakeQueries{uncompacted: fusionQualityRows(t, cfg), priorLogs: parents}
	var logOutput bytes.Buffer
	svc := NewService(slog.New(slog.NewTextHandler(&logOutput, nil)), q)
	res, err := svc.RunCompactionSync(context.Background(), cfg)
	if err != nil {
		t.Fatalf("RunCompactionSync: %v", err)
	}
	if res.Status != StatusOK || len(q.rollupCalls) != 1 {
		t.Fatalf("whole-frontier fusion result = %#v rollup_calls=%d, want ok with one rollup", res, len(q.rollupCalls))
	}
	if stub.maxTokens != maxCompactionSummaryTokens {
		t.Fatalf("fusion max output tokens = %d, want ordinary cap %d", stub.maxTokens, maxCompactionSummaryTokens)
	}
	if want := fmt.Sprintf("max_compact_tokens=%d", expectedInputBudget); !strings.Contains(logOutput.String(), want) {
		t.Fatalf("fusion input budget log missing %q:\n%s", want, logOutput.String())
	}

	if strings.Contains(stub.prompt, "The following are summaries of earlier parts of this conversation.") {
		t.Fatalf("whole-frontier fusion retained prior context:\n%s", stub.prompt)
	}
	absorbedStart := strings.LastIndex(stub.prompt, "<absorbed_context>")
	absorbedEnd := strings.LastIndex(stub.prompt, "</absorbed_context>")
	if absorbedStart < 0 || absorbedEnd <= absorbedStart {
		t.Fatalf("whole-frontier fusion prompt section offsets = absorbed %d..%d", absorbedStart, absorbedEnd)
	}
	absorbedSection := stub.prompt[absorbedStart:absorbedEnd]
	for _, absorbed := range []string{"absorbed-oldest:", "absorbed-middle:", "absorbed-newest:"} {
		if !strings.Contains(absorbedSection, absorbed) {
			t.Fatalf("absorbed context omitted parent %q:\n%s", absorbed, stub.prompt)
		}
	}

	rollup := q.rollupCalls[0]
	if len(rollup.Parents) != len(parents) {
		t.Fatalf("rollup parents = %v, want all %d active parents", rollup.Parents, len(parents))
	}
	for i, parent := range parents {
		if rollup.Parents[i] != parent.ID {
			t.Fatalf("rollup parent[%d] = %v, want %v", i, rollup.Parents[i], parent.ID)
		}
	}
	if want := rollupArtifactLevel(artifacts); int(rollup.Level) != want {
		t.Fatalf("rollup level = %d, want whole-frontier level %d", rollup.Level, want)
	}
	coverage, err := DecodeArtifactCoverage(rollup.Coverage)
	if err != nil {
		t.Fatalf("DecodeArtifactCoverage(rollup): %v", err)
	}
	coveredKeys := make(map[string]bool, len(coverage))
	for _, source := range coverage {
		coveredKeys[source.Ref.StableKey()] = true
	}
	for _, artifact := range artifacts {
		if !coveredKeys[artifact.Coverage[0].Ref.StableKey()] {
			t.Fatalf("rollup coverage omitted parent %q", artifact.ID)
		}
	}
	if rollup.AnchorStartMs != parents[0].AnchorStartMs || rollup.AnchorEndMs <= parents[len(parents)-1].AnchorEndMs {
		t.Fatalf("rollup anchor = %d..%d, want it to span all parents through a newer row", rollup.AnchorStartMs, rollup.AnchorEndMs)
	}
	for _, parent := range parents {
		if successor := q.parentSuccessors[parent.ID]; successor != q.createdLogID {
			t.Fatalf("absorbed parent %v successor = %v, want %v", parent.ID, successor, q.createdLogID)
		}
	}
}

func TestDoCompactionFusionLegacyCoverageFallsBackToOrdinaryPass(t *testing.T) {
	t.Parallel()

	stub := &stubModel{summary: "concise new-segment summary"}
	cfg := machineryConfig(stub, 200)
	cfg.AllowFrontierFusion = true
	cfg.MaxCompactTokens = 4000
	parents := fusionParentLogs(t, cfg, strings.Repeat("a", 2400), strings.Repeat("b", 2400))
	parents[1].Coverage = []byte("[]")
	q := &fakeQueries{uncompacted: fusionQualityRows(t, cfg), priorLogs: parents}
	svc := newMachineryService(q)

	res, err := svc.RunCompactionSync(context.Background(), cfg)
	if err != nil {
		t.Fatalf("RunCompactionSync: %v", err)
	}
	if res.Status != StatusOK || len(q.completeCalls) != 1 || q.completeCalls[0].Status != "ok" {
		t.Fatalf("legacy fallback result = %#v ordinary_calls=%#v, want successful ordinary completion", res, q.completeCalls)
	}
	if len(q.rollupCalls) != 0 {
		t.Fatalf("rollup completion calls = %d, want none for legacy parents", len(q.rollupCalls))
	}
	if stub.maxTokens != maxCompactionSummaryTokens {
		t.Fatalf("legacy fallback max output tokens = %d, want ordinary cap %d", stub.maxTokens, maxCompactionSummaryTokens)
	}
	if !strings.Contains(stub.prompt, "<prior_context>") || strings.Contains(stub.prompt, "<absorbed_context>") {
		t.Fatalf("legacy fallback prompt did not use ordinary mode:\n%s", stub.prompt)
	}
	assertFusionParentsActive(t, q, parents)
	if _, armed := svc.failedAt[cfg.SessionID]; armed {
		t.Fatal("successful legacy fallback armed the failure cooldown")
	}
}

func TestDoCompactionFusionRejectsSummaryNotSmallerThanEverythingReplaced(t *testing.T) {
	t.Parallel()

	stub := &stubModel{}
	cfg := machineryConfig(stub, 200)
	cfg.AllowFrontierFusion = true
	cfg.MaxCompactTokens = 4000
	rows := fusionQualityRows(t, cfg)
	parents := fusionParentLogs(t, cfg, strings.Repeat("a", 2400), strings.Repeat("b", 2400))

	items, _ := itemsFromRows(rows)
	entries, _ := buildEntriesAndIDs(splitByTarget(items, cfg.TargetTokens))
	replacementTokens := summaryReplacementTokens(entries, fusionArtifacts(t, parents))
	stub.summary = strings.Repeat("s", replacementTokens*4)
	if summaryTokens := estimateSummaryReplayTokens(stub.summary); summaryTokens < replacementTokens {
		t.Fatalf("test setup invalid: summary=%d replacement=%d", summaryTokens, replacementTokens)
	}

	q := &fakeQueries{uncompacted: rows, priorLogs: parents}
	_, err := newMachineryService(q).RunCompactionSync(context.Background(), cfg)
	if !errors.Is(err, errIneffectiveSummary) {
		t.Fatalf("RunCompactionSync error = %v, want errIneffectiveSummary", err)
	}
	if stub.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", stub.calls)
	}
	if len(q.rollupCalls) != 0 {
		t.Fatalf("rollup completion calls = %d, want none", len(q.rollupCalls))
	}
	assertFusionFailureState(t, q, parents)
}

func TestDoCompactionFusionProviderFailureKeepsParentsActiveAndClaimsReclaimable(t *testing.T) {
	t.Parallel()

	failing := &failingModel{}
	cfg := machineryConfig(&stubModel{}, 200)
	cfg.AllowFrontierFusion = true
	cfg.MaxCompactTokens = 4000
	cfg.HTTPClient = &http.Client{Transport: failing}
	parents := fusionParentLogs(t, cfg, strings.Repeat("a", 2400), strings.Repeat("b", 2400))
	q := &fakeQueries{uncompacted: fusionQualityRows(t, cfg), priorLogs: parents}

	_, err := newMachineryService(q).RunCompactionSync(context.Background(), cfg)
	if err == nil {
		t.Fatal("provider failure must surface an error")
	}
	if failing.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", failing.calls)
	}
	if len(q.rollupCalls) != 0 {
		t.Fatalf("rollup completion calls = %d, want none after provider failure", len(q.rollupCalls))
	}
	assertFusionFailureState(t, q, parents)
}

func TestDoCompactionFusionRollupCASFailureKeepsParentsActiveAndClaimsReclaimable(t *testing.T) {
	t.Parallel()

	stub := &stubModel{summary: "concise fused replacement"}
	cfg := machineryConfig(stub, 200)
	cfg.AllowFrontierFusion = true
	cfg.MaxCompactTokens = 4000
	parents := fusionParentLogs(t, cfg, strings.Repeat("a", 2400), strings.Repeat("b", 2400))
	q := &fakeQueries{
		uncompacted: fusionQualityRows(t, cfg),
		priorLogs:   parents,
		rollupErr:   pgx.ErrNoRows,
	}

	_, err := newMachineryService(q).RunCompactionSync(context.Background(), cfg)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("RunCompactionSync error = %v, want pgx.ErrNoRows", err)
	}
	if stub.calls != 1 {
		t.Fatalf("provider calls = %d, want 1 before finalize CAS failure", stub.calls)
	}
	if len(q.rollupCalls) != 1 {
		t.Fatalf("rollup completion calls = %d, want 1", len(q.rollupCalls))
	}
	assertFusionFailureState(t, q, parents)
}

func TestDoCompactionFusionFinalOverflowFailsBeforeClaimsAndProvider(t *testing.T) {
	t.Parallel()

	const fanout = 40
	callParts := make([]string, 0, fanout)
	for i := 0; i < fanout; i++ {
		callParts = append(callParts, fmt.Sprintf(`{"type":"tool-call","toolCallId":"c%d","toolName":"probe","input":{}}`, i))
	}
	rows := []sqlc.ListUncompactedMessagesBySessionRow{
		mkRow(t, "assistant", "["+strings.Join(callParts, ",")+"]", 100),
	}
	for i := 0; i < fanout; i++ {
		rows = append(rows, mkRow(t, "tool", fmt.Sprintf(`[{"type":"tool-result","toolCallId":"c%d","toolName":"probe","output":{"type":"text","value":"ok"}}]`, i), 100))
	}
	rows = append(rows, mkRow(t, "user", `"current question"`, 100))

	stub := &stubModel{summary: "must not run"}
	cfg := machineryConfig(stub, 100)
	cfg.AllowFrontierFusion = true
	cfg.MaxCompactTokens = 80
	setFusionRowScopeAndTimes(t, cfg, rows)
	parents := fusionParentLogs(t, cfg, strings.Repeat("a", 40), strings.Repeat("b", 40))
	q := &fakeQueries{uncompacted: rows, priorLogs: parents}

	_, err := newMachineryService(q).RunCompactionSync(context.Background(), cfg)
	if !errors.Is(err, errCompactionInputOverflow) {
		t.Fatalf("RunCompactionSync error = %v, want errCompactionInputOverflow", err)
	}
	if stub.calls != 0 {
		t.Fatalf("provider calls = %d, want 0", stub.calls)
	}
	if q.created || len(q.markedIDs) != 0 {
		t.Fatalf("overflow crossed the claim boundary: created=%t marked=%v", q.created, q.markedIDs)
	}
	if len(q.rollupCalls) != 0 || len(q.completeCalls) != 0 {
		t.Fatalf("overflow completion calls: rollup=%d ordinary=%d, want zero", len(q.rollupCalls), len(q.completeCalls))
	}
	assertFusionParentsActive(t, q, parents)
}

func TestDoCompactionFusionManyParentHeadersFailBeforeClaimsAndProvider(t *testing.T) {
	t.Parallel()

	stub := &stubModel{summary: "must not run"}
	cfg := machineryConfig(stub, 200)
	cfg.AllowFrontierFusion = true
	cfg.MaxCompactTokens = 100
	parents := fusionParentLogs(t, cfg,
		strings.Repeat("a", 32),
		strings.Repeat("b", 32),
		strings.Repeat("c", 32),
		strings.Repeat("d", 32),
		strings.Repeat("e", 32),
	)
	q := &fakeQueries{uncompacted: fusionQualityRows(t, cfg), priorLogs: parents}

	_, err := newMachineryService(q).RunCompactionSync(context.Background(), cfg)
	if !errors.Is(err, errCompactionInputOverflow) {
		t.Fatalf("RunCompactionSync error = %v, want errCompactionInputOverflow", err)
	}
	if stub.calls != 0 {
		t.Fatalf("provider calls = %d, want 0", stub.calls)
	}
	if q.created || len(q.markedIDs) != 0 {
		t.Fatalf("header overflow crossed the claim boundary: created=%t marked=%v", q.created, q.markedIDs)
	}
	assertFusionParentsActive(t, q, parents)
}

func fusionQualityRows(t *testing.T, cfg TriggerConfig) []sqlc.ListUncompactedMessagesBySessionRow {
	t.Helper()
	rows := qualityRows(t)
	setFusionRowScopeAndTimes(t, cfg, rows)
	return rows
}

func setFusionRowScopeAndTimes(t *testing.T, cfg TriggerConfig, rows []sqlc.ListUncompactedMessagesBySessionRow) {
	t.Helper()
	botID := pgtype.UUID{Bytes: uuid.MustParse(cfg.BotID), Valid: true}
	sessionID := pgtype.UUID{Bytes: uuid.MustParse(cfg.SessionID), Valid: true}
	for i := range rows {
		rows[i].BotID = botID
		rows[i].SessionID = sessionID
		rows[i].CreatedAt = pgtype.Timestamptz{Time: time.UnixMilli(int64(i+3) * 1000), Valid: true}
	}
}

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

func fusionArtifacts(t *testing.T, rows []sqlc.BotHistoryMessageCompact) []Artifact {
	t.Helper()
	artifacts := make([]Artifact, 0, len(rows))
	for _, row := range rows {
		artifact, err := artifactFromDBRow(row)
		if err != nil {
			t.Fatalf("artifactFromDBRow: %v", err)
		}
		artifacts = append(artifacts, artifact)
	}
	return artifacts
}

func assertFusionFailureState(t *testing.T, q *fakeQueries, parents []sqlc.BotHistoryMessageCompact) {
	t.Helper()
	if !q.created || len(q.markedIDs) == 0 {
		t.Fatalf("fusion attempt did not claim new rows: created=%t marked=%v", q.created, q.markedIDs)
	}
	if len(q.completeCalls) != 1 || q.completeCalls[0].Status != "error" {
		t.Fatalf("ordinary completions = %#v, want one terminal error", q.completeCalls)
	}
	if status := q.logStatuses[q.createdLogID]; status != "error" {
		t.Fatalf("attempt status = %q, want error so its rows are reclaimable", status)
	}
	for _, id := range q.markedIDs {
		if claim := q.claims[id]; claim != q.createdLogID {
			t.Fatalf("new row %v claim = %v, want failed log %v", id, claim, q.createdLogID)
		}
	}
	assertFusionParentsActive(t, q, parents)
}

func assertFusionParentsActive(t *testing.T, q *fakeQueries, parents []sqlc.BotHistoryMessageCompact) {
	t.Helper()
	for _, parent := range parents {
		if parent.SupersededBy.Valid || parent.SupersededAt.Valid {
			t.Fatalf("fixture parent %v was already superseded", parent.ID)
		}
		if successor, ok := q.parentSuccessors[parent.ID]; ok {
			t.Fatalf("parent %v was superseded by %v on failed fusion", parent.ID, successor)
		}
	}
}
