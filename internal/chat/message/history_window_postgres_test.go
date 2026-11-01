package message

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// Compare the bounded index walk to the cumulative-window definition of the
// raw history after a checkpoint. Timestamps deliberately disagree with turn
// order, and rows on the same turn share timestamps, so keyset pagination
// must use the full ordering tuple.
func TestPostgresHistoryIndexWalkPreservesAdmission(t *testing.T) {
	ctx := context.Background()
	tx := beginPostgresMessageTestTx(t, ctx)
	setupPostgresMessageTestFixtures(t, ctx, tx)
	if _, err := tx.Exec(ctx, `INSERT INTO bot_history_messages
  (id,bot_id,session_id,role,content,metadata,created_at,turn_id,turn_position,turn_message_seq,turn_visible)
  SELECT gen_random_uuid(),$1,$2,'assistant',
   jsonb_build_object('content',repeat('x',CASE WHEN n=70 THEN 100000 ELSE n%100+1 END)),
   CASE WHEN n%11=0 THEN '{"trigger_mode":"passive_sync"}'::jsonb ELSE '{}'::jsonb END,
   '2026-01-01'::timestamptz+(100-n/2)*interval '1 second',gen_random_uuid(),n/2,n%2,n%13<>0
  FROM generate_series(1,80) n`, postgresMessageTestBotID, postgresMessageTestSessionID); err != nil {
		t.Fatal(err)
	}
	var session pgtype.UUID
	if err := session.Scan(postgresMessageTestSessionID); err != nil {
		t.Fatal(err)
	}
	q := dbsqlc.New(tx)
	type position struct{ turn, seq int64 }
	for _, after := range []position{{0, 0}, {12, 1}} {
		for _, before := range []*position{nil, {35, 0}} {
			for _, budget := range []int64{-1, 0, 1, 100, 500, 10000, 1000000} {
				name := fmt.Sprintf("after_%d_%d/before_%v/%d", after.turn, after.seq, before != nil, budget)
				t.Run(name, func(t *testing.T) {
					var beforeTurn, beforeSeq pgtype.Int8
					if before != nil {
						beforeTurn = pgtype.Int8{Int64: before.turn, Valid: true}
						beforeSeq = pgtype.Int8{Int64: before.seq, Valid: true}
					}
					expected, err := tx.Query(ctx, `SELECT id::text FROM (
      SELECT m.id,m.turn_position,m.turn_message_seq,m.created_at,
       SUM(octet_length(m.content::text)) OVER
        (ORDER BY m.turn_position DESC,m.turn_message_seq DESC,m.created_at DESC,m.id DESC)
        - octet_length(m.content::text) AS preceding_bytes
      FROM bot_visible_history_messages m
      WHERE m.team_id=public.memoh_current_team_id() AND m.session_id=$1
       AND (m.turn_position,m.turn_message_seq) > ($2,$3)
       AND ($4::bigint IS NULL OR (m.turn_position,m.turn_message_seq) < ($4,$5))
       AND (m.metadata->>'trigger_mode' IS NULL OR m.metadata->>'trigger_mode'<>'passive_sync')
     ) ranked WHERE preceding_bytes<$6
     ORDER BY turn_position,turn_message_seq,created_at,id`, session, after.turn, after.seq, beforeTurn, beforeSeq, budget)
					if err != nil {
						t.Fatal(err)
					}
					var ids []string
					for expected.Next() {
						var id string
						if err := expected.Scan(&id); err != nil {
							t.Fatal(err)
						}
						ids = append(ids, id)
					}
					if err := expected.Err(); err != nil {
						t.Fatal(err)
					}
					expected.Close()
					got, err := q.ListHistoryWindow(ctx, dbsqlc.ListHistoryWindowParams{
						SessionID: session, AfterTurnPosition: after.turn, AfterTurnMessageSeq: after.seq,
						BeforeTurnPosition: beforeTurn, BeforeTurnMessageSeq: beforeSeq, MaxBytes: budget,
					})
					if err != nil {
						t.Fatal(err)
					}
					if len(got) != len(ids) {
						t.Fatalf("window=%d want=%d", len(got), len(ids))
					}
					for i, id := range ids {
						if got[i].ID.String() != id {
							t.Fatalf("wrong row at %d", i)
						}
					}
				})
			}
		}
	}
}

// Capture the actual sqlc query, so the plan assertion guards the runtime SQL
// rather than a separate hand-written approximation.
type historyWindowQueryRecorder struct {
	pgx.Tx
	query string
	args  []any
}

func (r *historyWindowQueryRecorder) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	r.query, r.args = query, args
	return r.Tx.Query(ctx, query, args...)
}

func TestPostgresHistoryWindowStopsBeforeOldBacklog(t *testing.T) {
	ctx := context.Background()
	tx := beginPostgresMessageTestTx(t, ctx)
	setupPostgresMessageTestFixtures(t, ctx, tx)
	if _, err := tx.Exec(ctx, `INSERT INTO bot_history_messages
      (id,bot_id,session_id,role,content,turn_id,turn_position,turn_message_seq,turn_visible,created_at)
      SELECT gen_random_uuid(),$1,$2,'assistant',jsonb_build_object('content',repeat('x',1024)),
        gen_random_uuid(),n,0,true,'2026-01-01'::timestamptz+n*interval '1 second'
      FROM generate_series(1,20000) n`, postgresMessageTestBotID, postgresMessageTestSessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `ANALYZE bot_history_messages`); err != nil {
		t.Fatal(err)
	}
	recorder := &historyWindowQueryRecorder{Tx: tx}
	q := dbsqlc.New(recorder)
	var session pgtype.UUID
	if err := session.Scan(postgresMessageTestSessionID); err != nil {
		t.Fatal(err)
	}
	rows, err := q.ListHistoryWindow(ctx, dbsqlc.ListHistoryWindowParams{SessionID: session, MaxBytes: 32768})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 32 {
		t.Fatalf("admitted %d rows, want 32 including crossing row", len(rows))
	}
	var raw []byte
	if err := tx.QueryRow(ctx, "EXPLAIN (ANALYZE, FORMAT JSON) "+recorder.query, recorder.args...).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var plans []struct{ Plan historyWindowPlan }
	if err := json.Unmarshal(raw, &plans); err != nil {
		t.Fatal(err)
	}
	visited, indexed := plans[0].Plan.visitedRows()
	if !indexed || visited > float64(len(rows)*2+2) {
		t.Fatalf("visited %.0f history rows for %d admitted, index=%v; plan=%s", visited, len(rows), indexed, raw)
	}
}

type historyWindowPlan struct {
	Relation string              `json:"Relation Name"`
	Index    string              `json:"Index Name"`
	Rows     float64             `json:"Actual Rows"`
	Loops    float64             `json:"Actual Loops"`
	Plans    []historyWindowPlan `json:"Plans"`
}

func (p historyWindowPlan) visitedRows() (float64, bool) {
	var rows float64
	indexed := strings.Contains(p.Index, "visible_session_order")
	if p.Relation == "bot_history_messages" {
		rows = p.Rows * p.Loops
	}
	for _, child := range p.Plans {
		n, i := child.visitedRows()
		rows += n
		indexed = indexed || i
	}
	return rows, indexed
}
