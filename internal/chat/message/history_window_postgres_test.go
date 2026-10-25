package message

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// Compare the bounded index walk to the former cumulative-window definition.
// Timestamps deliberately disagree with turn order, and rows on the same turn
// share timestamps, so keyset pagination must use the full ordering tuple.
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
	for group := range 2 {
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO bot_history_message_compacts(bot_id,session_id,status,summary)
   VALUES($1,$2,'ok','summary') RETURNING id::text`, postgresMessageTestBotID, postgresMessageTestSessionID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE bot_history_messages SET compact_id=$1 WHERE session_id=$2 AND turn_position%4=$3`, id, postgresMessageTestSessionID, group); err != nil {
			t.Fatal(err)
		}
	}
	var session pgtype.UUID
	if err := session.Scan(postgresMessageTestSessionID); err != nil {
		t.Fatal(err)
	}
	q := dbsqlc.New(tx)
	for _, fold := range []bool{false, true} {
		for _, since := range []time.Time{time.Unix(0, 0), time.Date(2026, 1, 1, 0, 1, 20, 0, time.UTC)} {
			for _, budget := range []int64{-1, 0, 1, 100, 500, 10000, 1000000} {
				t.Run(fmt.Sprintf("fold_%v/%d/%d", fold, since.Unix(), budget), func(t *testing.T) {
					expected, err := tx.Query(ctx, `SELECT id::text,summarized FROM (
      SELECT m.id,m.turn_position,m.turn_message_seq,m.created_at,(c.id IS NOT NULL) AS summarized,
       ROW_NUMBER() OVER (PARTITION BY c.id ORDER BY m.turn_position DESC,m.turn_message_seq DESC,m.created_at DESC,m.id DESC) AS summarized_rank,
       SUM(CASE WHEN c.id IS NULL THEN octet_length(m.content::text) ELSE 0 END) OVER
        (ORDER BY m.turn_position DESC,m.turn_message_seq DESC,m.created_at DESC,m.id DESC)
        - CASE WHEN c.id IS NULL THEN octet_length(m.content::text) ELSE 0 END AS preceding_bytes
      FROM bot_visible_history_messages m
      LEFT JOIN bot_sessions s ON s.id=m.session_id AND s.team_id=public.memoh_current_team_id()
      LEFT JOIN bot_history_message_compacts c ON $4::boolean AND c.id=m.compact_id
       AND c.team_id=public.memoh_current_team_id() AND c.bot_id=m.bot_id AND c.session_id=s.id
       AND c.compaction_epoch=s.compaction_epoch AND c.status='ok' AND NULLIF(BTRIM(c.summary),'') IS NOT NULL
      WHERE m.team_id=public.memoh_current_team_id() AND m.session_id=$1 AND m.created_at >= $2
       AND (m.metadata->>'trigger_mode' IS NULL OR m.metadata->>'trigger_mode'<>'passive_sync')
     ) ranked WHERE preceding_bytes<$3 AND (NOT summarized OR summarized_rank=1)
     ORDER BY turn_position,turn_message_seq,created_at,id`, session, since, budget, fold)
					if err != nil {
						t.Fatal(err)
					}
					var ids []string
					var folded []bool
					for expected.Next() {
						var id string
						var summary bool
						if err := expected.Scan(&id, &summary); err != nil {
							t.Fatal(err)
						}
						ids = append(ids, id)
						folded = append(folded, summary)
					}
					if err := expected.Err(); err != nil {
						t.Fatal(err)
					}
					expected.Close()
					got, err := q.ListActiveMessagesSinceBySessionWithinBytes(ctx, dbsqlc.ListActiveMessagesSinceBySessionWithinBytesParams{
						SessionID: session, CreatedAt: pgtype.Timestamptz{Time: since, Valid: true}, MaxBytes: budget, FoldSummarized: fold,
					})
					if err != nil {
						t.Fatal(err)
					}
					if len(got) != len(ids) {
						t.Fatalf("record=%d want=%d", len(got), len(ids))
					}
					for i, id := range ids {
						if got[i].ID.String() != id || got[i].Summarized != folded[i] {
							t.Fatalf("wrong row at %d", i)
						}
					}
					if !fold {
						projected, err := q.ListTurnResponseSourcesSinceBySessionWithinBytes(ctx, dbsqlc.ListTurnResponseSourcesSinceBySessionWithinBytesParams{
							SessionID: session, CreatedAt: pgtype.Timestamptz{Time: since, Valid: true}, MaxBytes: budget,
						})
						if err != nil {
							t.Fatal(err)
						}
						if len(projected) != len(ids) {
							t.Fatalf("TR=%d want=%d", len(projected), len(ids))
						}
						for i, id := range ids {
							if projected[i].ID.String() != id {
								t.Fatalf("wrong TR row at %d", i)
							}
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
	for _, projected := range []bool{false, true} {
		var count int
		if projected {
			rows, err := q.ListTurnResponseSourcesSinceBySessionWithinBytes(ctx, dbsqlc.ListTurnResponseSourcesSinceBySessionWithinBytesParams{
				SessionID: session, CreatedAt: pgtype.Timestamptz{Time: time.Unix(0, 0), Valid: true}, MaxBytes: 32768,
			})
			if err != nil {
				t.Fatal(err)
			}
			count = len(rows)
		} else {
			rows, err := q.ListActiveMessagesSinceBySessionWithinBytes(ctx, dbsqlc.ListActiveMessagesSinceBySessionWithinBytesParams{
				SessionID: session, CreatedAt: pgtype.Timestamptz{Time: time.Unix(0, 0), Valid: true}, MaxBytes: 32768,
			})
			if err != nil {
				t.Fatal(err)
			}
			count = len(rows)
		}
		if count != 32 {
			t.Fatalf("admitted %d rows, want 32 including crossing row", count)
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
		if !indexed || visited > float64(count*2+2) {
			t.Fatalf("projected=%v: visited %.0f history rows for %d admitted, index=%v; plan=%s", projected, visited, count, indexed, raw)
		}
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
