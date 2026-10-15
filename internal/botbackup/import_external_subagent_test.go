package botbackup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
)

func TestPostgresRestoreExternalSubagentHistoryWithoutRuntimeBinding(t *testing.T) {
	for _, runtimeType := range []string{thread.RuntimeCodex, thread.RuntimeClaudeCode, thread.RuntimeACPAgent} {
		t.Run(runtimeType, func(t *testing.T) {
			ctx := context.Background()
			tx := beginExternalSubagentRestoreTx(t, ctx)
			const (
				importer      = "00000000-0000-0000-0000-000000088001"
				targetBot     = "00000000-0000-0000-0000-000000088002"
				sourceOwner   = "00000000-0000-0000-0000-000000088003"
				sourceParent  = "00000000-0000-0000-0000-000000088004"
				sourceChild   = "00000000-0000-0000-0000-000000088005"
				sourceAgent   = "00000000-0000-0000-0000-000000088006"
				sourceMessage = "00000000-0000-0000-0000-000000088007"
			)
			if _, err := tx.Exec(ctx, `INSERT INTO users (id, username, is_active) VALUES ($1, 'external-subagent-importer', true)`, importer); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO team_members (user_id, role) VALUES ($1, 'admin')`, importer); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO bots (id, owner_user_id, name) VALUES ($1, $2, 'restored-bot')`, targetBot, importer); err != nil {
				t.Fatal(err)
			}
			metadata := []byte(fmt.Sprintf(`{"runtime_owner_account_id":%q,"agent_id":"worker","project_path":"/data","acp_agent_id":"custom-agent","codex_thread_id":"source-anchor","claude_session_id":"source-anchor"}`, sourceOwner))
			sessions := []sqlc.ListSessionsByBotRow{
				{ID: optionalUUID(sourceChild), BotAgentID: optionalUUID(sourceAgent), Type: thread.TypeSubagent, SessionMode: thread.TypeSubagent, RuntimeType: runtimeType, Title: "Worker", Metadata: metadata, RuntimeMetadata: metadata, ParentSessionID: optionalUUID(sourceParent)},
				{ID: optionalUUID(sourceParent), Type: thread.TypeChat, SessionMode: thread.TypeChat, RuntimeType: thread.RuntimeModel, Title: "Parent", Metadata: []byte(`{}`)},
			}
			messages := []sqlc.ListAllMessagesForBackupRow{{
				ID: optionalUUID(sourceMessage), SessionID: optionalUUID(sourceChild), Role: "assistant",
				Content:  []byte(`{"role":"assistant","content":"preserved external result"}`),
				Metadata: []byte(`{}`), Usage: []byte(`{}`),
			}}
			state := &importState{entries: map[string]backupZipEntry{}, counts: map[Section]int{}}
			for path, value := range map[string]any{"history/sessions.json": sessions, "history/messages.json": messages} {
				raw, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				state.entries[path] = backupZipEntry{data: raw}
			}
			queries := postgresstore.NewQueries(sqlc.New(tx))
			svc := New(Params{Queries: queries})
			if err := svc.restoreHistory(ctx, importer, targetBot, state, false, false); err != nil {
				t.Fatalf("restoreHistory: %v", err)
			}
			restored, err := queries.ListSessionsByBot(ctx, optionalUUID(targetBot))
			if err != nil || len(restored) != 2 {
				t.Fatalf("restored sessions = %+v, %v", restored, err)
			}
			var parentID string
			for _, session := range restored {
				if session.Type == thread.TypeChat {
					parentID = session.ID.String()
				}
			}
			threads := thread.NewService(nil, queries, nil)
			children, err := threads.ListSubagentsByParent(ctx, parentID)
			if err != nil || len(children) != 1 {
				t.Fatalf("restored child is not visible to parent: %+v, %v", children, err)
			}
			child := children[0]
			if child.ID == sourceChild || child.ParentThreadID == sourceParent || child.RuntimeType != runtimeType || child.SessionMode != thread.TypeSubagent || child.BotAgentID != "" || child.CreatedByUserID != importer {
				t.Fatalf("restored child binding = %+v", child)
			}
			for _, meta := range []map[string]any{child.Metadata, child.RuntimeMetadata} {
				if meta["runtime_owner_account_id"] != importer {
					t.Fatalf("restored owner = %v, want importer", meta["runtime_owner_account_id"])
				}
			}
			if _, err := threads.GetSubagentConfig(ctx, child.ID); !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("native configuration unexpectedly restored: %v", err)
			}
			restoredMessages, err := queries.ListAllMessagesForBackup(ctx, optionalUUID(targetBot))
			if err != nil || len(restoredMessages) != 1 || restoredMessages[0].SessionID.String() != child.ID || restoredMessages[0].RuntimeType != runtimeType || !strings.Contains(string(restoredMessages[0].Content), "preserved external result") {
				t.Fatalf("restored messages = %+v, %v", restoredMessages, err)
			}
			if len(state.warnings) != 1 || state.warnings[0] != "external subagent history restored without runtime bindings; imported subagents cannot execute" {
				t.Fatalf("missing unbound-runtime warning: %v", state.warnings)
			}
		})
	}
}

func beginExternalSubagentRestoreTx(t *testing.T, ctx context.Context) pgx.Tx {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set")
	}
	pool, err := db.OpenPostgresDSN(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	return tx
}
