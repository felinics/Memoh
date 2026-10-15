package application

import (
	"context"
	"log/slog"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/agent/background"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
	tools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/agent/toolexec"
	"github.com/felinics/memoh/internal/bots"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/runtimefence"
)

type nativeCreationExecutor struct{ calls []tools.SpawnRunConfig }

func (e *nativeCreationExecutor) Generate(ctx context.Context, cfg tools.SpawnRunConfig) (*tools.SpawnResult, error) {
	return e.GenerateWithWatchdog(ctx, cfg, nil)
}

func (e *nativeCreationExecutor) GenerateWithWatchdog(_ context.Context, cfg tools.SpawnRunConfig, _ func()) (*tools.SpawnResult, error) {
	e.calls = append(e.calls, cfg)
	text := "completed " + cfg.Query
	return &tools.SpawnResult{Text: text, Messages: []sdk.Message{sdk.AssistantMessage(text)}}, nil
}

type changedNativeAdmission struct {
	*Service
	change func(context.Context, string) error
}

func (s *changedNativeAdmission) WaitSubagentIdle(ctx context.Context, parent tools.SessionContext, childID string) error {
	if s.change != nil {
		if err := s.change(ctx, childID); err != nil {
			return err
		}
		s.change = nil
	}
	return s.Service.WaitSubagentIdle(ctx, parent, childID)
}

func TestPostgresSubagentNativeCreationAndFollowupKeepRuntimeBinding(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "created binding continues", true: "changed binding rejected"}[changed], func(t *testing.T) {
			ctx := t.Context()
			pool := openTurnAdmissionPostgres(t, ctx)
			providerID, modelID := uuid.NewString(), uuid.NewString()
			if _, err := pool.Exec(ctx, `INSERT INTO providers (id,name) VALUES ($1,$2)`, providerID, "native-creation-"+providerID); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM providers WHERE id=$1`, providerID) })
			if _, err := pool.Exec(ctx, `INSERT INTO models (id,provider_id,model_id,config) VALUES ($1,$2,'native-creation','{"compatibilities":["tool-call"]}')`, modelID, providerID); err != nil {
				t.Fatal(err)
			}
			botID, parentID := createTurnAdmissionFixture(t, ctx, pool)
			q := sqlc.New(pool)
			store := postgresstore.NewQueriesWithPool(pool, q)
			threads := thread.NewService(nil, store, nil)
			bot, err := q.GetBotByID(ctx, db.ParseUUIDOrEmpty(botID))
			if err != nil {
				t.Fatal(err)
			}
			owner := bot.OwnerUserID.String()
			manager := sessionruntime.NewManager(sessionruntime.NewMemoryBackend(), sessionruntime.Options{Ledger: ledger.NewPostgres(q, pool), Fence: runtimefence.NewActivator(store)})
			t.Cleanup(func() { _ = manager.CloseContext(context.WithoutCancel(ctx)) })
			logger := slog.Default()
			svc := &Service{logger: logger, queries: store, sessionService: threads, botPermissions: &fakeBotPermissionChecker{values: map[string]bool{botID + ":" + owner + ":" + bots.PermissionChat: true}}}
			svc.SetSessionRuntime(manager)
			admitter := &changedNativeAdmission{Service: svc}
			if changed {
				agentID := uuid.NewString()
				if _, err := pool.Exec(ctx, `INSERT INTO bot_agents (id,bot_id,name,runtime) VALUES ($1,$2,'Changed native binding','model')`, agentID, botID); err != nil {
					t.Fatal(err)
				}
				admitter.change = func(ctx context.Context, childID string) error {
					_, err := pool.Exec(ctx, `UPDATE bot_sessions SET bot_agent_id=$1 WHERE id=$2`, agentID, childID)
					return err
				}
			}
			provider := tools.NewSpawnProvider(logger, nil, models.NewService(logger, store), store, threads, background.New(logger))
			executor := &nativeCreationExecutor{}
			provider.SetAgent(executor)
			provider.SetSubagentAdmitter(admitter)
			provider.SetMessageService(messagepkg.NewService(logger, store))
			parent := tools.SessionContext{BotID: botID, ChatID: botID, SessionID: parentID, UserID: owner, CurrentModelUUID: modelID}
			invoke := func(name string, args map[string]any) map[string]any {
				t.Helper()
				registered, err := provider.Tools(ctx, parent)
				if err != nil {
					t.Fatal(err)
				}
				for _, tool := range registered {
					if tool.Name == name {
						output, err := tool.Execute(&toolexec.ToolExecContext{Context: ctx, ToolCallID: uuid.NewString()}, toolexec.ArgumentsFromValue(args))
						if err != nil {
							t.Fatal(err)
						}
						return toolexec.OutputValue(output).(map[string]any)
					}
				}
				t.Fatalf("tool %s unavailable", name)
				return nil
			}
			first := invoke("spawn_agent", map[string]any{"id": "worker", "task": "first task"})
			childID, _ := first["session_id"].(string)
			if changed {
				if first["status"] != "failed" || len(executor.calls) != 0 {
					t.Fatalf("changed binding executed: result=%v calls=%d", first, len(executor.calls))
				}
				var count int
				if err := pool.QueryRow(ctx, `SELECT count(*) FROM session_runs WHERE session_id=$1`, childID).Scan(&count); err != nil || count != 0 {
					t.Fatalf("changed binding admitted a run: count=%d err=%v", count, err)
				}
				return
			}
			if first["status"] != "completed" || first["text"] != "completed first task" || len(executor.calls) != 1 {
				t.Fatalf("native creation did not execute once: result=%v calls=%d", first, len(executor.calls))
			}
			second := invoke("send_message", map[string]any{"id": "worker", "message": "next task"})
			if second["status"] != "completed" || second["session_id"] != childID || second["text"] != "completed next task" || len(executor.calls) != 2 {
				t.Fatalf("native followup did not continue once: result=%v calls=%d", second, len(executor.calls))
			}
			child, err := threads.Get(ctx, childID)
			if err != nil || child.RuntimeType != thread.RuntimeModel || child.ParentThreadID != parentID {
				t.Fatalf("native child descriptor changed: child=%+v err=%v", child, err)
			}
			for _, call := range executor.calls {
				if call.Identity.SessionID != childID || !call.Identity.IsSubagent || call.RunID == "" || call.ModelUUID != modelID {
					t.Fatalf("native execution lost admitted identity: %+v", call.Identity)
				}
			}
			var completed, assistants int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM session_runs WHERE session_id=$1 AND state='completed'`, childID).Scan(&completed); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM bot_history_messages WHERE session_id=$1 AND role='assistant' AND run_id IS NOT NULL`, childID).Scan(&assistants); err != nil {
				t.Fatal(err)
			}
			if completed != 2 || assistants != 2 {
				t.Fatalf("durable native turns: completed=%d assistants=%d", completed, assistants)
			}
		})
	}
}
