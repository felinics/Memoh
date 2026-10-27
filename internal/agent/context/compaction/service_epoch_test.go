package compaction

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestDoCompactionRejectsEpochChangeBeforeAttemptCreation(t *testing.T) {
	rows := machineryCorpus(t)
	for i := range rows {
		rows[i].CompactionEpoch = 7
	}
	q := &fakeQueries{uncompacted: rows, createErr: pgx.ErrNoRows}
	stub := &stubModel{summary: "must not run"}
	svc := newMachineryService(q)

	_, err := svc.compactOnce(context.Background(), machineryConfig(stub, 450))
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("compact error = %v, want pgx.ErrNoRows", err)
	}
	if q.createArg.ExpectedEpoch != 7 {
		t.Fatalf("CreateCompactionLog expected epoch = %d, want 7", q.createArg.ExpectedEpoch)
	}
	if stub.calls != 0 || len(q.markedIDs) != 0 {
		t.Fatalf("stale selection reached model or marking: calls=%d marked=%d", stub.calls, len(q.markedIDs))
	}
}

func TestDoCompactionRecordsErrorWhenSuccessCompletionIsFenced(t *testing.T) {
	rows := machineryCorpus(t)
	q := &fakeQueries{uncompacted: rows, completeErrors: []error{pgx.ErrNoRows, nil}}
	stub := &stubModel{summary: "stale summary"}
	svc := newMachineryService(q)

	_, err := svc.compactOnce(context.Background(), machineryConfig(stub, 450))
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("compact error = %v, want pgx.ErrNoRows", err)
	}
	if len(q.completeCalls) != 2 {
		t.Fatalf("CompleteCompactionLog calls = %d, want success attempt plus terminal error", len(q.completeCalls))
	}
	if q.completeCalls[0].Status != "ok" || q.completeCalls[1].Status != "error" {
		t.Fatalf("completion statuses = %q then %q, want ok then error", q.completeCalls[0].Status, q.completeCalls[1].Status)
	}
	if q.completeCalls[1].Summary != "" || q.completeCalls[1].ErrorMessage == "" {
		t.Fatalf("terminal stale completion leaked summary or omitted error: %#v", q.completeCalls[1])
	}
}
