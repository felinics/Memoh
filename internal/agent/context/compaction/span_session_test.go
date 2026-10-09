package compaction

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// sessionStore is an in-memory session for multi-pass tests. It applies the
// bounded read's candidate rules to its whole history: an ok claim, a fresh
// pending one or a recent unusable-summary attempt of the current epoch takes
// rows out of the candidate set, GapBefore, PendingBefore and
// IneffectiveClaim follow the query's definitions, windows are admitted by
// payload bytes after the cursor, and the recorded scan position applies
// while its epoch is current. The PostgreSQL integration test pins the SQL
// itself.
type sessionStore struct {
	*fakeQueries
	history     []sqlc.ListUncompactedMessagesBySessionRow
	reasons     map[pgtype.UUID]string
	claimEpoch  map[pgtype.UUID]int64
	completedAt map[pgtype.UUID]time.Time
	now         func() time.Time
	epoch       int64
	scanAfter   pgtype.UUID
	scanEpoch   int64
	windows     int
	readBytes   int64
}

func newSessionStore(history ...sqlc.ListUncompactedMessagesBySessionRow) *sessionStore {
	return &sessionStore{fakeQueries: &fakeQueries{}, history: history, reasons: map[pgtype.UUID]string{}, claimEpoch: map[pgtype.UUID]int64{}, completedAt: map[pgtype.UUID]time.Time{}, now: time.Now}
}

func payloadBytes(row sqlc.ListUncompactedMessagesBySessionRow) int64 {
	return int64(len(row.Content) + len(row.Metadata) + len(row.Usage) + len(row.DisplayText.String))
}

// heldBy reports what keeps a row out of the candidate set: "ok", "pending",
// "unusable" or "" for a candidate.
func (q *sessionStore) heldBy(row sqlc.ListUncompactedMessagesBySessionRow) string {
	claim := q.claims[row.ID]
	if !claim.Valid || q.claimEpoch[claim] != q.epoch {
		return ""
	}
	switch status := q.logStatuses[claim]; {
	case status == "ok", status == "pending":
		return status
	case status == "error" && q.reasons[claim] == failureReasonUnusableSummary && q.now().Sub(q.completedAt[claim]) < unusableSummaryHold:
		return "unusable"
	}
	return ""
}

// isRaw reports a row the replay sends raw: no summary covers it, nor a claim
// still in flight. MeasureUncompactedMessagesBySession counts these.
func (q *sessionStore) isRaw(row sqlc.ListUncompactedMessagesBySessionRow) bool {
	held := q.heldBy(row)
	return held == "" || held == "unusable"
}

type storeCandidate struct {
	row                sqlc.ListUncompactedMessagesBySessionRow
	gap, pendingBefore bool
}

func (q *sessionStore) candidates(after pgtype.UUID) []storeCandidate {
	if !after.Valid && q.scanEpoch == q.epoch {
		after = q.scanAfter
	}
	start := 0
	for i, row := range q.history {
		if after.Valid && row.ID == after {
			start = i + 1
		}
	}
	var out []storeCandidate
	gap, pending := false, false
	if start > 0 {
		held := q.heldBy(q.history[start-1])
		gap, pending = held != "", held != "" && held != "ok"
	}
	for _, row := range q.history[start:] {
		if held := q.heldBy(row); held != "" {
			gap, pending = true, pending || held != "ok"
			continue
		}
		row.CompactID = q.claims[row.ID]
		row.CompactionEpoch = q.epoch
		out = append(out, storeCandidate{row: row, gap: gap, pendingBefore: pending})
		gap, pending = false, false
	}
	return out
}

// candidateRows lists every raw row, ignoring the scan position.
func (q *sessionStore) candidateRows() []sqlc.ListUncompactedMessagesBySessionRow {
	var rows []sqlc.ListUncompactedMessagesBySessionRow
	for _, row := range q.history {
		if q.isRaw(row) {
			rows = append(rows, row)
		}
	}
	return rows
}

func (q *sessionStore) MeasureUncompactedMessagesBySession(context.Context, pgtype.UUID) (sqlc.MeasureUncompactedMessagesBySessionRow, error) {
	var measure sqlc.MeasureUncompactedMessagesBySessionRow
	for _, row := range q.history {
		if !q.isRaw(row) {
			continue
		}
		measure.CandidateCount++
		measure.CandidateBytes += payloadBytes(row)
		measure.LargestCandidateBytes = max(measure.LargestCandidateBytes, payloadBytes(row))
	}
	return measure, nil
}

func (q *sessionStore) ListUncompactedMessagesBySessionWithinBytes(_ context.Context, arg sqlc.ListUncompactedMessagesBySessionWithinBytesParams) ([]sqlc.ListUncompactedMessagesBySessionWithinBytesRow, error) {
	candidates := q.candidates(arg.AfterMessageID)
	var total int64
	for _, c := range candidates {
		total += payloadBytes(c.row)
	}
	q.windows++
	latestUser := -1
	for i, c := range candidates {
		if c.row.Role == "user" {
			latestUser = i
		}
	}
	var window []sqlc.ListUncompactedMessagesBySessionWithinBytesRow
	var cumulative int64
	for i, c := range candidates {
		cumulative += payloadBytes(c.row)
		oversized := i == 0 && cumulative > arg.MaxBytes
		if cumulative > arg.MaxBytes && !oversized {
			break
		}
		row := c.row
		if oversized {
			row.Content, row.Metadata, row.Usage, row.DisplayText = nil, nil, nil, pgtype.Text{}
		}
		bounded := boundedRowsForTest([]sqlc.ListUncompactedMessagesBySessionRow{row})[0]
		bounded.Oversized = oversized
		bounded.LatestUser = i == latestUser
		bounded.CandidateCount = int64(len(candidates))
		bounded.CandidateBytes = total
		bounded.CumulativeBytes = cumulative
		bounded.GapBefore = c.gap
		bounded.PendingBefore = c.pendingBefore
		claim := q.claims[c.row.ID]
		bounded.IneffectiveClaim = q.claimEpoch[claim] == q.epoch && q.logStatuses[claim] == "error" && q.reasons[claim] == arg.IneffectiveFailureReason
		window = append(window, bounded)
		if oversized {
			break
		}
	}
	if len(window) > 0 && !window[0].Oversized {
		q.readBytes += window[len(window)-1].CumulativeBytes
	}
	return window, nil
}

func (q *sessionStore) AdvanceCompactionScan(_ context.Context, arg sqlc.AdvanceCompactionScanParams) error {
	if arg.CompactionEpoch == q.epoch {
		q.scanAfter, q.scanEpoch = arg.AfterMessageID, arg.CompactionEpoch
	}
	return nil
}

func (q *sessionStore) CreateCompactionLog(ctx context.Context, arg sqlc.CreateCompactionLogParams) (sqlc.BotHistoryMessageCompact, error) {
	row, err := q.fakeQueries.CreateCompactionLog(ctx, arg)
	if err == nil {
		q.claimEpoch[row.ID] = arg.ExpectedEpoch
	}
	return row, err
}

func (q *sessionStore) CompleteCompactionLog(ctx context.Context, arg sqlc.CompleteCompactionLogParams) (sqlc.BotHistoryMessageCompact, error) {
	row, err := q.fakeQueries.CompleteCompactionLog(ctx, arg)
	if err == nil {
		q.reasons[arg.ID] = arg.FailureReason
		q.completedAt[arg.ID] = q.now()
	}
	return row, err
}

func (q *sessionStore) append(rows ...sqlc.ListUncompactedMessagesBySessionRow) {
	q.history = append(q.history, rows...)
}

// assertClaimsContiguous checks the read-path invariant: every committed
// summary covers one unbroken range of the session history.
func assertClaimsContiguous(t *testing.T, q *sessionStore) {
	t.Helper()
	ranges := map[pgtype.UUID][2]int{}
	counts := map[pgtype.UUID]int{}
	for i, row := range q.history {
		claim := q.claims[row.ID]
		if q.logStatuses[claim] != "ok" {
			continue
		}
		r, seen := ranges[claim]
		if !seen {
			r[0] = i
		}
		r[1] = i
		ranges[claim] = r
		counts[claim]++
	}
	for claim, r := range ranges {
		if r[1]-r[0]+1 != counts[claim] {
			t.Fatalf("summary %s covers history rows %d..%d with %d rows: a claim spans rows it does not own", formatUUID(claim), r[0], r[1], counts[claim])
		}
	}
}

func TestGapsSplitGroupsAndRuns(t *testing.T) {
	t.Parallel()

	exchange := execExchange(t, 1)
	call, result := exchange[0], exchange[1]
	before := prose(t, "user", "BEFORE", 300, 10)
	after := prose(t, "assistant", "AFTER", 300, 10)
	items, _ := itemsFromRows([]sqlc.ListUncompactedMessagesBySessionRow{before, call, result, after})
	items[2].GapBefore = true
	items[3].GapBefore = true

	if groups := toolExchangeGroups(items); len(groups) != 4 {
		t.Fatalf("groups = %v, want the result behind a gap outside its call's group", groups)
	}
	if _, ids := buildEntriesAndIDs([]CompactionCandidate{items[0], items[3]}); len(ids) != 1 || ids[0] != before.ID {
		t.Fatalf("ids = %v, want one claim to stop at the gap", ids)
	}
	_, ids := buildEntriesAndIDs(items)
	claimed := idSet(ids)
	if !claimed[before.ID] || claimed[result.ID] || claimed[after.ID] {
		t.Fatalf("ids = %v, want the run in front of the first gap only", ids)
	}
}
