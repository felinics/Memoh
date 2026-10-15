package application

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/agent/background"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
	tools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/agent/toolexec"
	"github.com/felinics/memoh/internal/botagents"
	"github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	"github.com/felinics/memoh/internal/runtimefence"
)

type queueSubagentCatalog struct{ queries *sqlc.Queries }

func (*queueSubagentCatalog) List(context.Context, string) ([]botagents.BotAgent, error) {
	return nil, nil
}

func (c *queueSubagentCatalog) GetActive(ctx context.Context, botID, id string) (botagents.BotAgent, error) {
	row, err := c.queries.GetActiveBotAgentByID(ctx, sqlc.GetActiveBotAgentByIDParams{BotID: db.ParseUUIDOrEmpty(botID), ID: db.ParseUUIDOrEmpty(id)})
	return botagents.BotAgent{ID: row.ID.String(), BotID: row.BotID.String(), Runtime: row.Runtime, Enabled: row.Enabled}, err
}

func (*queueSubagentCatalog) ValidateConfiguration(context.Context, botagents.BotAgent, map[string]any) error {
	return nil
}

type queuedExternalExecutor struct {
	*Service
	calls atomic.Int32
}

func (s *queuedExternalExecutor) RunExternalSubagent(context.Context, tools.ExternalSubagentRun) (*tools.SpawnResult, error) {
	s.calls.Add(1)
	return &tools.SpawnResult{Text: "queued result", Persisted: true, TerminalResolved: true, TerminalOutcome: tools.SpawnAttemptCompleted}, nil
}

func TestPostgresFreshCoordinatorQueuesAndRevalidatesExternalBinding(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "executes after terminal", true: "disabled before terminal"}[disabled], func(t *testing.T) {
			ctx := t.Context()
			pool := openTurnAdmissionPostgres(t, ctx)
			botID, parentID := createTurnAdmissionFixture(t, ctx, pool)
			q := sqlc.New(pool)
			store := postgresstore.NewQueriesWithPool(pool, q)
			threads := thread.NewService(nil, store, nil)
			bot, err := q.GetBotByID(ctx, db.ParseUUIDOrEmpty(botID))
			if err != nil {
				t.Fatal(err)
			}
			owner := bot.OwnerUserID.String()
			agentID := uuid.NewString()
			if _, err := pool.Exec(ctx, `INSERT INTO bot_agents(id,bot_id,name,runtime) VALUES($1,$2,'Queue worker','codex')`, agentID, botID); err != nil {
				t.Fatal(err)
			}
			child, _, err := threads.CreateSubagent(ctx, thread.CreateSubagentInput{Thread: thread.CreateInput{BotID: botID, ParentThreadID: parentID, BotAgentID: agentID, RuntimeType: "codex", Metadata: map[string]any{"agent_id": "worker"}, RuntimeMetadata: map[string]any{"runtime_owner_account_id": owner, "project_path": "/data"}}})
			if err != nil {
				t.Fatal(err)
			}
			runs := ledger.NewPostgres(q, pool)
			manager := sessionruntime.NewManager(sessionruntime.NewMemoryBackend(), sessionruntime.Options{Ledger: runs, Fence: runtimefence.NewActivator(store)})
			t.Cleanup(func() { _ = manager.CloseContext(context.Background()) })
			svc := &Service{sessionService: threads, botPermissions: allowWorkspaceExecForBot(botID, owner)}
			svc.SetSessionRuntime(manager)
			svc.SetSubagentBackends(&queueSubagentCatalog{queries: q})
			svc.SetExternalRuntimes(noticeTestDriver{kind: "codex"})
			admission, err := manager.Admit(ctx, sessionruntime.AdmitInput{BotID: botID, SessionID: child.ID, InvocationID: "direct-child-turn", Payload: []byte(`{"query":"direct work"}`), Execution: sessionruntime.Execution{Admission: func(context.Context, sessionruntime.RunHandle) (sessionruntime.RunAdmissionView, error) {
				return sessionruntime.RunAdmissionView{}, nil
			}}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `UPDATE bot_sessions SET runtime_metadata=runtime_metadata || jsonb_build_object('external_execution_run',$1::text) WHERE id=$2`, admission.RunID, child.ID); err != nil {
				t.Fatal(err)
			}
			backgroundTasks := background.New(nil)
			provider := tools.NewSpawnProvider(nil, nil, nil, store, threads, backgroundTasks)
			provider.SetAgent(native.NewSpawnAdapter(native.New(native.Deps{})))
			provider.SetSubagentAdmitter(svc)
			executor := &queuedExternalExecutor{Service: svc}
			provider.SetExternalSubagents(executor)
			parent := tools.SessionContext{BotID: botID, SessionID: parentID, UserID: owner}
			registered, err := provider.Tools(ctx, parent)
			if err != nil {
				t.Fatal(err)
			}
			var taskID string
			for _, tool := range registered {
				if tool.Name == tools.ToolSendMessage().String() {
					output, err := tool.Execute(&toolexec.ToolExecContext{Context: ctx, ToolCallID: "queued-input"}, toolexec.ArgumentsFromValue(map[string]any{"id": "worker", "message": "queued follow-up", "run_in_background": true}))
					if err != nil {
						t.Fatal(err)
					}
					result := toolexec.OutputValue(output).(map[string]any)
					if result["status"] != "queued" {
						t.Fatalf("busy child=%v", result)
					}
					taskID = result["task_id"].(string)
				}
			}
			if taskID == "" || executor.calls.Load() != 0 {
				t.Fatal("queue executed before durable terminal")
			}
			if disabled {
				if _, err := pool.Exec(ctx, `UPDATE bot_agents SET enabled=false WHERE id=$1`, agentID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := pool.Exec(ctx, `UPDATE bot_sessions SET runtime_metadata=runtime_metadata-'external_execution_run' WHERE id=$1`, child.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := manager.FinishRun(ctx, admission.Handle, sessionruntime.RunStatusCompleted); err != nil {
				t.Fatal(err)
			}
			waitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			task, _, err := backgroundTasks.WaitForSessionTask(waitCtx, botID, parentID, taskID, 0)
			if err != nil {
				t.Fatal(err)
			}
			wantStatus, wantCalls := background.TaskCompleted, int32(1)
			if disabled {
				wantStatus, wantCalls = background.TaskFailed, 0
			}
			if task.Status != wantStatus || executor.calls.Load() != wantCalls {
				t.Fatalf("task=%+v calls=%d", task, executor.calls.Load())
			}
			var count int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM session_runs WHERE session_id=$1`, child.ID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1+int(wantCalls) {
				t.Fatalf("durable run count=%d", count)
			}
		})
	}
}
