package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	chatview "github.com/felinics/memoh/internal/agent/view"
	"github.com/felinics/memoh/internal/apperror"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/schedule"
)

type noticeTestDriver struct {
	kind   string
	prompt func(context.Context, external.PromptInput) (external.PromptResult, error)
}

func (d noticeTestDriver) RuntimeType() string { return d.kind }
func (d noticeTestDriver) Prompt(ctx context.Context, input external.PromptInput) (external.PromptResult, error) {
	return d.prompt(ctx, input)
}

func TestRuntimeNoticesSurviveTerminalAndHistory(t *testing.T) {
	for _, runtime := range []string{"codex", "claude-code", "acp_agent"} {
		for _, outcome := range []string{"completed", "failed", "stopped_during_setup", "configuration_failure"} {
			t.Run(runtime+"/"+outcome, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				messages := &recordingMessageService{}
				service := newACPLifecycleService(t, &recordingACPPrompter{}, messages, &recordingContextLifecycleStore{})
				driver := noticeTestDriver{kind: runtime, prompt: func(_ context.Context, input external.PromptInput) (external.PromptResult, error) {
					notice := event.StreamEvent{
						Type: event.RuntimeNotice, Code: "tools_unavailable", Delta: "public runtime notice",
						Metadata: map[string]any{"dep_id": "codex", "diagnostic": map[string]any{"private": "SECRET"}}, Error: "SECRET",
					}
					// The application must collect notices even before a driver has
					// created any transcript, and coalesce repeated delivery.
					input.Sink.EmitStreamEvent(notice)
					input.Sink.EmitStreamEvent(notice)
					if outcome == "stopped_during_setup" {
						cancel()
						return external.PromptResult{}, apperror.New(apperror.CodeExternalRuntimeUnavailable, nil)
					}
					if outcome == "configuration_failure" {
						return external.PromptResult{}, apperror.New(apperror.CodeExternalRuntimeUnavailable, nil)
					}
					result := external.PromptResult{Output: []sdk.Message{sdk.AssistantMessage("first"), sdk.AssistantMessage("last")}, TurnCompleted: outcome == "completed"}
					if outcome == "failed" {
						return result, errors.New("SECRET")
					}
					return result, nil
				}}
				ch := make(chan WSStreamEvent, 64)
				if err := service.streamRuntimeWS(ctx, driver, ChatRequest{BotID: lifecycleTestBotID, ThreadID: lifecycleTestSessionID, RunID: lifecycleTestRunID, Query: "test"}, ch, make(chan struct{})); err != nil {
					t.Fatal(err)
				}
				if len(messages.deleted) != 0 {
					t.Fatalf("a notice-bearing round was removed: %v", messages.deleted)
				}
				assertPersistedRuntimeNotice(t, messages)
				liveNotices, terminalNotices := 0, 0
				for _, ev := range drainAgentEvents(t, ch) {
					if ev.Type == event.RuntimeNotice {
						liveNotices++
					}
					if ev.IsTerminal() {
						terminalNotices += len(event.NoticesFromMetadata(ev.Metadata))
						if strings.Contains(string(ev.Messages), "public runtime notice") {
							t.Fatal("notice entered the model transcript")
						}
					}
				}
				if liveNotices != 1 || terminalNotices != 1 {
					t.Fatalf("live notices=%d terminal notices=%d, want one each", liveNotices, terminalNotices)
				}
			})
		}
	}
}

func assertPersistedRuntimeNotice(t *testing.T, messages *recordingMessageService) {
	t.Helper()
	var rows []messagepkg.Message
	count := 0
	for i, input := range messages.persisted {
		metadata, err := json.Marshal(input.Metadata)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(metadata), "SECRET") || strings.Contains(string(input.Content), "public runtime notice") {
			t.Fatal("private diagnostics persisted or notice leaked into model content")
		}
		count += len(event.NoticesFromMetadata(input.Metadata))
		// Exercise the lazy RawMetadata path used by the PostgreSQL history API.
		rows = append(rows, messagepkg.Message{ID: fmt.Sprintf("message-%d", i), TurnID: lifecycleTestRunID, Role: input.Role, Content: input.Content, RawMetadata: metadata})
	}
	if count != 1 {
		t.Fatalf("persisted notices=%d, want exactly one across all assistant rows", count)
	}
	count = 0
	for _, turn := range chatview.ConvertMessagesToUITurns(rows) {
		for _, block := range turn.Messages {
			if block.Type == chatview.UIMessageNotice && block.Name == "tools_unavailable" && block.Content == "public runtime notice" {
				count++
			}
		}
	}
	if count != 1 {
		t.Fatalf("reloaded UI notices=%d, want one", count)
	}
}

func TestScheduledRuntimeNoticeIsPersistedWithoutInteractiveSubscriber(t *testing.T) {
	messages := &recordingMessageService{}
	service := newACPLifecycleService(t, &recordingACPPrompter{}, messages, &recordingContextLifecycleStore{})
	driver := noticeTestDriver{kind: "codex", prompt: func(_ context.Context, input external.PromptInput) (external.PromptResult, error) {
		input.Sink.EmitStreamEvent(event.StreamEvent{Type: event.RuntimeNotice, Code: "tools_unavailable", Delta: "public runtime notice"})
		return external.PromptResult{Text: "done", Output: []sdk.Message{sdk.AssistantMessage("done")}, TurnCompleted: true}, nil
	}}
	_, err := service.triggerScheduleRuntime(context.Background(), lifecycleTestBotID, schedule.TriggerPayload{SessionID: lifecycleTestSessionID, Command: "test", OwnerUserID: "user-1"}, "", lifecycleTestRunID, driver)
	if err != nil {
		t.Fatal(err)
	}
	assertPersistedRuntimeNotice(t, messages)
}
