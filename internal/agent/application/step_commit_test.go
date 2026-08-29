package application

import (
	"context"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/uuid"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	sessionqueue "github.com/felinics/memoh/internal/agent/runtime/session/queue"
	"github.com/felinics/memoh/internal/agent/turn"
	chatview "github.com/felinics/memoh/internal/agent/view"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/runtimefence"
)

type recordingStepPersister struct {
	*recordingMessageService
	steps                  []messagepkg.AgentStep
	published              []messagepkg.Message
	replacementFinalized   int
	replacementRequestID   string
	replacementAssistantID string
}

func (s *recordingStepPersister) PublishAgentStep(messages []messagepkg.Message) {
	s.published = append(s.published, messages...)
}

func (s *recordingStepPersister) PersistAgentStep(_ context.Context, step messagepkg.AgentStep) ([]messagepkg.Message, error) {
	s.steps = append(s.steps, step)
	result := make([]messagepkg.Message, len(step.Messages))
	for i, input := range step.Messages {
		result[i] = messagepkg.Message{ID: "committed", Role: input.Role}
	}
	return result, nil
}

func (s *recordingStepPersister) PersistAgentStepTx(ctx context.Context, _ dbstore.Queries, step messagepkg.AgentStep) ([]messagepkg.Message, error) {
	return s.PersistAgentStep(ctx, step)
}

func (s *recordingStepPersister) PersistAgentReplacementStepTx(ctx context.Context, _ dbstore.Queries, step messagepkg.AgentStep) ([]messagepkg.Message, error) {
	return s.PersistAgentStep(ctx, step)
}

func (s *recordingStepPersister) FinalizeAgentReplacementTx(
	_ context.Context,
	_ dbstore.Queries,
	_ string,
	_ messagepkg.TurnReplacement,
	requestMessageID string,
	assistantMessageID string,
) error {
	s.replacementFinalized++
	s.replacementRequestID = requestMessageID
	s.replacementAssistantID = assistantMessageID
	return nil
}

type stepCommitAdmissionStub struct{ runID string }

func (s stepCommitAdmissionStub) AdmitQueue(context.Context, string, string) (sessionqueue.Admission, error) {
	return sessionqueue.Admission{RunID: s.runID, Active: true}, nil
}

func TestAgentStepCommitterPersistsOnlyStepDelta(t *testing.T) {
	botID, sessionID, runID, turnID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	position := int64(4)
	store := &recordingStepPersister{recordingMessageService: &recordingMessageService{}}
	service := &Service{messageService: store}
	holder := contextfrag.NewLifecycleHolder()
	holder.SetManifest(contextfrag.Manifest{View: contextfrag.ViewRunConfigPreProvider})
	req := ChatRequest{BotID: botID, ThreadID: sessionID, RunID: runID, TurnID: turnID, TurnPosition: &position, Query: "hello", SkipMemoryExtraction: true}
	ctx := runtimefence.WithContext(context.Background(), runtimefence.Fence{BotID: botID, SessionID: sessionID, Token: 7})
	rc := resolvedContext{model: models.GetResponse{ID: uuid.NewString()}}
	rc.runConfig.ContextLifecycle = holder
	committer := service.newAgentStepCommitter(ctx, req, rc)
	if committer == nil {
		t.Fatal("step committer was not enabled for an admitted fenced turn")
	}
	clock := newReasoningTimingTestClock()
	committer.reasoningTiming = newReasoningTimingTracker(clock.read)
	for i, text := range []string{"first", "second"} {
		if err := committer.commit(ctx, i, &sdk.StepResult{Messages: []sdk.Message{sdk.AssistantMessage(text)}}); err != nil {
			t.Fatalf("commit step %d: %v", i, err)
		}
	}
	partial := sdk.Message{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{sdk.ReasoningPart{Text: "partial reasoning"}}}
	committer.reasoningTiming.observe(native.StreamEvent{Type: native.EventReasoningDelta, Delta: "partial reasoning"})
	clock.advance(1500 * time.Millisecond)
	if err := committer.interrupt(ctx, 2, &sdk.StepResult{Messages: []sdk.Message{partial}}); err != nil {
		t.Fatalf("persist interrupted step: %v", err)
	}
	if len(store.steps) != 3 || len(store.steps[0].Messages) != 2 || len(store.steps[1].Messages) != 1 || !store.steps[2].Interrupted {
		t.Fatalf("persisted steps = %#v, want two complete plus one interrupted", store.steps)
	}
	if store.steps[2].Messages[0].Metadata[messagepkg.AgentStepInterruptedMetadataKey] != true {
		t.Fatalf("interrupted metadata = %#v", store.steps[2].Messages[0].Metadata)
	}
	timings := messagepkg.ReasoningTimingFromMetadata(store.steps[2].Messages[0].Metadata)
	if len(timings) != 1 || timings[0].DurationMS != 1500 || timings[0].State != "interrupted" {
		t.Fatalf("interrupted reasoning timing = %#v", timings)
	}
	if _, ok := store.steps[0].Messages[1].Metadata[contextfrag.MetadataContextLifecycleKey]; !ok {
		t.Fatalf("first step lifecycle metadata = %#v", store.steps[0].Messages[1].Metadata)
	}
	snapshot, ok := holder.Snapshot()
	if !ok || snapshot.AssistantMessageID != "committed" {
		t.Fatalf("lifecycle snapshot = %#v, set = %v", snapshot, ok)
	}
	if got := store.steps[1].Messages[0].TurnRequestMessageID; got != "committed" {
		t.Fatalf("second step request message = %q, want first committed user", got)
	}
	if len(committer.messages) != 3 {
		t.Fatalf("memory messages = %d, want interrupted output excluded", len(committer.messages))
	}
}

func TestReplacementRunConsumesSteerThenHandsOffFollowUpAtTrueFinal(t *testing.T) {
	botID, sessionID, runID, turnID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	position := int64(8)
	handle := sessionruntime.RunHandle{
		BotID: botID, SessionID: sessionID, RunID: runID,
		OwnerID: "owner-1", Generation: "generation-1", FencingToken: 9,
	}
	steer := sessionqueue.NewSteerQueue(stepCommitAdmissionStub{runID: runID})
	followUp := sessionqueue.NewFollowUpQueue(stepCommitAdmissionStub{runID: runID})
	if _, err := steer.Enqueue(context.Background(), botID, sessionID, "steer-retry", []byte(`{"text":"change direction"}`)); err != nil {
		t.Fatalf("enqueue steer: %v", err)
	}
	if _, err := followUp.Enqueue(context.Background(), botID, sessionID, "follow-retry", []byte(`{"text":"continue afterward"}`)); err != nil {
		t.Fatalf("enqueue follow-up: %v", err)
	}

	store := &recordingStepPersister{recordingMessageService: &recordingMessageService{}}
	service := &Service{
		messageService:   store,
		queueCoordinator: sessionqueue.NewMemoryCoordinator(steer, followUp),
	}
	holder := contextfrag.NewLifecycleHolder()
	holder.SetManifest(contextfrag.Manifest{View: contextfrag.ViewRunConfigPreProvider})
	rc := resolvedContext{model: models.GetResponse{ID: uuid.NewString()}}
	rc.runConfig.ContextLifecycle = holder
	req := ChatRequest{
		BotID: botID, ThreadID: sessionID, RunID: runID, RunHandle: handle,
		TurnID: turnID, TurnPosition: &position, Query: "edited request",
		SkipHistoryTurn: true, SkipMemoryExtraction: true,
		TurnReplacement: &messagepkg.TurnReplacement{
			OldTurnID: uuid.NewString(), ReplacementTurnID: turnID,
			ReplacementTurnPosition: &position, Reason: "edit",
		},
	}
	ctx := runtimefence.WithContext(context.Background(), runtimefence.Fence{BotID: botID, SessionID: sessionID, Token: 9})
	committer := service.newAgentStepCommitter(ctx, req, rc)
	if committer == nil {
		t.Fatal("replacement run did not receive a durable step committer")
	}
	committer.reasoningTiming = newReasoningTimingTracker(nil)

	if err := committer.commit(ctx, 0, &sdk.StepResult{Messages: []sdk.Message{sdk.AssistantMessage("first final")}}); err != nil {
		t.Fatalf("commit replacement steer boundary: %v", err)
	}
	if store.replacementFinalized != 0 {
		t.Fatal("replacement history finalized while a steer kept R0 active")
	}
	steerItem, ok := steer.Get("steer-retry")
	if !ok || steerItem.Status != sessionqueue.Claimed {
		t.Fatalf("steer after first boundary = %#v, want claimed", steerItem)
	}

	if err := committer.commit(ctx, 1, &sdk.StepResult{Messages: []sdk.Message{sdk.AssistantMessage("true final")}}); err != nil {
		t.Fatalf("commit replacement final handoff: %v", err)
	}
	steerItem, _ = steer.Get("steer-retry")
	if steerItem.Status != sessionqueue.Applied {
		t.Fatalf("steer after true final = %#v, want applied", steerItem)
	}
	followItem, ok := followUp.Get("follow-retry")
	if !ok || followItem.Status != sessionqueue.Accepted || followItem.AssignedRunID == "" {
		t.Fatalf("follow-up handoff = %#v, want assigned continuation", followItem)
	}
	if store.replacementFinalized != 1 || store.replacementRequestID == "" || store.replacementAssistantID == "" {
		t.Fatalf("replacement finalization = %d request=%q assistant=%q", store.replacementFinalized, store.replacementRequestID, store.replacementAssistantID)
	}
}

func TestRetryReplacementReusesOriginalRequestMessage(t *testing.T) {
	botID, sessionID, runID, turnID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	position := int64(3)
	handle := sessionruntime.RunHandle{
		BotID: botID, SessionID: sessionID, RunID: runID,
		OwnerID: "owner-1", Generation: "generation-1", FencingToken: 4,
	}
	store := &recordingStepPersister{recordingMessageService: &recordingMessageService{}}
	service := &Service{
		messageService: store,
		queueCoordinator: sessionqueue.NewMemoryCoordinator(
			sessionqueue.NewSteerQueue(stepCommitAdmissionStub{runID: runID}),
			sessionqueue.NewFollowUpQueue(stepCommitAdmissionStub{runID: runID}),
		),
	}
	holder := contextfrag.NewLifecycleHolder()
	holder.SetManifest(contextfrag.Manifest{View: contextfrag.ViewRunConfigPreProvider})
	rc := resolvedContext{model: models.GetResponse{ID: uuid.NewString()}}
	rc.runConfig.ContextLifecycle = holder
	req := ChatRequest{
		BotID: botID, ThreadID: sessionID, RunID: runID, RunHandle: handle,
		TurnID: turnID, TurnPosition: &position, Query: "original request",
		ReusePersistedUserMessage: true, PersistedUserMessageID: "existing-user",
		SkipHistoryTurn: true, SkipMemoryExtraction: true,
		TurnReplacement: &messagepkg.TurnReplacement{
			OldTurnID: uuid.NewString(), ReplacementTurnID: turnID,
			ReplacementTurnPosition: &position, RequestMessageID: "existing-user", Reason: "retry",
		},
	}
	ctx := runtimefence.WithContext(context.Background(), runtimefence.Fence{BotID: botID, SessionID: sessionID, Token: 4})
	committer := service.newAgentStepCommitter(ctx, req, rc)
	if committer == nil {
		t.Fatal("retry replacement did not receive a durable step committer")
	}
	committer.reasoningTiming = newReasoningTimingTracker(nil)
	if err := committer.commit(ctx, 0, &sdk.StepResult{Messages: []sdk.Message{sdk.AssistantMessage("replacement")}}); err != nil {
		t.Fatalf("commit retry replacement: %v", err)
	}
	if len(store.steps) != 1 || len(store.steps[0].Messages) != 1 || store.steps[0].Messages[0].Role != "assistant" {
		t.Fatalf("retry persisted messages = %#v, want only replacement assistant", store.steps)
	}
	if store.replacementRequestID != "existing-user" {
		t.Fatalf("retry replacement request id = %q, want existing-user", store.replacementRequestID)
	}
}

func TestDecisionContinuationUsesNextDurableStepAndDecodesSteerPayload(t *testing.T) {
	botID, sessionID, runID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	handle := sessionruntime.RunHandle{
		BotID: botID, SessionID: sessionID, RunID: runID,
		OwnerID: "owner-1", Generation: "generation-1", FencingToken: 7,
	}
	steer := sessionqueue.NewSteerQueue(stepCommitAdmissionStub{runID: runID})
	followUp := sessionqueue.NewFollowUpQueue(stepCommitAdmissionStub{runID: runID})
	if _, err := steer.Enqueue(context.Background(), botID, sessionID, "steer-1", []byte(`{"text":"use bun"}`)); err != nil {
		t.Fatalf("enqueue steer: %v", err)
	}

	store := &recordingStepPersister{recordingMessageService: &recordingMessageService{}}
	service := &Service{
		messageService:   store,
		queueCoordinator: sessionqueue.NewMemoryCoordinator(steer, followUp),
	}
	holder := contextfrag.NewLifecycleHolder()
	holder.SetManifest(contextfrag.Manifest{View: contextfrag.ViewRunConfigPreProvider})
	rc := resolvedContext{model: models.GetResponse{ID: uuid.NewString()}}
	rc.runConfig.ContextLifecycle = holder
	req := ChatRequest{
		BotID: botID, ThreadID: sessionID, RunID: runID, RunHandle: handle,
		UserMessagePersisted: true, StepIndexOffset: 1, SkipMemoryExtraction: true,
	}
	cfg := native.RunConfig{ContextLifecycle: holder}
	ctx := runtimefence.WithContext(context.Background(), runtimefence.Fence{BotID: botID, SessionID: sessionID, Token: 7})
	committer, stop, err := service.bindQueueContinuation(ctx, &req, &cfg, rc)
	if err != nil {
		t.Fatalf("bind queue continuation: %v", err)
	}
	defer stop()
	tracker := newReasoningTimingTracker(nil)
	configureNativeReasoningTiming(&cfg, tracker, committer)

	if err := cfg.OnStepCommitted(ctx, 1, &sdk.StepResult{
		Messages: []sdk.Message{sdk.AssistantMessage("first final")},
	}); err != nil {
		t.Fatalf("commit continuation step 1: %v", err)
	}
	if !committer.continueAfterFinal.Load() || len(committer.nextModelInputs) != 1 {
		t.Fatalf("final steer continuation = %v, inputs = %#v", committer.continueAfterFinal.Load(), committer.nextModelInputs)
	}
	part, ok := committer.nextModelInputs[0].Content[0].(sdk.TextPart)
	if !ok || part.Text != "use bun" {
		t.Fatalf("next model input = %#v, want decoded steer text", committer.nextModelInputs[0])
	}
	select {
	case injected := <-cfg.InjectCh:
		t.Fatalf("final-boundary steer was duplicated through InjectCh: %#v", injected)
	case <-time.After(25 * time.Millisecond):
		// Final-boundary steers are carried only by NextModelInputs. The
		// following model invocation applies the claim and persists the user
		// turn atomically; InjectCh is reserved for tool-loop boundaries.
	}

	if err := cfg.OnStepCommitted(ctx, 2, &sdk.StepResult{
		Messages:     []sdk.Message{sdk.AssistantMessage("second tool step")},
		FinishReason: sdk.FinishReasonToolCalls,
		ToolCalls:    []sdk.ToolCall{{ToolCallID: "call-2", ToolName: "exec"}},
	}); err != nil {
		t.Fatalf("commit continuation step 2: %v", err)
	}
	item, ok := steer.Get("steer-1")
	if !ok || item.Status != sessionqueue.Applied {
		t.Fatalf("steer status = %#v, want applied", item)
	}
	if committer.queueStep.pendingSteer != nil {
		t.Fatalf("applied steer claim remained pending: %#v", committer.queueStep.pendingSteer)
	}
	if err := cfg.OnStepCommitted(ctx, 3, &sdk.StepResult{
		Messages:     []sdk.Message{sdk.AssistantMessage("third tool step")},
		FinishReason: sdk.FinishReasonToolCalls,
		ToolCalls:    []sdk.ToolCall{{ToolCallID: "call-3", ToolName: "exec"}},
	}); err != nil {
		t.Fatalf("commit step after applied steer: %v", err)
	}
	if len(store.published) != 4 {
		t.Fatalf("post-commit message publications = %d, want final steer user plus three steps", len(store.published))
	}
	if store.published[1].Role != "user" {
		t.Fatalf("final-boundary steer publication role = %q, want user", store.published[1].Role)
	}
}

func TestToolBoundarySteerPublishesClaimBeforeFollowingStepCommit(t *testing.T) {
	botID, sessionID, runID, turnID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	manager := sessionruntime.NewManager(sessionruntime.NewMemoryBackend(), sessionruntime.Options{OwnerID: "owner-1"})
	t.Cleanup(func() { _ = manager.Close() })
	injectCh := make(chan turn.InjectMessage, 1)
	handle, err := manager.StartRunWithAdmissionBuilderHandle(
		context.Background(), botID, sessionID, runID,
		func(context.Context, sessionruntime.RunHandle) (sessionruntime.RunAdmissionView, error) {
			return sessionruntime.RunAdmissionView{RequestUserTurn: &chatview.UITurn{
				TurnID: turnID, Role: "user", Text: "build it", Timestamp: time.Now().UTC(),
			}}, nil
		},
		make(chan struct{}, 1), func() {}, injectCh,
	)
	if err != nil {
		t.Fatalf("start runtime run: %v", err)
	}
	// The in-memory runtime manager has no durable ledger, while queue claims are
	// deliberately fenced. Supply the test's synthetic ledger token on the same
	// otherwise-real runtime handle.
	handle.FencingToken = 1

	steer := sessionqueue.NewSteerQueue(stepCommitAdmissionStub{runID: runID})
	followUp := sessionqueue.NewFollowUpQueue(stepCommitAdmissionStub{runID: runID})
	if _, err := steer.Enqueue(context.Background(), botID, sessionID, "steer-live", []byte(`{"text":"use bun"}`)); err != nil {
		t.Fatalf("enqueue steer: %v", err)
	}
	store := &recordingStepPersister{recordingMessageService: &recordingMessageService{}}
	service := &Service{
		messageService: store, sessionManager: manager,
		queueCoordinator: sessionqueue.NewMemoryCoordinator(steer, followUp),
	}
	holder := contextfrag.NewLifecycleHolder()
	holder.SetManifest(contextfrag.Manifest{View: contextfrag.ViewRunConfigPreProvider})
	position := int64(1)
	req := ChatRequest{
		BotID: botID, ThreadID: sessionID, RunID: runID, RunHandle: handle,
		TurnID: turnID, TurnPosition: &position, UserMessagePersisted: true,
		QueueInjectCh: injectCh, SkipMemoryExtraction: true,
	}
	rc := resolvedContext{model: models.GetResponse{ID: uuid.NewString()}}
	rc.runConfig.ContextLifecycle = holder
	ctx := runtimefence.WithContext(context.Background(), runtimefence.Fence{
		BotID: botID, SessionID: sessionID, Token: 1,
	})
	committer := service.newAgentStepCommitter(ctx, req, rc)
	if committer == nil {
		t.Fatal("run did not receive a durable step committer")
	}
	committer.reasoningTiming = newReasoningTimingTracker(nil)
	if err := committer.commit(ctx, 0, &sdk.StepResult{
		Messages:     []sdk.Message{sdk.AssistantMessage("checking files")},
		FinishReason: sdk.FinishReasonToolCalls,
		ToolCalls:    []sdk.ToolCall{{ToolCallID: "call-1", ToolName: "exec"}},
	}); err != nil {
		t.Fatalf("commit tool boundary: %v", err)
	}

	snapshot, err := manager.Snapshot(context.Background(), botID, sessionID)
	if err != nil {
		t.Fatalf("load runtime snapshot: %v", err)
	}
	if snapshot.CurrentRunView == nil || len(snapshot.CurrentRunView.SteerTurns) != 1 {
		t.Fatalf("runtime steer projection = %#v", snapshot.CurrentRunView)
	}
	projected := snapshot.CurrentRunView.SteerTurns[0]
	if projected.ItemID != "steer-live" || projected.Status != "claimed" || projected.Text != "use bun" {
		t.Fatalf("runtime steer projection = %#v, want claimed steer-live", projected)
	}
	select {
	case injected := <-injectCh:
		if injected.Text != "use bun" {
			t.Fatalf("injected text = %q, want use bun", injected.Text)
		}
	default:
		t.Fatal("claimed steer was not delivered to the native loop")
	}
}

func TestContinuationDoesNotReuseAppliedFollowUpClaim(t *testing.T) {
	botID, sessionID := uuid.NewString(), uuid.NewString()
	initialRunID, continuationRunID := uuid.NewString(), uuid.NewString()
	handle := sessionruntime.RunHandle{
		BotID: botID, SessionID: sessionID, RunID: continuationRunID,
		OwnerID: "owner-1", Generation: "generation-1", FencingToken: 7,
	}
	steer := sessionqueue.NewSteerQueue(stepCommitAdmissionStub{runID: continuationRunID})
	followUp := sessionqueue.NewFollowUpQueue(stepCommitAdmissionStub{runID: initialRunID})
	if _, err := followUp.Enqueue(context.Background(), botID, sessionID, "follow-1", []byte(`{"text":"continue later"}`)); err != nil {
		t.Fatalf("enqueue follow-up: %v", err)
	}
	if _, err := followUp.Assign("follow-1", continuationRunID); err != nil {
		t.Fatalf("assign follow-up: %v", err)
	}
	_, claim, err := followUp.ClaimAssigned("follow-1", handle)
	if err != nil {
		t.Fatalf("claim assigned follow-up: %v", err)
	}

	store := &recordingStepPersister{recordingMessageService: &recordingMessageService{}}
	service := &Service{
		messageService:   store,
		queueCoordinator: sessionqueue.NewMemoryCoordinator(steer, followUp),
	}
	holder := contextfrag.NewLifecycleHolder()
	holder.SetManifest(contextfrag.Manifest{View: contextfrag.ViewRunConfigPreProvider})
	rc := resolvedContext{model: models.GetResponse{ID: uuid.NewString()}}
	rc.runConfig.ContextLifecycle = holder
	req := ChatRequest{
		BotID: botID, ThreadID: sessionID, RunID: continuationRunID, RunHandle: handle,
		QueueFollowUpClaim: &claim, UserMessagePersisted: true, SkipMemoryExtraction: true,
	}
	cfg := native.RunConfig{ContextLifecycle: holder}
	ctx := runtimefence.WithContext(context.Background(), runtimefence.Fence{BotID: botID, SessionID: sessionID, Token: 7})
	committer, stop, err := service.bindQueueContinuation(ctx, &req, &cfg, rc)
	if err != nil {
		t.Fatalf("bind queue continuation: %v", err)
	}
	defer stop()
	configureNativeReasoningTiming(&cfg, newReasoningTimingTracker(nil), committer)

	toolStep := func(callID string) *sdk.StepResult {
		return &sdk.StepResult{
			Messages:     []sdk.Message{sdk.AssistantMessage(callID)},
			FinishReason: sdk.FinishReasonToolCalls,
			ToolCalls:    []sdk.ToolCall{{ToolCallID: callID, ToolName: "exec"}},
		}
	}
	if err := cfg.OnStepCommitted(ctx, 0, toolStep("call-0")); err != nil {
		t.Fatalf("apply follow-up claim: %v", err)
	}
	item, ok := followUp.Get("follow-1")
	if !ok || item.Status != sessionqueue.Applied {
		t.Fatalf("follow-up status = %#v, want applied", item)
	}
	if committer.queueStep.pendingFollowUp != nil {
		t.Fatalf("applied follow-up claim remained pending: %#v", committer.queueStep.pendingFollowUp)
	}
	if err := cfg.OnStepCommitted(ctx, 1, toolStep("call-1")); err != nil {
		t.Fatalf("commit step after applied follow-up: %v", err)
	}
}

func TestDecisionContinuationFinalBoundaryAssignsFollowUp(t *testing.T) {
	botID, sessionID, runID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	handle := sessionruntime.RunHandle{
		BotID: botID, SessionID: sessionID, RunID: runID,
		OwnerID: "owner-1", Generation: "generation-1", FencingToken: 7,
	}
	steer := sessionqueue.NewSteerQueue(stepCommitAdmissionStub{runID: runID})
	followUp := sessionqueue.NewFollowUpQueue(stepCommitAdmissionStub{runID: runID})
	if _, err := followUp.Enqueue(context.Background(), botID, sessionID, "follow-1", []byte(`{"text":"continue later"}`)); err != nil {
		t.Fatalf("enqueue follow-up: %v", err)
	}

	store := &recordingStepPersister{recordingMessageService: &recordingMessageService{}}
	service := &Service{
		messageService:   store,
		queueCoordinator: sessionqueue.NewMemoryCoordinator(steer, followUp),
	}
	holder := contextfrag.NewLifecycleHolder()
	holder.SetManifest(contextfrag.Manifest{View: contextfrag.ViewRunConfigPreProvider})
	rc := resolvedContext{model: models.GetResponse{ID: uuid.NewString()}}
	rc.runConfig.ContextLifecycle = holder
	req := ChatRequest{
		BotID: botID, ThreadID: sessionID, RunID: runID, RunHandle: handle,
		UserMessagePersisted: true, StepIndexOffset: 1, SkipMemoryExtraction: true,
	}
	cfg := native.RunConfig{ContextLifecycle: holder}
	ctx := runtimefence.WithContext(context.Background(), runtimefence.Fence{BotID: botID, SessionID: sessionID, Token: 7})
	committer, stop, err := service.bindQueueContinuation(ctx, &req, &cfg, rc)
	if err != nil {
		t.Fatalf("bind queue continuation: %v", err)
	}
	defer stop()
	configureNativeReasoningTiming(&cfg, newReasoningTimingTracker(nil), committer)

	if err := cfg.OnStepCommitted(ctx, 1, &sdk.StepResult{
		Messages: []sdk.Message{sdk.AssistantMessage("final")},
	}); err != nil {
		t.Fatalf("commit continuation final: %v", err)
	}
	item, ok := followUp.Get("follow-1")
	if !ok || item.Status != sessionqueue.Accepted || item.AssignedRunID == "" {
		t.Fatalf("follow-up handoff = %#v, want assigned continuation", item)
	}
}
