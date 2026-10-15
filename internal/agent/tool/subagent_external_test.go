package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/felinics/memoh/internal/apperror"
	sessionpkg "github.com/felinics/memoh/internal/chat/thread"
)

type externalSubagentStub struct {
	executionUnconfirmed bool
	runs                 []ExternalSubagentRun
	err                  error
	mu                   sync.Mutex
	held                 map[string]bool
	settling             chan struct{}
	running              chan struct{}
}

func (s *externalSubagentStub) SettleExternalSubagents(ctx context.Context, _ string) error {
	if s.settling == nil {
		return nil
	}
	select {
	case <-s.settling:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *externalSubagentStub) HeldExternalSubagents(context.Context, string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.held))
	for id := range s.held {
		ids = append(ids, id)
	}
	return ids, nil
}

func (s *externalSubagentStub) resolveHeld() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.held = nil
}

func (*externalSubagentStub) ListSubagentBackends(context.Context, SessionContext) ([]SubagentBackend, error) {
	return []SubagentBackend{{BotAgentID: "external-1", Name: "worker", RuntimeType: "codex", Available: true}}, nil
}

func (*externalSubagentStub) PrepareExternalSubagent(_ context.Context, parent SessionContext, id string) (sessionpkg.CreateInput, error) {
	if id != "external-1" {
		return sessionpkg.CreateInput{}, errors.New("unavailable instance")
	}
	return sessionpkg.CreateInput{BotID: parent.BotID, ParentThreadID: parent.SessionID, RuntimeType: "codex", BotAgentID: id, RuntimeMetadata: map[string]any{"runtime_owner_account_id": "owner", "project_path": "/data/work"}}, nil
}

func (s *externalSubagentStub) RunExternalSubagent(_ context.Context, run ExternalSubagentRun) (*SpawnResult, error) {
	s.mu.Lock()
	s.runs = append(s.runs, run)
	if s.executionUnconfirmed {
		if s.held == nil {
			s.held = map[string]bool{}
		}
		s.held[run.SessionID] = true
	}
	running := s.running
	s.mu.Unlock()
	if running != nil {
		<-running
	}
	return &SpawnResult{Text: "external result", Persisted: true, ExecutionUnconfirmed: s.executionUnconfirmed}, s.err
}

func TestExternalSubagentRejectsNativeOptionsBeforeCreation(t *testing.T) {
	for _, option := range []map[string]any{{"model_id": "model"}, {"provider": "provider"}, {"fork": true}} {
		p, _, sessions, _ := newAgentControlProvider(t, &fakeSpawnAgent{})
		p.SetExternalSubagents(&externalSubagentStub{})
		option["bot_agent_id"] = "external-1"
		option["task"] = "work"
		_, err := executeAgentTool(t, p, SessionContext{BotID: "bot-1", SessionID: "parent"}, "spawn_agent", option)
		if err == nil || len(sessions.sessions) != 0 {
			t.Fatalf("options=%v err=%v sessions=%v", option, err, sessions.sessions)
		}
	}
}

func TestExternalSubagentFailureKeepsPartialResultWithoutRetry(t *testing.T) {
	p, _, _, _ := newAgentControlProvider(t, &fakeSpawnAgent{})
	external := &externalSubagentStub{err: ErrWatchdogTimedOut}
	p.SetExternalSubagents(external)
	value := mustExecuteAgentTool(t, p, SessionContext{BotID: "bot-1", SessionID: "parent"}, "spawn_agent", map[string]any{"bot_agent_id": "external-1", "task": "work"})
	result := asMap(t, value)
	if len(external.runs) != 1 || result["status"] != "failed" || result["text"] != "external result" {
		t.Fatalf("result=%v runs=%d", result, len(external.runs))
	}
}

func TestExternalSubagentCatalogAndBindingAreVisible(t *testing.T) {
	p, _, _, _ := newAgentControlProvider(t, &fakeSpawnAgent{})
	p.SetExternalSubagents(&externalSubagentStub{})
	parent := SessionContext{BotID: "bot-1", SessionID: "parent", UserID: "owner"}
	catalog := asMap(t, mustExecuteAgentTool(t, p, parent, "list_agent_backends", map[string]any{}))
	if catalog["count"] != float64(1) {
		t.Fatalf("catalog=%v", catalog)
	}
	mustExecuteAgentTool(t, p, parent, "spawn_agent", map[string]any{"id": "worker", "bot_agent_id": "external-1", "task": "work"})
	listed := asMap(t, mustExecuteAgentTool(t, p, parent, "list_agents", map[string]any{}))
	items := listed["agents"].([]any)
	item := items[0].(map[string]any)
	if item["runtime_type"] != "codex" || item["bot_agent_id"] != "external-1" {
		t.Fatalf("agent=%v", item)
	}
}

func TestExternalSubagentSelectionDoesNotRequireNativeModel(t *testing.T) {
	p, manager, _, _ := newAgentControlProvider(t, &fakeSpawnAgent{})
	external := &externalSubagentStub{}
	p.SetExternalSubagents(external)
	p.modelResolver = func(context.Context, SessionContext, string, string, string) (resolvedSubagentModel, error) {
		return resolvedSubagentModel{}, errors.New("no native model configured")
	}
	parent := SessionContext{BotID: "bot-1", SessionID: "parent", UserID: "owner"}
	value, err := executeAgentTool(t, p, parent, "spawn_agent", map[string]any{"id": "worker", "bot_agent_id": "external-1", "task": "inspect work"})
	if err != nil {
		t.Fatal(err)
	}
	result := asMap(t, value)
	if result["text"] != "external result" || result["runtime_type"] != "codex" || result["bot_agent_id"] != "external-1" {
		t.Fatalf("result = %#v", result)
	}
	if known, exists := result["usage_known"]; !exists || known != false {
		t.Fatalf("missing usage must be unknown: %v", result)
	}
	background, err := NewBackgroundProvider(nil, manager).execGetBackgroundStatus(context.Background(), parent, taskArgs{TaskID: result["task_id"].(string)})
	if err != nil {
		t.Fatal(err)
	}
	status := asMap(t, background)
	if known, exists := status["usage_known"]; !exists || known != false || status["runtime_type"] != "codex" || status["bot_agent_id"] != "external-1" {
		t.Fatalf("background=%v", status)
	}
	if len(external.runs) != 1 || external.runs[0].SessionID == parent.SessionID || external.runs[0].Admission.RunID == "" {
		t.Fatalf("runs = %#v", external.runs)
	}
	value, err = executeAgentTool(t, p, parent, "send_message", map[string]any{"id": "worker", "message": "continue"})
	if err != nil {
		t.Fatal(err)
	}
	if asMap(t, value)["text"] != "external result" || len(external.runs) != 2 || external.runs[1].SessionID != external.runs[0].SessionID {
		t.Fatalf("followup = %#v; runs = %#v", value, external.runs)
	}
}

type failingAgentSessions struct {
	*fakeAgentSessionService
	list, create, config, fork error
}

func (s failingAgentSessions) ListSubagentsByParent(ctx context.Context, parentSessionID string) ([]sessionpkg.Thread, error) {
	if s.list != nil {
		return nil, s.list
	}
	return s.fakeAgentSessionService.ListSubagentsByParent(ctx, parentSessionID)
}

func (s failingAgentSessions) CreateSubagent(ctx context.Context, input sessionpkg.CreateSubagentInput) (sessionpkg.Thread, sessionpkg.SubagentConfig, error) {
	if s.create != nil {
		return sessionpkg.Thread{}, sessionpkg.SubagentConfig{}, s.create
	}
	return s.fakeAgentSessionService.CreateSubagent(ctx, input)
}

func (s failingAgentSessions) GetSubagentConfig(ctx context.Context, sessionID string) (sessionpkg.SubagentConfig, error) {
	if s.config != nil {
		return sessionpkg.SubagentConfig{}, s.config
	}
	return s.fakeAgentSessionService.GetSubagentConfig(ctx, sessionID)
}

func (s failingAgentSessions) ListSubagentForkContext(ctx context.Context, sessionID string) ([]sessionpkg.SubagentForkContextMessage, error) {
	if s.fork != nil {
		return nil, s.fork
	}
	return s.fakeAgentSessionService.ListSubagentForkContext(ctx, sessionID)
}

func TestExternalSubagentSessionStoreErrorsStayPrivate(t *testing.T) {
	canary := errors.New("PRIVATE_DIAGNOSTIC_CANARY")
	parent := SessionContext{BotID: "bot-1", SessionID: "parent", UserID: "owner"}
	for _, tc := range []struct {
		name     string
		failing  func(*fakeAgentSessionService) agentSessionService
		tool     string
		args     map[string]any
		fallback bool
	}{
		{"list children", func(s *fakeAgentSessionService) agentSessionService {
			return failingAgentSessions{fakeAgentSessionService: s, list: canary}
		}, "list_agents", map[string]any{}, false},
		{"find child", func(s *fakeAgentSessionService) agentSessionService {
			return failingAgentSessions{fakeAgentSessionService: s, list: canary}
		}, "send_message", map[string]any{"id": "worker", "message": "more"}, false},
		{"create child", func(s *fakeAgentSessionService) agentSessionService {
			return failingAgentSessions{fakeAgentSessionService: s, create: canary}
		}, "spawn_agent", map[string]any{"bot_agent_id": "external-1", "task": "work"}, false},
		{"public code passes through", func(s *fakeAgentSessionService) agentSessionService {
			return failingAgentSessions{fakeAgentSessionService: s, create: apperror.New(apperror.CodeSessionNotFound, nil)}
		}, "spawn_agent", map[string]any{"bot_agent_id": "external-1", "task": "work"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _, sessions, _ := newAgentControlProvider(t, &fakeSpawnAgent{})
			p.SetExternalSubagents(&externalSubagentStub{})
			p.sessionService = tc.failing(sessions)
			want := apperror.CodeInternal
			if tc.fallback {
				want = apperror.CodeSessionNotFound
			}
			_, err := executeAgentTool(t, p, parent, tc.tool, tc.args)
			if err == nil || strings.Contains(err.Error(), "PRIVATE_DIAGNOSTIC_CANARY") || apperror.CodeOf(err) != want {
				t.Fatalf("tool error=%v code=%s", err, apperror.CodeOf(err))
			}
		})
	}
}

func TestNativeSubagentSessionStoreErrorsStayPrivate(t *testing.T) {
	canary := errors.New("PRIVATE_DIAGNOSTIC_CANARY")
	parent := SessionContext{BotID: "bot-1", SessionID: "parent", UserID: "owner"}
	for _, tc := range []struct {
		name    string
		failing func(*fakeAgentSessionService) agentSessionService
		tool    string
		args    map[string]any
	}{
		{"create child", func(s *fakeAgentSessionService) agentSessionService {
			return failingAgentSessions{fakeAgentSessionService: s, create: canary}
		}, "spawn_agent", map[string]any{"id": "fresh", "task": "work"}},
		{"child config", func(s *fakeAgentSessionService) agentSessionService {
			return failingAgentSessions{fakeAgentSessionService: s, config: canary}
		}, "send_message", map[string]any{"id": "worker", "message": "more"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _, sessions, _ := newAgentControlProvider(t, &fakeSpawnAgent{})
			mustExecuteAgentTool(t, p, parent, "spawn_agent", map[string]any{"id": "worker", "task": "first"})
			p.sessionService = tc.failing(sessions)
			value, err := executeAgentTool(t, p, parent, tc.tool, tc.args)
			text := fmt.Sprint(value)
			if err != nil {
				text += err.Error()
			}
			if strings.Contains(text, "PRIVATE_DIAGNOSTIC_CANARY") || (err != nil && apperror.CodeOf(err) != apperror.CodeInternal) {
				t.Fatalf("tool result=%v err=%v", value, err)
			}
		})
	}
}

func TestForkedSubagentContextErrorsStayPrivate(t *testing.T) {
	p, _, sessions, _ := newAgentControlProvider(t, &fakeSpawnAgent{})
	p.sessionService = failingAgentSessions{fakeAgentSessionService: sessions, fork: errors.New("PRIVATE_DIAGNOSTIC_CANARY")}
	if _, err := p.loadAgentForkContext(t.Context(), "child"); err == nil || strings.Contains(err.Error(), "PRIVATE_DIAGNOSTIC_CANARY") || apperror.CodeOf(err) != apperror.CodeInternal {
		t.Fatalf("fork context error=%v", err)
	}
}
