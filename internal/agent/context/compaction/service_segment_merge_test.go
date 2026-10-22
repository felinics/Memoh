package compaction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/chat/contextepoch"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// mergeEpochQueries is an in-memory contextepoch.Queries for merge tests. The
// merge only reads the state (the advance itself rides the rollup SQL), so
// writes are not needed.
type mergeEpochQueries struct {
	stored string
}

func (f *mergeEpochQueries) GetSessionContextEpoch(_ context.Context, _ pgtype.UUID) (string, error) {
	return f.stored, nil
}

func (*mergeEpochQueries) AdvanceSessionContextEpoch(_ context.Context, _ sqlc.AdvanceSessionContextEpochParams) (string, error) {
	panic("merge must not advance the epoch outside the rollup statement")
}

func (*mergeEpochQueries) PinSessionContextEpochPrefixHash(_ context.Context, _ sqlc.PinSessionContextEpochPrefixHashParams) (string, error) {
	panic("merge must not pin the prefix hash")
}

func (*mergeEpochQueries) PinSessionContextEpochBudget(_ context.Context, _ sqlc.PinSessionContextEpochBudgetParams) (string, error) {
	panic("merge must not pin the budget outside the rollup statement")
}

func newMergeFixture(t *testing.T, segments int, cfg TriggerConfig) (*Service, *fakeQueries, *stubModel, *mergeEpochQueries) {
	t.Helper()
	stub := &stubModel{summary: "merged segment"}
	summaries := make([]string, segments)
	for i := range summaries {
		summaries[i] = fmt.Sprintf("segment-%d %s", i, strings.Repeat("x", 200))
	}
	parents := fusionParentLogs(t, cfg, summaries...)
	q := &fakeQueries{priorLogs: parents}
	svc := newMachineryService(q)
	epochs := &mergeEpochQueries{}
	svc.SetContextEpochs(contextepoch.NewStore(epochs, nil))
	return svc, q, stub, epochs
}

func seedEpochState(t *testing.T, epoch int64, anchorMs int64) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"epoch": epoch, "anchor_created_at_ms": anchorMs, "last_reason": "budget",
	})
	if err != nil {
		t.Fatalf("marshal epoch state: %v", err)
	}
	return string(raw)
}

func TestSegmentMergeSkippedBelowCap(t *testing.T) {
	t.Parallel()

	stub := &stubModel{}
	cfg := machineryConfig(stub, 200)
	svc, q, _, _ := newMergeFixture(t, 3, cfg)

	svc.maybeMergeSegments(context.Background(), pgtype.UUID{}, pgtype.UUID{}, cfg)
	if stub.calls != 0 || len(q.rollupCalls) != 0 {
		t.Fatalf("merge below cap must not run: calls=%d rollups=%d", stub.calls, len(q.rollupCalls))
	}
}

func TestSegmentMergeMergesOldestCapAndAdvancesEpoch(t *testing.T) {
	t.Parallel()

	stub := &stubModel{summary: "merged segment"}
	cfg := machineryConfig(stub, 200)
	svc, q, _, epochs := newMergeFixture(t, defaultSegmentCap+1, cfg)
	epochs.stored = seedEpochState(t, 3, 1000)

	botUUID := pgtype.UUID{Bytes: uuid.MustParse(cfg.BotID), Valid: true}
	sessionUUID := pgtype.UUID{Bytes: uuid.MustParse(cfg.SessionID), Valid: true}
	svc.maybeMergeSegments(context.Background(), botUUID, sessionUUID, cfg)

	if stub.calls != 1 || len(q.rollupCalls) != 1 {
		t.Fatalf("merge did not run once: calls=%d rollups=%d", stub.calls, len(q.rollupCalls))
	}
	rollup := q.rollupCalls[0]
	if len(rollup.Parents) != defaultSegmentCap {
		t.Fatalf("merged parents = %d, want the oldest %d segments", len(rollup.Parents), defaultSegmentCap)
	}
	// The oldest cap segments are the parents; the newest segment survives.
	parentSet := idSet(rollup.Parents)
	frontier, err := NewArtifactProjection(q).LoadActiveSession(context.Background(), ArtifactOwner{BotID: cfg.BotID, SessionID: cfg.SessionID, SessionIDKnown: true})
	if err != nil {
		t.Fatalf("LoadActiveSession: %v", err)
	}
	if len(frontier.Artifacts) != 2 {
		t.Fatalf("frontier after merge = %d artifacts, want merged + newest", len(frontier.Artifacts))
	}
	for _, artifact := range frontier.Artifacts {
		if parentSet[pgtype.UUID{Bytes: uuid.MustParse(artifact.ID), Valid: true}] {
			t.Fatalf("merged parent %s still active", artifact.ID)
		}
	}
	// Read order: merged segment (oldest coverage) before the surviving tail.
	if !strings.HasPrefix(frontier.Artifacts[0].Summary, "merged segment") {
		t.Fatalf("frontier[0] = %q, want the merged segment first", frontier.Artifacts[0].Summary)
	}
	// The epoch advance rode the rollup statement: epoch 3→4, anchor moved to
	// the merged coverage end, reason segment-merge.
	if rollup.ContextEpoch != 4 || rollup.ContextEpochAnchorMs != int64(defaultSegmentCap)*1000 {
		t.Fatalf("epoch advance = (%d, %d), want (4, %d)", rollup.ContextEpoch, rollup.ContextEpochAnchorMs, int64(defaultSegmentCap)*1000)
	}
	var state map[string]any
	if err := json.Unmarshal(rollup.ContextEpochState, &state); err != nil {
		t.Fatalf("decode epoch state: %v", err)
	}
	if state["last_reason"] != contextepoch.ReasonSegmentMerge {
		t.Fatalf("epoch reason = %v, want segment-merge", state["last_reason"])
	}
}

func TestSegmentMergeSkippedWithoutEpochStore(t *testing.T) {
	t.Parallel()

	stub := &stubModel{summary: "merged segment"}
	cfg := machineryConfig(stub, 200)
	parents := fusionParentLogs(t, cfg, make([]string, defaultSegmentCap+1)...)
	q := &fakeQueries{priorLogs: parents}
	svc := newMachineryService(q) // no SetContextEpochs

	svc.maybeMergeSegments(context.Background(), pgtype.UUID{}, pgtype.UUID{}, cfg)
	if stub.calls != 0 || len(q.rollupCalls) != 0 {
		t.Fatalf("merge without epoch store must not run: calls=%d rollups=%d", stub.calls, len(q.rollupCalls))
	}
}

func TestSegmentMergeSkippedWithoutPersistedCoverage(t *testing.T) {
	t.Parallel()

	stub := &stubModel{summary: "merged segment"}
	cfg := machineryConfig(stub, 200)
	svc, q, _, _ := newMergeFixture(t, defaultSegmentCap+1, cfg)
	q.priorLogs[0].Coverage = nil // legacy fused segment without coverage

	svc.maybeMergeSegments(context.Background(), pgtype.UUID{}, pgtype.UUID{}, cfg)
	if stub.calls != 0 || len(q.rollupCalls) != 0 {
		t.Fatalf("merge with legacy coverage-less segment must skip: calls=%d rollups=%d", stub.calls, len(q.rollupCalls))
	}
}

func TestSegmentMergeFailureKeepsParentsActive(t *testing.T) {
	t.Parallel()

	stub := &stubModel{summary: "merged segment", finishReason: "length"}
	cfg := machineryConfig(stub, 200)
	svc, q, _, _ := newMergeFixture(t, defaultSegmentCap+1, cfg)

	svc.maybeMergeSegments(context.Background(), pgtype.UUID{}, pgtype.UUID{}, cfg)
	if len(q.rollupCalls) != 0 {
		t.Fatalf("incomplete summary must not reach the rollup: %d calls", len(q.rollupCalls))
	}
	for _, parent := range q.priorLogs {
		if parent.SupersededBy.Valid {
			t.Fatalf("parent %v superseded despite the failed merge", parent.ID)
		}
	}
}

func TestSegmentMergeRejectsIneffectiveSummary(t *testing.T) {
	t.Parallel()

	stub := &stubModel{summary: strings.Repeat("s", 100*1024)}
	cfg := machineryConfig(stub, 200)
	svc, q, _, _ := newMergeFixture(t, defaultSegmentCap+1, cfg)

	svc.maybeMergeSegments(context.Background(), pgtype.UUID{}, pgtype.UUID{}, cfg)
	if len(q.rollupCalls) != 0 {
		t.Fatalf("a summary no smaller than the merged segments must not complete: %d calls", len(q.rollupCalls))
	}
}

// The chain is bounded: merging the oldest cap segments when the chain
// exceeds the cap drops it back to 2, so the amortized full-invalidation
// rate stays at one merge per ~cap appended segments.
func TestSegmentChainStaysBoundedAcrossMerges(t *testing.T) {
	t.Parallel()

	stub := &stubModel{summary: "merged segment"}
	cfg := machineryConfig(stub, 200)
	svc, q, _, epochs := newMergeFixture(t, defaultSegmentCap+1, cfg)
	epochs.stored = seedEpochState(t, 1, 0)

	botUUID := pgtype.UUID{Bytes: uuid.MustParse(cfg.BotID), Valid: true}
	sessionUUID := pgtype.UUID{Bytes: uuid.MustParse(cfg.SessionID), Valid: true}
	svc.maybeMergeSegments(context.Background(), botUUID, sessionUUID, cfg)
	if len(q.rollupCalls) != 1 {
		t.Fatalf("first merge did not run: rollups=%d", len(q.rollupCalls))
	}
	// The first merge's epoch advance rode the rollup statement; reflect the
	// committed state like the SQL write would.
	epochs.stored = seedEpochState(t, 2, int64(defaultSegmentCap)*1000)

	// Immediately re-checking must not merge again: the chain is back under
	// the cap.
	svc.maybeMergeSegments(context.Background(), botUUID, sessionUUID, cfg)
	if len(q.rollupCalls) != 1 {
		t.Fatalf("second check merged again: rollups=%d", len(q.rollupCalls))
	}
	frontier, err := NewArtifactProjection(q).LoadActiveSession(context.Background(), ArtifactOwner{BotID: cfg.BotID, SessionID: cfg.SessionID, SessionIDKnown: true})
	if err != nil {
		t.Fatalf("LoadActiveSession: %v", err)
	}
	if len(frontier.Artifacts) != 2 {
		t.Fatalf("chain length = %d, want 2 (merged + newest)", len(frontier.Artifacts))
	}
}

// A merge whose coverage end is behind the current anchor advances the epoch
// in place (anchor untouched) instead of moving the bookmark backward.
func TestSegmentMergeKeepsAnchorWhenCoverageIsBehind(t *testing.T) {
	t.Parallel()

	stub := &stubModel{summary: "merged segment"}
	cfg := machineryConfig(stub, 200)
	svc, q, _, epochs := newMergeFixture(t, defaultSegmentCap+1, cfg)
	epochs.stored = seedEpochState(t, 7, 99999999)

	svc.maybeMergeSegments(context.Background(), pgtype.UUID{}, pgtype.UUID{}, cfg)
	if len(q.rollupCalls) != 1 {
		t.Fatalf("merge did not run: rollups=%d", len(q.rollupCalls))
	}
	rollup := q.rollupCalls[0]
	if rollup.ContextEpoch != 8 || rollup.ContextEpochAnchorMs != 99999999 {
		t.Fatalf("epoch advance = (%d, %d), want (8, 99999999) in place", rollup.ContextEpoch, rollup.ContextEpochAnchorMs)
	}
}

// merged segments ordered oldest-first after several rounds.
func TestSegmentMergeReadOrderEndToEnd(t *testing.T) {
	t.Parallel()

	stub := &stubModel{summary: "merged segment"}
	cfg := machineryConfig(stub, 200)
	svc, q, _, epochs := newMergeFixture(t, defaultSegmentCap+1, cfg)
	epochs.stored = seedEpochState(t, 1, 0)
	botUUID := pgtype.UUID{Bytes: uuid.MustParse(cfg.BotID), Valid: true}
	sessionUUID := pgtype.UUID{Bytes: uuid.MustParse(cfg.SessionID), Valid: true}

	svc.maybeMergeSegments(context.Background(), botUUID, sessionUUID, cfg)

	// Append 7 fresh segments to exceed the cap again, then merge once more.
	// The first merge's epoch advance rode the rollup statement; reflect the
	// committed state like the SQL write would.
	epochs.stored = seedEpochState(t, 2, int64(defaultSegmentCap)*1000)
	next := make([]string, defaultSegmentCap-1)
	for i := range next {
		next[i] = fmt.Sprintf("later-%d %s", i, strings.Repeat("y", 200))
	}
	fresh := fusionParentLogs(t, cfg, next...)
	base := int64(defaultSegmentCap+1) * 1000
	for i := range fresh {
		fresh[i].AnchorStartMs = base + int64(i+1)*1000
		fresh[i].AnchorEndMs = base + int64(i+1)*1000
		coverage := strictTestCoveredSource(fmt.Sprintf("later-parent-%d", i), base+int64(i+1)*1000)
		fresh[i].Coverage = mustMarshalCoverage(t, coverage)
	}
	q.priorLogs = append(q.priorLogs, fresh...)
	svc.maybeMergeSegments(context.Background(), botUUID, sessionUUID, cfg)
	if len(q.rollupCalls) != 2 {
		t.Fatalf("second merge did not run: rollups=%d", len(q.rollupCalls))
	}

	frontier, err := NewArtifactProjection(q).LoadActiveSession(context.Background(), ArtifactOwner{BotID: cfg.BotID, SessionID: cfg.SessionID, SessionIDKnown: true})
	if err != nil {
		t.Fatalf("LoadActiveSession: %v", err)
	}
	if len(frontier.Artifacts) != 2 {
		t.Fatalf("chain length after two merges = %d, want 2", len(frontier.Artifacts))
	}
	prevEnd := int64(-1)
	for _, artifact := range frontier.Artifacts {
		if artifact.AnchorStartMs < prevEnd {
			t.Fatalf("frontier not ordered by coverage: %+v", frontier.Artifacts)
		}
		prevEnd = artifact.AnchorEndMs
	}
	// Both merges advanced the epoch: second merge booked epoch 3.
	if q.rollupCalls[1].ContextEpoch != 3 {
		t.Fatalf("second merge epoch = %d, want 3", q.rollupCalls[1].ContextEpoch)
	}
}

// A failed merge pass leaves every parent segment active and claims nothing,
// regardless of which stage failed.
func TestSegmentMergeFailureMatrixKeepsParentsActive(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"empty", "ineffective", "incomplete", "rollup_cas", "cancelled", "missing_coverage", "no_epoch_store"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			stub := &stubModel{summary: "merged segment"}
			cfg := machineryConfig(stub, 200)
			svc, q, _, _ := newMergeFixture(t, defaultSegmentCap+1, cfg)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch failure {
			case "empty":
				stub.summary = ""
			case "ineffective":
				stub.summary = strings.Repeat("x", 100*1024)
			case "incomplete":
				stub.finishReason = "length"
			case "rollup_cas":
				q.rollupErr = pgx.ErrNoRows
			case "cancelled":
				cancel()
			case "missing_coverage":
				q.priorLogs[0].Coverage = nil
			case "no_epoch_store":
				svc.SetContextEpochs(nil)
			}
			svc.maybeMergeSegments(ctx, pgtype.UUID{}, pgtype.UUID{}, cfg)
			if len(q.rollupCalls) != 0 && failure != "rollup_cas" {
				t.Fatalf("failed merge reached rollup completion: %d calls", len(q.rollupCalls))
			}
			if failure == "cancelled" && stub.calls != 0 {
				t.Fatalf("cancelled merge called the provider: %d calls", stub.calls)
			}
			for _, parent := range q.priorLogs {
				if parent.SupersededBy.Valid {
					t.Fatalf("parent %v superseded despite the failed merge", parent.ID)
				}
			}
		})
	}
}

func TestSegmentMergeAnchorClampsToEarliestUncoveredMessage(t *testing.T) {
	t.Parallel()

	stub := &stubModel{summary: "merged segment"}
	cfg := machineryConfig(stub, 200)
	svc, q, _, epochs := newMergeFixture(t, defaultSegmentCap+1, cfg)
	epochs.stored = seedEpochState(t, 3, 1000)

	// A retained raw message (e.g. the window's preserved first user turn)
	// sits at 2000ms: older than the merged coverage end (8000ms) and covered
	// by no segment. Sliding the anchor to the coverage end would drop it
	// from every later anchored load, so the anchor must clamp to it.
	rawID := uuid.New()
	q.activeRangeRows = []sqlc.ListActiveSessionMessageIDsInRangeRow{{
		ID:        pgtype.UUID{Bytes: rawID, Valid: true},
		CreatedAt: pgtype.Timestamptz{Time: time.UnixMilli(2000).UTC(), Valid: true},
	}}

	botUUID := pgtype.UUID{Bytes: uuid.MustParse(cfg.BotID), Valid: true}
	sessionUUID := pgtype.UUID{Bytes: uuid.MustParse(cfg.SessionID), Valid: true}
	svc.maybeMergeSegments(context.Background(), botUUID, sessionUUID, cfg)

	if len(q.rollupCalls) != 1 {
		t.Fatalf("merge did not complete: rollups=%d", len(q.rollupCalls))
	}
	rollup := q.rollupCalls[0]
	if rollup.ContextEpoch != 4 || rollup.ContextEpochAnchorMs != 2000 {
		t.Fatalf("epoch advance = (%d, %d), want (4, 2000): anchor must clamp to the uncovered message", rollup.ContextEpoch, rollup.ContextEpochAnchorMs)
	}
	var state map[string]any
	if err := json.Unmarshal(rollup.ContextEpochState, &state); err != nil {
		t.Fatalf("decode epoch state: %v", err)
	}
	if state["anchor_message_id"] != rawID.String() {
		t.Fatalf("anchor message = %v, want the uncovered message %s", state["anchor_message_id"], rawID)
	}
	if state["last_reason"] != contextepoch.ReasonSegmentMerge {
		t.Fatalf("epoch reason = %v, want segment-merge", state["last_reason"])
	}
}

func TestSegmentMergeAnchorSkipsCoveredMessages(t *testing.T) {
	t.Parallel()

	stub := &stubModel{summary: "merged segment"}
	cfg := machineryConfig(stub, 200)
	svc, q, _, epochs := newMergeFixture(t, defaultSegmentCap+1, cfg)
	epochs.stored = seedEpochState(t, 3, 1000)

	// The first in-range row is covered by an active segment; only the later
	// uncovered row may hold the anchor back.
	coveredID := uuid.New()
	var coverage []CoveredSource
	if err := json.Unmarshal(q.priorLogs[0].Coverage, &coverage); err != nil {
		t.Fatalf("decode parent coverage: %v", err)
	}
	coverage = append(coverage, strictTestCoveredSource(coveredID.String(), 1500))
	q.priorLogs[0].Coverage = mustMarshalCoverage(t, coverage...)
	q.priorLogs[0].AnchorEndMs = 1500 // keep the anchor range consistent with the widened coverage

	uncoveredID := uuid.New()
	q.activeRangeRows = []sqlc.ListActiveSessionMessageIDsInRangeRow{
		{ID: pgtype.UUID{Bytes: coveredID, Valid: true}, CreatedAt: pgtype.Timestamptz{Time: time.UnixMilli(1500).UTC(), Valid: true}},
		{ID: pgtype.UUID{Bytes: uncoveredID, Valid: true}, CreatedAt: pgtype.Timestamptz{Time: time.UnixMilli(2500).UTC(), Valid: true}},
	}

	botUUID := pgtype.UUID{Bytes: uuid.MustParse(cfg.BotID), Valid: true}
	sessionUUID := pgtype.UUID{Bytes: uuid.MustParse(cfg.SessionID), Valid: true}
	svc.maybeMergeSegments(context.Background(), botUUID, sessionUUID, cfg)

	if len(q.rollupCalls) != 1 {
		t.Fatalf("merge did not complete: rollups=%d", len(q.rollupCalls))
	}
	rollup := q.rollupCalls[0]
	if rollup.ContextEpochAnchorMs != 2500 {
		t.Fatalf("anchor = %d, want 2500: covered rows must not hold the anchor back", rollup.ContextEpochAnchorMs)
	}
}

func TestSegmentMergeFailsClosedWhenUncoveredScanFails(t *testing.T) {
	t.Parallel()

	stub := &stubModel{summary: "merged segment"}
	cfg := machineryConfig(stub, 200)
	svc, q, _, epochs := newMergeFixture(t, defaultSegmentCap+1, cfg)
	epochs.stored = seedEpochState(t, 3, 1000)
	q.activeRangeErr = errors.New("range scan unavailable")

	botUUID := pgtype.UUID{Bytes: uuid.MustParse(cfg.BotID), Valid: true}
	sessionUUID := pgtype.UUID{Bytes: uuid.MustParse(cfg.SessionID), Valid: true}
	svc.maybeMergeSegments(context.Background(), botUUID, sessionUUID, cfg)

	// Without the scan the merge cannot prove the slide is lossless, so it
	// fails before booking anything; the next trigger retries.
	if stub.calls != 1 {
		t.Fatalf("summarizer calls = %d, want 1 (the merge attempted the fold)", stub.calls)
	}
	if q.created || len(q.rollupCalls) != 0 {
		t.Fatalf("merge must fail before booking: created=%v rollups=%d", q.created, len(q.rollupCalls))
	}
}

func TestCompactionRunRetriesOverCapMergeOnNoop(t *testing.T) {
	t.Parallel()

	stub := &stubModel{summary: "merged segment"}
	cfg := machineryConfig(stub, 200)
	svc, q, _, epochs := newMergeFixture(t, defaultSegmentCap+1, cfg)
	epochs.stored = seedEpochState(t, 3, 1000)
	q.uncompacted = nil // nothing new to append: the append pass noops

	botUUID := pgtype.UUID{Bytes: uuid.MustParse(cfg.BotID), Valid: true}
	sessionUUID := pgtype.UUID{Bytes: uuid.MustParse(cfg.SessionID), Valid: true}
	res, err := svc.doCompaction(context.Background(), botUUID, sessionUUID, cfg)
	if err != nil {
		t.Fatalf("doCompaction: %v", err)
	}
	// Nothing was appended, but the merge committed: the run must report a
	// history-changing success so budget recovery reloads the rewritten
	// summaries instead of reusing the pre-merge selection.
	if res.Status != StatusOK {
		t.Fatalf("status = %v, want ok (merge rewrote history)", res.Status)
	}
	// The over-cap chain still merges: a merge failure must not wait for the
	// next successful append to retry.
	if len(q.rollupCalls) != 1 {
		t.Fatalf("over-cap merge did not retry on a noop run: rollups=%d", len(q.rollupCalls))
	}
}

func TestSegmentMergeClampHoldsAcrossConsecutiveMerges(t *testing.T) {
	t.Parallel()

	stub := &stubModel{summary: "merged segment"}
	cfg := machineryConfig(stub, 200)
	svc, q, _, epochs := newMergeFixture(t, defaultSegmentCap+1, cfg)
	// As if a previous merge already clamped the anchor onto the uncovered
	// message at 2000ms: the next merge must keep it there instead of
	// scanning strictly after the anchor and sliding past the message.
	epochs.stored = seedEpochState(t, 4, 2000)

	rawID := uuid.New()
	q.activeRangeRows = []sqlc.ListActiveSessionMessageIDsInRangeRow{{
		ID:        pgtype.UUID{Bytes: rawID, Valid: true},
		CreatedAt: pgtype.Timestamptz{Time: time.UnixMilli(2000).UTC(), Valid: true},
	}}

	botUUID := pgtype.UUID{Bytes: uuid.MustParse(cfg.BotID), Valid: true}
	sessionUUID := pgtype.UUID{Bytes: uuid.MustParse(cfg.SessionID), Valid: true}
	svc.maybeMergeSegments(context.Background(), botUUID, sessionUUID, cfg)

	if len(q.rollupCalls) != 1 {
		t.Fatalf("merge did not complete: rollups=%d", len(q.rollupCalls))
	}
	rollup := q.rollupCalls[0]
	if rollup.ContextEpoch != 5 || rollup.ContextEpochAnchorMs != 2000 {
		t.Fatalf("epoch advance = (%d, %d), want (5, 2000): a clamped anchor must keep holding", rollup.ContextEpoch, rollup.ContextEpochAnchorMs)
	}
	var state map[string]any
	if err := json.Unmarshal(rollup.ContextEpochState, &state); err != nil {
		t.Fatalf("decode epoch state: %v", err)
	}
	if state["anchor_message_id"] != rawID.String() {
		t.Fatalf("anchor message = %v, want the uncovered message %s", state["anchor_message_id"], rawID)
	}
}
