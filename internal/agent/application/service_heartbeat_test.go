package application

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	sdk "github.com/memohai/twilight-ai/sdk"

	acpclient "github.com/memohai/memoh/internal/agent/runtime/acp/client"
	session "github.com/memohai/memoh/internal/chat/thread"
	"github.com/memohai/memoh/internal/heartbeat"
)

func TestTriggerHeartbeatACPUsesHeartbeatPromptWithoutNativeModel(t *testing.T) {
	pool := &recordingACPPrompter{result: acpclientPromptResult("HEARTBEAT_OK\nall clear")}
	messages := &recordingMessageService{}
	service, admitter := newHeartbeatACPTestService(pool, messages)

	result, err := service.TriggerHeartbeat(context.Background(), "bot-1", heartbeat.TriggerPayload{
		Interval:        15,
		OwnerUserID:     "owner-1",
		SessionID:       "session-1",
		LastHeartbeatAt: "2026-08-15T12:00:00Z",
	}, "Bearer heartbeat-token")
	if err != nil {
		t.Fatalf("TriggerHeartbeat() error = %v", err)
	}
	if result.Status != "ok" || result.Text != "HEARTBEAT_OK\nall clear" {
		t.Fatalf("heartbeat result = %#v, want ok result", result)
	}
	if result.ModelID != "" || result.SessionID != "session-1" {
		t.Fatalf("heartbeat result model/session = %q/%q, want empty model and session-1", result.ModelID, result.SessionID)
	}
	if pool.calls != 1 {
		t.Fatalf("ACP prompt calls = %d, want 1", pool.calls)
	}
	if pool.input.ModelID != "" {
		t.Fatalf("ACP heartbeat model id = %q, want empty", pool.input.ModelID)
	}
	if pool.input.SessionType != "heartbeat" || pool.input.CanRequestUserInput {
		t.Fatalf("ACP heartbeat controls = type %q can_request_user_input=%v", pool.input.SessionType, pool.input.CanRequestUserInput)
	}
	if pool.input.RuntimeOwnerAccountID != "owner-1" || pool.input.SessionToken != "Bearer heartbeat-token" {
		t.Fatalf("ACP heartbeat owner/token = %q/%q", pool.input.RuntimeOwnerAccountID, pool.input.SessionToken)
	}
	if pool.input.Prompt == "heartbeat" || !strings.Contains(pool.input.Prompt, "15") || !strings.Contains(pool.input.Prompt, "2026-08-15T12:00:00Z") {
		t.Fatalf("ACP heartbeat prompt = %q, want generated heartbeat details", pool.input.Prompt)
	}
	if pool.input.Sink == nil || !strings.Contains(pool.input.ContextMarkdown, "Current Runtime") {
		t.Fatalf("ACP heartbeat prompt context/sink not configured: sink=%v context=%q", pool.input.Sink != nil, pool.input.ContextMarkdown)
	}
	if pool.closedSession != "session-1" {
		t.Fatalf("closed ACP heartbeat session = %q, want session-1", pool.closedSession)
	}
	if len(messages.persisted) != 2 {
		t.Fatalf("persisted messages = %d, want leading user and assistant", len(messages.persisted))
	}
	if got := persistedText(t, messages.persisted[0].Content); got != pool.input.Prompt {
		t.Fatalf("persisted heartbeat prompt = %q, want ACP prompt", got)
	}
	if messages.persisted[0].Role != "user" || messages.persisted[1].Role != "assistant" {
		t.Fatalf("persisted roles = %q/%q, want user/assistant", messages.persisted[0].Role, messages.persisted[1].Role)
	}
	finish := admitter.awaitFinish(t)
	if finish.status != "completed" {
		t.Fatalf("admitted heartbeat finish status = %q, want completed", finish.status)
	}
}

func TestTriggerHeartbeatACPPersistsSanitizedFailure(t *testing.T) {
	promptErr := errors.New("adapter crashed in /private/auth.json")
	pool := &recordingACPPrompter{
		result: acpclientPromptResult("partial heartbeat output"),
		err:    promptErr,
	}
	messages := &recordingMessageService{}
	service, admitter := newHeartbeatACPTestService(pool, messages)

	_, err := service.TriggerHeartbeat(context.Background(), "bot-1", heartbeat.TriggerPayload{
		Interval:    30,
		OwnerUserID: "owner-1",
		SessionID:   "session-1",
	}, "Bearer heartbeat-token")
	if !errors.Is(err, promptErr) {
		t.Fatalf("TriggerHeartbeat() error = %v, want prompt error", err)
	}
	if strings.Contains(err.Error(), "/private/auth.json") {
		t.Fatalf("TriggerHeartbeat() leaked adapter path: %v", err)
	}
	if len(messages.persisted) != 2 {
		t.Fatalf("persisted messages = %d, want leading user and failed assistant", len(messages.persisted))
	}
	if got := persistedText(t, messages.persisted[1].Content); !strings.Contains(got, "ACP agent failed to complete the turn") {
		t.Fatalf("persisted failure text = %q, want sanitized ACP failure", got)
	}
	if got := messages.persisted[1].Metadata["error_code"]; got != "acp_runtime_prompt_failed" {
		t.Fatalf("persisted failure code = %#v, want acp_runtime_prompt_failed", got)
	}
	if strings.Contains(persistedText(t, messages.persisted[1].Content), "/private/auth.json") {
		t.Fatal("persisted failure leaked adapter path")
	}
	finish := admitter.awaitFinish(t)
	if finish.status != "errored" {
		t.Fatalf("admitted heartbeat finish status = %q, want errored", finish.status)
	}
	if pool.closedSession != "session-1" {
		t.Fatalf("closed ACP heartbeat session = %q, want session-1", pool.closedSession)
	}
}

func TestTriggerHeartbeatACPReportsRoundPersistenceFailure(t *testing.T) {
	persistErr := errors.New("history database unavailable")
	pool := &recordingACPPrompter{result: acpclientPromptResult("HEARTBEAT_OK")}
	service, admitter := newHeartbeatACPTestService(pool, &recordingMessageService{persistErr: persistErr})

	_, err := service.TriggerHeartbeat(context.Background(), "bot-1", heartbeat.TriggerPayload{
		Interval:    30,
		OwnerUserID: "owner-1",
		SessionID:   "session-1",
	}, "Bearer heartbeat-token")
	if err == nil || !strings.Contains(err.Error(), "persist ACP heartbeat round") {
		t.Fatalf("TriggerHeartbeat() error = %v, want persistence failure", err)
	}
	if pool.closedSession != "session-1" {
		t.Fatalf("closed ACP heartbeat session = %q, want session-1", pool.closedSession)
	}
	if finish := admitter.awaitFinish(t); finish.status != "errored" {
		t.Fatalf("admitted heartbeat finish status = %q, want errored", finish.status)
	}
}

func newHeartbeatACPTestService(pool *recordingACPPrompter, messages *recordingMessageService) (*Service, *scriptedAdmitter) {
	admitter := newScriptedAdmitter()
	service := &Service{
		messageService: messages,
		acpPool:        pool,
		sessionRuntime: admitter,
		sessionService: &fakeBackgroundSessionService{
			getFn: func(_ context.Context, sessionID string) (session.Thread, error) {
				return session.Thread{
					ID:              sessionID,
					BotID:           "bot-1",
					Type:            session.TypeHeartbeat,
					SessionMode:     session.TypeHeartbeat,
					RuntimeType:     session.RuntimeACPAgent,
					CreatedByUserID: "owner-1",
					RuntimeMetadata: map[string]any{
						"acp_agent_id":             "codex",
						"project_path":             "/data",
						"runtime_owner_account_id": "owner-1",
					},
				}, nil
			},
		},
		botPermissions: allowWorkspaceExecForBot("bot-1", "owner-1"),
		logger:         slog.New(slog.DiscardHandler),
	}
	return service, admitter
}

func acpclientPromptResult(text string) acpclient.PromptResult {
	return acpclient.PromptResult{
		Text:       text,
		StopReason: "end_turn",
		Usage:      &sdk.Usage{InputTokens: 3, OutputTokens: 5, TotalTokens: 8},
	}
}
