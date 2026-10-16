package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	chatview "github.com/felinics/memoh/internal/agent/view"
	"github.com/felinics/memoh/internal/apperror"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/schedule"
)

// A failed External Agent round records the code classifyRuntimeFailure gives
// its cause, and nothing beside it.
func TestPersistRuntimeRoundFailureCode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		cause    error
		wantCode string
		wantArgs map[string]any
	}{
		{name: "catalogued code", cause: apperror.Wrap(apperror.CodeAgentResponseTimeout, errors.New("SECRET"), nil), wantCode: "agent.response_timeout"},
		{name: "external agent code", cause: fmt.Errorf("prompt: %w", apperror.New(apperror.CodeACPRuntimeBusy, nil)), wantCode: "acp_runtime_busy"},
		{name: "plain error", cause: errors.New("SECRET driver exit"), wantCode: "runtime_prompt_failed"},
		{
			name:     "missing dependency",
			cause:    &external.DependencyMissingError{DependencyID: "codex", TaskID: "task-1"},
			wantCode: "agent_dependency_missing",
			wantArgs: map[string]any{"dep_id": "codex", "install_task_id": "task-1", "operation_in_progress": "false"},
		},
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
			stored, _ := json.Marshal(meta[messagepkg.HistoryErrorArgsMetadataKey])
			var gotArgs map[string]any
			_ = json.Unmarshal(stored, &gotArgs)
			if !reflect.DeepEqual(gotArgs, tc.wantArgs) {
				t.Fatalf("error_args = %s, want %v", stored, tc.wantArgs)
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
	}, ch, make(chan struct{}), true)
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

// The cause an External Agent turn hands to its result record names the
// runtime that failed, so one query over the records finds every External
// Agent failure whatever code it took.
func TestStreamRuntimeFailureNamesItsRuntime(t *testing.T) {
	service := newACPLifecycleService(t, &recordingACPPrompter{}, &recordingMessageService{}, &recordingContextLifecycleStore{})
	driver := noticeTestDriver{kind: "codex", prompt: func(context.Context, external.PromptInput) (external.PromptResult, error) {
		return external.PromptResult{Output: []sdk.Message{sdk.AssistantMessage("partial")}}, errors.New("driver exit")
	}}
	outcome, err := service.streamRuntimeWS(context.Background(), driver, ChatRequest{
		BotID: lifecycleTestBotID, ThreadID: lifecycleTestSessionID, RunID: lifecycleTestRunID, Query: "test",
	}, make(chan WSStreamEvent, 64), make(chan struct{}), true)
	if err != nil {
		t.Fatalf("streamRuntimeWS() error = %v", err)
	}
	attrs := errs.Analyze(context.Background(), outcome.Cause).Attrs
	if !slices.ContainsFunc(attrs, func(attr slog.Attr) bool { return attr.Key == "runtime" && attr.Value.String() == "codex" }) {
		t.Fatalf("result record attrs = %v, want runtime=codex", attrs)
	}
}

// A new Web send whose External Agent fails on configuration before anything
// ran, such as a runtime the workspace has not installed, keeps the user's
// message and records the failure under the code its run ends with. The
// history is written before the failure frame is sent, so a client that reloads
// the session on that frame reads both.
func TestStreamRuntimeWebConfigurationFailureIsInHistoryBeforeItsFrame(t *testing.T) {
	messages := &recordingMessageService{}
	service := newACPLifecycleService(t, &recordingACPPrompter{}, messages, &recordingContextLifecycleStore{})
	driver := noticeTestDriver{kind: "codex", prompt: func(context.Context, external.PromptInput) (external.PromptResult, error) {
		return external.PromptResult{}, &external.DependencyMissingError{DependencyID: "codex"}
	}}
	ch := make(chan WSStreamEvent)
	reloaded := make(chan []messagepkg.PersistInput, 1)
	var frameCode string
	var textDeltas []string
	go func() {
		defer close(reloaded)
		for data := range ch {
			var ev native.StreamEvent
			if err := json.Unmarshal(data, &ev); err != nil {
				t.Errorf("decode stream event: %v", err)
				return
			}
			if ev.Type == native.EventTextDelta {
				textDeltas = append(textDeltas, ev.Delta)
			}
			if ev.Type == native.EventError {
				frameCode = ev.Code
				reloaded <- slices.Clone(messages.persisted)
			}
		}
	}()
	outcome, err := service.streamRuntimeWS(context.Background(), driver, ChatRequest{
		BotID: lifecycleTestBotID, ThreadID: lifecycleTestSessionID, RunID: lifecycleTestRunID, Query: "inspect",
	}, ch, make(chan struct{}), true)
	close(ch)
	if err != nil {
		t.Fatalf("streamRuntimeWS() error = %v, want the failure delivered in the stream", err)
	}
	persistedAtFrame, framed := <-reloaded
	if !framed {
		t.Fatal("no failure frame was sent")
	}
	turns := historyTurns(t, persistedAtFrame)
	want := string(apperror.CodeAgentDependencyMissing)
	if frameCode != want || outcome.ErrorCode() != want {
		t.Fatalf("frame code = %q, outcome code = %q, want %q", frameCode, outcome.ErrorCode(), want)
	}
	if len(messages.deleted) != 0 {
		t.Fatalf("deleted = %v, want the user's message kept", messages.deleted)
	}
	var userText, historyCode string
	for _, turn := range turns {
		if turn.Role == "user" {
			userText = turn.Text
		}
		for _, block := range turn.Messages {
			switch block.Type {
			case chatview.UIMessageError:
				historyCode = block.Code
			case chatview.UIMessageText:
				t.Fatalf("history at the failure frame has assistant text %q, want only the failure block", block.Content)
			}
		}
	}
	if userText != "inspect" || historyCode != want {
		t.Fatalf("history at the failure frame: user text = %q, failure code = %q, want %q / %q", userText, historyCode, "inspect", want)
	}
	for range reloaded {
	}
	if len(textDeltas) != 0 {
		t.Fatalf("text deltas = %q, want the failure sent only as its error frame", textDeltas)
	}
}

// A turn that failed after some output keeps that output as it was, with the
// failure block after it. The failure reaches the stream as one error event
// and adds no text to the round.
func TestStreamRuntimeFailureKeepsPartialOutputUnchanged(t *testing.T) {
	messages := &recordingMessageService{}
	service := newACPLifecycleService(t, &recordingACPPrompter{}, messages, &recordingContextLifecycleStore{})
	driver := noticeTestDriver{kind: "codex", prompt: func(context.Context, external.PromptInput) (external.PromptResult, error) {
		return external.PromptResult{Output: []sdk.Message{sdk.AssistantMessage("partial answer")}},
			apperror.New(apperror.CodeACPRuntimeBusy, nil)
	}}
	ch := make(chan WSStreamEvent, 64)
	if _, err := service.streamRuntimeWS(context.Background(), driver, ChatRequest{
		BotID: lifecycleTestBotID, ThreadID: lifecycleTestSessionID, RunID: lifecycleTestRunID, Query: "test",
	}, ch, make(chan struct{}), true); err != nil {
		t.Fatalf("streamRuntimeWS() error = %v", err)
	}
	var failures []string
	for _, ev := range drainAgentEvents(t, ch) {
		switch ev.Type {
		case native.EventTextDelta:
			t.Fatalf("text delta %q, want the failure sent only as its error event", ev.Delta)
		case native.EventError:
			failures = append(failures, ev.Code)
		case native.EventAbort:
			if got := terminalAssistantText(t, ev); got != "partial answer" {
				t.Fatalf("terminal assistant text = %q, want the partial output alone", got)
			}
		}
	}
	const want = "acp_runtime_busy"
	if !slices.Equal(failures, []string{want}) {
		t.Fatalf("failure events = %q, want one %q", failures, want)
	}
	var texts []string
	var historyCode string
	for _, turn := range historyTurns(t, messages.persisted) {
		if turn.Role != "assistant" {
			continue
		}
		for _, block := range turn.Messages {
			switch block.Type {
			case chatview.UIMessageText:
				texts = append(texts, block.Content)
			case chatview.UIMessageError:
				historyCode = block.Code
			}
		}
	}
	if !slices.Equal(texts, []string{"partial answer"}) || historyCode != want {
		t.Fatalf("history assistant texts = %q, failure code = %q, want [partial answer] / %q", texts, historyCode, want)
	}
}

// A failed scheduled round is stored the same way: the output the runtime
// produced, or an empty assistant row, carrying the failure code.
func TestScheduledRuntimeFailureRoundHasNoFailureText(t *testing.T) {
	for _, tc := range []struct {
		name     string
		output   []sdk.Message
		wantText string
	}{
		{name: "no output"},
		{name: "partial output", output: []sdk.Message{sdk.AssistantMessage("partial answer")}, wantText: "partial answer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messages := &recordingMessageService{}
			service := newACPLifecycleService(t, &recordingACPPrompter{}, messages, &recordingContextLifecycleStore{})
			driver := noticeTestDriver{kind: "codex", prompt: func(context.Context, external.PromptInput) (external.PromptResult, error) {
				return external.PromptResult{Output: tc.output}, apperror.New(apperror.CodeACPRuntimeBusy, nil)
			}}
			if _, err := service.triggerScheduleRuntime(context.Background(), lifecycleTestBotID, schedule.TriggerPayload{
				SessionID: lifecycleTestSessionID, Command: "inspect", OwnerUserID: "user-1",
			}, "", lifecycleTestRunID, driver); apperror.CodeOf(err) != apperror.CodeACPRuntimeBusy {
				t.Fatalf("schedule error = %v, want %s", err, apperror.CodeACPRuntimeBusy)
			}
			last := messages.persisted[len(messages.persisted)-1]
			if last.Role != "assistant" || persistedText(t, last.Content) != tc.wantText {
				t.Fatalf("last persisted = %s %q, want assistant %q", last.Role, persistedText(t, last.Content), tc.wantText)
			}
			if got, _ := last.Metadata["error_code"].(string); got != string(apperror.CodeACPRuntimeBusy) {
				t.Fatalf("error_code = %#v, want %s", last.Metadata["error_code"], apperror.CodeACPRuntimeBusy)
			}
		})
	}
}

// The schedule log stores the text of the error a fire returns, and the API
// serves it. A scheduled External Agent failure therefore reads as its code
// alone, while the agent's own words stay behind it for the result record.
func TestScheduledRuntimeFailureTextIsItsCode(t *testing.T) {
	service := newACPLifecycleService(t, &recordingACPPrompter{}, &recordingMessageService{}, &recordingContextLifecycleStore{})
	driver := noticeTestDriver{kind: "codex", prompt: func(context.Context, external.PromptInput) (external.PromptResult, error) {
		return external.PromptResult{Output: []sdk.Message{sdk.AssistantMessage("partial")}}, errors.New("SECRET stderr tail")
	}}
	_, err := service.triggerScheduleRuntime(context.Background(), lifecycleTestBotID, schedule.TriggerPayload{
		SessionID: lifecycleTestSessionID, Command: "inspect", OwnerUserID: "user-1",
	}, "", lifecycleTestRunID, driver)
	if err == nil || err.Error() != string(apperror.CodeRuntimePromptFailed) {
		t.Fatalf("schedule error text = %q, want exactly %q", err, apperror.CodeRuntimePromptFailed)
	}
	if text := errs.Text(err); !strings.Contains(text, "SECRET") {
		t.Fatalf("result record text = %q, want it to keep the agent's words", text)
	}
}

// An IM turn that fails on configuration before anything ran leaves no round:
// the user's message is removed and the failure goes to the caller.
func TestStreamRuntimeChunksConfigurationFailureLeavesNoRound(t *testing.T) {
	messages := &recordingMessageService{}
	service := newACPLifecycleService(t, &recordingACPPrompter{}, messages, &recordingContextLifecycleStore{})
	driver := noticeTestDriver{kind: "codex", prompt: func(context.Context, external.PromptInput) (external.PromptResult, error) {
		return external.PromptResult{}, &external.DependencyMissingError{DependencyID: "codex"}
	}}
	chunkCh := make(chan StreamChunk, 64)
	var failed error
	service.streamRuntimeChunks(context.Background(), driver, ChatRequest{
		BotID: lifecycleTestBotID, ThreadID: lifecycleTestSessionID, RunID: lifecycleTestRunID, Query: "inspect",
	}, chunkCh, func(err error) { failed = err })
	close(chunkCh)
	if apperror.CodeOf(failed) != apperror.CodeAgentDependencyMissing {
		t.Fatalf("failure = %v, want %s", failed, apperror.CodeAgentDependencyMissing)
	}
	if len(messages.persisted) != 1 || messages.persisted[0].Role != "user" || len(messages.deleted) != 1 {
		t.Fatalf("persisted = %+v deleted = %v, want only the user message, then removed", messages.persisted, messages.deleted)
	}
	for _, ev := range drainStreamChunks(t, chunkCh) {
		if ev.Type == native.EventError {
			t.Fatalf("stream carried a failure frame %q, want the failure returned", ev.Code)
		}
	}
}

// historyTurns is the session history a client reloads, read from the rows
// persisted so far.
func historyTurns(t *testing.T, persisted []messagepkg.PersistInput) []chatview.UITurn {
	t.Helper()
	rows := make([]messagepkg.Message, 0, len(persisted))
	for i, input := range persisted {
		metadata, err := json.Marshal(input.Metadata)
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, messagepkg.Message{ID: fmt.Sprintf("message-%d", i), TurnID: lifecycleTestRunID, Role: input.Role, Content: input.Content, DisplayContent: input.DisplayText, RawMetadata: metadata})
	}
	return chatview.ConvertMessagesToUITurns(rows)
}

// A completed External Agent turn, or one an IM channel started that failed on
// configuration, delivered no failure in the stream, so its outcome is left
// unnamed.
func TestStreamRuntimeOutcomeWithoutDeliveredFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result external.PromptResult
		err    error
	}{
		{name: "completed", result: external.PromptResult{Output: []sdk.Message{sdk.AssistantMessage("done")}, TurnCompleted: true}},
		{name: "configuration failure", err: external.Unavailable(errors.New("bridge down"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := newACPLifecycleService(t, &recordingACPPrompter{}, &recordingMessageService{}, &recordingContextLifecycleStore{})
			driver := noticeTestDriver{kind: "codex", prompt: func(context.Context, external.PromptInput) (external.PromptResult, error) {
				return tc.result, tc.err
			}}
			ch := make(chan WSStreamEvent, 64)
			outcome, _ := service.streamRuntimeWS(context.Background(), driver, ChatRequest{
				BotID: lifecycleTestBotID, ThreadID: lifecycleTestSessionID, RunID: lifecycleTestRunID, Query: "test",
			}, ch, nil, false)
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
