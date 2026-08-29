package compaction

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

func TestDoCompactionDoesNotSplitToolExchangeAtUnparseableBarrier(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		breakIndex int
		bareCall   bool
	}{
		{name: "call is unparseable", breakIndex: 0},
		{name: "result is unparseable", breakIndex: 1},
		{name: "bare call part is unparseable", breakIndex: 0, bareCall: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows := []sqlc.ListUncompactedMessagesBySessionRow{
				toolCallRow(t, 100),
				toolResultRow(t, 100),
				mkRow(t, "user", `"old question"`, 100),
				mkRow(t, "assistant", `"old answer"`, 100),
				mkRow(t, "user", `"current question"`, 100),
				mkRow(t, "assistant", `"current answer"`, 100),
			}
			if tt.bareCall {
				rows[0].Content = []byte(`{"type":"tool-call","toolCallId":"c1","toolName":"search","input":{}}`)
			}
			rows[tt.breakIndex].ID = pgtype.UUID{Valid: false}
			q := &fakeQueries{uncompacted: rows}
			stub := &stubModel{summary: "SUMMARY"}

			if _, err := newMachineryService(q).RunCompactionSync(context.Background(), machineryConfig(stub, 200)); err != nil {
				t.Fatalf("RunCompactionSync: %v", err)
			}

			if len(q.markedIDs) != 2 || q.markedIDs[0] != rows[2].ID || q.markedIDs[1] != rows[3].ID {
				t.Fatalf("marked ids = %#v, want only the complete run after the protected tool exchange", q.markedIDs)
			}
		})
	}
}
