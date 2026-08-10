package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	sdk "github.com/memohai/twilight-ai/sdk"

	toolapproval "github.com/memohai/memoh/internal/agent/decision/approval"
	acpfeedback "github.com/memohai/memoh/internal/agent/decision/feedback"
	userinput "github.com/memohai/memoh/internal/agent/decision/input"
	"github.com/memohai/memoh/internal/agent/event"
	acpagent "github.com/memohai/memoh/internal/agent/runtime/acp"
	acpclient "github.com/memohai/memoh/internal/agent/runtime/acp/client"
	"github.com/memohai/memoh/internal/agent/runtime/native"
	"github.com/memohai/memoh/internal/apperror"
	"github.com/memohai/memoh/internal/bots"
	messagepkg "github.com/memohai/memoh/internal/chat/message"
	session "github.com/memohai/memoh/internal/chat/thread"
	"github.com/memohai/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/memohai/memoh/internal/db/store"
	memprovider "github.com/memohai/memoh/internal/memory/adapters"
	"github.com/memohai/memoh/internal/settings"
)

const (
	storeRoundBotID            = "11111111-1111-1111-1111-111111111111"
	storeRoundMemoryProviderID = "22222222-2222-2222-2222-222222222222"
)

func TestStreamChatWSRoutesACPAgentSessionToACPPool(t *testing.T) {
	t.Parallel()

	messages := &recordingMessageService{}
	pool := &recordingACPPrompter{
		result: acpclient.PromptResult{
			Text:       "done from codex",
			StopReason: "end_turn",
			Usage:      &sdk.Usage{InputTokens: 3, OutputTokens: 5, TotalTokens: 8},
		},
	}
	resolver := &Service{
		messageService: messages,
		acpPool:        pool,
		botPermissions: allowWorkspaceExecFor("user-1"),
		sessionService: &fakeBackgroundSessionService{
			getFn: func(_ context.Context, sessionID string) (session.Thread, error) {
				if sessionID != "session-1" {
					t.Fatalf("unexpected session id: %s", sessionID)
				}
				return session.Thread{
					ID:    "session-1",
					BotID: "bot-1",
					Type:  session.TypeACPAgent,
					Metadata: map[string]any{
						"acp_agent_id":             "codex",
						"project_path":             "/data/app",
						"runtime_owner_account_id": "user-1",
					},
				}, nil
			},
		},
		logger: slog.New(slog.DiscardHandler),
	}

	eventCh := make(chan WSStreamEvent, 8)
	if err := resolver.StreamChatWS(
		context.Background(),
		ChatRequest{
			BotID:           "bot-1",
			ThreadID:        "session-1",
			Query:           "inspect the app",
			Model:           "gpt-5.1-codex",
			ReasoningEffort: "high",
			Attachments: []ChatAttachment{{
				Type:   "image",
				Base64: "data:image/png;base64,aW1hZ2U=",
				Mime:   "image/png",
				Name:   "screenshot.png",
			}},
			ReplyAttachments: []ChatAttachment{{
				Type: "file",
				Name: "previous.log",
				URL:  "https://example.com/previous.log",
			}},
		},
		eventCh,
		make(chan struct{}),
	); err != nil {
		t.Fatalf("StreamChatWS() error = %v", err)
	}

	if pool.calls != 1 {
		t.Fatalf("ACP pool calls = %d, want 1", pool.calls)
	}
	if pool.input.BotID != "bot-1" || pool.input.SessionID != "session-1" || pool.input.AgentID != "codex" || pool.input.ProjectPath != "/data/app" {
		t.Fatalf("ACP prompt input = %#v", pool.input)
	}
	if pool.input.ModelID != "gpt-5.1-codex" || pool.input.ReasoningEffort != "high" {
		t.Fatalf("ACP turn config = model %q reasoning %q", pool.input.ModelID, pool.input.ReasoningEffort)
	}
	if pool.input.ContextURI != acpContextURI || !strings.Contains(pool.input.ContextMarkdown, "## Current Runtime") || !strings.Contains(pool.input.ContextMarkdown, "Bot ID: bot-1") {
		t.Fatalf("ACP context = uri %q markdown %q, want dynamic Memoh context", pool.input.ContextURI, pool.input.ContextMarkdown)
	}
	if len(pool.input.Images) != 1 || pool.input.Images[0].Data != "aW1hZ2U=" || pool.input.Images[0].MimeType != "image/png" {
		t.Fatalf("ACP prompt images = %#v, want inline PNG", pool.input.Images)
	}
	if len(pool.input.AttachmentReferences) != 1 || pool.input.AttachmentReferences[0] != "https://example.com/previous.log" {
		t.Fatalf("ACP attachment references = %#v, want reply attachment URL", pool.input.AttachmentReferences)
	}
	if !strings.Contains(pool.input.ContextMarkdown, "previous.log") || !strings.Contains(pool.input.ContextMarkdown, "https://example.com/previous.log") {
		t.Fatalf("ACP context = %q, want reply attachment", pool.input.ContextMarkdown)
	}
	if len(messages.persisted) != 2 {
		t.Fatalf("persisted %d messages, want user + assistant", len(messages.persisted))
	}
	if messages.persisted[0].Role != "user" || messages.persisted[1].Role != "assistant" {
		t.Fatalf("persisted roles = %q, %q", messages.persisted[0].Role, messages.persisted[1].Role)
	}
	if got := messages.persisted[1].Metadata["acp_agent_id"]; got != "codex" {
		t.Fatalf("assistant acp_agent_id = %#v, want codex", got)
	}
	if got := persistedText(t, messages.persisted[1].Content); got != "done from codex" {
		t.Fatalf("persisted assistant text = %q, want done from codex", got)
	}

	events := drainAgentEvents(t, eventCh)
	if !containsStreamEvent(events, native.EventStart) || !containsStreamEvent(events, native.EventEnd) {
		t.Fatalf("events = %#v, want agent start/end", events)
	}
	if !containsTextDelta(events, "streamed from acp") {
		t.Fatalf("events = %#v, want ACP stream delta", events)
	}
	end := requireStreamEvent(t, events, native.EventEnd)
	if got := terminalAssistantText(t, end); got != "done from codex" {
		t.Fatalf("terminal assistant text = %q, want done from codex", got)
	}
	var usage sdk.Usage
	if err := json.Unmarshal(end.Usage, &usage); err != nil {
		t.Fatalf("decode terminal usage: %v", err)
	}
	if usage.InputTokens != 3 || usage.OutputTokens != 5 || usage.TotalTokens != 8 {
		t.Fatalf("terminal usage = %+v, want input=3 output=5 total=8", usage)
	}
}

func TestStreamChatWSRejectsACPBotMismatchBeforePersistence(t *testing.T) {
	t.Parallel()

	messages := &recordingMessageService{}
	pool := &recordingACPPrompter{}
	resolver := &Service{
		messageService: messages,
		acpPool:        pool,
		botPermissions: allowWorkspaceExecFor("user-1"),
		sessionService: &fakeBackgroundSessionService{
			getFn: func(_ context.Context, sessionID string) (session.Thread, error) {
				if sessionID != "session-1" {
					t.Fatalf("unexpected session id: %s", sessionID)
				}
				return session.Thread{
					ID:    "session-1",
					BotID: "bot-2",
					Type:  session.TypeACPAgent,
					Metadata: map[string]any{
						"acp_agent_id":             "codex",
						"project_path":             "/data/app",
						"runtime_owner_account_id": "user-1",
					},
				}, nil
			},
		},
		logger: slog.New(slog.DiscardHandler),
	}

	err := resolver.StreamChatWS(
		context.Background(),
		ChatRequest{
			BotID:    "bot-1",
			ThreadID: "session-1",
			Query:    "inspect the app",
		},
		make(chan WSStreamEvent, 8),
		make(chan struct{}),
	)
	if err == nil {
		t.Fatal("StreamChatWS() error = nil, want bot mismatch")
	}
	if pool.calls != 0 {
		t.Fatalf("ACP pool calls = %d, want 0", pool.calls)
	}
	if len(messages.persisted) != 0 {
		t.Fatalf("persisted %d messages, want 0", len(messages.persisted))
	}
}

// Single-session execution is no longer this layer's guarantee, and there is no
// test for it here on purpose. An in-process check could only answer for one
// server, so it moved to durable admission, where a concurrent invocation is
// refused with a retryable session_busy before any runtime is asked to prompt.
// See TestAdmitAnswersBusyForConcurrentInvocation in the sessionruntime package,
// which also covers what the in-process version could not express: the rejected
// invocation is admitted normally once the active run reaches a terminal state.

func TestStreamChatRoutesACPAgentSessionToACPPool(t *testing.T) {
	t.Parallel()

	pool := &recordingACPPrompter{
		result: acpclient.PromptResult{
			Text:       "done from codex",
			StopReason: "end_turn",
		},
	}
	resolver := &Service{
		messageService: &recordingMessageService{},
		acpPool:        pool,
		botPermissions: allowWorkspaceExecFor("user-1"),
		sessionService: &fakeBackgroundSessionService{
			getFn: func(_ context.Context, sessionID string) (session.Thread, error) {
				if sessionID != "session-1" {
					t.Fatalf("unexpected session id: %s", sessionID)
				}
				return session.Thread{
					ID:    "session-1",
					BotID: "bot-1",
					Type:  session.TypeACPAgent,
					Metadata: map[string]any{
						"acp_agent_id":             "codex",
						"project_path":             "/data/app",
						"runtime_owner_account_id": "user-1",
					},
				}, nil
			},
		},
		logger: slog.New(slog.DiscardHandler),
	}

	chunks, errs := resolver.StreamChat(context.Background(), ChatRequest{
		BotID:    "bot-1",
		ThreadID: "session-1",
		Query:    "inspect the app",
	})
	events := drainStreamChunks(t, chunks)
	if err := <-errs; err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	if pool.calls != 1 {
		t.Fatalf("ACP pool calls = %d, want 1", pool.calls)
	}
	if pool.input.BotID != "bot-1" || pool.input.SessionID != "session-1" || pool.input.AgentID != "codex" || pool.input.ProjectPath != "/data/app" {
		t.Fatalf("ACP prompt input = %#v", pool.input)
	}
	if !containsStreamEvent(events, native.EventStart) || !containsStreamEvent(events, native.EventEnd) {
		t.Fatalf("events = %#v, want agent start/end", events)
	}
	if !containsTextDelta(events, "streamed from acp") {
		t.Fatalf("events = %#v, want ACP stream delta", events)
	}
	end := requireStreamEvent(t, events, native.EventEnd)
	if got := terminalAssistantText(t, end); got != "done from codex" {
		t.Fatalf("terminal assistant text = %q, want done from codex", got)
	}
}

func TestStreamChatRoutesDiscussACPRuntimeSessionToACPPool(t *testing.T) {
	t.Parallel()

	pool := &recordingACPPrompter{
		result: acpclient.PromptResult{
			Text:       "done from codex",
			StopReason: "end_turn",
		},
	}
	resolver := &Service{
		messageService: &recordingMessageService{},
		acpPool:        pool,
		botPermissions: allowWorkspaceExecFor("user-1"),
		sessionService: &fakeBackgroundSessionService{
			getFn: func(_ context.Context, sessionID string) (session.Thread, error) {
				if sessionID != "session-1" {
					t.Fatalf("unexpected session id: %s", sessionID)
				}
				return session.Thread{
					ID:          "session-1",
					BotID:       "bot-1",
					Type:        session.TypeDiscuss,
					SessionMode: session.TypeDiscuss,
					RuntimeType: session.RuntimeACPAgent,
					RuntimeMetadata: map[string]any{
						"acp_agent_id":             "codex",
						"project_path":             "/data/app",
						"runtime_owner_account_id": "user-1",
					},
				}, nil
			},
		},
		logger: slog.New(slog.DiscardHandler),
	}

	chunks, errs := resolver.StreamChat(context.Background(), ChatRequest{
		BotID:    "bot-1",
		ThreadID: "session-1",
		Query:    "inspect the group thread",
	})
	events := drainStreamChunks(t, chunks)
	if err := <-errs; err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	if pool.calls != 1 {
		t.Fatalf("ACP pool calls = %d, want 1", pool.calls)
	}
	if pool.input.BotID != "bot-1" || pool.input.SessionID != "session-1" || pool.input.AgentID != "codex" || pool.input.ProjectPath != "/data/app" {
		t.Fatalf("ACP prompt input = %#v", pool.input)
	}
	if !containsStreamEvent(events, native.EventStart) || !containsStreamEvent(events, native.EventEnd) {
		t.Fatalf("events = %#v, want agent start/end", events)
	}
	if !containsTextDelta(events, "streamed from acp") {
		t.Fatalf("events = %#v, want ACP stream delta", events)
	}
}

func TestACPTerminalStreamEventFallsBackToTranscriptEvents(t *testing.T) {
	t.Parallel()

	ev := acpTerminalStreamEvent(native.EventEnd, acpclient.PromptResult{
		Events: []event.StreamEvent{{Type: event.TextDelta, Delta: "from transcript"}},
		Usage:  &sdk.Usage{InputTokens: 2, OutputTokens: 4, TotalTokens: 6},
	})

	if ev.Type != native.EventEnd {
		t.Fatalf("terminal event type = %s, want %s", ev.Type, native.EventEnd)
	}
	if got := terminalAssistantText(t, ev); got != "from transcript" {
		t.Fatalf("terminal assistant text = %q, want from transcript", got)
	}
	var usage sdk.Usage
	if err := json.Unmarshal(ev.Usage, &usage); err != nil {
		t.Fatalf("decode terminal usage: %v", err)
	}
	if usage.InputTokens != 2 || usage.OutputTokens != 4 || usage.TotalTokens != 6 {
		t.Fatalf("terminal usage = %+v, want input=2 output=4 total=6", usage)
	}
}

func TestStreamACPAgentWSRechecksRuntimeOwnerWorkspaceExecBeforePrompt(t *testing.T) {
	t.Parallel()

	pool := &recordingACPPrompter{
		result: acpclient.PromptResult{
			Text:       "should not run",
			StopReason: "end_turn",
		},
	}
	resolver := &Service{
		messageService: &recordingMessageService{},
		acpPool:        pool,
		botPermissions: &fakeBotPermissionChecker{values: map[string]bool{}},
		sessionService: acpRuntimeSessionServiceForTest("user-1"),
		logger:         slog.New(slog.DiscardHandler),
	}

	err := resolver.streamACPAgentWS(
		context.Background(),
		ChatRequest{
			BotID:    "bot-1",
			ThreadID: "session-1",
			Query:    "inspect the app",
		},
		make(chan WSStreamEvent, 8),
		make(chan struct{}),
	)
	var feedback *acpfeedback.Error
	if !errors.As(err, &feedback) || feedback.Code != acpfeedback.CodeNoWorkspaceExec || feedback.HTTPStatus != 403 {
		t.Fatalf("streamACPAgentWS() error = %v, want no_workspace_exec feedback", err)
	}
	if pool.calls != 0 {
		t.Fatalf("ACP pool calls = %d, want 0 when runtime owner lost workspace_exec", pool.calls)
	}
}

func TestStreamChatWSPersistsACPUserInputProjectionOnceBeforePromptReturns(t *testing.T) {
	t.Parallel()
	turnPosition := int64(7)

	streamed := []event.StreamEvent{
		{
			Type:       event.ToolCallStart,
			ToolCallID: "ask-1",
			ToolName:   userinput.ToolNameAskUser,
			Input: map[string]any{
				"questions": []any{
					map[string]any{
						"id":   "q1",
						"text": "Pick one",
						"type": "single_choice",
						"options": []any{
							map[string]any{"id": "a", "label": "A"},
						},
					},
				},
			},
		},
		{
			Type:        event.UserInputRequest,
			ToolCallID:  "ask-1",
			ToolName:    userinput.ToolNameAskUser,
			UserInputID: "input-1",
			ShortID:     1,
			Status:      userinput.StatusPending,
			Input: map[string]any{
				"questions": []any{
					map[string]any{
						"id":   "q1",
						"text": "Pick one",
						"type": "single_choice",
						"options": []any{
							map[string]any{"id": "a", "label": "A"},
						},
					},
				},
			},
			Metadata: map[string]any{
				"user_input_id": "input-1",
				"short_id":      1,
				"status":        userinput.StatusPending,
				"ui_payload": map[string]any{
					"questions": []any{
						map[string]any{
							"id":   "q1",
							"text": "Pick one",
							"type": "single_choice",
							"options": []any{
								map[string]any{"id": "a", "label": "A"},
							},
						},
					},
				},
			},
		},
		{
			Type:        event.UserInputRequest,
			ToolCallID:  "ask-1",
			ToolName:    userinput.ToolNameAskUser,
			UserInputID: "input-1",
			ShortID:     1,
			Status:      userinput.StatusCanceled,
			Input: map[string]any{
				"questions": []any{
					map[string]any{
						"id":   "q1",
						"text": "Pick one",
						"type": "single_choice",
						"options": []any{
							map[string]any{"id": "a", "label": "A"},
						},
					},
				},
			},
			Metadata: map[string]any{
				"user_input_id": "input-1",
				"short_id":      1,
				"status":        userinput.StatusCanceled,
				"ui_payload": map[string]any{
					"questions": []any{
						map[string]any{
							"id":   "q1",
							"text": "Pick one",
							"type": "single_choice",
							"options": []any{
								map[string]any{"id": "a", "label": "A"},
							},
						},
					},
				},
			},
		},
		{Type: event.TextDelta, Delta: "done"},
	}
	messages := &recordingMessageService{}
	pool := &recordingACPPrompter{
		result: withTranscriptOutput(acpclient.PromptResult{
			Events: streamed,
		}),
		streamEvents: streamed,
		afterEvents: func() {
			if len(messages.persisted) != 2 {
				t.Fatalf("persisted before ACP prompt returned = %d, want user + initial decision projection", len(messages.persisted))
			}
			if messages.persisted[0].Role != "user" || messages.persisted[1].Role != "assistant" {
				t.Fatalf("leading persisted roles = %q, %q", messages.persisted[0].Role, messages.persisted[1].Role)
			}
			if got := messages.persisted[0]; got.RunID != "run-1" || got.TurnID != "turn-1" || got.TurnPosition == nil || *got.TurnPosition != turnPosition {
				t.Fatalf("leading user run/turn identity = (%q, %q, %v), want (run-1, turn-1, %d)", got.RunID, got.TurnID, got.TurnPosition, turnPosition)
			}
			if got := messages.persisted[1]; got.RunID != "run-1" || got.TurnRequestMessageID != "message-id" {
				t.Fatalf("decision projection run/request identity = (%q, %q), want (run-1, message-id)", got.RunID, got.TurnRequestMessageID)
			}
		},
	}
	resolver := &Service{
		messageService: messages,
		acpPool:        pool,
		botPermissions: allowWorkspaceExecFor("user-1"),
		sessionService: &fakeBackgroundSessionService{
			getFn: func(_ context.Context, sessionID string) (session.Thread, error) {
				return session.Thread{
					ID:    sessionID,
					BotID: "bot-1",
					Type:  session.TypeACPAgent,
					Metadata: map[string]any{
						"acp_agent_id":             "codex",
						"project_path":             "/data/app",
						"runtime_owner_account_id": "user-1",
					},
				}, nil
			},
		},
		logger: slog.New(slog.DiscardHandler),
	}

	if err := resolver.StreamChatWS(
		context.Background(),
		ChatRequest{
			BotID:          "bot-1",
			ThreadID:       "session-1",
			RunID:          "run-1",
			TurnID:         "turn-1",
			TurnPosition:   &turnPosition,
			Query:          "inspect the app",
			CurrentChannel: "web",
		},
		make(chan WSStreamEvent, 8),
		make(chan struct{}),
	); err != nil {
		t.Fatalf("StreamChatWS() error = %v", err)
	}

	if len(messages.persisted) != 5 {
		t.Fatalf("persisted %d messages, want user + temporary projection + complete ACP transcript", len(messages.persisted))
	}
	pendingProjection := persistedModelMessage(t, messages.persisted[1].Content)
	pendingCalls := extractAssistantToolCallParts(pendingProjection)
	if len(pendingCalls) != 1 || pendingCalls[0].ToolCallID != "ask-1" {
		t.Fatalf("pending projected tool calls = %#v, want ask-1", pendingCalls)
	}
	if got := toolCallMetadataStatus(pendingCalls[0], "user_input"); got != userinput.StatusPending {
		t.Fatalf("pending projection status = %q, want pending", got)
	}
	terminalCall := persistedModelMessage(t, messages.persisted[2].Content)
	terminalCalls := extractAssistantToolCallParts(terminalCall)
	if len(terminalCalls) != 1 || terminalCalls[0].ToolCallID != "ask-1" {
		t.Fatalf("terminal tool calls = %#v, want ask-1", terminalCalls)
	}
	if got := toolCallMetadataStatus(terminalCalls[0], "user_input"); got != userinput.StatusCanceled {
		t.Fatalf("terminal projection status = %q, want canceled", got)
	}
	results := extractToolResultParts(persistedModelMessage(t, messages.persisted[3].Content))
	if len(results) != 1 || results[0].ToolCallID != "ask-1" || !results[0].IsError {
		t.Fatalf("terminal synthetic result = %#v, want canceled ask-1 closure", results)
	}
	final := persistedModelMessage(t, messages.persisted[4].Content)
	if got := final.TextContent(); got != "done" {
		t.Fatalf("final assistant text = %q, want done", got)
	}
	if len(messages.deleted) != 1 || len(messages.deleted[0]) != 1 {
		t.Fatalf("temporary projection cleanup = %#v, want one deleted message", messages.deleted)
	}
}

func TestStreamChatWSPersistsACPSubmittedUserInputResult(t *testing.T) {
	t.Parallel()

	questionInput := map[string]any{
		"questions": []any{
			map[string]any{
				"text": "Which dynasty?",
				"kind": "single_select",
				"options": []any{
					map[string]any{"label": "Xia"},
					map[string]any{"label": "Qin"},
				},
			},
		},
	}
	uiPayload := map[string]any{
		"version": 2,
		"questions": []any{
			map[string]any{
				"id":   "q1",
				"text": "Which dynasty?",
				"kind": "single_select",
				"options": []any{
					map[string]any{"id": "q1.o1", "label": "Xia"},
					map[string]any{"id": "q1.o2", "label": "Qin"},
				},
			},
		},
	}
	submitted := map[string]any{
		"status": userinput.StatusSubmitted,
		"answers": []any{
			map[string]any{
				"question_id": "q1",
				"question":    "Which dynasty?",
				"selected": []any{
					map[string]any{"id": "q1.o1", "label": "Xia"},
				},
			},
		},
	}
	userInputEvent := func(status string) event.StreamEvent {
		metadata := map[string]any{
			"user_input_id": "input-1",
			"short_id":      1,
			"status":        status,
			"ui_payload":    uiPayload,
		}
		if status == userinput.StatusSubmitted {
			metadata["answers"] = userinput.AnswersFromResult(submitted)
		}
		return event.StreamEvent{
			Type:        event.UserInputRequest,
			ToolCallID:  "ask-1",
			ToolName:    userinput.ToolNameAskUser,
			UserInputID: "input-1",
			ShortID:     1,
			Status:      status,
			Input:       questionInput,
			Metadata:    metadata,
		}
	}
	toolResult := map[string]any{
		"content": []any{
			map[string]any{"type": "text", "text": `{"status":"submitted"}`},
		},
		"structuredContent": submitted,
	}
	streamed := []event.StreamEvent{
		{Type: event.TextDelta, Delta: "Let me quiz you on Chinese history."},
		{
			Type:       event.ToolCallStart,
			ToolCallID: "ask-1",
			ToolName:   userinput.ToolNameAskUser,
			Input:      questionInput,
		},
		userInputEvent(userinput.StatusPending),
		userInputEvent(userinput.StatusSubmitted),
		{
			Type:       event.ToolCallEnd,
			ToolCallID: "ask-1",
			ToolName:   userinput.ToolNameAskUser,
			Result:     toolResult,
		},
		{Type: event.TextDelta, Delta: "Qin is correct."},
	}
	messages := &recordingMessageService{}
	pool := &recordingACPPrompter{
		result:       withTranscriptOutput(acpclient.PromptResult{Events: streamed}),
		streamEvents: streamed,
	}
	resolver := &Service{
		messageService: messages,
		acpPool:        pool,
		botPermissions: allowWorkspaceExecFor("user-1"),
		sessionService: acpRuntimeSessionServiceForTest("user-1"),
		logger:         slog.New(slog.DiscardHandler),
	}

	if err := resolver.StreamChatWS(
		context.Background(),
		ChatRequest{
			BotID:          "bot-1",
			ThreadID:       "session-1",
			Query:          "quiz me",
			CurrentChannel: "web",
		},
		make(chan WSStreamEvent, 16),
		make(chan struct{}),
	); err != nil {
		t.Fatalf("StreamChatWS() error = %v", err)
	}

	if len(messages.persisted) != 5 {
		t.Fatalf("persisted %d messages, want user + projection + narration + tool result + final assistant", len(messages.persisted))
	}
	if got := persistedModelMessage(t, messages.persisted[2].Content).TextContent(); got != "Let me quiz you on Chinese history." {
		t.Fatalf("persisted leading narration = %q", got)
	}
	ordered := modelMessageToSDKMessage(persistedModelMessage(t, messages.persisted[2].Content)).Content
	if len(ordered) != 2 {
		t.Fatalf("narration/tool content = %#v, want two ordered parts", ordered)
	}
	if text, ok := ordered[0].(sdk.TextPart); !ok || text.Text != "Let me quiz you on Chinese history." {
		t.Fatalf("first persisted part = %#v, want narration", ordered[0])
	}
	if call, ok := ordered[1].(sdk.ToolCallPart); !ok || call.ToolCallID != "ask-1" {
		t.Fatalf("second persisted part = %#v, want ask_user card", ordered[1])
	}
	if got := messages.persisted[3].Role; got != "tool" {
		t.Fatalf("submitted result role = %q, want tool", got)
	}
	results := extractToolResultParts(persistedModelMessage(t, messages.persisted[3].Content))
	if len(results) != 1 || results[0].ToolCallID != "ask-1" {
		t.Fatalf("persisted submitted results = %#v, want ask-1", results)
	}

	if len(messages.deleted) != 1 || len(messages.deleted[0]) != 1 {
		t.Fatalf("temporary projection cleanup = %#v, want one deleted message", messages.deleted)
	}
	history := make([]ModelMessage, 0, len(messages.persisted)-1)
	for index, persisted := range messages.persisted {
		if index == 1 { // The temporary waiting projection was deleted.
			continue
		}
		history = append(history, persistedModelMessage(t, persisted.Content))
	}
	repaired := repairToolCallClosures(nonNilModelMessages(sanitizeMessages(history)), syntheticToolClosureError)
	projectionIndex := -1
	var repairedCalls, repairedResults int
	for index, message := range repaired {
		for _, call := range extractAssistantToolCallParts(message) {
			if call.ToolCallID == "ask-1" {
				repairedCalls++
				projectionIndex = index
			}
		}
		for _, result := range extractToolResultParts(message) {
			if result.ToolCallID != "ask-1" {
				continue
			}
			repairedResults++
			if result.IsError {
				t.Fatalf("next-turn history synthesized an error for submitted ask_user: %#v", result)
			}
		}
	}
	if repairedCalls != 1 || repairedResults != 1 {
		t.Fatalf("next-turn ask_user call/result count = %d/%d, want 1/1; history=%#v", repairedCalls, repairedResults, repaired)
	}
	if projectionIndex < 0 || projectionIndex+1 >= len(repaired) {
		t.Fatalf("projected call position %d leaves no room for its result", projectionIndex)
	}
	following := extractToolResultParts(repaired[projectionIndex+1])
	if len(following) != 1 || following[0].ToolCallID != "ask-1" {
		t.Fatalf("message after projection = %#v, want the genuine ask-1 result before narration", repaired[projectionIndex+1])
	}
}

func TestStreamChatWSPersistsACPApprovalProjectionOnce(t *testing.T) {
	t.Parallel()

	streamed := []event.StreamEvent{
		{
			Type:       event.ToolCallStart,
			ToolCallID: "write-1",
			ToolName:   "write",
			Input:      map[string]any{"path": "/data/review.txt"},
		},
		{
			Type:       event.ToolApprovalRequest,
			ToolCallID: "write-1",
			ToolName:   "write",
			ApprovalID: "approval-1",
			ShortID:    3,
			Status:     toolapproval.StatusPending,
			Input:      map[string]any{"path": "/data/review.txt"},
		},
		{
			Type:       event.ToolApprovalRequest,
			ToolCallID: "write-1",
			ToolName:   "write",
			ApprovalID: "approval-1",
			ShortID:    3,
			Status:     toolapproval.StatusApproved,
			Input:      map[string]any{"path": "/data/review.txt"},
		},
		{
			Type:       event.ToolCallEnd,
			ToolCallID: "write-1",
			ToolName:   "write",
			Result:     map[string]any{"ok": true},
		},
		{Type: event.TextDelta, Delta: "done"},
	}
	messages := &recordingMessageService{}
	pool := &recordingACPPrompter{
		result:       withTranscriptOutput(acpclient.PromptResult{Events: streamed}),
		streamEvents: streamed,
	}
	resolver := &Service{
		messageService: messages,
		acpPool:        pool,
		botPermissions: allowWorkspaceExecFor("user-1"),
		sessionService: &fakeBackgroundSessionService{
			getFn: func(_ context.Context, sessionID string) (session.Thread, error) {
				return session.Thread{
					ID:    sessionID,
					BotID: "bot-1",
					Type:  session.TypeACPAgent,
					Metadata: map[string]any{
						"acp_agent_id":             "codex",
						"project_path":             "/data/app",
						"runtime_owner_account_id": "user-1",
					},
				}, nil
			},
		},
		logger: slog.New(slog.DiscardHandler),
	}

	if err := resolver.StreamChatWS(
		context.Background(),
		ChatRequest{
			BotID:          "bot-1",
			ThreadID:       "session-1",
			Query:          "write the review",
			CurrentChannel: "web",
		},
		make(chan WSStreamEvent, 8),
		make(chan struct{}),
	); err != nil {
		t.Fatalf("StreamChatWS() error = %v", err)
	}

	if len(messages.persisted) != 5 {
		t.Fatalf("persisted %d messages, want user + temporary projection + complete ACP transcript", len(messages.persisted))
	}
	pendingProjection := persistedModelMessage(t, messages.persisted[1].Content)
	pendingCalls := extractAssistantToolCallParts(pendingProjection)
	if len(pendingCalls) != 1 || pendingCalls[0].ToolCallID != "write-1" {
		t.Fatalf("pending projected tool calls = %#v, want write-1", pendingCalls)
	}
	if got := toolCallMetadataStatus(pendingCalls[0], "approval"); got != toolapproval.StatusPending {
		t.Fatalf("pending projection status = %q, want pending", got)
	}
	terminalCall := persistedModelMessage(t, messages.persisted[2].Content)
	terminalCalls := extractAssistantToolCallParts(terminalCall)
	if len(terminalCalls) != 1 || terminalCalls[0].ToolCallID != "write-1" {
		t.Fatalf("terminal approval tool calls = %#v, want write-1", terminalCalls)
	}
	if got := toolCallMetadataStatus(terminalCalls[0], "approval"); got != toolapproval.StatusApproved {
		t.Fatalf("terminal approval status = %q, want approved", got)
	}
	toolResult := persistedModelMessage(t, messages.persisted[3].Content)
	results := extractToolResultParts(toolResult)
	if len(results) != 1 || results[0].ToolCallID != "write-1" {
		t.Fatalf("persisted approval tool result = %#v, want write-1", results)
	}
	final := persistedModelMessage(t, messages.persisted[4].Content)
	if got := final.TextContent(); got != "done" {
		t.Fatalf("final assistant text = %q, want done", got)
	}
	if len(messages.deleted) != 1 || len(messages.deleted[0]) != 1 {
		t.Fatalf("temporary approval projection cleanup = %#v, want one deleted message", messages.deleted)
	}
}

func TestAuthorizeACPToolApprovalRequiresRuntimeOwnerOrManage(t *testing.T) {
	t.Parallel()

	const (
		ownerID   = "owner-user"
		otherID   = "other-user"
		managerID = "manager-user"
	)
	perms := &fakeBotPermissionChecker{
		values: map[string]bool{
			"bot-1:" + ownerID + ":" + bots.PermissionWorkspaceExec: true,
			"bot-1:" + otherID + ":" + bots.PermissionWorkspaceExec: true,
			"bot-1:" + managerID + ":" + bots.PermissionManage:      true,
		},
	}
	resolver := &Service{
		botPermissions: perms,
		sessionService: &fakeBackgroundSessionService{
			getFn: func(_ context.Context, sessionID string) (session.Thread, error) {
				return session.Thread{
					ID:          sessionID,
					BotID:       "bot-1",
					Type:        session.TypeACPAgent,
					RuntimeType: session.RuntimeACPAgent,
					RuntimeMetadata: map[string]any{
						"runtime_owner_account_id": ownerID,
					},
				}, nil
			},
		},
	}
	target := toolapproval.Request{BotID: "bot-1", SessionID: "session-1", Operation: toolapproval.OperationExec}

	if err := resolver.authorizeACPToolApprovalResponse(context.Background(), target, ToolApprovalResponseInput{ActorUserID: ownerID}); err != nil {
		t.Fatalf("owner authorization error = %v", err)
	}
	if err := resolver.authorizeACPToolApprovalResponse(context.Background(), target, ToolApprovalResponseInput{ActorUserID: managerID}); !errors.Is(err, toolapproval.ErrForbidden) {
		t.Fatalf("manager authorization error = %v, want forbidden", err)
	}
	if err := resolver.authorizeACPToolApprovalResponse(context.Background(), target, ToolApprovalResponseInput{ActorUserID: otherID}); !errors.Is(err, toolapproval.ErrForbidden) {
		t.Fatalf("other user authorization error = %v, want forbidden", err)
	}
}

func TestAuthorizeACPUserInputAllowsChatResponderWhenRuntimeOwnerStillAuthorized(t *testing.T) {
	t.Parallel()

	const (
		ownerID      = "owner-user"
		chatMemberID = "chat-member-user"
		otherID      = "other-user"
		managerID    = "manager-user"
		workspaceID  = "workspace-user"
	)
	perms := &fakeBotPermissionChecker{
		values: map[string]bool{
			"bot-1:" + ownerID + ":" + bots.PermissionWorkspaceExec:     true,
			"bot-1:" + chatMemberID + ":" + bots.PermissionChat:         true,
			"bot-1:" + workspaceID + ":" + bots.PermissionWorkspaceExec: true,
			"bot-1:" + managerID + ":" + bots.PermissionManage:          true,
		},
	}
	resolver := &Service{
		botPermissions: perms,
		sessionService: &fakeBackgroundSessionService{
			getFn: func(_ context.Context, sessionID string) (session.Thread, error) {
				return session.Thread{
					ID:          sessionID,
					BotID:       "bot-1",
					Type:        session.TypeACPAgent,
					RuntimeType: session.RuntimeACPAgent,
					RuntimeMetadata: map[string]any{
						"runtime_owner_account_id": ownerID,
					},
				}, nil
			},
		},
	}
	target := userinput.Request{BotID: "bot-1", SessionID: "session-1"}

	if err := resolver.authorizeACPUserInputResponse(context.Background(), target, UserInputResponseInput{ActorUserID: ownerID}); err != nil {
		t.Fatalf("owner authorization error = %v", err)
	}
	if err := resolver.authorizeACPUserInputResponse(context.Background(), target, UserInputResponseInput{ActorUserID: managerID}); !errors.Is(err, userinput.ErrForbidden) {
		t.Fatalf("manager authorization error = %v, want forbidden", err)
	}
	if err := resolver.authorizeACPUserInputResponse(context.Background(), target, UserInputResponseInput{ActorUserID: chatMemberID}); !errors.Is(err, userinput.ErrForbidden) {
		t.Fatalf("chat member authorization error = %v, want forbidden", err)
	}
	if err := resolver.authorizeACPUserInputResponse(context.Background(), target, UserInputResponseInput{ActorUserID: workspaceID}); !errors.Is(err, userinput.ErrForbidden) {
		t.Fatalf("workspace-only user authorization error = %v, want forbidden", err)
	}
	if err := resolver.authorizeACPUserInputResponse(context.Background(), target, UserInputResponseInput{ActorUserID: otherID}); !errors.Is(err, userinput.ErrForbidden) {
		t.Fatalf("other user authorization error = %v, want forbidden", err)
	}

	delete(perms.values, "bot-1:"+ownerID+":"+bots.PermissionWorkspaceExec)
	if err := resolver.authorizeACPUserInputResponse(context.Background(), target, UserInputResponseInput{ActorUserID: ownerID}); !errors.Is(err, userinput.ErrForbidden) {
		t.Fatalf("owner authorization with revoked workspace_exec error = %v, want forbidden", err)
	}
}

func TestStreamACPAgentWSRequestsAutoTitle(t *testing.T) {
	t.Parallel()

	sessionGets := make(chan string, 2)
	messages := &recordingMessageService{}
	pool := &recordingACPPrompter{
		result: acpclient.PromptResult{
			Text:       "done",
			StopReason: "end_turn",
		},
	}
	resolver := &Service{
		messageService: messages,
		acpPool:        pool,
		botPermissions: allowWorkspaceExecFor("user-1"),
		sessionService: &fakeBackgroundSessionService{
			getFn: func(_ context.Context, sessionID string) (session.Thread, error) {
				recordSessionGet(sessionGets, sessionID)
				return session.Thread{
					ID:    sessionID,
					BotID: "bot-1",
					Type:  session.TypeACPAgent,
					Metadata: map[string]any{
						"acp_agent_id":             "codex",
						"project_path":             "/data/app",
						"runtime_owner_account_id": "user-1",
					},
				}, nil
			},
		},
		logger: slog.New(slog.DiscardHandler),
	}

	if err := resolver.streamACPAgentWS(
		context.Background(),
		ChatRequest{
			BotID:    "bot-1",
			ThreadID: "session-1",
			Query:    "inspect the app",
		},
		make(chan WSStreamEvent, 8),
		make(chan struct{}),
	); err != nil {
		t.Fatalf("streamACPAgentWS() error = %v", err)
	}

	if pool.input.SupportsImageInput {
		t.Fatalf("ACP prompt input SupportsImageInput = true, want false for read-media tool result decoration")
	}
	waitForSessionGets(t, sessionGets, 2)
}

func TestPersistACPRoundUsesDedicatedSessionMetadata(t *testing.T) {
	t.Parallel()

	messages := &recordingMessageService{}
	resolver := &Service{
		messageService: messages,
		logger:         slog.New(slog.DiscardHandler),
	}

	err := resolver.persistACPRound(
		context.Background(),
		ChatRequest{
			BotID:    "bot-1",
			ThreadID: "session-1",
			Query:    "inspect the project",
		},
		"codex",
		"/data/app",
		withTranscriptOutput(acpclient.PromptResult{
			Text:       "done",
			StopReason: "end_turn",
		}),
		nil,
	)
	if err != nil {
		t.Fatalf("persistACPRound returned error: %v", err)
	}
	if len(messages.persisted) != 2 {
		t.Fatalf("persisted %d messages, want 2", len(messages.persisted))
	}

	assistantMeta := messages.persisted[1].Metadata
	if assistantMeta["acp_agent_id"] != "codex" {
		t.Fatalf("acp_agent_id = %#v, want codex", assistantMeta["acp_agent_id"])
	}
	if assistantMeta["project_path"] != "/data/app" {
		t.Fatalf("project_path = %#v, want /data/app", assistantMeta["project_path"])
	}
	if assistantMeta["stop_reason"] != "end_turn" {
		t.Fatalf("stop_reason = %#v, want end_turn", assistantMeta["stop_reason"])
	}
}

func TestPersistACPRoundStoresACPEventsAsNativeToolMessages(t *testing.T) {
	t.Parallel()

	messages := &recordingMessageService{}
	resolver := &Service{
		messageService: messages,
		logger:         slog.New(slog.DiscardHandler),
	}

	err := resolver.persistACPRound(
		context.Background(),
		ChatRequest{BotID: "bot-1", ThreadID: "session-1", Query: "inspect"},
		"codex",
		"/data/app",
		withTranscriptOutput(acpclient.PromptResult{
			Events: []event.StreamEvent{
				{Type: event.TextDelta, Delta: "Before"},
				{
					Type:       event.ToolCallStart,
					ToolCallID: "read-1",
					ToolName:   "read",
					Input:      map[string]any{"path": "README.md"},
				},
				{
					Type:       event.ToolCallEnd,
					ToolCallID: "read-1",
					ToolName:   "read",
					Result:     map[string]any{"ok": true},
				},
				{Type: event.TextDelta, Delta: "After"},
			},
			StopReason: "end_turn",
		}),
		nil,
	)
	if err != nil {
		t.Fatalf("persistACPRound returned error: %v", err)
	}
	if len(messages.persisted) != 4 {
		t.Fatalf("persisted %d messages, want user + assistant + tool + assistant", len(messages.persisted))
	}
	roles := []string{
		messages.persisted[0].Role,
		messages.persisted[1].Role,
		messages.persisted[2].Role,
		messages.persisted[3].Role,
	}
	if strings.Join(roles, ",") != "user,assistant,tool,assistant" {
		t.Fatalf("persisted roles = %v", roles)
	}

	before := persistedModelMessage(t, messages.persisted[1].Content)
	if got := before.TextContent(); got != "Before" {
		t.Fatalf("first assistant text = %q, want Before", got)
	}
	calls := extractAssistantToolCallParts(before)
	if len(calls) != 1 || calls[0].ToolCallID != "read-1" || calls[0].ToolName != "read" {
		t.Fatalf("assistant tool calls = %#v, want read-1/read", calls)
	}
	tool := persistedModelMessage(t, messages.persisted[2].Content)
	results := extractToolResultParts(tool)
	if len(results) != 1 || results[0].ToolCallID != "read-1" || results[0].ToolName != "read" {
		t.Fatalf("tool results = %#v, want read-1/read", results)
	}
	after := persistedModelMessage(t, messages.persisted[3].Content)
	if got := after.TextContent(); got != "After" {
		t.Fatalf("last assistant text = %q, want After", got)
	}
}

func TestPersistACPRoundStoresACPThoughtsAsReasoningParts(t *testing.T) {
	t.Parallel()

	messages := &recordingMessageService{}
	resolver := &Service{
		messageService: messages,
		logger:         slog.New(slog.DiscardHandler),
	}

	err := resolver.persistACPRound(
		context.Background(),
		ChatRequest{BotID: "bot-1", ThreadID: "session-1", Query: "inspect"},
		"codex",
		"/data/app",
		withTranscriptOutput(acpclient.PromptResult{
			Events: []event.StreamEvent{
				{Type: event.ReasoningDelta, Delta: "I should inspect first."},
				{Type: event.TextDelta, Delta: "Done"},
			},
			StopReason: "end_turn",
		}),
		nil,
	)
	if err != nil {
		t.Fatalf("persistACPRound returned error: %v", err)
	}
	if len(messages.persisted) != 2 {
		t.Fatalf("persisted %d messages, want user + assistant", len(messages.persisted))
	}
	assistant := persistedModelMessage(t, messages.persisted[1].Content)
	if got := assistant.TextContent(); got != "Done" {
		t.Fatalf("assistant text = %q, want Done", got)
	}
	parts := assistant.ContentParts()
	if len(parts) < 2 || parts[0].Type != "reasoning" || parts[0].Text != "I should inspect first." {
		t.Fatalf("assistant parts = %#v, want leading reasoning part", parts)
	}
}

func TestPersistACPRoundEmptyTextLeavesAssistantBlank(t *testing.T) {
	t.Parallel()

	messages := &recordingMessageService{}
	resolver := &Service{
		messageService: messages,
		logger:         slog.New(slog.DiscardHandler),
	}
	if err := resolver.persistACPRound(
		context.Background(),
		ChatRequest{BotID: "bot-1", ThreadID: "session-1", Query: "run"},
		"codex",
		"/data/app",
		acpclient.PromptResult{},
		nil,
	); err != nil {
		t.Fatalf("persistACPRound() error = %v", err)
	}
	if len(messages.persisted) != 2 {
		t.Fatalf("persisted %d messages, want 2", len(messages.persisted))
	}
	if got := persistedText(t, messages.persisted[1].Content); got != "" {
		t.Fatalf("assistant text = %q, want empty", got)
	}
}

func TestPersistACPRoundEmptyOutputKeepsUsage(t *testing.T) {
	t.Parallel()

	messages := &recordingMessageService{}
	resolver := &Service{
		messageService: messages,
		logger:         slog.New(slog.DiscardHandler),
	}
	if err := resolver.persistACPRound(
		context.Background(),
		ChatRequest{BotID: "bot-1", ThreadID: "session-1", Query: "run"},
		"codex",
		"/data/app",
		acpclient.PromptResult{
			Usage: &sdk.Usage{
				InputTokens:  9,
				OutputTokens: 4,
			},
		},
		nil,
	); err != nil {
		t.Fatalf("persistACPRound() error = %v", err)
	}
	if len(messages.persisted) != 2 {
		t.Fatalf("persisted %d messages, want 2", len(messages.persisted))
	}
	var usage sdk.Usage
	if err := json.Unmarshal(messages.persisted[1].Usage, &usage); err != nil {
		t.Fatalf("decode usage: %v", err)
	}
	if usage.InputTokens != 9 || usage.OutputTokens != 4 {
		t.Fatalf("usage = %+v, want input=9 output=4", usage)
	}
}

func TestStreamACPAgentWSFailurePersistsRoundAndSkipsMemory(t *testing.T) {
	t.Parallel()

	messages := &recordingMessageService{}
	memory := &storeRoundMemoryProvider{afterChat: make(chan memprovider.AfterChatRequest, 1)}
	registry := memprovider.NewRegistry(slog.New(slog.DiscardHandler))
	registry.Register(storeRoundMemoryProviderID, memory)
	pool := &recordingACPPrompter{err: errors.New("missing codex-acp")}
	resolver := &Service{
		messageService:  messages,
		memoryRegistry:  registry,
		settingsService: settings.NewService(slog.New(slog.DiscardHandler), &storeRoundSettingsQueries{}, nil, nil),
		acpPool:         pool,
		botPermissions:  allowWorkspaceExecForBot(storeRoundBotID, "user-1"),
		sessionService: &fakeBackgroundSessionService{
			getFn: func(_ context.Context, sessionID string) (session.Thread, error) {
				return session.Thread{
					ID:    sessionID,
					BotID: storeRoundBotID,
					Type:  session.TypeACPAgent,
					Metadata: map[string]any{
						"acp_agent_id":             "codex",
						"project_path":             "/data/app",
						"runtime_owner_account_id": "user-1",
					},
				}, nil
			},
		},
		logger: slog.New(slog.DiscardHandler),
	}

	eventCh := make(chan WSStreamEvent, 8)
	if err := resolver.streamACPAgentWS(
		context.Background(),
		ChatRequest{
			BotID:    storeRoundBotID,
			ThreadID: "session-1",
			Query:    "inspect",
		},
		eventCh,
		make(chan struct{}),
	); err != nil {
		t.Fatalf("streamACPAgentWS() error = %v", err)
	}

	if len(messages.persisted) != 2 {
		t.Fatalf("persisted %d messages, want user + assistant", len(messages.persisted))
	}
	if got := persistedText(t, messages.persisted[1].Content); got != "ACP agent failed to complete the turn. Please retry." {
		t.Fatalf("assistant failure text = %q, want sanitized user-facing error", got)
	}
	if got, _ := messages.persisted[1].Metadata["error"].(string); got != "ACP agent failed to complete the turn. Please retry." {
		t.Fatalf("assistant error metadata = %#v, want sanitized message", messages.persisted[1].Metadata)
	}
	if got, _ := messages.persisted[1].Metadata["error_code"].(string); got != "acp_runtime_prompt_failed" {
		t.Fatalf("assistant error code metadata = %#v", messages.persisted[1].Metadata)
	}
	events := drainAgentEvents(t, eventCh)
	abort := requireStreamEvent(t, events, native.EventAbort)
	if got := terminalAssistantText(t, abort); got != "ACP agent failed to complete the turn. Please retry." {
		t.Fatalf("terminal abort assistant text = %q, want sanitized failure", got)
	}
	select {
	case got := <-memory.afterChat:
		t.Fatalf("memory was called for ACP stream despite SkipMemory=true: %#v", got)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestStreamACPAgentWSFeedbackErrorSkipsPersistence(t *testing.T) {
	t.Parallel()

	messages := &recordingMessageService{}
	feedback := acpfeedback.New(
		acpfeedback.CodeAgentNotConfigured,
		"agent_not_configured",
		400,
		"chat.acp.agentNotConfigured",
		"External agent setup is incomplete for this bot.",
		nil,
	)
	pool := &recordingACPPrompter{err: feedback}
	resolver := &Service{
		messageService: messages,
		acpPool:        pool,
		botPermissions: allowWorkspaceExecFor("user-1"),
		sessionService: &fakeBackgroundSessionService{
			getFn: func(_ context.Context, sessionID string) (session.Thread, error) {
				return session.Thread{
					ID:    sessionID,
					BotID: "bot-1",
					Type:  session.TypeACPAgent,
					Metadata: map[string]any{
						"acp_agent_id":             "codex",
						"project_path":             "/data/app",
						"runtime_owner_account_id": "user-1",
					},
				}, nil
			},
		},
		logger: slog.New(slog.DiscardHandler),
	}

	eventCh := make(chan WSStreamEvent, 8)
	err := resolver.streamACPAgentWS(
		context.Background(),
		ChatRequest{
			BotID:    "bot-1",
			ThreadID: "session-1",
			Query:    "inspect",
		},
		eventCh,
		make(chan struct{}),
	)
	if !errors.Is(err, feedback) {
		t.Fatalf("streamACPAgentWS() error = %v, want feedback error", err)
	}
	if len(messages.persisted) != 1 || messages.persisted[0].Role != "user" {
		t.Fatalf("staged messages = %#v, want only the user turn", messages.persisted)
	}
	if len(messages.deleted) != 1 || !slices.Equal(messages.deleted[0], []string{"message-id"}) {
		t.Fatalf("cleanup calls = %#v, want staged user deletion", messages.deleted)
	}
	events := drainAgentEvents(t, eventCh)
	if !containsStreamEvent(events, native.EventStart) || containsStreamEvent(events, native.EventAbort) {
		t.Fatalf("events = %#v, want only startup event before feedback return", events)
	}
}

func TestStreamACPAgentWSImageCapabilityErrorUsesStructuredFeedback(t *testing.T) {
	t.Parallel()

	messages := &recordingMessageService{}
	resolver := &Service{
		messageService: messages,
		acpPool:        &recordingACPPrompter{err: acpclient.ErrImagePromptUnsupported},
		botPermissions: allowWorkspaceExecFor("user-1"),
		sessionService: &fakeBackgroundSessionService{
			getFn: func(_ context.Context, sessionID string) (session.Thread, error) {
				return session.Thread{
					ID:    sessionID,
					BotID: "bot-1",
					Type:  session.TypeACPAgent,
					Metadata: map[string]any{
						"acp_agent_id":             "codex",
						"project_path":             "/data/app",
						"runtime_owner_account_id": "user-1",
					},
				}, nil
			},
		},
		logger: slog.New(slog.DiscardHandler),
	}

	err := resolver.streamACPAgentWS(
		context.Background(),
		ChatRequest{
			BotID:    "bot-1",
			ThreadID: "session-1",
			Query:    "inspect",
			Attachments: []ChatAttachment{{
				Type:   "image",
				Name:   "screen.png",
				Mime:   "image/png",
				Base64: "data:image/png;base64,aW1hZ2U=",
			}},
		},
		make(chan WSStreamEvent, 8),
		make(chan struct{}),
	)
	var feedback *acpfeedback.Error
	if !errors.As(err, &feedback) || feedback.Code != acpfeedback.CodeImageInputUnsupported || feedback.I18nKey != "chat.acp.imageInputUnsupported" {
		t.Fatalf("streamACPAgentWS() error = %#v, want image capability feedback", err)
	}
	if len(messages.persisted) != 1 || messages.persisted[0].Role != "user" {
		t.Fatalf("staged messages = %#v, want only the user turn", messages.persisted)
	}
	if len(messages.deleted) != 1 || !slices.Equal(messages.deleted[0], []string{"message-id"}) {
		t.Fatalf("cleanup calls = %#v, want staged user deletion", messages.deleted)
	}
}

func TestACPPromptConfigErrorsUseApplicationErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		code apperror.Code
	}{
		{
			name: "model unsupported",
			err:  acpclient.ErrModelSelectionUnsupported,
			code: apperror.CodeACPModelSelectionUnsupported,
		},
		{
			name: "model unavailable",
			err:  fmt.Errorf("%w: stale", acpclient.ErrModelUnavailable),
			code: apperror.CodeACPModelUnavailable,
		},
		{
			name: "model required",
			err:  acpclient.ErrModelIDRequired,
			code: apperror.CodeACPModelIDRequired,
		},
		{
			name: "reasoning unsupported",
			err:  acpclient.ErrReasoningSelectionUnsupported,
			code: apperror.CodeACPReasoningUnsupported,
		},
		{
			name: "reasoning unavailable",
			err:  fmt.Errorf("%w: stale", acpclient.ErrReasoningEffortUnavailable),
			code: apperror.CodeACPReasoningUnavailable,
		},
		{
			name: "reasoning required",
			err:  acpclient.ErrReasoningEffortRequired,
			code: apperror.CodeACPReasoningEffortRequired,
		},
		{
			name: "config transport failure",
			err:  fmt.Errorf("%w: transport closed", acpagent.ErrRuntimeConfigUpdateFailed),
			code: apperror.CodeACPConfigUpdateFailed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			appErr := acpPromptConfigAppError(tt.err)
			if got := apperror.CodeOf(appErr); got != tt.code {
				t.Fatalf("acpPromptConfigAppError(%v) code = %q, want %q", tt.err, got, tt.code)
			}
		})
	}
}

func TestStreamACPAgentWSSuccessStoresMemory(t *testing.T) {
	t.Parallel()

	messages := &recordingMessageService{}
	memory := &storeRoundMemoryProvider{afterChat: make(chan memprovider.AfterChatRequest, 1)}
	registry := memprovider.NewRegistry(slog.New(slog.DiscardHandler))
	registry.Register(storeRoundMemoryProviderID, memory)
	pool := &recordingACPPrompter{
		result: withTranscriptOutput(acpclient.PromptResult{
			Events: []event.StreamEvent{{Type: event.TextDelta, Delta: "done"}},
		}),
	}
	resolver := &Service{
		messageService:  messages,
		memoryRegistry:  registry,
		settingsService: settings.NewService(slog.New(slog.DiscardHandler), &storeRoundSettingsQueries{}, nil, nil),
		acpPool:         pool,
		botPermissions:  allowWorkspaceExecForBot(storeRoundBotID, "user-1"),
		sessionService: &fakeBackgroundSessionService{
			getFn: func(_ context.Context, sessionID string) (session.Thread, error) {
				return session.Thread{
					ID:    sessionID,
					BotID: storeRoundBotID,
					Type:  session.TypeACPAgent,
					Metadata: map[string]any{
						"acp_agent_id":             "codex",
						"project_path":             "/data/app",
						"runtime_owner_account_id": "user-1",
					},
				}, nil
			},
		},
		logger: slog.New(slog.DiscardHandler),
	}

	if err := resolver.streamACPAgentWS(
		context.Background(),
		ChatRequest{
			BotID:    storeRoundBotID,
			ThreadID: "session-1",
			Query:    "inspect",
		},
		make(chan WSStreamEvent, 8),
		make(chan struct{}),
	); err != nil {
		t.Fatalf("streamACPAgentWS() error = %v", err)
	}

	select {
	case got := <-memory.afterChat:
		if len(got.Messages) != 2 {
			t.Fatalf("memory messages = %#v, want user + assistant", got.Messages)
		}
		if got.Messages[0].Role != "user" || got.Messages[0].Content != "inspect" {
			t.Fatalf("memory user message = %#v", got.Messages[0])
		}
		if got.Messages[1].Role != "assistant" || got.Messages[1].Content != "done" {
			t.Fatalf("memory assistant message = %#v", got.Messages[1])
		}
	case <-time.After(time.Second):
		t.Fatal("memory was not called for successful ACP stream")
	}
}

func TestACPFailureResultSanitizesGenericRuntimeErrors(t *testing.T) {
	t.Parallel()

	partial := acpclient.PromptResult{
		Text: "partial answer",
	}
	got, delta := acpFailureResult(partial, errors.New("adapter crashed"))
	if !strings.Contains(got.Text, "partial answer") || !strings.Contains(got.Text, "ACP agent failed to complete the turn. Please retry.") {
		t.Fatalf("acpFailureResult() = %#v, want partial output plus sanitized failure", got)
	}
	if strings.Contains(got.Text, "adapter crashed") || strings.Contains(delta, "adapter crashed") {
		t.Fatalf("generic failure leaked raw upstream error: text=%q delta=%q", got.Text, delta)
	}

	empty, delta := acpFailureResult(acpclient.PromptResult{}, errors.New("missing codex-acp"))
	if empty.Text == "" {
		t.Fatalf("empty failure result should contain a user-facing error")
	}
	if empty.Text != delta {
		t.Fatalf("empty failure result text = %q, delta = %q; want same visible text", empty.Text, delta)
	}
	if empty.Text != "ACP agent failed to complete the turn. Please retry." {
		t.Fatalf("empty failure result text = %q, want sanitized error", empty.Text)
	}
}

func TestACPResultOutputMessagesPersistsUserInputMetadata(t *testing.T) {
	t.Parallel()

	output := transcriptModelMessages(acpclient.PromptResult{
		Events: []event.StreamEvent{
			{
				Type:       event.ToolCallStart,
				ToolCallID: "mcp-http-call-1",
				ToolName:   "ask_user",
				Input:      map[string]any{"questions": []any{map[string]any{"text": "Which plan?", "kind": "single_select"}}},
			},
			{
				Type:        event.UserInputRequest,
				ToolCallID:  "mcp-http-call-1",
				ToolName:    "ask_user",
				UserInputID: "input-1",
				ShortID:     3,
				Status:      "pending",
				Metadata: map[string]any{
					"ui_payload": map[string]any{
						"version": 2,
						"questions": []any{
							map[string]any{"id": "q1", "text": "Which plan?", "kind": "single_select"},
						},
					},
				},
			},
		},
	})
	if len(output) != 1 || output[0].Role != "assistant" {
		t.Fatalf("output = %#v, want one assistant message", output)
	}
	var parts []struct {
		Type             string         `json:"type"`
		ToolCallID       string         `json:"toolCallId"`
		ProviderMetadata map[string]any `json:"providerMetadata"`
	}
	if err := json.Unmarshal(output[0].Content, &parts); err != nil {
		t.Fatalf("unmarshal assistant content: %v", err)
	}
	if len(parts) != 1 || parts[0].Type != "tool-call" || parts[0].ToolCallID != "mcp-http-call-1" {
		t.Fatalf("assistant parts = %#v", parts)
	}
	userInput, ok := parts[0].ProviderMetadata["user_input"].(map[string]any)
	if !ok {
		t.Fatalf("provider metadata = %#v, want user_input", parts[0].ProviderMetadata)
	}
	if userInput["user_input_id"] != "input-1" || userInput["status"] != "pending" {
		t.Fatalf("user_input metadata = %#v", userInput)
	}
	if _, ok := userInput["ui_payload"].(map[string]any); !ok {
		t.Fatalf("user_input ui_payload = %#v", userInput["ui_payload"])
	}
}

func TestACPResultOutputMessagesPersistsToolApprovalMetadata(t *testing.T) {
	t.Parallel()

	output := transcriptModelMessages(acpclient.PromptResult{
		Events: []event.StreamEvent{
			{
				Type:       event.ToolCallStart,
				ToolCallID: "write-1",
				ToolName:   "write",
				Input:      map[string]any{"path": "/data/review.txt"},
			},
			{
				Type:       event.ToolApprovalRequest,
				ToolCallID: "write-1",
				ToolName:   "write",
				ApprovalID: "approval-1",
				ShortID:    4,
				Status:     toolapproval.StatusPending,
			},
		},
	})
	if len(output) != 1 || output[0].Role != "assistant" {
		t.Fatalf("output = %#v, want one assistant message", output)
	}
	var parts []struct {
		Type             string         `json:"type"`
		ToolCallID       string         `json:"toolCallId"`
		ProviderMetadata map[string]any `json:"providerMetadata"`
	}
	if err := json.Unmarshal(output[0].Content, &parts); err != nil {
		t.Fatalf("unmarshal assistant content: %v", err)
	}
	if len(parts) != 1 || parts[0].Type != "tool-call" || parts[0].ToolCallID != "write-1" {
		t.Fatalf("assistant parts = %#v", parts)
	}
	approval, ok := parts[0].ProviderMetadata["approval"].(map[string]any)
	if !ok {
		t.Fatalf("provider metadata = %#v, want approval", parts[0].ProviderMetadata)
	}
	if approval["approval_id"] != "approval-1" || approval["status"] != toolapproval.StatusPending {
		t.Fatalf("approval metadata = %#v", approval)
	}
	if approval["short_id"] != float64(4) {
		t.Fatalf("approval short_id = %#v, want 4", approval["short_id"])
	}
}

func TestACPResultOutputMessagesPersistsResolvedToolApprovalMetadata(t *testing.T) {
	t.Parallel()

	output := transcriptModelMessages(acpclient.PromptResult{
		Events: []event.StreamEvent{
			{
				Type:       event.ToolCallStart,
				ToolCallID: "write-1",
				ToolName:   "write",
				Input:      map[string]any{"path": "/data/review.txt"},
			},
			{
				Type:       event.ToolApprovalRequest,
				ToolCallID: "write-1",
				ToolName:   "write",
				ApprovalID: "approval-1",
				ShortID:    4,
				Status:     toolapproval.StatusPending,
			},
			{
				Type:       event.ToolApprovalRequest,
				ToolCallID: "write-1",
				ToolName:   "write",
				ApprovalID: "approval-1",
				ShortID:    4,
				Status:     toolapproval.StatusApproved,
			},
		},
	})
	if len(output) != 1 || output[0].Role != "assistant" {
		t.Fatalf("output = %#v, want one assistant message", output)
	}
	var parts []struct {
		Type             string         `json:"type"`
		ToolCallID       string         `json:"toolCallId"`
		ProviderMetadata map[string]any `json:"providerMetadata"`
	}
	if err := json.Unmarshal(output[0].Content, &parts); err != nil {
		t.Fatalf("unmarshal assistant content: %v", err)
	}
	if len(parts) != 1 || parts[0].Type != "tool-call" || parts[0].ToolCallID != "write-1" {
		t.Fatalf("assistant parts = %#v", parts)
	}
	approval, ok := parts[0].ProviderMetadata["approval"].(map[string]any)
	if !ok {
		t.Fatalf("provider metadata = %#v, want approval", parts[0].ProviderMetadata)
	}
	if approval["approval_id"] != "approval-1" || approval["status"] != toolapproval.StatusApproved || approval["can_approve"] != false {
		t.Fatalf("approval metadata = %#v", approval)
	}
}

func TestACPResultOutputMessagesMergesApprovalBeforeToolStart(t *testing.T) {
	t.Parallel()

	output := transcriptModelMessages(acpclient.PromptResult{
		Events: []event.StreamEvent{
			{
				Type:       event.ToolApprovalRequest,
				ToolCallID: "exec-1",
				ToolName:   "exec",
				Input:      map[string]any{"command": "pwd"},
				ApprovalID: "approval-1",
				ShortID:    1,
				Status:     toolapproval.StatusPending,
			},
			{
				Type:       event.ToolCallStart,
				ToolCallID: "exec-1",
				ToolName:   "exec",
				Input:      map[string]any{"command": "pwd"},
			},
		},
	})
	if len(output) != 1 || output[0].Role != "assistant" {
		t.Fatalf("output = %#v, want one assistant message", output)
	}
	var parts []struct {
		Type             string         `json:"type"`
		ToolCallID       string         `json:"toolCallId"`
		Input            map[string]any `json:"input"`
		ProviderMetadata map[string]any `json:"providerMetadata"`
	}
	if err := json.Unmarshal(output[0].Content, &parts); err != nil {
		t.Fatalf("unmarshal assistant content: %v", err)
	}
	if len(parts) != 1 || parts[0].Type != "tool-call" || parts[0].ToolCallID != "exec-1" {
		t.Fatalf("assistant parts = %#v, want one merged tool-call", parts)
	}
	if parts[0].Input["command"] != "pwd" {
		t.Fatalf("merged input = %#v, want pwd", parts[0].Input)
	}
	if _, ok := parts[0].ProviderMetadata["approval"].(map[string]any); !ok {
		t.Fatalf("provider metadata = %#v, want approval", parts[0].ProviderMetadata)
	}
}

func TestShouldGenerateSessionTitleAllowsACPPlaceholderTitle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		sess session.Thread
		want bool
	}{
		{
			name: "empty title",
			sess: session.Thread{Type: session.TypeChat},
			want: true,
		},
		{
			name: "normal chat existing title",
			sess: session.Thread{Type: session.TypeChat, Title: "Existing"},
			want: false,
		},
		{
			name: "acp display placeholder",
			sess: session.Thread{
				Type:  session.TypeACPAgent,
				Title: "Codex",
				Metadata: map[string]any{
					"acp_agent_id": "codex",
				},
			},
			want: true,
		},
		{
			name: "acp user title",
			sess: session.Thread{
				Type:  session.TypeACPAgent,
				Title: "Real work",
				Metadata: map[string]any{
					"acp_agent_id": "codex",
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldGenerateSessionTitle(tt.sess); got != tt.want {
				t.Fatalf("shouldGenerateSessionTitle() = %v, want %v", got, tt.want)
			}
		})
	}
}

type recordingACPPrompter struct {
	calls        int
	input        acpagent.PromptInput
	result       acpclient.PromptResult
	err          error
	onPrompt     func()
	streamEvents []event.StreamEvent
	afterEvents  func()
}

type storeRoundMemoryProvider struct {
	memprovider.Provider
	afterChat chan memprovider.AfterChatRequest
}

func (*storeRoundMemoryProvider) Type() string {
	return "test"
}

func (p *storeRoundMemoryProvider) OnAfterChat(_ context.Context, req memprovider.AfterChatRequest) error {
	p.afterChat <- req
	return nil
}

type storeRoundSettingsQueries struct {
	dbstore.Queries
}

func (*storeRoundSettingsQueries) GetSettingsByBotID(_ context.Context, botID pgtype.UUID) (sqlc.GetSettingsByBotIDRow, error) {
	return sqlc.GetSettingsByBotIDRow{
		BotID:                   botID,
		Language:                "auto",
		ReasoningEffort:         "medium",
		HeartbeatInterval:       30,
		CompactionTargetPercent: pgtype.Int4{},
		MemoryProviderID:        flowTestUUID(storeRoundMemoryProviderID),
	}, nil
}

func flowTestUUID(value string) pgtype.UUID {
	var out pgtype.UUID
	if err := out.Scan(value); err != nil {
		panic(err)
	}
	return out
}

func (p *recordingACPPrompter) Prompt(_ context.Context, input acpagent.PromptInput) (acpclient.PromptResult, error) {
	p.calls++
	p.input = input
	if p.onPrompt != nil {
		p.onPrompt()
	}
	if input.Sink != nil {
		events := p.streamEvents
		if len(events) == 0 && p.err == nil {
			events = []event.StreamEvent{{Type: event.TextDelta, Delta: "streamed from acp"}}
		}
		for _, ev := range events {
			input.Sink.EmitStreamEvent(ev)
		}
	}
	if p.afterEvents != nil {
		p.afterEvents()
	}
	return p.result, p.err
}

type fakeBotPermissionChecker struct {
	values map[string]bool
	err    error
}

func allowWorkspaceExecFor(accountID string) *fakeBotPermissionChecker {
	return allowWorkspaceExecForBot("bot-1", accountID)
}

func allowWorkspaceExecForBot(botID, accountID string) *fakeBotPermissionChecker {
	return &fakeBotPermissionChecker{values: map[string]bool{
		strings.TrimSpace(botID) + ":" + strings.TrimSpace(accountID) + ":" + bots.PermissionWorkspaceExec: true,
	}}
}

func (f *fakeBotPermissionChecker) HasBotPermission(_ context.Context, botID, accountID, permission string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.values[strings.TrimSpace(botID)+":"+strings.TrimSpace(accountID)+":"+strings.TrimSpace(permission)], nil
}

func acpRuntimeSessionServiceForTest(runtimeOwnerAccountID string) *fakeBackgroundSessionService {
	return &fakeBackgroundSessionService{
		getFn: func(_ context.Context, sessionID string) (session.Thread, error) {
			return session.Thread{
				ID:    sessionID,
				BotID: "bot-1",
				Type:  session.TypeACPAgent,
				Metadata: map[string]any{
					"acp_agent_id":             "codex",
					"project_path":             "/data/app",
					"runtime_owner_account_id": strings.TrimSpace(runtimeOwnerAccountID),
				},
			}, nil
		},
	}
}

func drainAgentEvents(t *testing.T, eventCh <-chan WSStreamEvent) []native.StreamEvent {
	t.Helper()
	events := make([]native.StreamEvent, 0, len(eventCh))
	for len(eventCh) > 0 {
		var event native.StreamEvent
		if err := json.Unmarshal(<-eventCh, &event); err != nil {
			t.Fatalf("decode stream event: %v", err)
		}
		events = append(events, event)
	}
	return events
}

func drainStreamChunks(t *testing.T, chunkCh <-chan StreamChunk) []native.StreamEvent {
	t.Helper()
	var events []native.StreamEvent
	for chunk := range chunkCh {
		var event native.StreamEvent
		if err := json.Unmarshal(chunk, &event); err != nil {
			t.Fatalf("decode stream chunk: %v", err)
		}
		events = append(events, event)
	}
	return events
}

func containsStreamEvent(events []native.StreamEvent, eventType native.StreamEventType) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

func requireStreamEvent(t *testing.T, events []native.StreamEvent, eventType native.StreamEventType) native.StreamEvent {
	t.Helper()
	for _, event := range events {
		if event.Type == eventType {
			return event
		}
	}
	t.Fatalf("events = %#v, want %s", events, eventType)
	return native.StreamEvent{}
}

func terminalAssistantText(t *testing.T, event native.StreamEvent) string {
	t.Helper()
	var messages []ModelMessage
	if err := json.Unmarshal(event.Messages, &messages); err != nil {
		t.Fatalf("decode terminal messages: %v", err)
	}
	for _, msg := range messages {
		if msg.Role == "assistant" {
			return strings.TrimSpace(msg.TextContent())
		}
	}
	t.Fatalf("terminal messages = %#v, want assistant message", messages)
	return ""
}

func containsTextDelta(events []native.StreamEvent, delta string) bool {
	for _, event := range events {
		if event.Type == native.EventTextDelta && event.Delta == delta {
			return true
		}
	}
	return false
}

func recordSessionGet(ch chan<- string, sessionID string) {
	select {
	case ch <- sessionID:
	default:
	}
}

func waitForSessionGets(t *testing.T, ch <-chan string, want int) {
	t.Helper()
	deadline := time.After(time.Second)
	for count := 0; count < want; count++ {
		select {
		case <-ch:
		case <-deadline:
			t.Fatalf("observed %d session Get calls, want %d", count, want)
		}
	}
}

func persistedText(t *testing.T, content json.RawMessage) string {
	t.Helper()
	return persistedModelMessage(t, content).TextContent()
}

func persistedModelMessage(t *testing.T, content json.RawMessage) ModelMessage {
	t.Helper()
	var msg ModelMessage
	if err := json.Unmarshal(content, &msg); err != nil {
		t.Fatalf("decode persisted content: %v", err)
	}
	return msg
}

func toolCallMetadataStatus(call sdk.ToolCallPart, key string) string {
	raw, ok := call.ProviderMetadata[key].(map[string]any)
	if !ok {
		return ""
	}
	status, _ := raw["status"].(string)
	return status
}

func recordedMessages(inputs []messagepkg.PersistInput) []messagepkg.Message {
	messages := make([]messagepkg.Message, 0, len(inputs))
	for _, input := range inputs {
		messages = append(messages, messagepkg.Message{
			BotID:          input.BotID,
			SessionID:      input.SessionID,
			Role:           input.Role,
			Content:        input.Content,
			DisplayContent: input.DisplayText,
			Metadata:       input.Metadata,
			TurnID:         firstNonEmpty(input.TurnID, "test-turn"),
		})
	}
	return messages
}

// transcriptModelMessages builds model messages from streamed ACP events.
func transcriptModelMessages(result acpclient.PromptResult) []ModelMessage {
	output := sdkMessagesToModelMessages(acpclient.TranscriptFromEvents(result.Events, result.Text))
	if len(output) == 0 {
		return []ModelMessage{{Role: "assistant", Content: newTextContent("")}}
	}
	return output
}

// withTranscriptOutput fills PromptResult.Output from streamed events.
func withTranscriptOutput(result acpclient.PromptResult) acpclient.PromptResult {
	result.Output = acpclient.TranscriptFromEvents(result.Events, result.Text)
	return result
}
