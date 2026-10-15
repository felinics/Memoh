package application

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
	tools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	"github.com/felinics/memoh/internal/runtimefence"
)

func TestPostgresSubagentControlPinsRunAndWaitsForTerminal(t *testing.T) {
	ctx := t.Context()
	pool := openTurnAdmissionPostgres(t, ctx)
	botID, parentID := createTurnAdmissionFixture(t, ctx, pool)
	q := sqlc.New(pool)
	store := postgresstore.NewQueriesWithPool(pool, q)
	threads := thread.NewService(nil, store, nil)
	child, err := threads.Create(ctx, thread.CreateInput{BotID: botID, ParentThreadID: parentID, Type: thread.TypeSubagent})
	if err != nil {
		t.Fatal(err)
	}
	bot, err := q.GetBotByID(ctx, db.ParseUUIDOrEmpty(botID))
	if err != nil {
		t.Fatal(err)
	}
	owner := bot.OwnerUserID.String()
	parent := tools.SessionContext{BotID: botID, SessionID: parentID, UserID: owner}
	runs := ledger.NewPostgres(q, pool)
	manager := sessionruntime.NewManager(sessionruntime.NewMemoryBackend(), sessionruntime.Options{Ledger: runs, Fence: runtimefence.NewActivator(store)})
	t.Cleanup(func() { _ = manager.CloseContext(context.Background()) })
	svc := &Service{logger: slog.New(slog.DiscardHandler), sessionService: threads, botPermissions: &fakeBotPermissionChecker{values: map[string]bool{botID + ":" + owner + ":" + bots.PermissionChat: true}}}
	svc.SetSessionRuntime(manager)
	admit := func() sessionruntime.Admission {
		a, err := manager.Admit(ctx, sessionruntime.AdmitInput{BotID: botID, SessionID: child.ID, InvocationID: uuid.NewString(), Payload: []byte(`{"message":"work"}`), Execution: sessionruntime.Execution{Admission: func(context.Context, sessionruntime.RunHandle) (sessionruntime.RunAdmissionView, error) {
			return sessionruntime.RunAdmissionView{}, nil
		}}})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	first := admit()
	state, err := svc.SnapshotSubagent(ctx, parent, child.ID)
	if err != nil || !state.Active || state.RunID != first.RunID || state.Status != sessionruntime.RunStatusRunning {
		t.Fatalf("snapshot = %+v, %v", state, err)
	}
	steerInput := tools.SubagentControlInput{Parent: parent, SessionID: child.ID, RunID: first.RunID, Action: "steer", Message: "narrow the task", ControlID: "steer-first"}
	if _, err := svc.ControlSubagent(ctx, steerInput); !errors.Is(err, sessionruntime.ErrQueueSteerUnsupported) {
		t.Fatalf("unadvertised steer = %v", err)
	}
	if err := manager.EnableSteer(ctx, first.Handle); err != nil {
		t.Fatal(err)
	}
	steered, err := svc.ControlSubagent(ctx, steerInput)
	if err != nil || !steered.Accepted || steered.ItemID == "" {
		t.Fatalf("steer = %+v, %v", steered, err)
	}
	replayed, err := svc.ControlSubagent(ctx, steerInput)
	if err != nil || replayed.ItemID != steered.ItemID {
		t.Fatalf("steer replay = %+v, %v", replayed, err)
	}
	items, _, err := manager.PendingQueues(ctx, sessionruntime.Key{BotID: botID, SessionID: child.ID}, 0)
	if err != nil || len(items) != 1 || items[0].TargetRunID != first.RunID || decodeQueuePayload(items[0].Payload).Text != steerInput.Message {
		t.Fatalf("steer queue = %+v, %v", items, err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if err := svc.WaitSubagentIdle(waitCtx, parent, child.ID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait = %v", err)
	}
	stopped, err := svc.ControlSubagent(ctx, tools.SubagentControlInput{Parent: parent, SessionID: child.ID, RunID: first.RunID, Action: "stop", ControlID: "stop-first"})
	if err != nil || !stopped.Accepted || stopped.Status != sessionruntime.RunStatusAborting {
		t.Fatalf("stop = %+v, %v", stopped, err)
	}
	if _, err := manager.FinishRun(ctx, first.Handle, sessionruntime.RunStatusAborted); err != nil {
		t.Fatal(err)
	}
	if err := svc.WaitSubagentIdle(ctx, parent, child.ID); err != nil {
		t.Fatal(err)
	}
	second := admit()
	_, err = svc.ControlSubagent(ctx, tools.SubagentControlInput{Parent: parent, SessionID: child.ID, RunID: first.RunID, Action: "stop", ControlID: "stale-stop"})
	if !errors.Is(err, sessionruntime.ErrCommandTargetMismatch) {
		t.Fatalf("stale stop = %v", err)
	}
	if _, err := svc.ControlSubagent(ctx, tools.SubagentControlInput{Parent: tools.SessionContext{BotID: botID, SessionID: uuid.NewString(), UserID: owner}, SessionID: child.ID, RunID: second.RunID, Action: "stop"}); err == nil {
		t.Fatal("foreign parent controlled child")
	}
	state, err = svc.SnapshotSubagent(ctx, parent, child.ID)
	if err != nil || state.RunID != second.RunID || state.Status != sessionruntime.RunStatusRunning {
		t.Fatalf("successor was changed: %+v, %v", state, err)
	}
	permissions := svc.botPermissions.(*fakeBotPermissionChecker)
	permissions.values[botID+":"+owner+":"+bots.PermissionChat] = false
	if err := svc.WaitSubagentIdle(ctx, parent, child.ID); err == nil {
		t.Fatal("revoked parent permission did not end wait")
	}
	permissions.values[botID+":"+owner+":"+bots.PermissionChat] = true
	if _, err := manager.FinishRunWithErrorCode(ctx, second.Handle, sessionruntime.RunStatusErrored, "agent.response_timeout"); err != nil {
		t.Fatal(err)
	}
	fresh := sessionruntime.NewManager(sessionruntime.NewMemoryBackend(), sessionruntime.Options{Ledger: runs, Fence: runtimefence.NewActivator(store)})
	t.Cleanup(func() { _ = fresh.CloseContext(context.Background()) })
	svc = &Service{logger: slog.New(slog.DiscardHandler), sessionService: threads, botPermissions: permissions}
	svc.SetSessionRuntime(fresh)
	state, err = svc.SnapshotSubagent(ctx, parent, child.ID)
	if err != nil || state.RunID != second.RunID || state.Status != sessionruntime.RunStatusErrored || state.Code != "agent.response_timeout" || state.Active {
		t.Fatalf("durable failure after projection loss = %+v, %v", state, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE bot_sessions SET runtime_type='codex', runtime_metadata=jsonb_build_object('external_termination_unconfirmed',$1::text) WHERE id=$2`, second.RunID, child.ID); err != nil {
		t.Fatal(err)
	}
	permissions.values[botID+":"+owner+":"+bots.PermissionWorkspaceExec] = true
	state, err = svc.SnapshotSubagent(ctx, parent, child.ID)
	if err != nil || state.Status != "execution_unknown" || state.Code != string(apperror.CodeExternalRuntimeTerminationUnconfirmed) || !state.Active || state.SteerSupported {
		t.Fatalf("unknown gate = %+v, %v", state, err)
	}
	if err := svc.WaitSubagentIdle(ctx, parent, child.ID); apperror.CodeOf(err) != apperror.CodeExternalRuntimeTerminationUnconfirmed {
		t.Fatalf("unknown gate wait = %v", err)
	}
	unknownStop, err := svc.ControlSubagent(ctx, tools.SubagentControlInput{Parent: parent, SessionID: child.ID, RunID: second.RunID, Action: "stop"})
	if err != nil || unknownStop.Accepted || unknownStop.Status != "execution_unknown" {
		t.Fatalf("unconfirmed stop claimed success = %+v,%v", unknownStop, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE bot_sessions SET runtime_metadata=jsonb_build_object('external_execution_run',$1::text) WHERE id=$2`, second.RunID, child.ID); err != nil {
		t.Fatal(err)
	}
	state, err = svc.SnapshotSubagent(ctx, parent, child.ID)
	if err != nil || state.Status != "execution_unknown" || state.RunID != second.RunID {
		t.Fatalf("orphaned execution intent=%+v,%v", state, err)
	}
	if err := svc.WaitSubagentIdle(ctx, parent, child.ID); apperror.CodeOf(err) != apperror.CodeExternalRuntimeTerminationUnconfirmed {
		t.Fatalf("orphaned execution resumed: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE bot_sessions SET metadata=jsonb_build_object('external_execution_run',$1::text), runtime_metadata='{}' WHERE id=$2`, second.RunID, child.ID); err != nil {
		t.Fatal(err)
	}
	state, err = svc.SnapshotSubagent(ctx, parent, child.ID)
	if err != nil || state.Status == "execution_unknown" {
		t.Fatalf("ordinary metadata altered execution authority=%+v,%v", state, err)
	}
	ordinary, err := threads.Get(ctx, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.externalSubagentExecutionGate(ctx, ordinary); err != nil {
		t.Fatalf("ordinary metadata blocked runtime controls: %v", err)
	}
}
