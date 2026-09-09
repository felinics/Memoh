package application

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/agent/sessionmode"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/workspace"
)

type denyToolApprovalPolicyProvider struct{}

type workspaceTargetPolicyErrorResolver struct {
	err error
}

func (r workspaceTargetPolicyErrorResolver) ResolveWorkspaceTargetPolicy(context.Context, string, string) (toolapproval.WorkspaceTargetPolicy, error) {
	return toolapproval.WorkspaceTargetPolicy{}, r.err
}

func (denyToolApprovalPolicyProvider) ToolApprovalPolicy(context.Context, string) (toolapproval.PolicyConfig, error) {
	return toolapproval.PolicyConfig{
		Enabled: true,
		Read:    toolapproval.FilePolicy{Mode: toolapproval.PolicyModeDeny},
	}, nil
}

func TestIsInteractiveApprovalSession(t *testing.T) {
	t.Parallel()

	for _, sessionType := range []string{"", sessionmode.Chat, "CHAT", sessionmode.ACPAgent} {
		if !isInteractiveApprovalSession(sessionType) {
			t.Fatalf("expected %q to allow interactive approvals", sessionType)
		}
	}

	for _, sessionType := range []string{sessionmode.Discuss, sessionmode.Schedule, sessionmode.Subagent} {
		if isInteractiveApprovalSession(sessionType) {
			t.Fatalf("expected %q to reject interactive approvals", sessionType)
		}
	}
}

func TestToolApprovalHandlerLimitsForcedApprovalRejectionReason(t *testing.T) {
	t.Parallel()

	large := "HEAD\n" + strings.Repeat("rejected detail ", 300) + "\nTAIL"
	resolver := &Service{
		agent: native.New(native.Deps{
			Limits: native.Limits{ToolOutputMaxBytes: 512, ToolOutputMaxLines: 80},
		}),
	}
	handler := resolver.buildToolApprovalHandler(baseRunConfigParams{
		BotID:       "bot-1",
		SessionID:   "session-1",
		SessionType: sessionmode.Chat,
	})

	result, err := handler(native.ContextWithHookForcedApproval(context.Background(), large), sdk.ToolCall{
		ToolCallID: "call-1",
		ToolName:   "write",
		Input:      map[string]any{},
	})
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if result.Decision != sdk.ToolApprovalDecisionRejected {
		t.Fatalf("decision = %q, want rejected", result.Decision)
	}
	if len(result.Reason) >= len(large) {
		t.Fatalf("approval reason was not pruned: got %d bytes, original %d", len(result.Reason), len(large))
	}
	if !strings.Contains(result.Reason, "[memoh pruned]") {
		t.Fatalf("approval reason missing prune marker:\n%s", result.Reason)
	}
}

func TestToolApprovalPolicyDenyWinsOverHookForcedApproval(t *testing.T) {
	t.Parallel()

	log := slog.New(slog.DiscardHandler)
	approvalService := toolapproval.NewService(log, nil, denyToolApprovalPolicyProvider{})
	resolver := &Service{toolApproval: approvalService}
	handler := resolver.buildToolApprovalHandler(baseRunConfigParams{
		BotID:       "11111111-1111-1111-1111-111111111111",
		SessionID:   "22222222-2222-2222-2222-222222222222",
		SessionType: sessionmode.Chat,
	})

	result, err := handler(native.ContextWithHookForcedApproval(context.Background(), "hook asks for review"), sdk.ToolCall{
		ToolCallID: "call-1",
		ToolName:   "read",
		Input:      map[string]any{"path": "/data/file.txt"},
	})
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if result.Decision != sdk.ToolApprovalDecisionRejected || result.Reason != toolapproval.PolicyDeniedReason {
		t.Fatalf("result = %+v, want policy rejection", result)
	}
}

func TestToolApprovalHandlerOnlyRecoversMissingWorkspaceTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		resolveErr   error
		wantApproved bool
	}{
		{name: "missing target", resolveErr: workspace.ErrWorkspaceTargetNotFound, wantApproved: true},
		{name: "offline target", resolveErr: workspace.ErrRemoteRuntimeOffline},
		{name: "internal failure", resolveErr: errors.New("database unavailable")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			log := slog.New(slog.DiscardHandler)
			approvalService := toolapproval.NewService(log, nil, nil)
			approvalService.SetWorkspaceTargetPolicyResolver(workspaceTargetPolicyErrorResolver{err: tt.resolveErr})
			resolver := &Service{toolApproval: approvalService, logger: log}
			handler := resolver.buildToolApprovalHandler(baseRunConfigParams{
				BotID:       "bot-1",
				SessionID:   "session-1",
				SessionType: sessionmode.Chat,
			})

			result, err := handler(context.Background(), sdk.ToolCall{
				ToolCallID: "call-1",
				ToolName:   "exec",
				Input: map[string]any{
					"command":   "node --version",
					"target_id": "server_workspace",
				},
			})
			if tt.wantApproved {
				if err != nil {
					t.Fatalf("handler returned error: %v", err)
				}
				if result.Decision != sdk.ToolApprovalDecisionApproved {
					t.Fatalf("decision = %q, want approved", result.Decision)
				}
				return
			}
			if !errors.Is(err, tt.resolveErr) {
				t.Fatalf("handler error = %v, want %v", err, tt.resolveErr)
			}
		})
	}
}

func TestAgentSessionModesMatchPersistedSessionTypes(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		sessionmode.Chat:     session.TypeChat,
		sessionmode.Schedule: session.TypeSchedule,
		sessionmode.Subagent: session.TypeSubagent,
		sessionmode.Discuss:  session.TypeDiscuss,
		sessionmode.ACPAgent: session.TypeACPAgent,
	}
	for got, want := range cases {
		if got != want {
			t.Fatalf("agent session mode %q must match persisted type %q", got, want)
		}
	}
}

func TestResolveRunConfigSessionTypeUsesStoredSessionType(t *testing.T) {
	t.Parallel()

	resolver := &Service{
		sessionService: &fakeBackgroundSessionService{
			getFn: func(_ context.Context, sessionID string) (session.Thread, error) {
				if sessionID != "session-1" {
					t.Fatalf("unexpected session id: %s", sessionID)
				}
				return session.Thread{ID: sessionID, Type: session.TypeChat}, nil
			},
		},
	}

	if got := resolver.resolveRunConfigSessionType(context.Background(), "session-1"); got != session.TypeChat {
		t.Fatalf("session type = %q, want %q", got, session.TypeChat)
	}
}

func TestResolveRunConfigSessionTypeFallsBackToChat(t *testing.T) {
	t.Parallel()

	resolver := &Service{
		sessionService: &fakeBackgroundSessionService{
			getFn: func(context.Context, string) (session.Thread, error) {
				return session.Thread{}, errors.New("db unavailable")
			},
		},
	}

	if got := resolver.resolveRunConfigSessionType(context.Background(), "session-1"); got != session.TypeChat {
		t.Fatalf("session type = %q, want %q", got, session.TypeChat)
	}
}

func TestResolveRunConfigSkipsModelResolutionForACPRuntime(t *testing.T) {
	t.Parallel()

	resolver := &Service{
		sessionService: &fakeBackgroundSessionService{
			getFn: func(_ context.Context, sessionID string) (session.Thread, error) {
				if sessionID != "session-1" {
					t.Fatalf("unexpected session id: %s", sessionID)
				}
				return session.Thread{
					ID:          sessionID,
					Type:        session.TypeDiscuss,
					SessionMode: session.TypeDiscuss,
					RuntimeType: session.RuntimeACPAgent,
				}, nil
			},
		},
	}

	got, err := resolver.ResolveRunConfig(context.Background(), "bot-1", "session-1", "user-1", "telegram", "", "group", "")
	if err != nil {
		t.Fatalf("ResolveRunConfig() error = %v", err)
	}
	if got.RuntimeType != session.RuntimeACPAgent {
		t.Fatalf("runtime type = %q, want %q", got.RuntimeType, session.RuntimeACPAgent)
	}
	if got.RunConfig.SessionType != session.TypeDiscuss {
		t.Fatalf("run config session type = %q, want %q", got.RunConfig.SessionType, session.TypeDiscuss)
	}
	if got.ModelID != "" || got.RunConfig.Model != nil {
		t.Fatalf("ACP runtime should not resolve a model, model_id=%q model=%#v", got.ModelID, got.RunConfig.Model)
	}
}

func TestApprovalResultMetadata(t *testing.T) {
	t.Parallel()

	got := approvalResultMetadata(toolapproval.Request{
		ShortID:    7,
		Status:     toolapproval.StatusRejected,
		ToolName:   "exec",
		ToolCallID: "call-1",
	})

	if got["short_id"] != 7 ||
		got["status"] != toolapproval.StatusRejected ||
		got["tool_name"] != "exec" ||
		got["tool_call_id"] != "call-1" {
		t.Fatalf("unexpected metadata: %#v", got)
	}
}

func TestServiceLimitToolResultTextUsesAgentLimits(t *testing.T) {
	t.Parallel()

	r := &Service{
		agent: native.New(native.Deps{
			Limits: native.Limits{ToolOutputMaxBytes: 512, ToolOutputMaxLines: 80},
		}),
	}
	large := "HEAD\n" + strings.Repeat("rejected detail ", 300) + "\nTAIL"

	got := r.limitToolResultText(large, "write")
	if len(got) >= len(large) {
		t.Fatalf("tool result text was not pruned: got %d bytes, original %d", len(got), len(large))
	}
	if !strings.Contains(got, "[memoh pruned]") {
		t.Fatalf("tool result text missing prune marker:\n%s", got)
	}
}
