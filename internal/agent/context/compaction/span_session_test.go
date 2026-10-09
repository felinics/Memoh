package compaction

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// sessionStore is an in-memory session for multi-pass tests. It applies the
// bounded read's candidate rules to its whole history: an ok or pending claim
// takes rows out of the candidate set, GapBefore and IneffectiveClaim follow
// the query's definitions, and windows are admitted by payload bytes after
// the cursor. The PostgreSQL integration test pins the SQL itself.
type sessionStore struct {
	*fakeQueries
	history   []sqlc.ListUncompactedMessagesBySessionRow
	reasons   map[pgtype.UUID]string
	windows   int
	readBytes int64
}

func newSessionStore(history ...sqlc.ListUncompactedMessagesBySessionRow) *sessionStore {
	return &sessionStore{fakeQueries: &fakeQueries{}, history: history, reasons: map[pgtype.UUID]string{}}
}

func payloadBytes(row sqlc.ListUncompactedMessagesBySessionRow) int64 {
	return int64(len(row.Content) + len(row.Metadata) + len(row.Usage) + len(row.DisplayText.String))
}

func (q *sessionStore) isCandidate(row sqlc.ListUncompactedMessagesBySessionRow) bool {
	status := q.logStatuses[q.claims[row.ID]]
	return status != "ok" && status != "pending"
}

func (q *sessionStore) candidates(after pgtype.UUID) ([]sqlc.ListUncompactedMessagesBySessionRow, []bool) {
	var rows []sqlc.ListUncompactedMessagesBySessionRow
	var gaps []bool
	started := !after.Valid
	for i, row := range q.history {
		if !started {
			started = row.ID == after
			continue
		}
		if !q.isCandidate(row) {
			continue
		}
		row.CompactID = q.claims[row.ID]
		rows = append(rows, row)
		gaps = append(gaps, i > 0 && !q.isCandidate(q.history[i-1]))
	}
	return rows, gaps
}

func (q *sessionStore) MeasureUncompactedMessagesBySession(context.Context, pgtype.UUID) (sqlc.MeasureUncompactedMessagesBySessionRow, error) {
	rows, _ := q.candidates(pgtype.UUID{})
	var measure sqlc.MeasureUncompactedMessagesBySessionRow
	for _, row := range rows {
		measure.CandidateCount++
		measure.CandidateBytes += payloadBytes(row)
		measure.LargestCandidateBytes = max(measure.LargestCandidateBytes, payloadBytes(row))
	}
	return measure, nil
}

func (q *sessionStore) ListUncompactedMessagesBySessionWithinBytes(_ context.Context, arg sqlc.ListUncompactedMessagesBySessionWithinBytesParams) ([]sqlc.ListUncompactedMessagesBySessionWithinBytesRow, error) {
	rows, gaps := q.candidates(arg.AfterMessageID)
	var total int64
	for _, row := range rows {
		total += payloadBytes(row)
	}
	q.windows++
	var window []sqlc.ListUncompactedMessagesBySessionWithinBytesRow
	var cumulative int64
	for i, row := range rows {
		cumulative += payloadBytes(row)
		if cumulative > arg.MaxBytes {
			break
		}
		bounded := boundedRowsForTest([]sqlc.ListUncompactedMessagesBySessionRow{row})[0]
		bounded.CandidateCount = int64(len(rows))
		bounded.CandidateBytes = total
		bounded.CumulativeBytes = cumulative
		bounded.GapBefore = gaps[i]
		claim := q.claims[row.ID]
		bounded.IneffectiveClaim = q.logStatuses[claim] == "error" && q.reasons[claim] == arg.IneffectiveFailureReason
		window = append(window, bounded)
	}
	if len(window) > 0 {
		q.readBytes += window[len(window)-1].CumulativeBytes
	}
	return window, nil
}

func (q *sessionStore) CompleteCompactionLog(ctx context.Context, arg sqlc.CompleteCompactionLogParams) (sqlc.BotHistoryMessageCompact, error) {
	row, err := q.fakeQueries.CompleteCompactionLog(ctx, arg)
	if err == nil {
		q.reasons[arg.ID] = arg.FailureReason
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
