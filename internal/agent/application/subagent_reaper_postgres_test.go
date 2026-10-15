package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/agent/background"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
	tools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/agent/toolexec"
	"github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	"github.com/felinics/memoh/internal/runtimefence"
)

func TestPostgresSubagentSlotsReclaimedOnlyAfterReapingOnRestart(t *testing.T) {
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
	if _, err := pool.Exec(ctx, `INSERT INTO bot_agents(id,bot_id,name,runtime) VALUES($1,$2,'Worker','codex')`, agentID, botID); err != nil {
		t.Fatal(err)
	}
	createChild := func(name string) string {
		child, _, err := threads.CreateSubagent(ctx, thread.CreateSubagentInput{Thread: thread.CreateInput{BotID: botID, ParentThreadID: parentID, BotAgentID: agentID, RuntimeType: "codex", Metadata: map[string]any{"agent_id": name}, RuntimeMetadata: map[string]any{"runtime_owner_account_id": owner, "project_path": "/data"}}})
		if err != nil {
			t.Fatal(err)
		}
		return child.ID
	}
	mark := func(childID string, keys ...string) string {
		runID := uuid.NewString()
		for _, key := range keys {
			if _, err := pool.Exec(ctx, `UPDATE bot_sessions SET runtime_metadata=runtime_metadata || jsonb_build_object($1::text,$2::text) WHERE id=$3`, key, runID, childID); err != nil {
				t.Fatal(err)
			}
		}
		return runID
	}
	held := map[string]string{}
	for i := range background.MaxRunningSpawnTasks {
		id := createChild(fmt.Sprintf("held%d", i))
		keys := []string{"external_execution_run", "external_termination_unconfirmed"}
		if i == 0 {
			keys = keys[:1]
		}
		held[id] = mark(id, keys...)
	}
	deleted := createChild("deleted")
	mark(deleted, "external_execution_run", "external_termination_unconfirmed")
	if err := threads.SoftDelete(ctx, deleted); err != nil {
		t.Fatal(err)
	}
	createChild("fresh")
	if _, err := pool.Exec(ctx, `UPDATE bot_sessions SET runtime_fencing_token = nextval('session_runtime_fencing_token_seq') WHERE parent_session_id=$1 AND metadata->>'agent_id' <> 'fresh'`, parentID); err != nil {
		t.Fatal(err)
	}
	fenced := runtimefence.WithContext(ctx, runtimefence.Fence{BotID: botID, SessionID: parentID, Token: 1})

	manager := sessionruntime.NewManager(sessionruntime.NewMemoryBackend(), sessionruntime.Options{Ledger: ledger.NewPostgres(q, pool), Fence: runtimefence.NewActivator(store)})
	t.Cleanup(func() { _ = manager.CloseContext(context.Background()) })
	svc := &Service{logger: slog.New(slog.DiscardHandler), sessionService: threads, botPermissions: allowWorkspaceExecForBot(botID, owner)}
	svc.SetSessionRuntime(manager)
	svc.SetSubagentBackends(&queueSubagentCatalog{queries: q})
	svc.SetExternalRuntimes(noticeTestDriver{kind: "codex"})
	reaper := &scriptedReaper{err: errors.New("workspace unreachable")}
	svc.SetSubagentProcessReaper(reaper)
	provider := tools.NewSpawnProvider(nil, nil, nil, store, threads, background.New(nil))
	provider.SetAgent(native.NewSpawnAdapter(native.New(native.Deps{})))
	provider.SetSubagentAdmitter(svc)
	executor := &queuedExternalExecutor{Service: svc}
	provider.SetExternalSubagents(executor)
	parent := tools.SessionContext{BotID: botID, SessionID: parentID, UserID: owner}
	registered, err := provider.Tools(ctx, parent)
	if err != nil {
		t.Fatal(err)
	}
	sendFresh := func() (any, error) {
		for _, tool := range registered {
			if tool.Name == tools.ToolSendMessage().String() {
				output, err := tool.Execute(&toolexec.ToolExecContext{Context: fenced, ToolCallID: uuid.NewString()}, toolexec.ArgumentsFromValue(map[string]any{"id": "fresh", "message": "work"}))
				if err != nil {
					return nil, err
				}
				return toolexec.OutputValue(output), nil
			}
		}
		t.Fatal("send_message is not registered")
		return nil, nil
	}

	result, err := sendFresh()
	if err == nil {
		t.Fatalf("unreachable workspace released held slots: %v", result)
	}
	if executor.calls.Load() != 0 || len(reaper.calls) != len(held)+1 {
		t.Fatalf("calls=%d reaps=%v err=%v", executor.calls.Load(), reaper.calls, err)
	}
	if result, err := sendFresh(); err == nil || len(reaper.calls) != len(held)+1 {
		t.Fatalf("failed confirmations were retried before their claims expired: %v reaps=%v", result, reaper.calls)
	}

	if tag, err := pool.Exec(ctx, `UPDATE bot_sessions SET runtime_metadata = jsonb_set(runtime_metadata, '{external_termination_reap}', '"2000-01-01 00:00:00+00"') WHERE bot_id=$1 AND runtime_metadata ? 'external_termination_reap'`, botID); err != nil || tag.RowsAffected() != int64(len(held)+1) {
		t.Fatalf("expire claims: %v %v", tag, err)
	}
	reaper.err = nil
	result, err = sendFresh()
	if err != nil {
		t.Fatal(err)
	}
	if status := result.(map[string]any)["status"]; status != "completed" || executor.calls.Load() != 1 {
		t.Fatalf("reaped slots were not reclaimed: %v calls=%d", result, executor.calls.Load())
	}
	readMeta := func(id string) map[string]any {
		var raw []byte
		if err := pool.QueryRow(ctx, `SELECT runtime_metadata FROM bot_sessions WHERE id=$1`, id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		meta := map[string]any{}
		if err := json.Unmarshal(raw, &meta); err != nil {
			t.Fatal(err)
		}
		return meta
	}
	for id, runID := range held {
		meta := readMeta(id)
		if meta["external_execution_run"] != nil || meta["external_termination_unconfirmed"] != nil || meta["external_termination_resolved"] != runID {
			t.Fatalf("child %s was not settled: %v", id, meta)
		}
	}
	if meta := readMeta(deleted); meta["external_termination_unconfirmed"] != nil || meta["external_termination_resolved"] == nil {
		t.Fatalf("deleted child's leftovers were not settled: %v", meta)
	}
}

func TestPostgresSubagentStaleUnknownRowNeverReapsALaterRun(t *testing.T) {
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
	agentID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO bot_agents(id,bot_id,name,runtime) VALUES($1,$2,'Worker','codex')`, agentID, botID); err != nil {
		t.Fatal(err)
	}
	child, _, err := threads.CreateSubagent(ctx, thread.CreateSubagentInput{Thread: thread.CreateInput{BotID: botID, ParentThreadID: parentID, BotAgentID: agentID, RuntimeType: "codex", Metadata: map[string]any{"agent_id": "worker"}, RuntimeMetadata: map[string]any{"runtime_owner_account_id": bot.OwnerUserID.String(), "project_path": "/data"}}})
	if err != nil {
		t.Fatal(err)
	}
	manager := sessionruntime.NewManager(sessionruntime.NewMemoryBackend(), sessionruntime.Options{Ledger: ledger.NewPostgres(q, pool), Fence: runtimefence.NewActivator(store)})
	t.Cleanup(func() { _ = manager.CloseContext(context.Background()) })
	later, err := manager.Admit(ctx, sessionruntime.AdmitInput{BotID: botID, SessionID: child.ID, InvocationID: "later-run", Payload: []byte(`{"query":"later"}`), Execution: sessionruntime.Execution{Admission: func(context.Context, sessionruntime.RunHandle) (sessionruntime.RunAdmissionView, error) {
		return sessionruntime.RunAdmissionView{}, nil
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE bot_sessions SET runtime_metadata=runtime_metadata || jsonb_build_object('external_execution_run',$1::text) WHERE id=$2`, later.RunID, child.ID); err != nil {
		t.Fatal(err)
	}
	stale := child
	stale.RuntimeMetadata = map[string]any{"external_execution_run": "earlier-run", "external_termination_unconfirmed": "earlier-run"}
	reaper := &scriptedReaper{}
	svc := &Service{logger: slog.New(slog.DiscardHandler), sessionService: threads}
	svc.SetSessionRuntime(manager)
	svc.SetSubagentProcessReaper(reaper)
	_, state, err := svc.subagentStateSnapshot(ctx, stale)
	if err != nil || state.RunID != later.RunID || state.Status == "execution_unknown" {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	var guard, resolved *string
	if err := pool.QueryRow(ctx, `SELECT runtime_metadata->>'external_execution_run', runtime_metadata->>'external_termination_resolved' FROM bot_sessions WHERE id=$1`, child.ID).Scan(&guard, &resolved); err != nil {
		t.Fatal(err)
	}
	if len(reaper.calls) != 0 || guard == nil || *guard != later.RunID || resolved != nil {
		t.Fatalf("a stale row reaped the later run: reaps=%v guard=%v resolved=%v", reaper.calls, guard, resolved)
	}
}

func TestPostgresSubagentHoldsSurviveClearingHistory(t *testing.T) {
	ctx := t.Context()
	pool := openTurnAdmissionPostgres(t, ctx)
	botID, parentID := createTurnAdmissionFixture(t, ctx, pool)
	q := sqlc.New(pool)
	threads := thread.NewService(nil, postgresstore.NewQueriesWithPool(pool, q), nil)
	bot, err := q.GetBotByID(ctx, db.ParseUUIDOrEmpty(botID))
	if err != nil {
		t.Fatal(err)
	}
	owner := bot.OwnerUserID.String()
	agentID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO bot_agents(id,bot_id,name,runtime) VALUES($1,$2,'Worker','codex')`, agentID, botID); err != nil {
		t.Fatal(err)
	}
	for _, clear := range []string{"session", "bot"} {
		t.Run(clear, func(t *testing.T) {
			child, _, err := threads.CreateSubagent(ctx, thread.CreateSubagentInput{Thread: thread.CreateInput{BotID: botID, ParentThreadID: parentID, BotAgentID: agentID, RuntimeType: "codex", Metadata: map[string]any{"agent_id": "worker-" + clear}, RuntimeMetadata: map[string]any{"runtime_owner_account_id": owner, "project_path": "/data/work"}}})
			if err != nil {
				t.Fatal(err)
			}
			runID := uuid.NewString()
			if _, err := pool.Exec(ctx, `UPDATE bot_sessions SET runtime_metadata=runtime_metadata || jsonb_build_object('external_execution_run',$1::text,'external_termination_unconfirmed',$1::text,'external_termination_reap','2026-01-01 00:00:00+00','external_termination_resolved','earlier-run','codex_thread_id','native-thread') WHERE id=$2`, runID, child.ID); err != nil {
				t.Fatal(err)
			}
			if clear == "session" {
				err = q.ClearHistoryBySession(ctx, db.ParseUUIDOrEmpty(child.ID))
			} else {
				err = q.ClearHistoryByBot(ctx, db.ParseUUIDOrEmpty(botID))
			}
			if err != nil {
				t.Fatal(err)
			}
			cleared, err := threads.Get(ctx, child.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]any{"external_execution_run": runID, "external_termination_unconfirmed": runID, "external_termination_reap": "2026-01-01 00:00:00+00", "external_termination_resolved": "earlier-run", "runtime_owner_account_id": owner, "project_path": "/data/work"}
			for key, value := range want {
				if cleared.RuntimeMetadata[key] != value {
					t.Fatalf("clearing history dropped %s: %v", key, cleared.RuntimeMetadata)
				}
			}
			if cleared.RuntimeMetadata["codex_thread_id"] != nil {
				t.Fatalf("clearing history kept the continuity anchor: %v", cleared.RuntimeMetadata)
			}
			holds, err := threads.ListExternalExecutionHolds(ctx, botID)
			if err != nil || !slices.ContainsFunc(holds, func(held thread.Thread) bool { return held.ID == child.ID }) {
				t.Fatalf("clearing history released the hold: %v %v", holds, err)
			}
			if _, err := pool.Exec(ctx, `UPDATE bot_sessions SET runtime_metadata='{}'::jsonb WHERE id=$1`, child.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPostgresSubagentSupersededClaimCannotSettle(t *testing.T) {
	ctx := t.Context()
	pool := openTurnAdmissionPostgres(t, ctx)
	botID, parentID := createTurnAdmissionFixture(t, ctx, pool)
	q := sqlc.New(pool)
	threads := thread.NewService(nil, postgresstore.NewQueriesWithPool(pool, q), nil)
	bot, err := q.GetBotByID(ctx, db.ParseUUIDOrEmpty(botID))
	if err != nil {
		t.Fatal(err)
	}
	agentID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO bot_agents(id,bot_id,name,runtime) VALUES($1,$2,'Worker','codex')`, agentID, botID); err != nil {
		t.Fatal(err)
	}
	runID := uuid.NewString()
	child, _, err := threads.CreateSubagent(ctx, thread.CreateSubagentInput{Thread: thread.CreateInput{BotID: botID, ParentThreadID: parentID, BotAgentID: agentID, RuntimeType: "codex", Metadata: map[string]any{"agent_id": "worker"}, RuntimeMetadata: map[string]any{"runtime_owner_account_id": bot.OwnerUserID.String(), "project_path": "/data", "external_execution_run": runID, "external_termination_unconfirmed": runID}}})
	if err != nil {
		t.Fatal(err)
	}
	first, ok, err := threads.ClaimExternalSubagentReap(ctx, child.ID, runID, subagentReapLease)
	if err != nil || !ok {
		t.Fatalf("first claim: ok=%v err=%v", ok, err)
	}
	if _, ok, err := threads.ClaimExternalSubagentReap(ctx, child.ID, runID, subagentReapLease); err != nil || ok {
		t.Fatalf("a live claim was taken again: ok=%v err=%v", ok, err)
	}
	second, ok, err := threads.ClaimExternalSubagentReap(ctx, child.ID, runID, 0)
	if err != nil || !ok || second == first {
		t.Fatalf("expired claim was not taken over: first=%q second=%q ok=%v err=%v", first, second, ok, err)
	}
	if _, ok, err := threads.ResolveExternalSubagentTermination(ctx, child.ID, runID, first); err != nil || ok {
		t.Fatalf("superseded claim settled the run: ok=%v err=%v", ok, err)
	}
	if _, ok, err := threads.ResolveExternalSubagentTermination(ctx, child.ID, uuid.NewString(), second); err != nil || ok {
		t.Fatalf("claim settled a different run: ok=%v err=%v", ok, err)
	}
	resolved, ok, err := threads.ResolveExternalSubagentTermination(ctx, child.ID, runID, second)
	if err != nil || !ok || resolved.RuntimeMetadata["external_termination_resolved"] != runID || resolved.RuntimeMetadata["external_execution_run"] != nil || resolved.RuntimeMetadata["external_termination_reap"] != nil {
		t.Fatalf("current claim did not settle: meta=%v ok=%v err=%v", resolved.RuntimeMetadata, ok, err)
	}
}
