package message

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbpkg "github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
)

func TestPostgresTurnResponseSourcesProjectMetadataAndPreserveWindow(t *testing.T) {
	ctx := context.Background()
	tx := beginPostgresMessageTestTx(t, ctx)
	setupPostgresMessageTestFixtures(t, ctx, tx)
	queries := dbsqlc.New(tx)
	svc := NewService(nil, postgresstore.NewQueries(queries))
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// Keep content tiny while the audit, usage and display fields are large.
	// Only a JSON boolean true is an interrupted checkpoint or a Discuss step,
	// not the string "true". Rows are written newest first, so the window
	// follows the turn order rather than creation time.
	flags := []string{`true`, `false`, `"true"`, `null`}
	for i, flag := range flags {
		_, err := tx.Exec(ctx, `INSERT INTO bot_history_messages
   (id, bot_id, session_id, role, content, metadata, usage, display_text, created_at, turn_position, turn_message_seq, turn_id, turn_visible)
   VALUES ($1, $2, $3, 'assistant', $4::jsonb,
    jsonb_build_object('agent_step_interrupted', $5::jsonb, 'discuss_step', $5::jsonb,
      'context_lifecycle', jsonb_build_object('selection_decisions', repeat('x', 2*1024*1024))),
    jsonb_build_object('unused', repeat('u', 1024)), repeat('d', 1024), $6, $7, 1, gen_random_uuid(), true)`,
			uuid.NewString(), postgresMessageTestBotID, postgresMessageTestSessionID,
			fmt.Sprintf(`{"role":"assistant","content":"reply-%d"}`, i), flag,
			base.Add(time.Duration(len(flags)-i)*time.Second), int64(i+1))
		if err != nil {
			t.Fatal(err)
		}
	}
	// Neither passive-sync nor superseded rows may enter the budget/window.
	for _, excluded := range []struct {
		metadata string
		visible  bool
	}{
		{`{"trigger_mode":"passive_sync"}`, true}, {`{}`, false},
	} {
		if _, err := tx.Exec(ctx, `INSERT INTO bot_history_messages
   (id,bot_id,session_id,role,content,metadata,created_at,turn_position,turn_message_seq,turn_visible,turn_id)
   VALUES ($1,$2,$3,'assistant','{"role":"assistant","content":"excluded"}', $4::jsonb,$5,99,1,$6,gen_random_uuid())`,
			uuid.NewString(), postgresMessageTestBotID, postgresMessageTestSessionID, excluded.metadata, base, excluded.visible); err != nil {
			t.Fatal(err)
		}
	}
	pgSessionID, err := dbpkg.ParseUUID(postgresMessageTestSessionID)
	if err != nil {
		t.Fatal(err)
	}
	for _, after := range []HistoryPosition{{}, {Turn: 2, Seq: 1}} {
		for _, budget := range []int64{0, 1, 100, 1 << 20} {
			t.Run(fmt.Sprintf("after_%d/budget_%d", after.Turn, budget), func(t *testing.T) {
				window, err := queries.ListHistoryMessagesAfterWithinBytes(ctx, dbsqlc.ListHistoryMessagesAfterWithinBytesParams{
					SessionID: pgSessionID, AfterTurnPosition: after.Turn, AfterTurnMessageSeq: after.Seq, MaxBytes: budget,
				})
				if err != nil {
					t.Fatal(err)
				}
				load, err := svc.ListTurnResponseSourcesWithinBytes(ctx, postgresMessageTestSessionID, after, budget)
				if err != nil {
					t.Fatal(err)
				}
				got := load.Messages
				if len(got) != len(window) {
					t.Fatalf("window length=%d, want %d", len(got), len(window))
				}
				if want := len(window) > 0 && window[0].Truncated; load.Truncated != want {
					t.Fatalf("truncated=%v, want %v as the history window reports it", load.Truncated, want)
				}
				for i, m := range got {
					if m.ID != window[i].ID.String() || m.Role != window[i].Role || !bytes.Equal(m.Content, window[i].Content) || !m.CreatedAt.Equal(window[i].CreatedAt.Time) {
						t.Fatalf("history ordering/content changed at row %d", i)
					}
					var metadata map[string]any
					if err := json.Unmarshal(window[i].Metadata, &metadata); err != nil {
						t.Fatal(err)
					}
					if m.Metadata[AgentStepInterruptedMetadataKey] != (metadata[AgentStepInterruptedMetadataKey] == true) {
						t.Fatalf("interrupted checkpoint changed at row %d", i)
					}
					step := metadata[DiscussStepMetadataKey] == true
					if _, marked := m.Metadata[DiscussStepMetadataKey]; marked != step {
						t.Fatalf("discuss step mark changed at row %d", i)
					}
					projected := 1
					if step {
						projected++
					}
					if len(m.Metadata) != projected || len(m.RawMetadata) != 0 || len(m.Usage) != 0 || m.DisplayContent != "" || m.Assets != nil {
						t.Fatalf("row %d retained unused metadata/display/assets", i)
					}
				}
				encoded, err := json.Marshal(got)
				if err != nil {
					t.Fatal(err)
				}
				if len(encoded) > 8192 {
					t.Fatalf("lightweight history serialized to %d bytes", len(encoded))
				}
				if after == (HistoryPosition{}) && budget == 1<<20 && len(got) != len(flags) {
					t.Fatalf("loaded %d rows, want %d active rows", len(got), len(flags))
				}
				// Full-row admission keeps the same window but hands metadata
				// over undecoded, for history records to decode row by row.
				fullLoad, err := svc.ListHistoryWithinBytes(ctx, postgresMessageTestSessionID, HistoryWindow{After: after, MaxBytes: budget})
				if err != nil {
					t.Fatal(err)
				}
				full := fullLoad.Messages
				if len(full) != len(window) {
					t.Fatalf("full window length=%d, want %d", len(full), len(window))
				}
				for i, m := range full {
					if m.ID != window[i].ID.String() || m.Metadata != nil || !bytes.Equal(m.RawMetadata, window[i].Metadata) {
						t.Fatalf("full row %d decoded or lost its metadata", i)
					}
				}
			})
		}
	}
	// Explicit query scoping must also hold when the test connection bypasses RLS.
	if _, err := tx.Exec(ctx, "SELECT set_config('memoh.team_id', $1, true)", uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	leaked, err := svc.ListTurnResponseSourcesWithinBytes(ctx, postgresMessageTestSessionID, HistoryPosition{}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(leaked.Messages) != 0 {
		t.Fatalf("cross-team history leaked %d rows", len(leaked.Messages))
	}
}

// A history window reads the context rows after a checkpoint's position and
// before an edit's cutoff, admitted newest first by content bytes: the row
// that crosses the budget still loads, older ones do not, and the load reports
// that it left them out. Passive and hidden rows spend nothing and never load,
// by position or by ID, and the measure counts what the window could read.
func TestPostgresHistoryWindowReadsTheContextRowsAfterACheckpoint(t *testing.T) {
	ctx := context.Background()
	tx := beginPostgresMessageTestTx(t, ctx)
	setupPostgresMessageTestFixtures(t, ctx, tx)
	svc := NewService(nil, postgresstore.NewQueries(dbsqlc.New(tx)))
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ids := map[string]string{}
	insert := func(name string, position HistoryPosition, metadata string, visible bool, size int) {
		t.Helper()
		id := uuid.NewString()
		if _, err := tx.Exec(ctx, `INSERT INTO bot_history_messages (id, bot_id, session_id, role, content, metadata, created_at, turn_position, turn_message_seq, turn_id, turn_visible)
   VALUES ($1, $2, $3, 'assistant', jsonb_build_object('role', 'assistant', 'content', $4 || ' ' || repeat('x', $5)), $6::jsonb, $7, $8, $9, gen_random_uuid(), $10)`,
			id, postgresMessageTestBotID, postgresMessageTestSessionID, name, size, metadata, base.Add(time.Duration(len(ids))*time.Second), position.Turn, position.Seq, visible); err != nil {
			t.Fatal(err)
		}
		ids[name] = id
	}
	for turn := int64(1); turn <= 8; turn++ {
		insert(fmt.Sprintf("r%d", turn), HistoryPosition{Turn: turn, Seq: 1}, `{}`, true, 1000)
	}
	insert("passive", HistoryPosition{Turn: 5, Seq: 2}, `{"trigger_mode":"passive_sync"}`, true, 100000)
	insert("hidden", HistoryPosition{Turn: 6, Seq: 2}, `{}`, false, 100000)
	var rowBytes int64
	if err := tx.QueryRow(ctx, `SELECT octet_length(content::text) FROM bot_history_messages WHERE id = $1`, ids["r1"]).Scan(&rowBytes); err != nil {
		t.Fatal(err)
	}
	names := func(messages []Message) []string {
		out := make([]string, 0, len(messages))
		for _, m := range messages {
			var content struct{ Content string }
			if err := json.Unmarshal(m.Content, &content); err != nil {
				t.Fatal(err)
			}
			out = append(out, strings.Fields(content.Content)[0])
		}
		return out
	}
	after := HistoryPosition{Turn: 2, Seq: 1}
	for _, tc := range []struct {
		name      string
		window    HistoryWindow
		want      []string
		truncated bool
	}{
		{"after a checkpoint", HistoryWindow{After: after, MaxBytes: 3*rowBytes + 1}, []string{"r5", "r6", "r7", "r8"}, true},
		{"before a cutoff", HistoryWindow{After: after, Before: &HistoryPosition{Turn: 7, Seq: 1}, MaxBytes: 3*rowBytes + 1}, []string{"r3", "r4", "r5", "r6"}, false},
		{"one byte", HistoryWindow{After: after, MaxBytes: 1}, []string{"r8"}, true},
		{"no budget", HistoryWindow{After: after}, []string{}, false},
		{"the whole history", HistoryWindow{MaxBytes: 1 << 20}, []string{"r1", "r2", "r3", "r4", "r5", "r6", "r7", "r8"}, false},
		{"exactly the budget", HistoryWindow{After: after, Before: &HistoryPosition{Turn: 7, Seq: 1}, MaxBytes: 4 * rowBytes}, []string{"r3", "r4", "r5", "r6"}, false},
	} {
		got, err := svc.ListHistoryWithinBytes(ctx, postgresMessageTestSessionID, tc.window)
		if err != nil {
			t.Fatal(err)
		}
		if names := names(got.Messages); !slices.Equal(names, tc.want) {
			t.Fatalf("%s: window = %v, want %v", tc.name, names, tc.want)
		}
		if got.Truncated != tc.truncated {
			t.Fatalf("%s: truncated = %t, want %t", tc.name, got.Truncated, tc.truncated)
		}
	}

	byID, err := svc.ListHistoryByIDs(ctx, postgresMessageTestSessionID, []string{ids["r6"], ids["passive"], ids["r2"], ids["hidden"]})
	if err != nil {
		t.Fatal(err)
	}
	if got := names(byID); !slices.Equal(got, []string{"r2", "r6"}) {
		t.Fatalf("rows by ID = %v, want r2 and r6 in history order", got)
	}
	if position, err := svc.HistoryPositionByID(ctx, postgresMessageTestSessionID, ids["r6"]); err != nil || position != (HistoryPosition{Turn: 6, Seq: 1}) {
		t.Fatalf("position of r6 = %+v err=%v", position, err)
	}
	if _, err := svc.HistoryPositionByID(ctx, postgresMessageTestSessionID, ids["hidden"]); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("position of a hidden row: err=%v, want no rows", err)
	}
	measure, err := svc.MeasureHistory(ctx, postgresMessageTestSessionID, after)
	if err != nil {
		t.Fatal(err)
	}
	if measure.MessageCount != 6 || measure.ContentBytes != 6*rowBytes {
		t.Fatalf("measure = %+v, want the 6 rows after the checkpoint at %d bytes each", measure, rowBytes)
	}
}
