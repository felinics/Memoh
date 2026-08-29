package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/bots"
	session "github.com/felinics/memoh/internal/chat/thread"
)

const testACPUserInputOwnerID = "owner-user"

type fakeUserInputService struct {
	target   userinput.Request
	resolved userinput.Request

	submitCalls   int
	cancelCalls   int
	createCalls   int
	submitted     userinput.SubmitInput
	canceled      userinput.CancelInput
	submitErr     error
	cancelErr     error
	submitHook    func()
	canRespond    bool
	canRespondSet bool
}

func (*fakeUserInputService) AdvanceText(context.Context, userinput.AdvanceTextInput) (userinput.AdvanceTextResult, error) {
	return userinput.AdvanceTextResult{}, errors.New("unexpected AdvanceText")
}

func (f *fakeUserInputService) CreatePending(context.Context, userinput.CreatePendingInput) (userinput.Request, error) {
	f.createCalls++
	return userinput.Request{}, errors.New("unexpected CreatePending")
}

func (f *fakeUserInputService) ResolveTarget(context.Context, userinput.ResolveInput) (userinput.Request, error) {
	return f.target, nil
}

func (f *fakeUserInputService) Submit(_ context.Context, input userinput.SubmitInput) (userinput.Request, error) {
	f.submitCalls++
	f.submitted = input
	if f.submitHook != nil {
		f.submitHook()
	}
	if f.submitErr != nil {
		return userinput.Request{}, f.submitErr
	}
	return f.resolved, nil
}

func (f *fakeUserInputService) Cancel(_ context.Context, input userinput.CancelInput) (userinput.Request, error) {
	f.cancelCalls++
	f.canceled = input
	if f.cancelErr != nil {
		return userinput.Request{}, f.cancelErr
	}
	return f.resolved, nil
}

func (f *fakeUserInputService) CanRespond(req userinput.Request) bool {
	if f.canRespondSet {
		return f.canRespond
	}
	if userinput.IsProcessLocalACPRequest(req) {
		return false
	}
	return req.Status == userinput.StatusPending
}

func chatResolvedRequest() userinput.Request {
	return userinput.Request{
		ID:         "input-1",
		SessionID:  "session-1",
		ToolCallID: "call-1",
		ToolName:   userinput.ToolNameAskUser,
		Status:     userinput.StatusSubmitted,
		Result: map[string]any{
			"status": userinput.StatusSubmitted,
			"answers": []any{
				map[string]any{"question_id": "q1", "selected": []any{map[string]any{"id": "q1.o1", "label": "Plan A"}}},
			},
		},
	}
}

// The continuation path (prepareContinuationRunConfig) builds its outgoing
// messages via repairToolCallClosures(nonNilModelMessages(sanitizeMessages(...))).
// This asserts that composition closes an orphaned older ask_user call while
// preserving the current call's real result — the reason a process restart
// mid-ask_user no longer breaks strict assistant-tool adjacency.
func TestUserInputContinuationHistoryClosesOlderPendingCall(t *testing.T) {
	t.Parallel()

	oldCall := sdkMessagesToModelMessages([]sdk.Message{{
		Role: sdk.MessageRoleAssistant,
		Content: []sdk.MessagePart{sdk.ToolCallPart{
			ToolCallID: "old-call",
			ToolName:   userinput.ToolNameAskUser,
		}},
	}})[0]
	currentCall := sdkMessagesToModelMessages([]sdk.Message{{
		Role: sdk.MessageRoleAssistant,
		Content: []sdk.MessagePart{sdk.ToolCallPart{
			ToolCallID: "current-call",
			ToolName:   userinput.ToolNameAskUser,
		}},
	}})[0]
	currentResult := sdkMessagesToModelMessages([]sdk.Message{sdk.ToolMessage(sdk.ToolResultPart{
		ToolCallID: "current-call",
		ToolName:   userinput.ToolNameAskUser,
		Result:     map[string]any{"status": userinput.StatusSubmitted},
	})})[0]

	input := nonNilModelMessages(sanitizeMessages([]ModelMessage{
		oldCall,
		{Role: "user", Content: newTextContent("start another ask_user")},
		currentCall,
		currentResult,
	}))
	got := repairToolCallClosures(input, syntheticToolClosureError)
	if len(got) != 5 {
		t.Fatalf("history length = %d, want 5: %#v", len(got), got)
	}
	oldResults := extractToolResultParts(got[1])
	if len(oldResults) != 1 || oldResults[0].ToolCallID != "old-call" || !oldResults[0].IsError {
		t.Fatalf("old call closure = %#v", oldResults)
	}
	currentResults := extractToolResultParts(got[4])
	if len(currentResults) != 1 || currentResults[0].ToolCallID != "current-call" || currentResults[0].IsError {
		t.Fatalf("current result = %#v", currentResults)
	}
}

func collectAgentStreamEvents(t *testing.T, ch <-chan WSStreamEvent, count int) []native.StreamEvent {
	t.Helper()
	events := make([]native.StreamEvent, 0, count)
	timeout := time.After(2 * time.Second)
	for len(events) < count {
		select {
		case raw := <-ch:
			var ev native.StreamEvent
			if err := json.Unmarshal(raw, &ev); err != nil {
				t.Fatalf("unmarshal stream event: %v", err)
			}
			events = append(events, ev)
		case <-timeout:
			t.Fatalf("timed out waiting for stream event %d/%d", len(events)+1, count)
		}
	}
	return events
}

func attachACPUserInputAuth(resolver *Service) {
	resolver.botPermissions = &fakeBotPermissionChecker{
		values: map[string]bool{
			"bot-1:" + testACPUserInputOwnerID + ":" + bots.PermissionWorkspaceExec: true,
		},
	}
	resolver.sessionService = &fakeBackgroundSessionService{
		getFn: func(_ context.Context, sessionID string) (session.Thread, error) {
			return session.Thread{
				ID:          sessionID,
				BotID:       "bot-1",
				Type:        session.TypeACPAgent,
				RuntimeType: session.RuntimeACPAgent,
				RuntimeMetadata: map[string]any{
					"runtime_owner_account_id": testACPUserInputOwnerID,
				},
			}, nil
		},
	}
}

func TestUserInputAnswersFromText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		payload    userinput.UIPayload
		text       string
		wantAnswer userinput.QuestionAnswer
		wantErr    bool
	}{
		{
			name: "text question",
			payload: userinput.UIPayload{Questions: []userinput.UIQuestion{{
				ID:   "q1",
				Kind: userinput.QuestionKindText,
			}}},
			text:       "ship it",
			wantAnswer: userinput.QuestionAnswer{QuestionID: "q1", Text: "ship it"},
		},
		{
			name: "single select by label",
			payload: userinput.UIPayload{Questions: []userinput.UIQuestion{{
				ID:   "q1",
				Kind: userinput.QuestionKindSingleSelect,
				Options: []userinput.UIOption{
					{ID: "q1.o1", Label: "Plan A"},
					{ID: "q1.o2", Label: "Plan B"},
				},
			}}},
			text:       "plan b",
			wantAnswer: userinput.QuestionAnswer{QuestionID: "q1", OptionIDs: []string{"q1.o2"}},
		},
		{
			name: "multi select by labels",
			payload: userinput.UIPayload{Questions: []userinput.UIQuestion{{
				ID:   "q1",
				Kind: userinput.QuestionKindMultiSelect,
				Options: []userinput.UIOption{
					{ID: "q1.o1", Label: "One"},
					{ID: "q1.o2", Label: "Two"},
				},
			}}},
			text:       "One, Two",
			wantAnswer: userinput.QuestionAnswer{QuestionID: "q1", OptionIDs: []string{"q1.o1", "q1.o2"}},
		},
		{
			name: "custom select answer",
			payload: userinput.UIPayload{Questions: []userinput.UIQuestion{{
				ID:          "q1",
				Kind:        userinput.QuestionKindSingleSelect,
				AllowCustom: true,
				Options:     []userinput.UIOption{{ID: "q1.o1", Label: "Known"}},
			}}},
			text:       "Something else",
			wantAnswer: userinput.QuestionAnswer{QuestionID: "q1", CustomText: "Something else"},
		},
		{
			name: "multiple questions unsupported",
			payload: userinput.UIPayload{Questions: []userinput.UIQuestion{
				{ID: "q1", Kind: userinput.QuestionKindText},
				{ID: "q2", Kind: userinput.QuestionKindText},
			}},
			text:    "answer",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := userInputAnswersFromText(tt.payload, tt.text)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("userInputAnswersFromText() error = %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("answers = %#v, want one", got)
			}
			if got[0].QuestionID != tt.wantAnswer.QuestionID || got[0].Text != tt.wantAnswer.Text || got[0].CustomText != tt.wantAnswer.CustomText {
				t.Fatalf("answer = %#v, want %#v", got[0], tt.wantAnswer)
			}
			if strings.Join(got[0].OptionIDs, ",") != strings.Join(tt.wantAnswer.OptionIDs, ",") {
				t.Fatalf("option ids = %#v, want %#v", got[0].OptionIDs, tt.wantAnswer.OptionIDs)
			}
		})
	}
}

func TestRespondUserInputContinuesChatSession(t *testing.T) {
	t.Parallel()

	fake := &fakeUserInputService{
		target:   userinput.Request{ID: "input-1", Status: userinput.StatusPending},
		resolved: chatResolvedRequest(),
	}
	var continued *sdk.ToolResultPart
	resolver := &Service{
		userInput: fake,
		continueUserInputFn: func(_ context.Context, req userinput.Request, _ UserInputResponseInput, result sdk.ToolResultPart, _ chan<- WSStreamEvent) error {
			if req.ID != "input-1" {
				t.Errorf("continued request = %#v", req)
			}
			continued = &result
			return nil
		},
	}

	eventCh := make(chan WSStreamEvent, 4)
	answers := []userinput.QuestionAnswer{{QuestionID: "q1", OptionIDs: []string{"q1.o1"}}}
	err := resolver.respondUserInput(context.Background(), UserInputResponseInput{
		BotID:    "bot-1",
		ThreadID: "session-1",
		Answers:  answers,
	}, eventCh)
	if err != nil {
		t.Fatalf("respond user input: %v", err)
	}

	if fake.submitCalls != 1 || fake.cancelCalls != 0 {
		t.Fatalf("submit/cancel calls = %d/%d", fake.submitCalls, fake.cancelCalls)
	}
	if fake.submitted.RequestID != "input-1" || len(fake.submitted.Answers) != 1 || fake.submitted.Answers[0].QuestionID != "q1" {
		t.Fatalf("submitted input = %#v", fake.submitted)
	}
	if continued == nil {
		t.Fatal("chat request must continue the session")
	}
	if continued.ToolCallID != "call-1" || continued.ToolName != userinput.ToolNameAskUser {
		t.Fatalf("continued tool result = %#v", continued)
	}
	if len(eventCh) != 0 {
		t.Fatalf("chat continuation must not emit ack events, got %d", len(eventCh))
	}
}

func TestRuntimeUserInputCommandCommitsAndResumesSameRun(t *testing.T) {
	t.Parallel()

	const (
		botID     = "bot-1"
		sessionID = "session-1"
		runID     = "run-1"
		inputID   = "input-1"
	)
	fake := &fakeUserInputService{
		target: userinput.Request{
			ID:         inputID,
			BotID:      botID,
			SessionID:  sessionID,
			ToolCallID: "call-1",
			ToolName:   userinput.ToolNameAskUser,
			Status:     userinput.StatusPending,
		},
		resolved: chatResolvedRequest(),
	}
	releaseContinuation := make(chan struct{})
	continuationStarted := make(chan struct{})
	var continuationStartedOnce sync.Once
	resolver := &Service{
		userInput: fake,
		continueUserInputFn: func(ctx context.Context, _ userinput.Request, _ UserInputResponseInput, _ sdk.ToolResultPart, eventCh chan<- WSStreamEvent) error {
			continuationStartedOnce.Do(func() { close(continuationStarted) })
			select {
			case <-releaseContinuation:
			case <-ctx.Done():
				return ctx.Err()
			}
			if err := sendAgentStreamEvent(ctx, eventCh, native.StreamEvent{Type: native.EventAgentStart}); err != nil {
				return err
			}
			return sendAgentStreamEvent(ctx, eventCh, native.StreamEvent{Type: native.EventAgentEnd})
		},
	}
	manager := sessionruntime.NewManager(sessionruntime.NewMemoryBackend(), sessionruntime.Options{
		OwnerID:       "owner-1",
		StateTTL:      time.Minute,
		OwnerLeaseTTL: time.Second,
		CommandAckTTL: time.Second,
	})
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start runtime manager: %v", err)
	}
	resolver.SetSessionRuntime(manager)
	if err := manager.StartRun(
		context.Background(),
		botID,
		sessionID,
		runID,
		make(chan struct{}, 1),
		func() {},
		make(chan turn.InjectMessage, 1),
	); err != nil {
		t.Fatalf("start runtime run: %v", err)
	}
	handle := sessionruntime.RunHandle{
		BotID:     botID,
		SessionID: sessionID,
		RunID:     runID,
	}
	snapshot, err := manager.Snapshot(context.Background(), botID, sessionID)
	if err != nil || snapshot.CurrentRunView == nil {
		t.Fatalf("load runtime run: %#v, %v", snapshot.CurrentRunView, err)
	}
	handle.Generation = snapshot.CurrentRunView.Generation
	if _, err := manager.HandleAgentEvent(context.Background(), handle, native.StreamEvent{
		Type:        native.EventUserInputRequest,
		ToolName:    userinput.ToolNameAskUser,
		ToolCallID:  "call-1",
		UserInputID: inputID,
		Status:      "pending",
	}); err != nil {
		t.Fatalf("publish user input request: %v", err)
	}

	payload, err := json.Marshal(UserInputResponseInput{
		BotID:       botID,
		ThreadID:    sessionID,
		UserInputID: inputID,
		ExplicitID:  inputID,
		Answers:     []userinput.QuestionAnswer{{QuestionID: "q1", Text: "continue"}},
	})
	if err != nil {
		t.Fatalf("encode response: %v", err)
	}
	handled, err := manager.DispatchRunCommand(
		context.Background(),
		botID,
		sessionID,
		runID,
		sessionruntime.CommandUserInputResponse,
		inputID,
		payload,
	)
	if err != nil || !handled {
		t.Fatalf("dispatch response = handled:%v err:%v", handled, err)
	}
	if fake.submitCalls != 1 {
		t.Fatalf("submit calls = %d, want 1 before acknowledgement", fake.submitCalls)
	}

	snapshot, err = manager.Snapshot(context.Background(), botID, sessionID)
	if err != nil || snapshot.CurrentRunView == nil {
		t.Fatalf("load acknowledged decision snapshot: %#v, %v", snapshot.CurrentRunView, err)
	}
	if snapshot.CurrentRunView.Status != sessionruntime.RunStatusWaitingDecision {
		t.Fatalf("run status before continuation = %q, want waiting_decision", snapshot.CurrentRunView.Status)
	}
	var projectedStatus string
	var projectedCanRespond bool
	for _, message := range snapshot.CurrentRunView.Messages {
		if message.UserInput != nil && message.UserInput.UserInputID == inputID {
			projectedStatus = message.UserInput.Status
			projectedCanRespond = message.UserInput.CanRespond
			break
		}
	}
	if projectedStatus != userinput.StatusSubmitted || projectedCanRespond {
		t.Fatalf("acknowledged decision projection = status:%q can_respond:%v, want submitted/false", projectedStatus, projectedCanRespond)
	}

	select {
	case <-continuationStarted:
		t.Fatal("continuation started before the deferred producer finished persistence")
	case <-time.After(25 * time.Millisecond):
	}
	if err := manager.FinishRun(context.Background(), handle, "", ""); err != nil {
		t.Fatalf("park deferred producer: %v", err)
	}
	select {
	case <-continuationStarted:
	case <-time.After(time.Second):
		t.Fatal("continuation did not start after the deferred producer finished")
	}
	close(releaseContinuation)
	deadline := time.Now().Add(time.Second)
	for {
		snapshot, err = manager.Snapshot(context.Background(), botID, sessionID)
		if err != nil {
			t.Fatalf("load completed run: %v", err)
		}
		if snapshot.CurrentRunView != nil && snapshot.CurrentRunView.Status == sessionruntime.RunStatusCompleted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("resumed run did not complete: %#v", snapshot.CurrentRunView)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRespondUserInputLimitsChatToolResult(t *testing.T) {
	t.Parallel()

	large := "HEAD\n" + strings.Repeat("answer detail ", 300) + "\nTAIL"
	resolved := chatResolvedRequest()
	resolved.Result = map[string]any{
		"status": userinput.StatusSubmitted,
		"answers": []any{
			map[string]any{"question_id": "q1", "text": large},
		},
		"instruction": "Continue with the answer.",
	}
	fake := &fakeUserInputService{
		target:   userinput.Request{ID: "input-1", Status: userinput.StatusPending},
		resolved: resolved,
	}
	var continued *sdk.ToolResultPart
	resolver := &Service{
		agent:     native.New(native.Deps{Limits: native.Limits{ToolOutputMaxBytes: 512, ToolOutputMaxLines: 80}}),
		userInput: fake,
		continueUserInputFn: func(_ context.Context, _ userinput.Request, _ UserInputResponseInput, result sdk.ToolResultPart, _ chan<- WSStreamEvent) error {
			continued = &result
			return nil
		},
	}

	err := resolver.respondUserInput(context.Background(), UserInputResponseInput{
		BotID:    "bot-1",
		ThreadID: "session-1",
		Answers:  []userinput.QuestionAnswer{{QuestionID: "q1", Text: large}},
	}, nil)
	if err != nil {
		t.Fatalf("respond user input: %v", err)
	}
	if continued == nil {
		t.Fatal("chat request must continue the session")
	}
	result, ok := continued.Result.(map[string]any)
	if !ok {
		t.Fatalf("continued result = %#v, want map", continued.Result)
	}
	answers, ok := result["answers"].([]any)
	if !ok || len(answers) != 1 {
		t.Fatalf("continued answers = %#v, want one answer", result["answers"])
	}
	answer, ok := answers[0].(map[string]any)
	if !ok {
		t.Fatalf("continued answer = %#v, want map", answers[0])
	}
	text, ok := answer["text"].(string)
	if !ok {
		t.Fatalf("continued answer text = %#v, want string", answer["text"])
	}
	if len(text) >= len(large) {
		t.Fatalf("answer text was not pruned: got %d bytes, original %d", len(text), len(large))
	}
	if !strings.Contains(text, "[memoh pruned]") {
		t.Fatalf("answer text missing prune marker:\n%s", text)
	}
}

func TestRespondUserInputOnlyAcksACPRequests(t *testing.T) {
	t.Parallel()

	resolved := chatResolvedRequest()
	resolved.ProviderMetadata = map[string]any{"source": userinput.ProviderSourceACPMCP}
	fake := &fakeUserInputService{
		target:        userinput.Request{ID: "input-1", Status: userinput.StatusPending, ProviderMetadata: map[string]any{"source": userinput.ProviderSourceACPMCP}},
		resolved:      resolved,
		canRespond:    true,
		canRespondSet: true,
	}
	resolver := &Service{
		userInput: fake,
		continueUserInputFn: func(context.Context, userinput.Request, UserInputResponseInput, sdk.ToolResultPart, chan<- WSStreamEvent) error {
			t.Error("ACP request must not continue the chat session; the blocked waiter resumes it")
			return nil
		},
	}
	attachACPUserInputAuth(resolver)

	eventCh := make(chan WSStreamEvent, 4)
	err := resolver.respondUserInput(context.Background(), UserInputResponseInput{
		BotID:       "bot-1",
		ThreadID:    "session-1",
		ActorUserID: testACPUserInputOwnerID,
		Answers:     []userinput.QuestionAnswer{{QuestionID: "q1", OptionIDs: []string{"q1.o1"}}},
	}, eventCh)
	if err != nil {
		t.Fatalf("respond user input: %v", err)
	}
	if fake.submitCalls != 1 {
		t.Fatalf("submit calls = %d", fake.submitCalls)
	}
	// emitApprovalAck sends agent start + end so the client stream settles.
	if len(eventCh) != 2 {
		t.Fatalf("ack events = %d, want 2", len(eventCh))
	}
}

func TestRespondUserInputAcksAlreadyDecidedACPRequest(t *testing.T) {
	t.Parallel()

	fake := &fakeUserInputService{
		target: userinput.Request{
			ID:               "input-1",
			Status:           userinput.StatusPending,
			ProviderMetadata: map[string]any{"source": userinput.ProviderSourceACPMCP},
		},
		submitErr:     userinput.ErrAlreadyDecided,
		canRespond:    true,
		canRespondSet: true,
	}
	resolver := &Service{
		userInput: fake,
		continueUserInputFn: func(context.Context, userinput.Request, UserInputResponseInput, sdk.ToolResultPart, chan<- WSStreamEvent) error {
			t.Error("already-decided ACP request must not continue the chat session")
			return nil
		},
	}
	attachACPUserInputAuth(resolver)

	eventCh := make(chan WSStreamEvent, 4)
	err := resolver.respondUserInput(context.Background(), UserInputResponseInput{
		BotID:       "bot-1",
		ThreadID:    "session-1",
		ActorUserID: testACPUserInputOwnerID,
		Answers:     []userinput.QuestionAnswer{{QuestionID: "q1", OptionIDs: []string{"q1.o1"}}},
	}, eventCh)
	if err != nil {
		t.Fatalf("respond user input: %v", err)
	}
	if fake.submitCalls != 1 || fake.cancelCalls != 0 {
		t.Fatalf("submit/cancel calls = %d/%d", fake.submitCalls, fake.cancelCalls)
	}
	if len(eventCh) != 2 {
		t.Fatalf("ack events = %d, want 2", len(eventCh))
	}
}

func TestRespondUserInputACPRequestSubmitsWithLiveWaiter(t *testing.T) {
	t.Parallel()

	resolved := chatResolvedRequest()
	resolved.ProviderMetadata = map[string]any{"source": userinput.ProviderSourceACPMCP}
	fake := &fakeUserInputService{
		target: userinput.Request{
			ID:               "input-1",
			Status:           userinput.StatusPending,
			ProviderMetadata: map[string]any{"source": userinput.ProviderSourceACPMCP},
		},
		resolved:      resolved,
		canRespond:    true,
		canRespondSet: true,
	}
	resolver := &Service{
		userInput: fake,
		continueUserInputFn: func(context.Context, userinput.Request, UserInputResponseInput, sdk.ToolResultPart, chan<- WSStreamEvent) error {
			t.Error("ACP request must not continue the session in this response handler")
			return nil
		},
	}
	attachACPUserInputAuth(resolver)

	eventCh := make(chan WSStreamEvent, 4)
	err := resolver.respondUserInput(context.Background(), UserInputResponseInput{
		BotID:       "bot-1",
		ThreadID:    "session-1",
		ActorUserID: testACPUserInputOwnerID,
		Answers:     []userinput.QuestionAnswer{{QuestionID: "q1", OptionIDs: []string{"q1.o1"}}},
	}, eventCh)
	if err != nil {
		t.Fatalf("respond user input: %v", err)
	}
	if fake.submitCalls != 1 {
		t.Fatalf("submit calls = %d, want 1", fake.submitCalls)
	}
	if fake.cancelCalls != 0 {
		t.Fatalf("cancel calls = %d, want 0", fake.cancelCalls)
	}
	if len(eventCh) != 2 {
		t.Fatalf("ack events = %d, want 2", len(eventCh))
	}
}

func TestRespondUserInputACPRequestReattachesActivePrompt(t *testing.T) {
	t.Parallel()

	resolved := chatResolvedRequest()
	resolved.ProviderMetadata = map[string]any{"source": userinput.ProviderSourceACPMCP}
	submitted := make(chan struct{})
	fake := &fakeUserInputService{
		target: userinput.Request{
			ID:               "input-1",
			SessionID:        "session-1",
			ToolCallID:       "call-1",
			Status:           userinput.StatusPending,
			ProviderMetadata: map[string]any{"source": userinput.ProviderSourceACPMCP},
		},
		resolved:      resolved,
		submitHook:    func() { close(submitted) },
		canRespond:    true,
		canRespondSet: true,
	}
	resolver := &Service{
		userInput: fake,
		continueUserInputFn: func(context.Context, userinput.Request, UserInputResponseInput, sdk.ToolResultPart, chan<- WSStreamEvent) error {
			t.Error("ACP request must resume through the active ACP prompt")
			return nil
		},
	}
	attachACPUserInputAuth(resolver)
	hub := resolver.registerACPActivePrompt("bot-1", "session-1")
	if hub == nil {
		t.Fatal("expected active ACP prompt hub")
	}
	defer resolver.unregisterACPActivePrompt("bot-1", "session-1", hub)

	eventCh := make(chan WSStreamEvent, 8)
	done := make(chan error, 1)
	go func() {
		done <- resolver.respondUserInput(context.Background(), UserInputResponseInput{
			BotID:       "bot-1",
			ThreadID:    "session-1",
			ActorUserID: testACPUserInputOwnerID,
			Answers:     []userinput.QuestionAnswer{{QuestionID: "q1", OptionIDs: []string{"q1.o1"}}},
		}, eventCh)
	}()

	select {
	case <-submitted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for submit")
	}
	hub.emit(native.StreamEvent{
		Type:        native.EventUserInputRequest,
		ToolCallID:  "call-1",
		ToolName:    userinput.ToolNameAskUser,
		UserInputID: "input-1",
		Status:      userinput.StatusSubmitted,
	})
	hub.emit(native.StreamEvent{
		Type:       native.EventToolCallEnd,
		ToolCallID: "call-1",
		ToolName:   userinput.ToolNameAskUser,
		Result:     map[string]any{"status": userinput.StatusSubmitted},
	})
	hub.emit(native.StreamEvent{Type: native.EventTextDelta, Delta: "continuing"})
	hub.emit(native.StreamEvent{Type: native.EventEnd})

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("respond user input: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ACP prompt reattach")
	}

	events := collectAgentStreamEvents(t, eventCh, 3)
	if events[0].Type != native.EventStart {
		t.Fatalf("first event = %q, want %q", events[0].Type, native.EventStart)
	}
	if events[1].Type != native.EventTextDelta || events[1].Delta != "continuing" {
		t.Fatalf("second event = %#v, want text delta", events[1])
	}
	if events[2].Type != native.EventEnd {
		t.Fatalf("third event = %q, want %q", events[2].Type, native.EventEnd)
	}
	if fake.submitCalls != 1 || fake.cancelCalls != 0 {
		t.Fatalf("submit/cancel calls = %d/%d", fake.submitCalls, fake.cancelCalls)
	}
}

func TestRespondUserInputACPRequestCanSuppressActivePromptReattach(t *testing.T) {
	t.Parallel()

	resolved := chatResolvedRequest()
	resolved.ProviderMetadata = map[string]any{"source": userinput.ProviderSourceACPMCP}
	fake := &fakeUserInputService{
		target: userinput.Request{
			ID:               "input-1",
			SessionID:        "session-1",
			Status:           userinput.StatusPending,
			ProviderMetadata: map[string]any{"source": userinput.ProviderSourceACPMCP},
		},
		resolved:      resolved,
		canRespond:    true,
		canRespondSet: true,
	}
	resolver := &Service{
		userInput: fake,
		continueUserInputFn: func(context.Context, userinput.Request, UserInputResponseInput, sdk.ToolResultPart, chan<- WSStreamEvent) error {
			t.Error("ACP request must not continue the chat session")
			return nil
		},
	}
	attachACPUserInputAuth(resolver)
	hub := resolver.registerACPActivePrompt("bot-1", "session-1")
	if hub == nil {
		t.Fatal("expected active ACP prompt hub")
	}
	defer resolver.unregisterACPActivePrompt("bot-1", "session-1", hub)

	eventCh := make(chan WSStreamEvent, 4)
	err := resolver.respondUserInput(context.Background(), UserInputResponseInput{
		BotID:                      "bot-1",
		ThreadID:                   "session-1",
		ActorUserID:                testACPUserInputOwnerID,
		Answers:                    []userinput.QuestionAnswer{{QuestionID: "q1", OptionIDs: []string{"q1.o1"}}},
		SuppressActivePromptAttach: true,
	}, eventCh)
	if err != nil {
		t.Fatalf("respond user input: %v", err)
	}
	if fake.submitCalls != 1 || fake.cancelCalls != 0 {
		t.Fatalf("submit/cancel calls = %d/%d", fake.submitCalls, fake.cancelCalls)
	}
	if len(eventCh) != 2 {
		t.Fatalf("ack events = %d, want 2", len(eventCh))
	}
}

func TestRespondUserInputACPRequestWithoutWaiterCancelsInsteadOfSubmitting(t *testing.T) {
	t.Parallel()

	resolved := chatResolvedRequest()
	resolved.ProviderMetadata = map[string]any{"source": userinput.ProviderSourceACPMCP}
	fake := &fakeUserInputService{
		target: userinput.Request{
			ID:               "input-1",
			Status:           userinput.StatusPending,
			ProviderMetadata: map[string]any{"source": userinput.ProviderSourceACPMCP},
		},
		resolved: resolved,
	}
	resolver := &Service{
		userInput: fake,
		continueUserInputFn: func(context.Context, userinput.Request, UserInputResponseInput, sdk.ToolResultPart, chan<- WSStreamEvent) error {
			t.Error("orphaned ACP request must not continue the chat session")
			return nil
		},
	}
	attachACPUserInputAuth(resolver)

	eventCh := make(chan WSStreamEvent, 4)
	err := resolver.respondUserInput(context.Background(), UserInputResponseInput{
		BotID:       "bot-1",
		ThreadID:    "session-1",
		ActorUserID: testACPUserInputOwnerID,
		Answers:     []userinput.QuestionAnswer{{QuestionID: "q1", OptionIDs: []string{"q1.o1"}}},
	}, eventCh)
	if err != nil {
		t.Fatalf("respond user input: %v", err)
	}
	if fake.submitCalls != 0 {
		t.Fatalf("submit calls = %d, want 0", fake.submitCalls)
	}
	if fake.cancelCalls != 1 {
		t.Fatalf("cancel calls = %d, want 1", fake.cancelCalls)
	}
	if fake.canceled.RequestID != "input-1" || fake.canceled.Reason == "" {
		t.Fatalf("canceled input = %#v", fake.canceled)
	}
	if len(eventCh) != 2 {
		t.Fatalf("ack events = %d, want 2", len(eventCh))
	}
}

func TestRespondUserInputCancelRoutesToCancel(t *testing.T) {
	t.Parallel()

	fake := &fakeUserInputService{
		target:   userinput.Request{ID: "input-1", Status: userinput.StatusPending},
		resolved: chatResolvedRequest(),
	}
	continueCalls := 0
	resolver := &Service{
		userInput: fake,
		continueUserInputFn: func(context.Context, userinput.Request, UserInputResponseInput, sdk.ToolResultPart, chan<- WSStreamEvent) error {
			continueCalls++
			return nil
		},
	}

	err := resolver.respondUserInput(context.Background(), UserInputResponseInput{
		BotID:    "bot-1",
		ThreadID: "session-1",
		Canceled: true,
		Reason:   "user_canceled",
	}, nil)
	if err != nil {
		t.Fatalf("respond user input: %v", err)
	}
	if fake.cancelCalls != 1 || fake.submitCalls != 0 {
		t.Fatalf("cancel/submit calls = %d/%d", fake.cancelCalls, fake.submitCalls)
	}
	if fake.canceled.RequestID != "input-1" || fake.canceled.Reason != "user_canceled" {
		t.Fatalf("canceled input = %#v", fake.canceled)
	}
	if continueCalls != 1 {
		t.Fatalf("canceled chat request must still continue the session, calls = %d", continueCalls)
	}
}
