package application

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/bots"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

type controlQueries struct {
	dbstore.Queries
	row    sqlc.BotSession
	writes int
	stale  bool
}

func (*controlQueries) SupportsTransactions() bool                                     { return true }
func (q *controlQueries) InTx(_ context.Context, fn func(dbstore.Queries) error) error { return fn(q) }
func (*controlQueries) LockBotForSessionWrite(_ context.Context, id pgtype.UUID) (pgtype.UUID, error) {
	return id, nil
}

func (q *controlQueries) LockSessionRuntimeFence(_ context.Context, in sqlc.LockSessionRuntimeFenceParams) (int64, error) {
	if q.stale {
		return 0, pgx.ErrNoRows
	}
	return in.RuntimeFencingToken, nil
}

func (q *controlQueries) GetSessionByID(context.Context, pgtype.UUID) (sqlc.BotSession, error) {
	return q.row, nil
}

func (q *controlQueries) UpdateSessionRuntimeMetadata(_ context.Context, in sqlc.UpdateSessionRuntimeMetadataParams) (sqlc.BotSession, error) {
	if in.FencingToken.Int64 != 1 {
		return sqlc.BotSession{}, errors.New("missing fence")
	}
	q.writes++
	q.row.RuntimeMetadata = in.RuntimeMetadata
	return q.row, nil
}

type controlDriver struct {
	runtimeType  string
	readCalls    int
	commands     []external.Command
	compactCalls int
	mode         string
}

func (d *controlDriver) RuntimeType() string {
	if d.runtimeType != "" {
		return d.runtimeType
	}
	return "codex"
}

// Caps declares what the stub implements; a runtime that reads its native
// session on a read command declares so, like Grok Build.
func (d *controlDriver) Caps() external.Caps {
	return external.Caps{
		ReadCommandsRequireIdle: d.RuntimeType() == "grok-build",
	}
}

func (*controlDriver) Prompt(context.Context, external.PromptInput) (external.PromptResult, error) {
	panic("control must not start chat")
}

func (d *controlDriver) Commands(context.Context, external.PromptInput) ([]external.Command, error) {
	return d.commands, nil
}

func (d *controlDriver) ReadCommand(context.Context, external.PromptInput) (external.CommandResult, error) {
	d.readCalls++
	return external.CommandResult{Data: map[string]any{"native_field": "cached"}, Notice: "last_observed"}, nil
}

func (d *controlDriver) Compact(context.Context, external.PromptInput) (external.CompactionResult, error) {
	d.compactCalls++
	return external.CompactionResult{RuntimeMetadata: map[string]any{"tokens": 12}}, nil
}

func (*controlDriver) Modes(context.Context, external.PromptInput) (external.ModeState, error) {
	return external.ModeState{Supported: true, AvailableModes: []external.Mode{{ID: "choice"}}, CurrentModeID: "choice"}, nil
}

func (d *controlDriver) SetMode(_ context.Context, _ external.PromptInput, id string) (external.ModeState, error) {
	d.mode = id
	return external.ModeState{Supported: true, CurrentModeID: id}, nil
}

func runtimeControlFixture(t *testing.T) (*Service, *controlQueries, *controlDriver, *scriptedAdmitter, RuntimeControlRequest) {
	t.Helper()
	botID, threadID, actorID := uuid.New(), uuid.New(), uuid.New()
	q := &controlQueries{row: sqlc.BotSession{ID: pgtype.UUID{Bytes: threadID, Valid: true}, BotID: pgtype.UUID{Bytes: botID, Valid: true}, CreatedByUserID: pgtype.UUID{Bytes: actorID, Valid: true}, Type: "chat", RuntimeType: "codex", RuntimeMetadata: []byte(`{"keep":"value"}`)}}
	driver := &controlDriver{commands: []external.Command{{Name: "inspect", Kind: external.CommandRead}, {Name: "shrink", Kind: external.CommandOperation}}}
	admitter := newScriptedAdmitter()
	request := RuntimeControlRequest{BotID: botID.String(), ThreadID: threadID.String(), ActorID: actorID.String()}
	svc := &Service{sessionService: session.NewService(nil, q, nil), botPermissions: &fakeBotPermissionChecker{values: map[string]bool{request.BotID + ":" + request.ActorID + ":" + bots.PermissionWorkspaceExec: true}}, runtimes: testRuntimes(t, driver), sessionRuntime: admitter}
	return svc, q, driver, admitter, request
}

func TestRuntimeControlsRejectOtherActorBeforeDispatch(t *testing.T) {
	svc, _, driver, admitter, req := runtimeControlFixture(t)
	req.ActorID = uuid.NewString()
	req.Command = "shrink"
	_, err := svc.ExecuteRuntimeCommand(t.Context(), req)
	if apperror.CodeOf(err) != apperror.CodeRuntimeControlForbidden || driver.compactCalls != 0 || len(admitter.inputs) != 0 {
		t.Fatalf("unauthorized dispatch: %v", err)
	}
}

func TestRuntimeControlUsesSlotWithoutChatAdmissionAndMergesMetadata(t *testing.T) {
	svc, q, driver, admitter, req := runtimeControlFixture(t)
	req.Command = "shrink"
	if _, err := svc.ExecuteRuntimeCommand(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if driver.compactCalls != 1 || q.writes != 1 || len(admitter.finishes) != 1 {
		t.Fatal("missing operation lifecycle")
	}
	view, err := admitter.inputs[0].Execution.Admission(t.Context(), admitter.finishes[0].handle)
	if err != nil || view.RequestUserTurn != nil || view.Operation != nil {
		t.Fatalf("control created chat admission: %+v, %v", view, err)
	}
	if admitter.inputs[0].Execution.ConfigurationOnly {
		t.Fatal("compaction changed its admission presentation")
	}
	var meta map[string]any
	if err := json.Unmarshal(q.row.RuntimeMetadata, &meta); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(meta, map[string]any{"keep": "value", "tokens": float64(12)}) {
		t.Fatalf("metadata: %v", meta)
	}
}

func TestRuntimeModePersistence(t *testing.T) {
	svc, q, driver, _, req := runtimeControlFixture(t)
	req.ModeID = "choice"
	if _, err := svc.SetRuntimeMode(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if driver.mode != "choice" || q.writes != 1 {
		t.Fatal("mode not persisted")
	}
}

type planControlDriver struct{ *controlDriver }

func (*planControlDriver) PlanMode(context.Context, external.PromptInput) (external.ModeState, error) {
	return external.ModeState{Supported: true, AvailableModes: []external.Mode{{ID: "plan"}, {ID: "default"}}}, nil
}

func (d *planControlDriver) SetPlanMode(ctx context.Context, input external.PromptInput, mode string) (external.ModeState, error) {
	state, err := d.PlanMode(ctx, input)
	state.CurrentModeID = mode
	return state, err
}

func TestPlanModePersistsWithoutReplacingPermission(t *testing.T) {
	for _, mode := range []string{"plan", "default"} {
		svc, q, driver, _, req := runtimeControlFixture(t)
		svc.runtimes = testRuntimes(t, &planControlDriver{driver})
		q.row.RuntimeMetadata = []byte(`{"permission_mode":"yolo","keep":"value"}`)
		req.ModeKind, req.ModeID = "plan", mode
		if _, err := svc.SetRuntimeMode(t.Context(), req); err != nil {
			t.Fatal(err)
		}
		var metadata map[string]any
		if err := json.Unmarshal(q.row.RuntimeMetadata, &metadata); err != nil {
			t.Fatal(err)
		}
		if metadata["permission_mode"] != "yolo" || metadata["collaboration_mode"] != mode || metadata["keep"] != "value" || q.writes != 1 || driver.mode != "" {
			t.Fatalf("mode isolation: %s", q.row.RuntimeMetadata)
		}
	}
}

func TestReadCommandAdmissionMatchesRuntimePolicy(t *testing.T) {
	for _, runtime := range []string{"codex", "grok-build"} {
		for _, busy := range []bool{false, true} {
			t.Run(runtime+"/"+strconv.FormatBool(busy), func(t *testing.T) {
				svc, q, driver, admitter, req := runtimeControlFixture(t)
				q.row.RuntimeType = runtime
				driver.runtimeType = runtime
				svc.runtimes = testRuntimes(t, driver)
				req.Command = "inspect"
				if busy {
					admitter.admitErr = sessionruntime.ErrSessionBusy
				}
				result, err := svc.ExecuteRuntimeCommand(t.Context(), req)
				if runtime == "grok-build" && busy {
					if apperror.CodeOf(err) != apperror.CodeSessionBusy || driver.readCalls != 0 {
						t.Fatalf("busy read dispatched: calls=%d err=%v", driver.readCalls, err)
					}
					return
				}
				if err != nil || driver.readCalls != 1 || !reflect.DeepEqual(result.Data, map[string]any{"native_field": "cached"}) {
					t.Fatalf("read result=%+v calls=%d err=%v", result, driver.readCalls, err)
				}
				if runtime == "grok-build" && len(admitter.finishes) != 1 {
					t.Fatal("exclusive read did not finish admission")
				}
				// The slot keeps the read off a running turn; the read itself is
				// no assistant turn, so clients must not render it as streaming.
				if runtime == "grok-build" && !admitter.inputs[0].Execution.ConfigurationOnly {
					t.Fatal("exclusive read was admitted as an assistant turn")
				}
				if runtime == "codex" && len(admitter.inputs) != 0 {
					t.Fatal("cached read unnecessarily took a slot")
				}
			})
		}
	}
}
