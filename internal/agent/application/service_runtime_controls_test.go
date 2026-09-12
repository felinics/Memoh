package application

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/agent/runtime/external"
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
	commands     []external.Command
	compactCalls int
	mode         string
}

func (*controlDriver) RuntimeType() string { return "codex" }
func (*controlDriver) Prompt(context.Context, external.PromptInput) (external.PromptResult, error) {
	panic("control must not start chat")
}

func (d *controlDriver) Commands(context.Context, external.PromptInput) ([]external.Command, error) {
	return d.commands, nil
}

func (*controlDriver) ReadCommand(context.Context, external.PromptInput) (string, error) {
	return "cached", nil
}

func (d *controlDriver) Compact(context.Context, external.PromptInput) (map[string]any, error) {
	d.compactCalls++
	return map[string]any{"tokens": 12}, nil
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
	svc := &Service{sessionService: session.NewService(nil, q, nil), botPermissions: &fakeBotPermissionChecker{values: map[string]bool{request.BotID + ":" + request.ActorID + ":" + bots.PermissionWorkspaceExec: true}}, externalDrivers: map[string]external.Driver{"codex": driver}, sessionRuntime: admitter}
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
		svc.externalDrivers["codex"] = &planControlDriver{driver}
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
