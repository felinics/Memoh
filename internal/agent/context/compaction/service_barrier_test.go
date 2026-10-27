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
		{name: "call is unparseable", breakIndex: 2},
		{name: "result is unparseable", breakIndex: 3},
		{name: "bare call part is unparseable", breakIndex: 2, bareCall: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows := []sqlc.ListUncompactedMessagesBySessionRow{
				mkRow(t, "user", `"old question"`, 100),
				mkRow(t, "assistant", `"old answer"`, 100),
				toolCallRow(t, 100),
				toolResultRow(t, 100),
				mkRow(t, "user", `"current question"`, 100),
				mkRow(t, "assistant", `"current answer"`, 100),
			}
			if tt.bareCall {
				rows[2].Content = []byte(`{"type":"tool-call","toolCallId":"c1","toolName":"search","input":{}}`)
			}
			rows[tt.breakIndex].ID = pgtype.UUID{Valid: false}
			q := &fakeQueries{uncompacted: rows}
			stub := &stubModel{summary: "SUMMARY"}

			if _, err := newMachineryService(q).compactOnce(context.Background(), currentConfig(stub, 200, rows[4])); err != nil {
				t.Fatalf("compact: %v", err)
			}

			if len(q.markedIDs) != 2 || q.markedIDs[0] != rows[0].ID || q.markedIDs[1] != rows[1].ID {
				t.Fatalf("marked ids = %#v, want only the run before the protected tool exchange", q.markedIDs)
			}
		})
	}
}
