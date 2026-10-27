package message

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

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
	// not the string "true".
	flags := []string{`true`, `false`, `"true"`, `null`}
	for i, flag := range flags {
		_, err := tx.Exec(ctx, `INSERT INTO bot_history_messages
   (id, bot_id, session_id, role, content, metadata, usage, display_text, created_at, turn_position, turn_message_seq, turn_id, turn_visible)
   VALUES ($1, $2, $3, 'assistant', $4::jsonb,
    jsonb_build_object('agent_step_interrupted', $5::jsonb, 'discuss_step', $5::jsonb,
      'context_lifecycle', jsonb_build_object('selection_decisions', repeat('x', 2*1024*1024))),
    jsonb_build_object('unused', repeat('u', 1024)), repeat('d', 1024), $6, $7, 0, gen_random_uuid(), true)`,
			uuid.NewString(), postgresMessageTestBotID, postgresMessageTestSessionID,
			fmt.Sprintf(`{"role":"assistant","content":"reply-%d"}`, i), flag,
			base.Add(time.Duration(len(flags)-i)*time.Second), int64(i))
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
   VALUES ($1,$2,$3,'assistant','{"role":"assistant","content":"excluded"}', $4::jsonb,$5,99,0,$6,gen_random_uuid())`,
			uuid.NewString(), postgresMessageTestBotID, postgresMessageTestSessionID, excluded.metadata, base, excluded.visible); err != nil {
			t.Fatal(err)
		}
	}
	pgSessionID, err := dbpkg.ParseUUID(postgresMessageTestSessionID)
	if err != nil {
		t.Fatal(err)
	}
	for _, since := range []time.Time{time.Unix(0, 0), base.Add(3 * time.Second)} {
		for _, budget := range []int64{0, 1, 100, 1 << 20} {
			t.Run(fmt.Sprintf("since_%d/budget_%d", since.Unix(), budget), func(t *testing.T) {
				old, err := queries.ListActiveMessagesSinceBySessionWithinBytes(ctx, dbsqlc.ListActiveMessagesSinceBySessionWithinBytesParams{
					SessionID: pgSessionID, CreatedAt: pgtype.Timestamptz{Time: since, Valid: true}, MaxBytes: budget,
				})
				if err != nil {
					t.Fatal(err)
				}
				got, err := svc.ListTurnResponseSourcesSinceBySessionWithinBytes(ctx, postgresMessageTestSessionID, since, budget)
				if err != nil {
					t.Fatal(err)
				}
				if len(got) != len(old) {
					t.Fatalf("window length=%d, want %d", len(got), len(old))
				}
				for i, m := range got {
					if m.ID != old[i].ID.String() || m.Role != old[i].Role || !bytes.Equal(m.Content, old[i].Content) || !m.CreatedAt.Equal(old[i].CreatedAt.Time) {
						t.Fatalf("history ordering/content changed at row %d", i)
					}
					var metadata map[string]any
					if err := json.Unmarshal(old[i].Metadata, &metadata); err != nil {
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
				if since.Equal(time.Unix(0, 0)) && budget == 1<<20 && len(got) != len(flags) {
					t.Fatalf("loaded %d rows, want %d active rows", len(got), len(flags))
				}
				// Full-row admission keeps the same window but hands metadata
				// over undecoded, for history records to decode row by row.
				full, err := svc.ListActiveSinceBySessionWithinBytes(ctx, postgresMessageTestSessionID, since, budget, true)
				if err != nil {
					t.Fatal(err)
				}
				if len(full) != len(old) {
					t.Fatalf("full window length=%d, want %d", len(full), len(old))
				}
				for i, m := range full {
					if m.ID != old[i].ID.String() || m.Metadata != nil || !bytes.Equal(m.RawMetadata, old[i].Metadata) {
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
	got, err := svc.ListTurnResponseSourcesSinceBySessionWithinBytes(ctx, postgresMessageTestSessionID, time.Unix(0, 0), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("cross-team history leaked %d rows", len(got))
	}
}

// A folded window spends nothing on rows a usable summary replaced and loads
// the newest of each compact group, Summarized, to place the summary; rows of
// a failed summary, or of an earlier epoch, still count. Unfolded, the same
// rows fill the window and push the oldest input out.
func TestPostgresHistoryWindowFoldsSummarizedRows(t *testing.T) {
	ctx := context.Background()
	tx := beginPostgresMessageTestTx(t, ctx)
	setupPostgresMessageTestFixtures(t, ctx, tx)
	svc := NewService(nil, postgresstore.NewQueries(dbsqlc.New(tx)))
	if _, err := tx.Exec(ctx, `UPDATE bot_sessions SET compaction_epoch = 1 WHERE id = $1`, postgresMessageTestSessionID); err != nil {
		t.Fatal(err)
	}
	compacts := map[string]string{}
	for _, c := range []struct {
		name, status string
		epoch        int
	}{{"ok", "ok", 1}, {"failed", "error", 1}, {"stale", "ok", 0}} {
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO bot_history_message_compacts (bot_id, session_id, status, summary, compaction_epoch) VALUES ($1, $2, $3, 'summary', $4) RETURNING id::text`,
			postgresMessageTestBotID, postgresMessageTestSessionID, c.status, c.epoch).Scan(&id); err != nil {
			t.Fatal(err)
		}
		compacts[c.name] = id
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rows := []struct{ name, compact string }{
		{"beyond", ""}, {"input", ""}, {"folded-0", "ok"}, {"folded-1", "ok"}, {"folded-2", "ok"}, {"failed", "failed"}, {"stale", "stale"}, {"newest", ""},
	}
	for i, row := range rows {
		var compactID any
		if row.compact != "" {
			compactID = compacts[row.compact]
		}
		if _, err := tx.Exec(ctx, `INSERT INTO bot_history_messages (id, bot_id, session_id, role, content, compact_id, created_at, turn_position, turn_message_seq, turn_id, turn_visible)
   VALUES (gen_random_uuid(), $1, $2, 'assistant', jsonb_build_object('role', 'assistant', 'content', $3 || ' ' || repeat('x', 1000)), $4, $5, $6, 0, gen_random_uuid(), true)`,
			postgresMessageTestBotID, postgresMessageTestSessionID, row.name, compactID, base.Add(time.Duration(i)*time.Second), i); err != nil {
			t.Fatal(err)
		}
	}
	names := func(messages []Message) []string {
		out := make([]string, 0, len(messages))
		for _, m := range messages {
			var content struct{ Content string }
			if err := json.Unmarshal(m.Content, &content); err != nil {
				t.Fatal(err)
			}
			name := strings.Fields(content.Content)[0]
			if m.Summarized {
				name += "*"
			}
			out = append(out, name)
		}
		return out
	}
	folded, err := svc.ListActiveSinceBySessionWithinBytes(ctx, postgresMessageTestSessionID, base, 3500, true)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := names(folded), []string{"input", "folded-2*", "failed", "stale", "newest"}; !slices.Equal(got, want) {
		t.Fatalf("folded window = %v, want %v", got, want)
	}
	full, err := svc.ListActiveSinceBySessionWithinBytes(ctx, postgresMessageTestSessionID, base, 3500, false)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := names(full), []string{"folded-2", "failed", "stale", "newest"}; !slices.Equal(got, want) {
		t.Fatalf("unfolded window = %v, want %v", got, want)
	}
}
