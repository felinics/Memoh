package schedule

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/workdir"
)

const (
	execTestBotID     = "11111111-1111-1111-1111-111111111111"
	execTestSessionID = "22222222-2222-2222-2222-222222222222"
	execTestModelID   = "33333333-3333-3333-3333-333333333333"
	execTestWorkdirID = "44444444-4444-4444-4444-444444444444"
)

// executionQueries fakes the lookups normalizeExecution performs.
type executionQueries struct {
	dbstore.Queries
	sessions map[string]sqlc.BotSession
	models   map[string]sqlc.Model
	// noDefaultChatModel strips the bot's default chat model. Fixtures leave
	// it false because every pre-existing case assumes a bot that has one;
	// dropping it is exactly what makes an explicit model mandatory.
	noDefaultChatModel bool
	settingsMissing    bool
}

func (q *executionQueries) GetSessionByID(_ context.Context, id pgtype.UUID) (sqlc.BotSession, error) {
	sess, ok := q.sessions[id.String()]
	if !ok {
		return sqlc.BotSession{}, pgx.ErrNoRows
	}
	return sess, nil
}

func (q *executionQueries) GetSettingsByBotID(_ context.Context, _ pgtype.UUID) (sqlc.GetSettingsByBotIDRow, error) {
	if q.settingsMissing {
		return sqlc.GetSettingsByBotIDRow{}, pgx.ErrNoRows
	}
	row := sqlc.GetSettingsByBotIDRow{}
	if !q.noDefaultChatModel {
		parsed, err := db.ParseUUID(execTestModelID)
		if err != nil {
			return sqlc.GetSettingsByBotIDRow{}, err
		}
		row.ChatModelID = parsed
	}
	return row, nil
}

func (q *executionQueries) GetModelByID(_ context.Context, id pgtype.UUID) (sqlc.Model, error) {
	model, ok := q.models[id.String()]
	if !ok {
		return sqlc.Model{}, pgx.ErrNoRows
	}
	return model, nil
}

type fakeWorkdirValidator struct {
	workdirs map[string]workdir.Workdir
}

func (f *fakeWorkdirValidator) RequireActive(_ context.Context, _, workdirID string) (workdir.Workdir, error) {
	wd, ok := f.workdirs[workdirID]
	if !ok {
		return workdir.Workdir{}, workdir.ErrWorkdirNotFound
	}
	return wd, nil
}

func mustUUID(t *testing.T, id string) pgtype.UUID {
	t.Helper()
	parsed, err := db.ParseUUID(id)
	if err != nil {
		t.Fatalf("parse uuid %q: %v", id, err)
	}
	return parsed
}

func newExecutionService(t *testing.T, queries *executionQueries, workdirs WorkdirValidator) *Service {
	t.Helper()
	return &Service{
		queries:  queries,
		workdirs: workdirs,
		logger:   slog.Default(),
	}
}

func chatSession(t *testing.T, botID, runtimeType, legacyType, mode string) sqlc.BotSession {
	t.Helper()
	return sqlc.BotSession{
		ID:          mustUUID(t, execTestSessionID),
		BotID:       mustUUID(t, botID),
		Type:        legacyType,
		SessionMode: mode,
		RuntimeType: runtimeType,
	}
}

func TestNormalizeExecutionDefaults(t *testing.T) {
	svc := newExecutionService(t, &executionQueries{}, nil)
	exec, err := svc.normalizeExecution(context.Background(), execTestBotID, ExecutionConfig{})
	if err != nil {
		t.Fatalf("normalizeExecution() error = %v", err)
	}
	if exec.RunTarget != RunTargetNewSession {
		t.Fatalf("RunTarget = %q, want %q", exec.RunTarget, RunTargetNewSession)
	}
}

func TestNormalizeExecutionRejectsUnknownRunTarget(t *testing.T) {
	svc := newExecutionService(t, &executionQueries{}, nil)
	if _, err := svc.normalizeExecution(context.Background(), execTestBotID, ExecutionConfig{RunTarget: "sometimes"}); err == nil {
		t.Fatal("expected error for unknown run_target")
	}
}

func TestNormalizeExecutionExistingSessionRequiresTarget(t *testing.T) {
	svc := newExecutionService(t, &executionQueries{}, nil)
	_, err := svc.normalizeExecution(context.Background(), execTestBotID, ExecutionConfig{RunTarget: RunTargetExistingSession})
	if !errors.Is(err, ErrTargetSessionRequired) {
		t.Fatalf("error = %v, want ErrTargetSessionRequired", err)
	}
}

func TestNormalizeExecutionExistingSessionInheritsRuntimeAndWorkdir(t *testing.T) {
	queries := &executionQueries{sessions: map[string]sqlc.BotSession{
		execTestSessionID: chatSession(t, execTestBotID, "model", "chat", "chat"),
	}}
	svc := newExecutionService(t, queries, nil)
	for _, exec := range []ExecutionConfig{
		{RunTarget: RunTargetExistingSession, TargetSessionID: execTestSessionID, RuntimeType: RuntimeModel},
		{RunTarget: RunTargetExistingSession, TargetSessionID: execTestSessionID, ACPAgentID: "codex"},
		{RunTarget: RunTargetExistingSession, TargetSessionID: execTestSessionID, WorkdirID: execTestWorkdirID},
	} {
		if _, err := svc.normalizeExecution(context.Background(), execTestBotID, exec); err == nil {
			t.Fatalf("expected inheritance violation error for %+v", exec)
		}
	}
}

func TestNormalizeExecutionExistingSessionModelColumnMatchesRuntime(t *testing.T) {
	native := chatSession(t, execTestBotID, "model", "chat", "chat")
	acp := chatSession(t, execTestBotID, "acp_agent", "acp_agent", "chat")

	t.Run("native target rejects acp_model_id", func(t *testing.T) {
		queries := &executionQueries{sessions: map[string]sqlc.BotSession{execTestSessionID: native}}
		svc := newExecutionService(t, queries, nil)
		_, err := svc.normalizeExecution(context.Background(), execTestBotID, ExecutionConfig{
			RunTarget: RunTargetExistingSession, TargetSessionID: execTestSessionID, ACPModelID: "gpt-5-codex",
		})
		if err == nil || !strings.Contains(err.Error(), "model_id") {
			t.Fatalf("error = %v, want native/acp model column mismatch", err)
		}
	})

	t.Run("acp target rejects model_id", func(t *testing.T) {
		queries := &executionQueries{sessions: map[string]sqlc.BotSession{execTestSessionID: acp}}
		svc := newExecutionService(t, queries, nil)
		_, err := svc.normalizeExecution(context.Background(), execTestBotID, ExecutionConfig{
			RunTarget: RunTargetExistingSession, TargetSessionID: execTestSessionID, ModelID: execTestModelID,
		})
		if err == nil || !strings.Contains(err.Error(), "acp_model_id") {
			t.Fatalf("error = %v, want ACP model column mismatch", err)
		}
	})

	t.Run("acp target accepts acp_model_id and free-form effort", func(t *testing.T) {
		queries := &executionQueries{sessions: map[string]sqlc.BotSession{execTestSessionID: acp}}
		svc := newExecutionService(t, queries, nil)
		exec, err := svc.normalizeExecution(context.Background(), execTestBotID, ExecutionConfig{
			RunTarget: RunTargetExistingSession, TargetSessionID: execTestSessionID,
			ACPModelID: "gpt-5-codex", ReasoningEffort: "xhigh",
		})
		if err != nil {
			t.Fatalf("normalizeExecution() error = %v", err)
		}
		if exec.ACPModelID != "gpt-5-codex" || exec.ReasoningEffort != "xhigh" {
			t.Fatalf("unexpected normalized exec: %+v", exec)
		}
	})
}

func TestNormalizeExecutionExistingSessionRejectsForeignAndInternalSessions(t *testing.T) {
	t.Run("session of another bot", func(t *testing.T) {
		queries := &executionQueries{sessions: map[string]sqlc.BotSession{
			execTestSessionID: chatSession(t, "99999999-9999-9999-9999-999999999999", "model", "chat", "chat"),
		}}
		svc := newExecutionService(t, queries, nil)
		_, err := svc.normalizeExecution(context.Background(), execTestBotID, ExecutionConfig{
			RunTarget: RunTargetExistingSession, TargetSessionID: execTestSessionID,
		})
		if !errors.Is(err, ErrTargetSessionNotFound) {
			t.Fatalf("error = %v, want ErrTargetSessionNotFound", err)
		}
	})
}

func TestNormalizeExecutionNewSessionRules(t *testing.T) {
	svc := newExecutionService(t, &executionQueries{}, nil)
	ctx := context.Background()

	if _, err := svc.normalizeExecution(ctx, execTestBotID, ExecutionConfig{TargetSessionID: execTestSessionID}); err == nil {
		t.Fatal("expected error: target_session_id with new_session")
	}
	if _, err := svc.normalizeExecution(ctx, execTestBotID, ExecutionConfig{RuntimeType: RuntimeACPAgent}); err == nil {
		t.Fatal("expected error: acp_agent runtime without agent id")
	}
	if _, err := svc.normalizeExecution(ctx, execTestBotID, ExecutionConfig{RuntimeType: RuntimeACPAgent, ACPAgentID: "codex", ModelID: execTestModelID}); err == nil {
		t.Fatal("expected error: acp runtime with native model_id")
	}
	if _, err := svc.normalizeExecution(ctx, execTestBotID, ExecutionConfig{ACPAgentID: "codex"}); err == nil {
		t.Fatal("expected error: acp fields without acp runtime")
	}
	if _, err := svc.normalizeExecution(ctx, execTestBotID, ExecutionConfig{ACPModelID: "gpt-5-codex"}); err == nil {
		t.Fatal("expected error: acp model without acp runtime")
	}
	if _, err := svc.normalizeExecution(ctx, execTestBotID, ExecutionConfig{ModelID: execTestModelID, ACPModelID: "gpt-5-codex", RuntimeType: RuntimeACPAgent, ACPAgentID: "codex"}); err == nil {
		t.Fatal("expected error: both model columns set")
	}
	if _, err := svc.normalizeExecution(ctx, execTestBotID, ExecutionConfig{ReasoningEffort: "extreme"}); err == nil {
		t.Fatal("expected error: unknown native reasoning effort")
	}
	// "disable" is not a wire tier, so it is absent from the tier list, but it
	// is the stored on/off value everywhere else reasoning is configured.
	disabled, err := svc.normalizeExecution(ctx, execTestBotID, ExecutionConfig{ReasoningEffort: "disable"})
	if err != nil {
		t.Fatalf("normalizeExecution() error = %v", err)
	}
	if disabled.ReasoningEffort != "disable" {
		t.Fatalf("unexpected normalized exec: %+v", disabled)
	}

	exec, err := svc.normalizeExecution(ctx, execTestBotID, ExecutionConfig{RuntimeType: RuntimeACPAgent, ACPAgentID: "codex", ACPModelID: "gpt-5-codex", ReasoningEffort: "anything-agent-defined"})
	if err != nil {
		t.Fatalf("normalizeExecution() error = %v", err)
	}
	if exec.ACPAgentID != "codex" {
		t.Fatalf("unexpected normalized exec: %+v", exec)
	}
}

func TestNormalizeExecutionValidatesNativeModel(t *testing.T) {
	ctx := context.Background()

	t.Run("missing model", func(t *testing.T) {
		svc := newExecutionService(t, &executionQueries{models: map[string]sqlc.Model{}}, nil)
		if _, err := svc.normalizeExecution(ctx, execTestBotID, ExecutionConfig{ModelID: execTestModelID}); err == nil {
			t.Fatal("expected error for unknown model")
		}
	})

	t.Run("non-chat model", func(t *testing.T) {
		svc := newExecutionService(t, &executionQueries{models: map[string]sqlc.Model{
			execTestModelID: {ID: mustUUID(t, execTestModelID), Type: "embedding", Enable: true},
		}}, nil)
		if _, err := svc.normalizeExecution(ctx, execTestBotID, ExecutionConfig{ModelID: execTestModelID}); err == nil {
			t.Fatal("expected error for embedding model")
		}
	})

	t.Run("disabled model", func(t *testing.T) {
		svc := newExecutionService(t, &executionQueries{models: map[string]sqlc.Model{
			execTestModelID: {ID: mustUUID(t, execTestModelID), Type: "chat", Enable: false},
		}}, nil)
		if _, err := svc.normalizeExecution(ctx, execTestBotID, ExecutionConfig{ModelID: execTestModelID}); err == nil {
			t.Fatal("expected error for disabled model")
		}
	})

	t.Run("valid chat model with tier effort", func(t *testing.T) {
		svc := newExecutionService(t, &executionQueries{models: map[string]sqlc.Model{
			execTestModelID: {ID: mustUUID(t, execTestModelID), Type: "chat", Enable: true},
		}}, nil)
		exec, err := svc.normalizeExecution(ctx, execTestBotID, ExecutionConfig{ModelID: execTestModelID, ReasoningEffort: "high"})
		if err != nil {
			t.Fatalf("normalizeExecution() error = %v", err)
		}
		if exec.ModelID != execTestModelID || exec.ReasoningEffort != "high" {
			t.Fatalf("unexpected normalized exec: %+v", exec)
		}
	})
}

func TestNormalizeExecutionWorkdirRules(t *testing.T) {
	ctx := context.Background()
	native := &fakeWorkdirValidator{workdirs: map[string]workdir.Workdir{
		execTestWorkdirID: {ID: execTestWorkdirID, TargetKind: workdir.TargetKindNative, Path: "/data/project"},
	}}
	remote := &fakeWorkdirValidator{workdirs: map[string]workdir.Workdir{
		execTestWorkdirID: {ID: execTestWorkdirID, TargetKind: workdir.TargetKindRemote, Path: "/home/user/project"},
	}}

	t.Run("native workdir binds", func(t *testing.T) {
		svc := newExecutionService(t, &executionQueries{}, native)
		exec, err := svc.normalizeExecution(ctx, execTestBotID, ExecutionConfig{WorkdirID: execTestWorkdirID})
		if err != nil {
			t.Fatalf("normalizeExecution() error = %v", err)
		}
		if exec.WorkdirID != execTestWorkdirID {
			t.Fatalf("unexpected normalized exec: %+v", exec)
		}
	})

	t.Run("acp runtime rejects remote workdir", func(t *testing.T) {
		svc := newExecutionService(t, &executionQueries{}, remote)
		_, err := svc.normalizeExecution(ctx, execTestBotID, ExecutionConfig{
			RuntimeType: RuntimeACPAgent, ACPAgentID: "codex", WorkdirID: execTestWorkdirID,
		})
		if err == nil || !strings.Contains(err.Error(), "remote") {
			t.Fatalf("error = %v, want remote workdir rejection", err)
		}
	})

	t.Run("unknown workdir", func(t *testing.T) {
		svc := newExecutionService(t, &executionQueries{}, &fakeWorkdirValidator{})
		if _, err := svc.normalizeExecution(ctx, execTestBotID, ExecutionConfig{WorkdirID: execTestWorkdirID}); err == nil {
			t.Fatal("expected error for unknown workdir")
		}
	})
}

func TestNormalizeExecutionRequiresModelWithoutBotDefault(t *testing.T) {
	// A fresh session per fire has no previous round to inherit a model from,
	// so with no bot default and no explicit model every fire would die at
	// run time. Catch it while the user can still fix it.
	t.Run("new session rejects an empty model", func(t *testing.T) {
		svc := newExecutionService(t, &executionQueries{noDefaultChatModel: true}, nil)
		_, err := svc.normalizeExecution(context.Background(), execTestBotID, ExecutionConfig{})
		if !errors.Is(err, ErrModelRequired) {
			t.Fatalf("error = %v, want ErrModelRequired", err)
		}
	})

	t.Run("a bot with no settings row at all is the same case", func(t *testing.T) {
		svc := newExecutionService(t, &executionQueries{settingsMissing: true}, nil)
		_, err := svc.normalizeExecution(context.Background(), execTestBotID, ExecutionConfig{})
		if !errors.Is(err, ErrModelRequired) {
			t.Fatalf("error = %v, want ErrModelRequired", err)
		}
	})

	t.Run("an explicit model satisfies it", func(t *testing.T) {
		queries := &executionQueries{
			noDefaultChatModel: true,
			models: map[string]sqlc.Model{
				mustUUID(t, execTestModelID).String(): {Type: "chat", Enable: true},
			},
		}
		svc := newExecutionService(t, queries, nil)
		exec, err := svc.normalizeExecution(context.Background(), execTestBotID, ExecutionConfig{ModelID: execTestModelID})
		if err != nil {
			t.Fatalf("normalizeExecution() error = %v", err)
		}
		if exec.ModelID != execTestModelID {
			t.Fatalf("ModelID = %q, want %q", exec.ModelID, execTestModelID)
		}
	})

	// An ACP agent brings its own model, so the bot default is irrelevant.
	t.Run("acp runtime needs no native model", func(t *testing.T) {
		svc := newExecutionService(t, &executionQueries{noDefaultChatModel: true}, nil)
		if _, err := svc.normalizeExecution(context.Background(), execTestBotID, ExecutionConfig{
			RuntimeType: RuntimeACPAgent, ACPAgentID: "codex",
		}); err != nil {
			t.Fatalf("normalizeExecution() error = %v", err)
		}
	})

	// An existing session can still fall back to the model that produced its
	// latest round, so it is not blocked here.
	t.Run("existing session is left alone", func(t *testing.T) {
		queries := &executionQueries{
			noDefaultChatModel: true,
			sessions: map[string]sqlc.BotSession{
				execTestSessionID: chatSession(t, execTestBotID, "model", "chat", "chat"),
			},
		}
		svc := newExecutionService(t, queries, nil)
		if _, err := svc.normalizeExecution(context.Background(), execTestBotID, ExecutionConfig{
			RunTarget: RunTargetExistingSession, TargetSessionID: execTestSessionID,
		}); err != nil {
			t.Fatalf("normalizeExecution() error = %v", err)
		}
	})
}
