package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/apperror"
)

// A failed External Agent round records the code classifyRuntimeFailure gives
// its cause, and nothing beside it.
func TestPersistRuntimeRoundFailureCode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		cause    error
		wantCode string
	}{
		{name: "catalogued code", cause: apperror.Wrap(apperror.CodeAgentResponseTimeout, errors.New("SECRET"), nil), wantCode: "agent.response_timeout"},
		{name: "external agent code", cause: fmt.Errorf("prompt: %w", apperror.New(apperror.CodeACPRuntimeBusy, nil)), wantCode: "acp_runtime_busy"},
		{name: "plain error", cause: errors.New("SECRET driver exit"), wantCode: "runtime_prompt_failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			messages := &recordingMessageService{}
			service := &Service{messageService: messages, logger: slog.New(slog.DiscardHandler)}
			if err := service.persistRuntimeRound(
				context.Background(),
				ChatRequest{BotID: "bot-1", ThreadID: "session-1", Query: "run"},
				"codex",
				"/data/app",
				external.PromptResult{},
				tc.cause,
				false,
				nil,
				nil,
			); err != nil {
				t.Fatalf("persistRuntimeRound() error = %v", err)
			}
			if len(messages.persisted) != 2 {
				t.Fatalf("persisted %d messages, want user + assistant", len(messages.persisted))
			}
			meta := messages.persisted[1].Metadata
			if got, _ := meta["error_code"].(string); got != tc.wantCode {
				t.Fatalf("error_code = %q, want %q", got, tc.wantCode)
			}
			for _, key := range []string{"error_reason", "i18n_key"} {
				if got, found := meta[key]; found {
					t.Fatalf("%s = %#v, want it absent", key, got)
				}
			}
		})
	}
}

// X5: an External Agent turn that ran and then failed with an External Agent
// code. The live failure event, the terminal event and the history marker all
// carry that code, and the outcome names it for the run's terminal write.
func TestStreamRuntimeExternalAgentFailureCarriesOneCode(t *testing.T) {
	messages := &recordingMessageService{}
	service := newACPLifecycleService(t, &recordingACPPrompter{}, messages, &recordingContextLifecycleStore{})
	driver := noticeTestDriver{kind: "codex", prompt: func(context.Context, external.PromptInput) (external.PromptResult, error) {
		return external.PromptResult{Output: []sdk.Message{sdk.AssistantMessage("partial")}},
			apperror.New(apperror.CodeACPRuntimeBusy, nil)
	}}
	ch := make(chan WSStreamEvent, 64)
	outcome, err := service.streamRuntimeWS(context.Background(), driver, ChatRequest{
		BotID: lifecycleTestBotID, ThreadID: lifecycleTestSessionID, RunID: lifecycleTestRunID, Query: "test",
	}, ch, make(chan struct{}))
	if err != nil {
		t.Fatalf("streamRuntimeWS() error = %v", err)
	}
	var failureCode, terminalCode string
	for _, ev := range drainAgentEvents(t, ch) {
		switch {
		case ev.Type == native.EventError:
			failureCode = ev.Code
		case ev.IsTerminal():
			terminalCode = ev.Code
		}
	}
	const want = "acp_runtime_busy"
	if failureCode != want || terminalCode != want {
		t.Fatalf("failure event code = %q, terminal event code = %q, want %q", failureCode, terminalCode, want)
	}
	if outcome.Status != "errored" || outcome.ErrorCode() != want {
		t.Fatalf("outcome = %q / %q, want errored / %q", outcome.Status, outcome.ErrorCode(), want)
	}
	if got, _ := messages.persisted[len(messages.persisted)-1].Metadata["error_code"].(string); got != want {
		t.Fatalf("history error_code = %q, want %q", got, want)
	}
}

// A completed or configuration-failed External Agent turn delivered no failure
// in the stream, so its outcome is left unnamed.
func TestStreamRuntimeOutcomeWithoutDeliveredFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result external.PromptResult
		err    error
	}{
		{name: "completed", result: external.PromptResult{Output: []sdk.Message{sdk.AssistantMessage("done")}, TurnCompleted: true}},
		{name: "configuration failure", err: apperror.New(apperror.CodeExternalRuntimeUnavailable, nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := newACPLifecycleService(t, &recordingACPPrompter{}, &recordingMessageService{}, &recordingContextLifecycleStore{})
			driver := noticeTestDriver{kind: "codex", prompt: func(context.Context, external.PromptInput) (external.PromptResult, error) {
				return tc.result, tc.err
			}}
			ch := make(chan WSStreamEvent, 64)
			outcome, _ := service.streamRuntimeWS(context.Background(), driver, ChatRequest{
				BotID: lifecycleTestBotID, ThreadID: lifecycleTestSessionID, RunID: lifecycleTestRunID, Query: "test",
			}, ch, make(chan struct{}))
			for _, ev := range drainAgentEvents(t, ch) {
				if ev.IsTerminal() && ev.Code != "" {
					t.Fatalf("terminal event code = %q, want none", ev.Code)
				}
			}
			if outcome != (RunOutcome{}) {
				t.Fatalf("outcome = %+v, want unnamed", outcome)
			}
		})
	}
}
