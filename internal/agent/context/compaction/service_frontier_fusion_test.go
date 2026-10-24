package compaction

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/models"
)

func TestDoCompactionFusionIneffectiveGateCountsAllReplacedParentSummaries(t *testing.T) {
	t.Parallel()

	stub := &stubModel{}
	cfg := machineryConfig(stub, 100000)
	rows := fusionQualityRows(t, cfg)
	parents := fusionParentLogs(t, cfg, strings.Repeat("a", 2400), strings.Repeat("b", 2400))

	items, _ := candidatesFromRows(rows)
	entries, ids := buildEntriesAndIDs(foldableItems(items))
	raw := max(entriesPromptCost(entries), compactedReplayTokens(items, ids))
	partialReplacement := raw + summaryReplayTokens(parents[0].Summary)
	wholeReplacement := partialReplacement + summaryReplayTokens(parents[1].Summary)
	stub.summary = strings.Repeat("s", partialReplacement*4)
	summaryTokens := summaryReplayTokens(stub.summary)
	if summaryTokens < partialReplacement || summaryTokens >= wholeReplacement {
		t.Fatalf("test setup invalid: summary=%d partial=%d whole=%d", summaryTokens, partialReplacement, wholeReplacement)
	}

	q := &fakeQueries{uncompacted: rows, priorLogs: parents}
	res, err := newMachineryService(q).compactOnce(context.Background(), cfg)
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	if res.Status != passOK || len(q.rollupCalls) != 1 {
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

func TestDoCompactionFoldsEveryParentSummaryWhole(t *testing.T) {
	t.Parallel()

	stub := &stubModel{summary: "concise whole frontier replacement"}
	cfg := machineryConfig(stub, 100000)
	cfg.SummaryWindowTokens = 50000

	oldest := "absorbed-oldest:" + strings.Repeat("a", 18800)
	middle := "absorbed-middle:" + strings.Repeat("b", 4000)
	newest := "absorbed-newest:" + strings.Repeat("c", 1200)
	parents := fusionParentLogs(t, cfg, oldest, middle, newest)
	artifacts := fusionArtifacts(t, parents)

	q := &fakeQueries{uncompacted: fusionQualityRows(t, cfg), priorLogs: parents}
	res, err := newMachineryService(q).compactOnce(context.Background(), cfg)
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	if res.Status != passOK || len(q.rollupCalls) != 1 {
		t.Fatalf("whole-frontier fusion result = %#v rollup_calls=%d, want ok with one rollup", res, len(q.rollupCalls))
	}
	if want := models.ResolveGenerationLimits(models.ClientTypeOpenAICompletions, nil, cfg.SummaryWindowTokens, 0).MaxOutputTokens; stub.maxTokens != want {
		t.Fatalf("summarizer max output tokens = %d, want the model's allowance %d", stub.maxTokens, want)
	}
	for _, summary := range []string{oldest, middle, newest} {
		if !strings.Contains(stub.prompt, "<earlier_summary>\n"+summary+"\n</earlier_summary>") {
			t.Fatalf("summarizer did not read parent %q whole", summary[:16])
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

func TestDoCompactionFoldsTheActiveSummaryIntoOneArtifact(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		summaries []string
		budget    int
		newRows   bool
		wantFold  bool
	}{
		{name: "one parent and new rows", summaries: []string{"earlier decision: ship on friday"}, budget: 100000, newRows: true, wantFold: true},
		{name: "legacy parents that fit without new rows", summaries: []string{"earlier decision: ship on friday", "later decision: freeze on thursday"}, budget: 100000},
		{name: "parents that no longer fit without new rows", summaries: []string{strings.Repeat("a", 2000), strings.Repeat("b", 2000)}, budget: 800, wantFold: true},
		{name: "parents that replay within the room but past the summary limit", summaries: []string{strings.Repeat("a", 2000), strings.Repeat("b", 2000)}, budget: 1400},
		{name: "one parent without new rows", summaries: []string{"earlier decision: ship on friday"}, budget: 100000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			stub := &stubModel{summary: "one replacement summary"}
			cfg := machineryConfig(stub, tc.budget)
			parents := fusionParentLogs(t, cfg, tc.summaries...)
			q := &fakeQueries{priorLogs: parents}
			if tc.newRows {
				q.uncompacted = fusionQualityRows(t, cfg)
			}

			res, err := newMachineryService(q).compactOnce(context.Background(), cfg)
			if err != nil {
				t.Fatalf("compact: %v", err)
			}
			if !tc.wantFold {
				if res.Status != passNoop || stub.calls != 0 || q.created {
					t.Fatalf("result = %#v calls=%d created=%t, want a noop without a summarizer call", res, stub.calls, q.created)
				}
				return
			}
			if res.Status != passOK || len(q.rollupCalls) != 1 || len(q.completeCalls) != 0 {
				t.Fatalf("result = %#v rollups=%d ordinary=%d, want one artifact replacing the frontier", res, len(q.rollupCalls), len(q.completeCalls))
			}
			if got := q.rollupCalls[0].Parents; len(got) != len(parents) {
				t.Fatalf("rollup parents = %v, want every active summary", got)
			}
			for _, parent := range parents {
				if q.parentSuccessors[parent.ID] != q.createdLogID {
					t.Fatalf("parent %v stayed active beside the new summary", parent.ID)
				}
			}
			for _, summary := range tc.summaries {
				if !strings.Contains(stub.prompt, summary) {
					t.Fatalf("summarizer prompt omitted the folded summary %q:\n%s", summary, stub.prompt)
				}
			}
		})
	}
}

func TestDoCompactionBackfillsLegacyParentCoverageFromItsRows(t *testing.T) {
	t.Parallel()

	stub := &stubModel{summary: "one replacement summary"}
	cfg := machineryConfig(stub, 200)
	q, parents, legacyRows := legacyParentQueries(t, cfg)

	res, err := newMachineryService(q).compactOnce(context.Background(), cfg)
	if err != nil || res.Status != passOK || len(q.rollupCalls) != 1 {
		t.Fatalf("result = %#v err=%v rollups=%d, want the legacy parent folded", res, err, len(q.rollupCalls))
	}
	coverage, err := DecodeArtifactCoverage(q.rollupCalls[0].Coverage)
	if err != nil {
		t.Fatalf("DecodeArtifactCoverage: %v", err)
	}
	covered := make(map[string]bool, len(coverage))
	for _, source := range coverage {
		covered[source.Ref.StableKey()] = true
	}
	legacyItems, _ := candidatesFromRows(legacyRows)
	for _, item := range legacyItems {
		if !covered[item.Record.Ref.StableKey()] {
			t.Fatalf("rollup coverage %v omitted legacy row %v", coverage, item.ID)
		}
	}
	if len(coverage) != len(legacyItems)+len(q.markedIDs) {
		t.Fatalf("rollup coverage = %d sources, want %d legacy rows plus %d new rows", len(coverage), len(legacyItems), len(q.markedIDs))
	}
	if len(q.backfills) != 1 || q.backfills[0].ID != parents[0].ID {
		t.Fatalf("backfills = %#v, want the legacy parent's coverage persisted before it is superseded", q.backfills)
	}
	persisted, err := DecodeArtifactCoverage(q.backfills[0].Coverage)
	if err != nil || len(persisted) != len(legacyItems) || q.backfills[0].AnchorStartMs != persisted[0].CreatedAtMs {
		t.Fatalf("persisted coverage = %v anchor=%d err=%v, want the legacy rows", persisted, q.backfills[0].AnchorStartMs, err)
	}
}

func TestDoCompactionKeepsLegacyParentUntouchedWhenTheSummaryFails(t *testing.T) {
	t.Parallel()

	cfg := machineryConfig(&stubModel{}, 200)
	q, _, _ := legacyParentQueries(t, cfg)

	if _, err := newMachineryService(q).compactOnce(context.Background(), cfg); err == nil {
		t.Fatal("empty summary succeeded")
	}
	if len(q.backfills) != 0 || len(q.rollupCalls) != 0 {
		t.Fatalf("failed compaction wrote backfills=%d rollups=%d, want the legacy parent unchanged", len(q.backfills), len(q.rollupCalls))
	}
}

func TestDoCompactionRefusesASummaryPastTheRequestCutoff(t *testing.T) {
	t.Parallel()

	stub := &stubModel{summary: "would fold rows after the cutoff"}
	cfg := machineryConfig(stub, 100000)
	parents := fusionParentLogs(t, cfg, "earlier", "spans the cutoff")
	cfg.CandidatesBefore = time.UnixMilli(parents[1].AnchorEndMs)
	q := &fakeQueries{uncompacted: fusionQualityRows(t, cfg), priorLogs: parents}

	if _, err := newMachineryService(q).compactOnce(context.Background(), cfg); !errors.Is(err, errSummaryPastCutoff) {
		t.Fatalf("err = %v, want errSummaryPastCutoff", err)
	}
	if stub.calls != 0 || len(q.rollupCalls) != 0 {
		t.Fatalf("summarizer calls=%d rollups=%d, want nothing folded", stub.calls, len(q.rollupCalls))
	}
}

// An edit or retry's event first rewinds the session to before the turn it
// replaces, as the replacement's first step will, so it folds onto the
// summary that ends before the cutoff instead of refusing the one the
// replaced run wrote past it.
func TestCompactRewindsTheReplacedTurnBeforeFolding(t *testing.T) {
	t.Parallel()

	stub := &stubModel{summary: "folded before the cutoff"}
	cfg := machineryConfig(stub, 100000)
	parents := fusionParentLogs(t, cfg, "earlier", "spans the cutoff")
	cfg.CandidatesBefore = time.UnixMilli(parents[1].AnchorEndMs)
	cfg.ReplacedTurnID = uuid.NewString()
	q := &fakeQueries{uncompacted: fusionQualityRows(t, cfg), priorLogs: parents}
	q.onRewind = func() { q.priorLogs = parents[:1] }

	passes, err := newMachineryService(q).Compact(context.Background(), cfg)
	if err != nil || len(passes) != 1 {
		t.Fatalf("passes=%d err=%v, want one pass after the rewind", len(passes), err)
	}
	turnID := pgtype.UUID{Bytes: uuid.MustParse(cfg.ReplacedTurnID), Valid: true}
	if len(q.rewinds) != 1 || q.rewinds[0].TurnID != turnID || q.rewinds[0].SessionID.String() != cfg.SessionID {
		t.Fatalf("rewinds=%+v, want one before the replaced turn", q.rewinds)
	}
	if absorbed := passes[0].Absorbed; len(absorbed) != 1 || absorbed[0] != formatUUID(parents[0].ID) {
		t.Fatalf("absorbed=%v, want only the summary that ends before the cutoff", absorbed)
	}
}

func legacyParentQueries(t *testing.T, cfg TriggerConfig) (*fakeQueries, []sqlc.BotHistoryMessageCompact, []sqlc.ListUncompactedMessagesBySessionRow) {
	t.Helper()
	parents := fusionParentLogs(t, cfg, "legacy summary")
	parents[0].Coverage = []byte("[]")
	legacyRows := []sqlc.ListUncompactedMessagesBySessionRow{
		mkRow(t, "user", `"legacy question"`, 10),
		mkRow(t, "assistant", `"legacy answer"`, 10),
	}
	setFusionRowScopeAndTimes(t, cfg, legacyRows)
	absorbed := make([]sqlc.ListMessagesByCompactIDRow, 0, len(legacyRows))
	for _, row := range legacyRows {
		row.CompactID = parents[0].ID
		absorbed = append(absorbed, sqlc.ListMessagesByCompactIDRow(row))
	}
	return &fakeQueries{
		uncompacted:  fusionQualityRows(t, cfg),
		priorLogs:    parents,
		absorbedRows: map[pgtype.UUID][]sqlc.ListMessagesByCompactIDRow{parents[0].ID: absorbed},
	}, parents, legacyRows
}

func TestDoCompactionFusionRejectsSummaryNotSmallerThanEverythingReplaced(t *testing.T) {
	t.Parallel()

	stub := &stubModel{}
	cfg := machineryConfig(stub, 100000)
	rows := fusionQualityRows(t, cfg)
	parents := fusionParentLogs(t, cfg, strings.Repeat("a", 2400), strings.Repeat("b", 2400))

	items, _ := candidatesFromRows(rows)
	entries, ids := buildEntriesAndIDs(foldableItems(items))
	replacementTokens := max(entriesPromptCost(entries), compactedReplayTokens(items, ids)) + summaryReplayTokens(parents[0].Summary) + summaryReplayTokens(parents[1].Summary)
	stub.summary = strings.Repeat("s", replacementTokens*4)
	if summaryTokens := summaryReplayTokens(stub.summary); summaryTokens < replacementTokens {
		t.Fatalf("test setup invalid: summary=%d replacement=%d", summaryTokens, replacementTokens)
	}

	q := &fakeQueries{uncompacted: rows, priorLogs: parents}
	_, err := newMachineryService(q).compactOnce(context.Background(), cfg)
	if !errors.Is(err, errIneffectiveSummary) {
		t.Fatalf("compact error = %v, want errIneffectiveSummary", err)
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
	cfg.HTTPClient = &http.Client{Transport: failing}
	parents := fusionParentLogs(t, cfg, strings.Repeat("a", 2400), strings.Repeat("b", 2400))
	q := &fakeQueries{uncompacted: fusionQualityRows(t, cfg), priorLogs: parents}

	_, err := newMachineryService(q).compactOnce(context.Background(), cfg)
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
	parents := fusionParentLogs(t, cfg, strings.Repeat("a", 2400), strings.Repeat("b", 2400))
	q := &fakeQueries{
		uncompacted: fusionQualityRows(t, cfg),
		priorLogs:   parents,
		rollupErr:   pgx.ErrNoRows,
	}

	_, err := newMachineryService(q).compactOnce(context.Background(), cfg)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("compact error = %v, want pgx.ErrNoRows", err)
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
		rows = append(rows, mkRow(t, "tool", fmt.Sprintf(`[{"type":"tool-result","toolCallId":"c%d","toolName":"probe","output":{"type":"text","value":"%s"}}]`, i, strings.Repeat("ok ", 200)), 100))
	}
	rows = append(rows, mkRow(t, "user", `"current question"`, 100))

	stub := &stubModel{summary: "must not run"}
	cfg := machineryConfig(stub, 100000)
	cfg.SummaryWindowTokens = 8000
	setFusionRowScopeAndTimes(t, cfg, rows)
	parents := fusionParentLogs(t, cfg, strings.Repeat("a", 40), strings.Repeat("b", 40))
	q := &fakeQueries{uncompacted: rows, priorLogs: parents}

	_, err := newMachineryService(q).compactOnce(context.Background(), cfg)
	if !errors.Is(err, errCompactionInputOverflow) {
		t.Fatalf("compact error = %v, want errCompactionInputOverflow", err)
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

func TestDoCompactionFusionUnreadableEarlierSummariesFailBeforeClaimsAndProvider(t *testing.T) {
	t.Parallel()

	stub := &stubModel{summary: "must not run"}
	cfg := machineryConfig(stub, 100000)
	cfg.SummaryWindowTokens = 8000
	parents := fusionParentLogs(t, cfg,
		strings.Repeat("a", 5000),
		strings.Repeat("b", 5000),
		strings.Repeat("c", 5000),
		strings.Repeat("d", 5000),
		strings.Repeat("e", 5000),
	)
	q := &fakeQueries{uncompacted: fusionQualityRows(t, cfg), priorLogs: parents}

	_, err := newMachineryService(q).compactOnce(context.Background(), cfg)
	if !errors.Is(err, errCompactionInputOverflow) {
		t.Fatalf("compact error = %v, want errCompactionInputOverflow", err)
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
