package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"unicode/utf8"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	tools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/agent/toolexec"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/botagents"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/workdir"
)

type subagentCatalogStub struct{ enabled bool }

func (c *subagentCatalogStub) List(context.Context, string) ([]botagents.BotAgent, error) {
	return []botagents.BotAgent{{ID: "backend", Name: "worker", Runtime: "codex", Enabled: c.enabled}}, nil
}

func TestExternalSubagentRejectsRemoteParentWorkspace(t *testing.T) {
	for _, bound := range []bool{false, true} {
		t.Run(map[bool]string{false: "turn target", true: "bound workdir"}[bound], func(t *testing.T) {
			s := newACPLifecycleService(t, &recordingACPPrompter{}, &recordingMessageService{}, &recordingContextLifecycleStore{})
			s.SetSubagentBackends(&subagentCatalogStub{enabled: true})
			s.SetExternalRuntimes(noticeTestDriver{kind: "codex"})
			parent := session.Thread{ID: "parent", BotID: lifecycleTestBotID, Type: session.TypeChat, SessionMode: session.TypeChat}
			input := tools.SessionContext{SessionID: "parent", BotID: lifecycleTestBotID, UserID: "user-1", WorkspaceTargetKind: "remote", WorkspaceTargetID: "computer"}
			if bound {
				parent.WorkdirID = "workdir"
				input.WorkspaceTargetKind = ""
				input.WorkspaceTargetID = ""
				s.SetWorkdirResolver(fakeSessionWorkdirResolver{resolved: workdir.Resolved{Kind: workdir.TargetKindRemote, WorkDir: "/remote/repo"}})
			}
			s.sessionService = sessionWorkdirSessionService{thread: parent}
			_, err := s.PrepareExternalSubagent(t.Context(), input, "backend")
			if apperror.CodeOf(err) != apperror.CodeExternalAgentContainerWorkspaceRequired {
				t.Fatalf("wrong-workspace delegation allowed: %v", err)
			}
		})
	}
}

func TestExternalSubagentRunRejectsRemoteWorkspace(t *testing.T) {
	for _, target := range []string{"native", "turn target", "bound workdir"} {
		t.Run(target, func(t *testing.T) {
			s := newACPLifecycleService(t, &recordingACPPrompter{}, &recordingMessageService{}, &recordingContextLifecycleStore{})
			s.SetSubagentBackends(&subagentCatalogStub{enabled: true})
			calls := 0
			s.SetExternalRuntimes(noticeTestDriver{kind: "codex", prompt: func(context.Context, external.PromptInput) (external.PromptResult, error) {
				calls++
				return external.PromptResult{TurnCompleted: true}, nil
			}})
			parent := tools.SessionContext{BotID: lifecycleTestBotID, SessionID: "parent", UserID: "user-1"}
			child := session.Thread{ID: lifecycleTestSessionID, BotID: lifecycleTestBotID, Type: session.TypeSubagent, SessionMode: session.TypeSubagent, RuntimeType: "codex", BotAgentID: "backend", ParentThreadID: "parent", RuntimeMetadata: map[string]any{"runtime_owner_account_id": "user-1", "project_path": "/data/work"}}
			switch target {
			case "turn target":
				parent.WorkspaceTargetKind, parent.WorkspaceTargetID = "remote", "computer"
			case "bound workdir":
				child.WorkdirID = "workdir"
				s.SetWorkdirResolver(fakeSessionWorkdirResolver{resolved: workdir.Resolved{Kind: workdir.TargetKindRemote, WorkDir: "/remote/repo"}})
			}
			s.sessionService = &fakeBackgroundSessionService{getFn: func(_ context.Context, id string) (session.Thread, error) {
				if id == "parent" {
					return session.Thread{ID: id, BotID: lifecycleTestBotID, Type: session.TypeChat}, nil
				}
				return child, nil
			}, mergeRuntimeMetadataFn: func(_ context.Context, _, _ string, delta map[string]any) (session.Thread, error) {
				for key, value := range delta {
					if value == nil {
						delete(child.RuntimeMetadata, key)
					} else {
						child.RuntimeMetadata[key] = value
					}
				}
				return child, nil
			}}
			handle := sessionruntime.RunHandle{BotID: lifecycleTestBotID, SessionID: lifecycleTestSessionID, RunID: lifecycleTestRunID, FencingToken: 1}
			_, err := s.RunExternalSubagent(withSubagentRunHandle(context.Background(), handle), tools.ExternalSubagentRun{Parent: parent, SessionID: lifecycleTestSessionID, RuntimeType: "codex", BotAgentID: "backend", Query: "inspect", Admission: tools.SubagentAdmission{RunID: lifecycleTestRunID, TurnID: lifecycleTestRunID}})
			if target == "native" {
				if err != nil || calls != 1 {
					t.Fatalf("native run did not reach the runtime: calls=%d err=%v", calls, err)
				}
				return
			}
			if apperror.CodeOf(err) != apperror.CodeExternalAgentContainerWorkspaceRequired || calls != 0 {
				t.Fatalf("wrong-workspace run allowed: calls=%d err=%v", calls, err)
			}
		})
	}
}

func (c *subagentCatalogStub) GetActive(_ context.Context, botID, id string) (botagents.BotAgent, error) {
	if !c.enabled || id != "backend" {
		return botagents.BotAgent{}, botagents.ErrUnavailable
	}
	return botagents.BotAgent{ID: id, BotID: botID, Runtime: "codex", Enabled: true}, nil
}

func (*subagentCatalogStub) ValidateConfiguration(context.Context, botagents.BotAgent, map[string]any) error {
	return nil
}

func TestExternalSubagentRunPersistsOneTurnAndPreservesPartialFailure(t *testing.T) {
	for _, tc := range []struct {
		name     string
		failed   bool
		report   string
		progress string
	}{
		{"complete", false, "saved result", ""},
		{"partial failure", true, "saved result", ""},
		{"bounded report", false, strings.Repeat("结果", 8192), ""},
		{"final report after progress", false, "verification complete", strings.Repeat("progress", 4096)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messages := &recordingMessageService{}
			s := newACPLifecycleService(t, &recordingACPPrompter{}, messages, &recordingContextLifecycleStore{})
			catalog := &subagentCatalogStub{enabled: true}
			s.SetSubagentBackends(catalog)
			var metadata map[string]any
			s.sessionService = &fakeBackgroundSessionService{getFn: func(_ context.Context, id string) (session.Thread, error) {
				if id == "parent" {
					return session.Thread{ID: id, BotID: lifecycleTestBotID, Type: session.TypeChat}, nil
				}
				return session.Thread{ID: id, BotID: lifecycleTestBotID, Type: session.TypeSubagent, SessionMode: session.TypeSubagent, RuntimeType: "codex", BotAgentID: "backend", ParentThreadID: "parent", RuntimeMetadata: map[string]any{"runtime_owner_account_id": "user-1", "project_path": "/data/work"}}, nil
			}, mergeRuntimeMetadataFn: func(_ context.Context, id, _ string, delta map[string]any) (session.Thread, error) {
				metadata = delta
				return session.Thread{ID: id}, nil
			}}
			calls := 0
			s.SetExternalRuntimes(noticeTestDriver{kind: "codex", prompt: func(_ context.Context, in external.PromptInput) (external.PromptResult, error) {
				calls++
				if in.ActorUserID != "user-1" {
					t.Errorf("caller identity lost: %q", in.ActorUserID)
				}
				if in.ThreadID != lifecycleTestSessionID || in.RunID != lifecycleTestRunID || in.SessionMode != session.TypeSubagent || in.BotAgentID != "backend" {
					t.Errorf("wrong child identity: %+v", in)
				}
				result := external.PromptResult{Text: tc.report, Output: []sdk.Message{sdk.AssistantMessage(tc.report)}, TurnCompleted: !tc.failed, AgentTurnID: "native-turn", RuntimeMetadata: map[string]any{"codex_thread_id": "native-child"}}
				if tc.progress != "" {
					result.Output = append([]sdk.Message{sdk.AssistantMessage(tc.progress)}, result.Output...)
				}
				if tc.failed {
					return result, errors.New("private driver failure")
				}
				return result, nil
			}})
			handle := sessionruntime.RunHandle{BotID: lifecycleTestBotID, SessionID: lifecycleTestSessionID, RunID: lifecycleTestRunID, FencingToken: 1}
			ctx := withSubagentRunHandle(context.Background(), handle)
			result, err := s.RunExternalSubagent(ctx, tools.ExternalSubagentRun{Parent: tools.SessionContext{BotID: lifecycleTestBotID, SessionID: "parent", UserID: "user-1"}, SessionID: lifecycleTestSessionID, RuntimeType: "codex", BotAgentID: "backend", Query: "inspect", Admission: tools.SubagentAdmission{RunID: lifecycleTestRunID, TurnID: lifecycleTestRunID}})
			if (err != nil) != tc.failed {
				t.Fatalf("err=%v failed=%v", err, tc.failed)
			}
			if calls != 1 || result == nil || !result.Persisted || metadata["codex_thread_id"] != "native-child" {
				t.Fatalf("calls=%d result=%+v metadata=%v", calls, result, metadata)
			}
			if len(result.Text) > 16*1024 || !utf8.ValidString(result.Text) {
				t.Fatalf("invalid report: bytes=%d", len(result.Text))
			}
			if len(tc.report) <= 16*1024 && result.Text != tc.report {
				t.Fatalf("report=%q", result.Text)
			}
			if len(tc.report) > 16*1024 && !strings.Contains(result.Text, "Full transcript") {
				t.Fatal("missing truncation notice")
			}
			users := 0
			fullReportSaved := false
			for _, input := range messages.persisted {
				if input.Role == "assistant" && strings.Contains(string(input.Content), tc.report) {
					fullReportSaved = true
				}
				if input.Role == "user" {
					users++
				}
				if input.SessionID != lifecycleTestSessionID {
					t.Errorf("wrong persisted session: %s", input.SessionID)
				}
			}
			if users != 1 {
				t.Fatalf("user messages=%d", users)
			}
			if !fullReportSaved {
				t.Fatal("full report was not persisted")
			}
		})
	}
}

type failingSubagentCatalog struct{ subagentCatalogStub }

func (*failingSubagentCatalog) List(context.Context, string) ([]botagents.BotAgent, error) {
	return nil, errors.New("PRIVATE_DIAGNOSTIC_CANARY")
}

type unusedSpawnAgent struct{}

func (unusedSpawnAgent) Generate(context.Context, tools.SpawnRunConfig) (*tools.SpawnResult, error) {
	panic("unused")
}

func (unusedSpawnAgent) GenerateWithWatchdog(context.Context, tools.SpawnRunConfig, func()) (*tools.SpawnResult, error) {
	panic("unused")
}

func TestExternalSubagentToolErrorsKeepDiagnosticsPrivate(t *testing.T) {
	t.Run("list backends", func(t *testing.T) {
		s := newACPLifecycleService(t, &recordingACPPrompter{}, &recordingMessageService{}, &recordingContextLifecycleStore{})
		s.SetSubagentBackends(&failingSubagentCatalog{})
		p := tools.NewSpawnProvider(nil, nil, nil, nil, nil, nil)
		p.SetAgent(unusedSpawnAgent{})
		p.SetExternalSubagents(s)
		list, err := p.Tools(t.Context(), tools.SessionContext{BotID: lifecycleTestBotID, SessionID: "parent", UserID: "user-1"})
		if err != nil {
			t.Fatal(err)
		}
		out, err := toolexec.ExecuteTools(t.Context(), []sdk.ToolCall{{ToolCallID: "call", ToolName: tools.ToolListAgentBackends().String(), Input: sdk.ParseToolArguments(`{}`)}}, toolexec.ToolExecOptions{Tools: list})
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(out.Results)
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Results) != 1 || !out.Results[0].IsError || strings.Contains(string(encoded), "PRIVATE_DIAGNOSTIC_CANARY") || !strings.Contains(string(encoded), string(apperror.CodeInternal)) {
			t.Fatalf("tool result=%s", encoded)
		}
	})
	t.Run("prepare child", func(t *testing.T) {
		s := newACPLifecycleService(t, &recordingACPPrompter{}, &recordingMessageService{}, &recordingContextLifecycleStore{})
		s.SetSubagentBackends(&subagentCatalogStub{enabled: true})
		s.SetExternalRuntimes(noticeTestDriver{kind: "codex"})
		s.sessionService = &fakeBackgroundSessionService{getFn: func(context.Context, string) (session.Thread, error) {
			return session.Thread{}, errors.New("PRIVATE_DIAGNOSTIC_CANARY")
		}}
		_, err := s.PrepareExternalSubagent(t.Context(), tools.SessionContext{BotID: lifecycleTestBotID, SessionID: "parent", UserID: "user-1"}, "backend")
		if err == nil || strings.Contains(err.Error(), "PRIVATE_DIAGNOSTIC_CANARY") || apperror.CodeOf(err) != apperror.CodeInternal {
			t.Fatalf("prepare error=%v", err)
		}
	})
	t.Run("child state and control", func(t *testing.T) {
		s := newACPLifecycleService(t, &recordingACPPrompter{}, &recordingMessageService{}, &recordingContextLifecycleStore{})
		s.sessionService = &fakeBackgroundSessionService{getFn: func(context.Context, string) (session.Thread, error) {
			return session.Thread{}, errors.New("PRIVATE_DIAGNOSTIC_CANARY")
		}}
		parent := tools.SessionContext{BotID: lifecycleTestBotID, SessionID: "parent", UserID: "user-1"}
		_, snapshotErr := s.SnapshotSubagent(t.Context(), parent, "child")
		_, controlErr := s.ControlSubagent(t.Context(), tools.SubagentControlInput{Parent: parent, SessionID: "child", Action: "stop"})
		for _, err := range []error{snapshotErr, controlErr} {
			if err == nil || strings.Contains(err.Error(), "PRIVATE_DIAGNOSTIC_CANARY") || apperror.CodeOf(err) != apperror.CodeInternal {
				t.Fatalf("child error=%v", err)
			}
		}
	})
	t.Run("public code passes through", func(t *testing.T) {
		s := newACPLifecycleService(t, &recordingACPPrompter{}, &recordingMessageService{}, &recordingContextLifecycleStore{})
		s.SetSubagentBackends(&subagentCatalogStub{enabled: false})
		s.SetExternalRuntimes(noticeTestDriver{kind: "codex"})
		s.sessionService = sessionWorkdirSessionService{thread: session.Thread{ID: "parent", BotID: lifecycleTestBotID, Type: session.TypeChat, SessionMode: session.TypeChat}}
		_, err := s.PrepareExternalSubagent(t.Context(), tools.SessionContext{BotID: lifecycleTestBotID, SessionID: "parent", UserID: "user-1"}, "backend")
		if apperror.CodeOf(err) != apperror.CodeBotAgentUnavailable {
			t.Fatalf("prepare error=%v", err)
		}
	})
}

type failingBotQueries struct{ modelSelectionFakeQueries }

func (*failingBotQueries) GetBotByID(context.Context, pgtype.UUID) (sqlc.GetBotByIDRow, error) {
	return sqlc.GetBotByIDRow{}, errors.New("PRIVATE_DIAGNOSTIC_CANARY")
}

func TestExternalSubagentAvailabilityFaultsAreLogged(t *testing.T) {
	t.Run("backend list", func(t *testing.T) {
		var logs bytes.Buffer
		s := newACPLifecycleService(t, &recordingACPPrompter{}, &recordingMessageService{}, &recordingContextLifecycleStore{})
		s.logger = slog.New(slog.NewTextHandler(&logs, nil))
		s.queries = &failingBotQueries{}
		s.SetSubagentBackends(&subagentCatalogStub{enabled: true})
		s.SetExternalRuntimes(noticeTestDriver{kind: "codex"})
		s.sessionService = sessionWorkdirSessionService{thread: session.Thread{ID: "parent", BotID: lifecycleTestBotID, Type: session.TypeChat, SessionMode: session.TypeChat}}
		items, err := s.ListSubagentBackends(t.Context(), tools.SessionContext{BotID: lifecycleTestBotID, SessionID: "parent", UserID: "user-1"})
		if err != nil || len(items) != 1 || items[0].Available || items[0].Code != string(apperror.CodeInternal) {
			t.Fatalf("items=%+v err=%v", items, err)
		}
		if !strings.Contains(logs.String(), "PRIVATE_DIAGNOSTIC_CANARY") {
			t.Fatalf("backend fault was not logged: %s", logs.String())
		}
	})
	t.Run("child state", func(t *testing.T) {
		var logs bytes.Buffer
		f := newReapTestFixture(t)
		f.mu.Lock()
		delete(f.meta, "external_execution_run")
		delete(f.meta, "external_termination_unconfirmed")
		f.mu.Unlock()
		f.svc.logger = slog.New(slog.NewTextHandler(&logs, nil))
		f.svc.queries = &failingBotQueries{}
		state, err := f.svc.SnapshotSubagent(t.Context(), tools.SessionContext{BotID: lifecycleTestBotID, SessionID: "parent", UserID: "user-1"}, lifecycleTestSessionID)
		if err != nil || state.Code != string(apperror.CodeInternal) {
			t.Fatalf("state=%+v err=%v", state, err)
		}
		if !strings.Contains(logs.String(), "PRIVATE_DIAGNOSTIC_CANARY") {
			t.Fatalf("child availability fault was not logged: %s", logs.String())
		}
	})
}
