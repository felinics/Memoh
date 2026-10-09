package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/apperror"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/schedule"
)

// WSStreamEvent represents a raw JSON event forwarded from the agent.
type WSStreamEvent = json.RawMessage

// terminalSnapshot captures the partial state extracted from a terminal
// agent event. It is used both for the success-path persistence and for the
// interrupted-path fallback so that real partial messages get saved instead
// of a synthetic placeholder.
type terminalSnapshot struct {
	sdkMessages             []sdk.Message
	internalFeedbackIndexes []int
	usage                   json.RawMessage
	reasoningTiming         []messagepkg.ReasoningTimingSegment
	deferredToolID          string
	aborted                 bool
	visibleOutput           bool
	failureCode             apperror.Code
}

// snapshotFailureCode is the code a failed turn's history marker records, or
// empty when the turn leaves no marker, for every turn except a new Web chat
// send (see wsFailureCode). It reads the code the cause carries the same way
// classifyRunFailure does, without that function's default: a turn stopped by
// the user, or failed for a reason nothing named, leaves no marker. Only the
// codes historyFailureCode lists are recorded.
func snapshotFailureCode(idleFired bool, cause error) apperror.Code {
	code := publicFailureCode(cause)
	if code == "" {
		if idleFired {
			return apperror.CodeAgentResponseTimeout
		}
		return ""
	}
	return historyFailureCode(code)
}

// historyFailureCode is code when a failed turn's history records it, and empty
// otherwise, for the turns snapshotFailureCode and wsFailureCode leave to it.
func historyFailureCode(code apperror.Code) apperror.Code {
	switch code {
	case apperror.CodeAgentResponseTimeout,
		apperror.CodeAgentToolTimeout,
		apperror.CodeScheduleExecutionTimeout,
		apperror.CodeAgentResponseInterrupted,
		apperror.CodeAgentProviderOverloaded,
		apperror.CodeAgentProviderRateLimited,
		apperror.CodeAgentProviderQuotaExhausted,
		apperror.CodeAgentProviderAuthFailed,
		apperror.CodeAgentProviderPermissionDenied,
		apperror.CodeAgentProviderRequestRejected,
		apperror.CodeAgentProviderUnreachable:
		return code
	default:
		return ""
	}
}

// wsFailureCode is the code a failed Web chat turn records in history, or empty
// when it records none. A new send the server admitted is recorded whatever it
// failed with, under the code its run ends with, so the history row and
// session_runs name the same failure and the sent message is never left
// outside the history. A stop is not a failure and records nothing. A retry or
// edit replaces a turn the history already has, and keeps it unless the
// failure is one historyFailureCode lists.
func wsFailureCode(req ChatRequest, outcome *outcomeRecorder) apperror.Code {
	code := outcome.failureCode()
	if req.TurnReplacement != nil {
		return historyFailureCode(code)
	}
	return code
}

func classifyUserMessageHookError(err error) error {
	if err == nil || apperror.CodeOf(err) == apperror.CodeHookUserMessageFailed {
		return err
	}
	return apperror.Wrap(apperror.CodeHookUserMessageFailed, err, nil)
}

// persistPreflightFailureTurn gives an admitted Web send a durable target even
// when the user-message hook rejects it before resolve() can create a runtime
// context. The user row is visible for the UI, while both rows carry the
// origin marker so normal model history can exclude the rejected input.
func (s *Service) persistPreflightFailureTurn(ctx context.Context, req ChatRequest, code apperror.Code) error {
	if code == "" || s == nil || s.messageService == nil || req.SkipHistoryTurn ||
		req.UserMessagePersisted || req.ReusePersistedUserMessage ||
		strings.TrimSpace(req.TurnID) == "" || req.TurnPosition == nil {
		return nil
	}
	if strings.TrimSpace(req.Query) == "" && req.UserMessageKind != UserMessageKindSkillActivation {
		return nil
	}

	output := []ModelMessage{{
		Role:    "assistant",
		Content: newTextContent(""),
	}}
	round := prependTurnUserMessage(req, output)
	if len(round) != 2 {
		return nil
	}
	userMetadata := map[string]any{
		messagepkg.HistoryFailureOriginMetadataKey: messagepkg.HistoryFailureOriginUserMessageHook,
	}
	assistantMetadata := map[string]any{
		messagepkg.AgentStepInterruptedMetadataKey: true,
		messagepkg.HistoryErrorCodeMetadataKey:     string(code),
		messagepkg.HistoryFailureOriginMetadataKey: messagepkg.HistoryFailureOriginUserMessageHook,
	}
	_, err := s.storeRoundWithOptionsResult(context.WithoutCancel(ctx), req, round, "", storeRoundOptions{
		AllowEmptyAssistantText: true,
		MessageMetadataByIndex: map[int]map[string]any{
			0: userMetadata,
			1: assistantMetadata,
		},
		RequireCompletePersist: true,
	})
	return err
}

func shouldForwardAfterIdleFailure(event native.StreamEvent, failureEventForwarded bool) bool {
	if !failureEventForwarded {
		return true
	}
	return event.Type == native.EventAgentAbort
}

func hasVisibleAgentStreamOutput(event native.StreamEvent) bool {
	switch event.Type {
	case native.EventTextDelta,
		native.EventReasoningDelta:
		return strings.TrimSpace(event.Delta) != ""
	case native.EventToolCallInputStart,
		native.EventToolCallStart,
		native.EventToolCallProgress,
		native.EventToolCallEnd,
		native.EventToolApprovalRequest,
		native.EventUserInputRequest,
		native.EventReaction,
		native.EventSpeech:
		return true
	case native.EventAttachment:
		return len(event.Attachments) > 0
	default:
		return false
	}
}

// agentStreamFailure translates the failure an error event reports into the
// run's public error, keeping the event's cause, and returns nil for any other
// event. Every consumer of agent events uses it, so the run's terminal write,
// the history marker, the session's live view and the published event name a
// failure alike.
//
// An event that already carries a catalogued code keeps it and its args: the
// application and the External Agent runtimes name their failures themselves. A native
// failure is named from its cause by nativeFailureCode.
func agentStreamFailure(event native.StreamEvent) error {
	if event.Type != native.EventError {
		return nil
	}
	if code := apperror.Code(strings.TrimSpace(event.Code)); code != "" {
		if _, ok := apperror.Lookup(code); ok {
			return apperror.Wrap(code, event.Cause, event.Args)
		}
	}
	return apperror.Wrap(nativeFailureCode(event.Cause), event.Cause, nil)
}

// nativeFailureCode names the failure a native run ended with. A context the
// budget cannot fit is named before anything else, since only compacting or
// another model helps. A provider that named why it refused the request is
// reported with that reason: an exhausted balance and a rejected key both need
// the user to go change something, and "the model response was interrupted,
// please try again" sends them back into a call that cannot succeed. A model
// call that got no response reports the provider unreachable. Anything else
// is an interrupted response; the runtime names its own failures itself.
func nativeFailureCode(cause error) apperror.Code {
	switch {
	case errors.Is(cause, contextfrag.ErrProtectedContextOverflow):
		return apperror.CodeContextProtectedOverflow
	case errors.Is(cause, contextfrag.ErrBudgetUnsatisfied):
		return apperror.CodeContextBudgetUnsatisfied
	}
	if code := providerFailureCode(cause, native.IsModelCallFailure(cause)); code != "" {
		return code
	}
	return apperror.CodeAgentResponseInterrupted
}

func agentFailureStreamEvent(cause error) native.StreamEvent {
	code := classifyRunFailure(cause)
	definition, _ := apperror.Lookup(code)
	event := native.StreamEvent{
		Type:  native.EventError,
		Code:  string(code),
		Error: definition.Detail,
	}
	if apperror.CodeOf(cause) == code {
		if args := apperror.ArgsOf(cause); len(args) > 0 {
			event.Args = args
		}
	}
	return event
}

// publicAgentStreamEvent is event as it leaves the application: an error
// event is replaced by its code and the catalog detail, so its cause never
// reaches a subscriber.
func publicAgentStreamEvent(event native.StreamEvent) native.StreamEvent {
	if cause := agentStreamFailure(event); cause != nil {
		return agentFailureStreamEvent(cause)
	}
	return event
}

func agentAbortCause(ctx context.Context) error {
	if ctx != nil {
		if cause := context.Cause(ctx); cause != nil {
			if errors.Is(cause, schedule.ErrExecutionTimeout) {
				return apperror.Wrap(apperror.CodeScheduleExecutionTimeout, cause, nil)
			}
			return cause
		}
	}
	return errors.New("agent run aborted")
}

// extractTerminalSnapshot decodes a terminal stream event payload into the
// raw SDK messages plus auxiliary metadata. Returns ok=false when the event
// has no usable messages.
func extractTerminalSnapshot(data []byte) (terminalSnapshot, bool) {
	var envelope struct {
		InternalFeedbackIndexes []int           `json:"internal_feedback_indexes"`
		Type                    string          `json:"type"`
		Messages                json.RawMessage `json:"messages"`
		Usage                   json.RawMessage `json:"usage,omitempty"`
		ApprovalID              string          `json:"approvalId,omitempty"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return terminalSnapshot{}, false
	}
	if len(envelope.Messages) == 0 {
		return terminalSnapshot{}, false
	}
	var sdkMsgs []sdk.Message
	if err := json.Unmarshal(envelope.Messages, &sdkMsgs); err != nil || len(sdkMsgs) == 0 {
		return terminalSnapshot{}, false
	}
	return terminalSnapshot{
		sdkMessages:             sdkMsgs,
		internalFeedbackIndexes: envelope.InternalFeedbackIndexes,
		usage:                   envelope.Usage,
		deferredToolID:          strings.TrimSpace(envelope.ApprovalID),
		aborted:                 envelope.Type == string(native.EventAgentAbort),
	}, true
}

// StreamChat runs a streaming chat via the internal agent.
func (s *Service) StreamChat(ctx context.Context, req ChatRequest) (<-chan StreamChunk, <-chan error) {
	chunkCh := make(chan StreamChunk)
	errCh := make(chan error, 1)
	endActiveTurn, err := s.activeTurns.begin()
	if err != nil {
		errCh <- err
		close(chunkCh)
		close(errCh)
		return chunkCh, errCh
	}
	go func() {
		defer endActiveTurn()
		defer close(chunkCh)
		defer close(errCh)
		ctx, endTurn := startTurnSpan(ctx, req)
		// turnErr is what the caller will see on errCh; the span reports the
		// same outcome the caller is told about, not a separate opinion.
		var turnErr error
		defer func() { endTurn(turnErr) }()
		// Every failure exit goes through fail, so the span's outcome cannot
		// drift from what the caller is told. The guard is mechanical: no
		// bare send to errCh may remain in this function.
		fail := func(err error) {
			// First error only, and never blocking. errCh holds one; a second
			// send would block until someone reads, and if the consumer is
			// stuck on the event channel at the same moment neither side ever
			// moves — this goroutine would not exit and its channels would
			// never close. The first error is the cause; later ones are its
			// consequences.
			if turnErr != nil {
				return
			}
			turnErr = err
			select {
			case errCh <- err:
			default:
			}
		}
		if err := s.recordRunResumeContext(ctx, req); err != nil {
			fail(err)
			return
		}
		streamReq := req
		if streamReq.RawQuery == "" {
			streamReq.RawQuery = strings.TrimSpace(streamReq.Query)
		}
		if err := rejectReservedSkillMetadataIfPresent(streamReq); err != nil {
			fail(err)
			return
		}
		if err := s.rejectRequestedSkillsIfUnsupportedContext(ctx, streamReq); err != nil {
			fail(err)
			return
		}
		dispatch, err := s.resolveRuntimeDispatch(ctx, streamReq)
		if err != nil {
			fail(err)
			return
		}
		if dispatch.kind == dispatchExternal {
			if err := rejectExternalAgentWorkspaceTarget(streamReq); err != nil {
				fail(err)
				return
			}
			s.streamRuntimeChunks(ctx, dispatch.driver, streamReq, chunkCh, fail)
			return
		}
		streamCtx, preparedReq, prepareErr := s.prepareWorkspaceRequest(ctx, streamReq)
		if prepareErr != nil {
			fail(prepareErr)
			return
		}
		streamReq = preparedReq

		if streamReq.RawQuery == "" {
			streamReq.RawQuery = strings.TrimSpace(streamReq.Query)
		}
		if !streamReq.UserMessagePersisted {
			streamReq, err = s.applyUserMessageHook(streamCtx, streamReq)
			if err != nil {
				fail(err)
				return
			}
		}
		rc, streamReq, err := s.resolve(streamCtx, streamReq)
		if err != nil {
			fail(err)
			return
		}
		streamReq.Query = rc.query
		streamReq.RunID = rc.runConfig.RunID

		s.maybeGenerateSessionTitle(context.WithoutCancel(streamCtx), streamReq, streamReq.RawQuery)

		cfg := rc.runConfig
		cfg.LiveToolStream = true
		cfg.CanRequestUserInput = s.canDeliverUserInputStream()
		reasoningTiming := newReasoningTimingTracker(nil)
		stepCommitter := s.newAgentStepCommitter(streamCtx, streamReq, rc)
		configureNativeReasoningTiming(&cfg, reasoningTiming, stepCommitter)
		if stepCommitter != nil {
			stepCommitter.bindContinuation(&cfg)
		}
		cfg = s.prepareRunConfig(streamCtx, cfg)
		terminal := s.contextLifecycleTerminal(streamCtx, cfg)
		outcome := newOutcomeRecorder(streamCtx)
		defer outcome.finishLifecycle(terminal)

		// Wrap with idle timeout: if no events arrive within the adaptive timeout, cancel the stream.
		idleCtx, idleCancel := s.withStreamIdleTimeout(streamCtx, reasoningEffortForIdle(cfg))
		defer idleCancel.Stop()
		outcome.watchIdle(idleCtx, idleCancel)
		cfg = pauseIdleDuringBudgetRecovery(cfg, idleCancel)

		eventCh := s.agent.Stream(idleCtx, cfg)
		stored := false
		clientGone := false
		var lastSnapshot terminalSnapshot
		var hasSnapshot bool
		var toolCallCount int
		var hasVisibleOutput bool
		var failureEventForwarded bool
		var deferredRuntimeTerminal *native.StreamEvent
		for event := range eventCh {
			idleCancel.Observe(event)

			// Track tool calls for adaptive idle timeout and progress events
			if event.Type == native.EventToolCallStart {
				toolCallCount++
			}

			// A failure event is recorded, not logged: the run's result
			// record reports how the run ended, once.
			_ = outcome.observe(event)
			if hasVisibleAgentStreamOutput(event) {
				hasVisibleOutput = true
			}
			if event.Type == native.EventAgentAbort && idleCancel.DidFire() && !clientGone {
				failureEvent := agentFailureStreamEvent(context.Cause(idleCtx))
				if failureData, marshalErr := json.Marshal(failureEvent); marshalErr == nil {
					select {
					case chunkCh <- StreamChunk(failureData):
						failureEventForwarded = true
					case <-streamCtx.Done():
						clientGone = true
					}
				}
			}

			published := outcome.stampTerminal(publicAgentStreamEvent(event))
			data, err := json.Marshal(published)
			if err != nil {
				continue
			}
			var terminalPersistErr error
			// A live queue step must commit history before its terminal runtime event
			// marks the live projection completed. Otherwise CommitStep cannot
			// publish the claimed steer or create the follow-up continuation: the
			// manager quite correctly rejects a queue mutation against a terminal
			// run. Non-terminal events retain their low-latency publication path.
			if streamReq.PublishRuntimeEvents && s.publishTurnEvent != nil {
				if event.IsTerminal() && stepCommitter != nil {
					terminal := published
					deferredRuntimeTerminal = &terminal
				} else if publishErr := s.publishTurnEvent(streamCtx, streamReq.RunHandle, published); publishErr != nil {
					s.logger.WarnContext(ctx, "continuation runtime event publish failed", slog.String("run_id", streamReq.RunID), slog.Any("error", publishErr))
				}
			}
			if event.IsTerminal() && len(event.Messages) > 0 {
				if snap, ok := extractTerminalSnapshot(data); ok {
					if stepCommitter == nil {
						snap.reasoningTiming = takeTerminalReasoningTiming(reasoningTiming, event.Type)
					}
					snap.visibleOutput = hasVisibleOutput
					snap.failureCode = snapshotFailureCode(idleCancel.DidFire(), outcome.cause)
					lastSnapshot = snap
					hasSnapshot = true
					outcome.observeSnapshot(snap)
					if !stored && !runOwnershipLost(streamCtx) && stepCommitter != nil {
						if storeErr := stepCommitter.finish(streamCtx, extractInputTokensFromUsage(snap.usage)); storeErr != nil {
							terminalPersistErr = runtimeHistoryError(storeErr)
							outcome.recordCause(terminalPersistErr)
							s.logger.ErrorContext(ctx, "stream step finalization failed", slog.Any("error", storeErr))
						} else {
							stored = true
						}
					} else if !stored && !runOwnershipLost(streamCtx) {
						// Use WithoutCancel so persistence still succeeds even
						// when the parent ctx has already been cancelled by a
						// client disconnect or idle timeout.
						if storeErr := s.persistTerminalSnapshot(context.WithoutCancel(streamCtx), streamReq, rc, snap); storeErr != nil {
							terminalPersistErr = runtimeHistoryError(storeErr)
							outcome.recordCause(terminalPersistErr)
							s.logger.ErrorContext(ctx, "stream persist failed", slog.Any("error", storeErr))
						} else {
							stored = true
						}
					}
				}
			}
			if event.IsTerminal() && !stored && !runOwnershipLost(streamCtx) && terminalPersistErr == nil {
				switch {
				case !hasVisibleOutput:
					stored = true
				case stepCommitter != nil:
					if storeErr := stepCommitter.finish(streamCtx, rc.estimatedTokens); storeErr != nil {
						terminalPersistErr = runtimeHistoryError(storeErr)
					} else {
						stored = true
					}
				default:
					terminalPersistErr = runtimeHistoryError(errors.New("agent terminal event has no persistable snapshot"))
				}
				if terminalPersistErr != nil {
					outcome.recordCause(terminalPersistErr)
				}
			}
			if event.IsTerminal() && (terminalPersistErr != nil || runOwnershipLost(streamCtx)) {
				if terminalPersistErr != nil {
					outcome.recordReported(terminalPersistErr)
				}
				deferredRuntimeTerminal = nil
				continue
			}

			// Forward to the client unless the client is already gone. Once
			// the client disconnects we keep draining eventCh so the agent
			// goroutine can finish and the terminal event (with partial
			// messages) is captured for persistence above.
			if !clientGone && shouldForwardAfterIdleFailure(event, failureEventForwarded) {
				select {
				case chunkCh <- StreamChunk(data):
				case <-streamCtx.Done():
					clientGone = true
				}
			}
		}
		outcome.endStream()

		// Intermediate persistence on abort/error: persist only concrete
		// partial assistant/tool state. Failed sends without a terminal
		// snapshot are treated as unsent so the Web UI can restore the draft
		// without polluting history.
		if !stored && stepCommitter != nil && !runOwnershipLost(streamCtx) {
			if storeErr := stepCommitter.finish(streamCtx, rc.estimatedTokens); storeErr != nil {
				outcome.recordCause(storeErr)
				s.logger.ErrorContext(ctx, "stream step finalization failed", slog.Any("error", storeErr))
			}
		} else if !stored {
			switch {
			case runOwnershipLost(streamCtx):
				s.logger.WarnContext(ctx, "skip persisting stream after run ownership loss",
					slog.String("bot_id", streamReq.BotID),
					slog.String("chat_id", streamReq.ChatID),
				)
			case hasSnapshot:
				_ = s.persistPartialResult(streamCtx, streamReq, rc, lastSnapshot.sdkMessages, lastSnapshot.reasoningTiming, toolCallCount, idleCancel.DidFire(), hasVisibleOutput, lastSnapshot.failureCode, lastSnapshot.internalFeedbackIndexes)
			default:
				if code := snapshotFailureCode(idleCancel.DidFire(), outcome.cause); code != "" {
					if _, storeErr := s.persistTurnFailure(context.WithoutCancel(streamCtx), streamReq, rc, code); storeErr != nil {
						s.logger.ErrorContext(ctx, "stream timeout persist failed", slog.Any("error", storeErr))
					}
				} else {
					s.logger.InfoContext(ctx, "skip persisting failed startup stream",
						slog.String("bot_id", streamReq.BotID),
						slog.String("chat_id", streamReq.ChatID),
					)
				}
			}
		}
		if deferredRuntimeTerminal != nil && streamReq.PublishRuntimeEvents && s.publishTurnEvent != nil {
			if publishErr := s.publishTurnEvent(context.WithoutCancel(streamCtx), streamReq.RunHandle, *deferredRuntimeTerminal); publishErr != nil {
				s.logger.WarnContext(ctx, "continuation terminal runtime event publish failed", slog.String("run_id", streamReq.RunID), slog.Any("error", publishErr))
			}
		}
		if commitErr := stepCommitter.err(); commitErr != nil && streamCtx.Err() == nil {
			outcome.recordCause(commitErr)
			fail(withDeliveredOutcome(commitErr, outcome.deliveredOutcome()))
		}

		if idleCancel.DidFire() {
			s.logger.WarnContext(ctx, "agent stream aborted: inactivity timeout",
				slog.String("code", string(apperror.CodeOf(context.Cause(idleCtx)))),
				slog.String("bot_id", streamReq.BotID),
				slog.String("chat_id", streamReq.ChatID),
				slog.String("model_id", rc.model.ID),
				slog.Int("tool_calls", toolCallCount),
			)
			if !clientGone && !failureEventForwarded {
				timeoutEvent := agentFailureStreamEvent(context.Cause(idleCtx))
				if data, err := json.Marshal(timeoutEvent); err == nil {
					select {
					case chunkCh <- StreamChunk(data):
					case <-streamCtx.Done():
					}
				}
			}
			outcome.reportIdleTimeout()
		}
		if outcome.reported != nil {
			fail(outcome.reported)
		}
	}()
	return chunkCh, errCh
}

// StreamChatWS resolves the agent context and streams agent events.
// Events are sent on eventCh. When abortCh is closed, the context is cancelled.
// The returned error is a failure the caller still has to report; the outcome
// names a failure the stream already delivered, for the run's terminal write.
func (s *Service) StreamChatWS(
	ctx context.Context,
	req ChatRequest,
	eventCh chan<- WSStreamEvent,
	abortCh <-chan struct{},
) (RunOutcome, error) {
	_, outcome, err := s.streamChatWSResult(ctx, req, eventCh, abortCh)
	return outcome, err
}

func (s *Service) streamChatWSResult(
	ctx context.Context,
	req ChatRequest,
	eventCh chan<- WSStreamEvent,
	abortCh <-chan struct{},
) ([]messagepkg.Message, RunOutcome, error) {
	return s.streamChatWSResultWithHooks(ctx, req, eventCh, abortCh, nil, nil)
}

func (s *Service) streamChatWSResultWithHooks(
	ctx context.Context,
	req ChatRequest,
	eventCh chan<- WSStreamEvent,
	abortCh <-chan struct{},
	preflight func(context.Context) error,
	postPersist func(context.Context, []messagepkg.Message) error,
) (_ []messagepkg.Message, runOutcome RunOutcome, turnErr error) {
	endActiveTurn, err := s.activeTurns.begin()
	if err != nil {
		return nil, RunOutcome{}, err
	}
	defer endActiveTurn()
	if err := s.recordRunResumeContext(ctx, req); err != nil {
		return nil, RunOutcome{}, err
	}
	// Named so the deferred span close sees whatever any of this function's
	// returns produced. Tracking it by hand would mean touching fifteen
	// return statements and being wrong the first time someone adds a
	// sixteenth.
	//
	// A failure the stream delivered is not returned as an error, so the span
	// reads the run's lifecycle cause as well: the turn span and the run's
	// result record reach the same conclusion.
	var lifecycle *outcomeRecorder
	ctx, endTurn := startTurnSpan(ctx, req)
	defer func() { endTurn(turnSpanCause(turnErr, lifecycle, runOutcome)) }()

	if err := rejectReservedSkillMetadataIfPresent(req); err != nil {
		return nil, RunOutcome{}, err
	}
	if err := s.rejectRequestedSkillsIfUnsupportedContext(ctx, req); err != nil {
		return nil, RunOutcome{}, err
	}
	dispatch, err := s.resolveRuntimeDispatch(ctx, req)
	if err != nil {
		return nil, RunOutcome{}, err
	}
	if dispatch.kind == dispatchExternal {
		if err := rejectExternalAgentWorkspaceTarget(req); err != nil {
			return nil, RunOutcome{}, err
		}
		// Hooks currently mean retry/edit turn replacement. Runtimes that own
		// their conversation context have no rewind primitive, so running the
		// turn would leave that context inconsistent with the visible history.
		if preflight != nil || postPersist != nil {
			return nil, RunOutcome{}, apperror.New(apperror.CodeExternalAgentTurnReplacementUnsupported, nil)
		}
		outcome, err := s.streamRuntimeWS(ctx, dispatch.driver, req, eventCh, abortCh, true)
		return nil, outcome, err
	}
	var prepareErr error
	ctx, req, prepareErr = s.prepareWorkspaceRequest(ctx, req)
	if prepareErr != nil {
		return nil, RunOutcome{}, prepareErr
	}

	if preflight != nil {
		if err := preflight(ctx); err != nil {
			return nil, RunOutcome{}, err
		}
	}

	if req.RawQuery == "" {
		req.RawQuery = strings.TrimSpace(req.Query)
	}
	if !req.UserMessagePersisted && !req.ReusePersistedUserMessage {
		req, err = s.applyUserMessageHook(ctx, req)
		if err != nil {
			// A caller abort or request deadline is not a Hook rejection. Let the
			// normal cancellation/timeout path classify it instead of creating a
			// failed user turn that the user never intentionally submitted.
			if ctx.Err() != nil {
				return nil, RunOutcome{}, err
			}
			err = classifyUserMessageHookError(err)
			if persistErr := s.persistPreflightFailureTurn(ctx, req, apperror.CodeOf(err)); persistErr != nil && s.logger != nil {
				// Keep the Hook error as the run's public outcome. The persistence
				// failure is private and is logged here; replacing the
				// actionable Hook code with a generic save error would hide the
				// reason the input was rejected.
				s.logger.ErrorContext(ctx, "persist preflight failure turn failed", slog.Any("error", persistErr))
			}
			return nil, RunOutcome{}, err
		}
	}
	rc, req, err := s.resolve(ctx, req)
	if err != nil {
		return nil, RunOutcome{}, fmt.Errorf("resolve: %w", err)
	}
	req.Query = rc.query
	req.RunID = rc.runConfig.RunID

	s.maybeGenerateSessionTitle(context.WithoutCancel(ctx), req, req.RawQuery)

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		select {
		case <-abortCh:
			cancel()
		case <-streamCtx.Done():
		}
	}()

	cfg := rc.runConfig
	cfg.LiveToolStream = true
	cfg.CanRequestUserInput = s.canDeliverUserInputWS(eventCh)
	reasoningTiming := newReasoningTimingTracker(nil)
	stepCommitter := s.newAgentStepCommitter(streamCtx, req, rc)
	configureNativeReasoningTiming(&cfg, reasoningTiming, stepCommitter)
	if stepCommitter != nil {
		stepCommitter.bindContinuation(&cfg)
	}
	cfg = s.prepareRunConfig(streamCtx, cfg)
	terminal := s.contextLifecycleTerminal(streamCtx, cfg)
	outcome := newOutcomeRecorder(streamCtx)
	lifecycle = outcome
	// A run that fails for a reason without a catalogued code, such as an
	// abort nobody requested, is named when its terminal event is stamped, so
	// the live frame, the run's recorded code and its history agree.
	outcome.defaultCode = apperror.CodeAgentResponseInterrupted
	defer outcome.finishLifecycle(terminal)

	// Wrap with idle timeout: if no events arrive within the adaptive timeout, cancel the stream.
	idleCtx, idleCancel := s.withStreamIdleTimeout(streamCtx, reasoningEffortForIdle(cfg))
	defer idleCancel.Stop()
	outcome.watchIdle(idleCtx, idleCancel)
	cfg = pauseIdleDuringBudgetRecovery(cfg, idleCancel)

	agentEventCh := s.agent.Stream(idleCtx, cfg)
	modelID := rc.model.ID
	stored := false
	clientGone := false
	var lastSnapshot terminalSnapshot
	var hasSnapshot bool
	var toolCallCount int
	var hasVisibleOutput bool
	var persistedMessages []messagepkg.Message
	postPersistApplied := false
	failureEventForwarded := false
	var uncommittedText uncommittedStepText
	var failureRows []messagepkg.Message
	failureRecorded := false
	for event := range agentEventCh {
		idleCancel.Observe(event)

		// Track tool calls for adaptive idle timeout
		if event.Type == native.EventToolCallStart {
			toolCallCount++
		}

		// A failure event is recorded, not logged: the run's result record
		// reports how the run ended, once.
		_ = outcome.observe(event)
		if hasVisibleAgentStreamOutput(event) {
			hasVisibleOutput = true
		}
		uncommittedText.observe(event)
		if event.Type == native.EventAgentAbort && idleCancel.DidFire() && !clientGone {
			failureEvent := agentFailureStreamEvent(context.Cause(idleCtx))
			if failureData, marshalErr := json.Marshal(failureEvent); marshalErr == nil {
				select {
				case eventCh <- json.RawMessage(failureData):
					failureEventForwarded = true
				case <-ctx.Done():
					clientGone = true
				}
			}
		}

		data, err := json.Marshal(outcome.stampTerminal(publicAgentStreamEvent(event)))
		if err != nil {
			continue
		}

		// Forwarding the terminal event proposes the run's end, after which no
		// step can be written, so a failed run's history is written first.
		if event.IsTerminal() && stepCommitter != nil && !stored && !failureRecorded && !runOwnershipLost(ctx) {
			failureRows = s.recordStepRunFailure(ctx, stepCommitter, uncommittedText.String(), wsFailureCode(req, outcome))
			failureRecorded = true
		}

		if event.IsTerminal() && len(event.Messages) > 0 {
			if snap, ok := extractTerminalSnapshot(data); ok {
				if stepCommitter == nil {
					snap.reasoningTiming = takeTerminalReasoningTiming(reasoningTiming, event.Type)
				}
				snap.visibleOutput = hasVisibleOutput
				snap.failureCode = wsFailureCode(req, outcome)
				lastSnapshot = snap
				hasSnapshot = true
				outcome.observeSnapshot(snap)
				if !stored && !runOwnershipLost(ctx) && stepCommitter != nil {
					if storeErr := stepCommitter.finish(ctx, extractInputTokensFromUsage(snap.usage)); storeErr != nil {
						outcome.recordCause(storeErr)
						s.logger.ErrorContext(ctx, "ws step finalization failed", slog.Any("error", storeErr))
					} else {
						persistedMessages = append(stepCommitter.persistedMessages(), failureRows...)
						stored = true
					}
				} else if !stored && !runOwnershipLost(ctx) {
					persisted, storeErr := s.persistTerminalSnapshotResult(context.WithoutCancel(ctx), req, rc, snap)
					if storeErr != nil {
						outcome.recordCause(storeErr)
						s.logger.ErrorContext(ctx, "ws persist failed", slog.Any("error", storeErr))
					} else {
						persistedMessages = persisted
						stored = true
					}
				}
			}
		}

		if event.IsTerminal() && postPersist != nil && stepCommitter == nil && !postPersistApplied {
			if err := postPersist(context.WithoutCancel(ctx), persistedMessages); err != nil {
				outcome.setCause(err)
				return persistedMessages, outcome.deliveredOutcome(), err
			}
			postPersistApplied = true
		}

		if !clientGone && shouldForwardAfterIdleFailure(event, failureEventForwarded) {
			select {
			case eventCh <- json.RawMessage(data):
			case <-ctx.Done():
				clientGone = true
			}
		}
	}
	outcome.endStream()

	// Intermediate persistence on abort/error. A stream that ended without a
	// terminal event still writes its failure before its steps are finalized.
	if !stored && stepCommitter != nil && !runOwnershipLost(ctx) {
		if !failureRecorded {
			failureRows = s.recordStepRunFailure(ctx, stepCommitter, uncommittedText.String(), wsFailureCode(req, outcome))
		}
		if storeErr := stepCommitter.finish(ctx, rc.estimatedTokens); storeErr != nil {
			outcome.recordCause(storeErr)
			s.logger.ErrorContext(ctx, "ws step finalization failed", slog.Any("error", storeErr))
		} else {
			persistedMessages = append(stepCommitter.persistedMessages(), failureRows...)
		}
	} else if !stored {
		switch {
		case runOwnershipLost(ctx):
			s.logger.WarnContext(ctx, "skip persisting ws stream after run ownership loss",
				slog.String("bot_id", req.BotID),
				slog.String("chat_id", req.ChatID),
			)
		case hasSnapshot:
			persistedMessages = s.persistPartialResult(ctx, req, rc, lastSnapshot.sdkMessages, lastSnapshot.reasoningTiming, toolCallCount, idleCancel.DidFire(), hasVisibleOutput, lastSnapshot.failureCode, lastSnapshot.internalFeedbackIndexes)
		default:
			if code := wsFailureCode(req, outcome); code != "" {
				persisted, storeErr := s.persistTurnFailure(context.WithoutCancel(ctx), req, rc, code)
				if storeErr != nil {
					s.logger.ErrorContext(ctx, "ws failure persist failed", slog.Any("error", storeErr))
				} else {
					persistedMessages = persisted
				}
			} else {
				s.logger.InfoContext(ctx, "skip persisting ws stream without a failure code",
					slog.String("bot_id", req.BotID),
					slog.String("chat_id", req.ChatID),
				)
			}
		}
	}

	if idleCancel.DidFire() {
		s.logger.WarnContext(ctx, "agent ws stream aborted: inactivity timeout",
			slog.String("code", string(apperror.CodeOf(context.Cause(idleCtx)))),
			slog.String("bot_id", req.BotID),
			slog.String("chat_id", req.ChatID),
			slog.String("model_id", modelID),
			slog.Int("tool_calls", toolCallCount),
		)
		if !clientGone && !failureEventForwarded {
			timeoutEvent := agentFailureStreamEvent(context.Cause(idleCtx))
			if data, err := json.Marshal(timeoutEvent); err == nil {
				select {
				case eventCh <- json.RawMessage(data):
				case <-ctx.Done():
				}
			}
		}
	}

	if postPersist != nil && stepCommitter == nil && !postPersistApplied {
		if err := postPersist(context.WithoutCancel(ctx), persistedMessages); err != nil {
			outcome.setCause(err)
			return persistedMessages, outcome.deliveredOutcome(), err
		}
	}
	if commitErr := stepCommitter.err(); commitErr != nil && ctx.Err() == nil {
		outcome.recordCause(commitErr)
		return persistedMessages, outcome.deliveredOutcome(), commitErr
	}

	if idleCancel.DidFire() {
		return persistedMessages, RunOutcome{}, context.Cause(idleCtx)
	}
	// A failure found in the stream was already delivered there, so it is not
	// returned as an error; the outcome still names it for the terminal write.
	return persistedMessages, outcome.ownerOutcome(), nil
}

// recordStepRunFailure writes the history of a run whose steps are committed as
// they complete, when the run failed with code (see wsFailureCode). A write
// failure is logged: the run has already failed, and its outcome stays that
// failure.
func (s *Service) recordStepRunFailure(ctx context.Context, committer *agentStepCommitter, partialText string, code apperror.Code) []messagepkg.Message {
	if code == "" {
		return nil
	}
	persisted, err := committer.recordFailure(ctx, partialText, code)
	if err != nil {
		s.logger.ErrorContext(ctx, "ws step failure persist failed",
			slog.String("code", string(code)),
			slog.Any("error", err),
		)
	}
	return persisted
}

// persistTerminalSnapshot stores the SDK messages produced by an agent run
// (or partial run) into bot history. Triggers compaction when usage data
// indicates the context is large.
func (s *Service) persistTerminalSnapshot(ctx context.Context, req ChatRequest, rc resolvedContext, snap terminalSnapshot) error {
	_, err := s.persistTerminalSnapshotResult(ctx, req, rc, snap)
	return err
}

func (s *Service) persistTerminalSnapshotResult(ctx context.Context, req ChatRequest, rc resolvedContext, snap terminalSnapshot) ([]messagepkg.Message, error) {
	outputMessages := sdkMessagesWithOrigins(snap.sdkMessages, snap.internalFeedbackIndexes)
	if snap.failureCode != "" && (!snap.visibleOutput || !hasPersistableAssistantOutput(outputMessages)) {
		return s.persistTurnFailure(ctx, req, rc, snap.failureCode)
	}
	if snap.aborted && !snap.visibleOutput {
		s.logger.InfoContext(ctx, "skip persisting aborted terminal snapshot before visible output",
			slog.String("bot_id", req.BotID),
			slog.String("chat_id", req.ChatID),
			slog.Int("messages", len(outputMessages)),
		)
		return nil, nil
	}
	if !hasPersistableAssistantOutput(outputMessages) {
		s.logger.InfoContext(ctx, "skip persisting terminal snapshot without assistant output",
			slog.String("bot_id", req.BotID),
			slog.String("chat_id", req.ChatID),
			slog.Int("messages", len(outputMessages)),
		)
		return nil, nil
	}

	storeReq := req
	if req.ReusePersistedUserMessage {
		storeReq.UserMessagePersisted = true
	}
	roundMessages := prependTurnUserMessage(storeReq, outputMessages)

	if rc.injectedRecords != nil && len(*rc.injectedRecords) > 0 {
		roundMessages = interleaveInjectedMessages(roundMessages, *rc.injectedRecords)
	}

	persisted, err := s.storeRoundWithOptionsResult(ctx, storeReq, roundMessages, rc.model.ID, storeRoundOptions{
		AllowPendingToolCalls:  snap.deferredToolID != "",
		RequireCompletePersist: true,
		ContextLifecycle:       rc.runConfig.ContextLifecycle,
		ReasoningTiming:        snap.reasoningTiming,
	})
	if err != nil {
		return nil, err
	}
	if len(persisted) > 0 {
		if err := s.persistSessionWorkspaceTarget(ctx, storeReq); err != nil {
			return nil, err
		}
	}

	if inputTokens := extractInputTokensFromUsage(snap.usage); inputTokens > 0 {
		s.maybeCompact(context.WithoutCancel(ctx), req, rc, inputTokens)
	}

	return persisted, nil
}

func hasPersistableAssistantOutput(messages []ModelMessage) bool {
	for _, msg := range messages {
		if strings.EqualFold(strings.TrimSpace(msg.Role), "assistant") && !isEmptyAssistantMessage(msg) {
			return true
		}
	}
	return false
}

// persistPartialResult is the interrupt-path fallback. When the agent stream
// was interrupted (provider error, user abort, idle timeout) and partial SDK
// messages are available, those are persisted via the normal pipeline so
// orphaned tool_calls get repaired with synthetic error tool_results, keeping
// the conversation coherent for "ask the bot to continue".
//
// When no partial messages are available, a timeout or interrupt still
// persists a turn-level failure so the send has a durable identity. User
// cancel before visible output stays unpersisted so the draft can return.
func (s *Service) persistPartialResult(
	ctx context.Context,
	req ChatRequest,
	rc resolvedContext,
	partialMessages []sdk.Message,
	reasoningTiming []messagepkg.ReasoningTimingSegment,
	toolCallCount int,
	wasIdleTimeout bool,
	hasVisibleOutput bool,
	failureCode apperror.Code,
	feedbackIndexes []int,
) []messagepkg.Message {
	persistCtx := context.WithoutCancel(ctx)
	if failureCode == "" && wasIdleTimeout {
		failureCode = apperror.CodeAgentResponseTimeout
	}

	if len(partialMessages) > 0 {
		// AllowPendingToolCalls=false → repairToolCallClosures will inject
		// synthetic error tool_results for any tool_calls that never received
		// a real result, preserving the assistant ↔ tool pairing required by
		// downstream provider serializers (especially Anthropic).
		persisted, err := s.persistTerminalSnapshotResult(persistCtx, req, rc, terminalSnapshot{
			sdkMessages:             partialMessages,
			internalFeedbackIndexes: feedbackIndexes,
			reasoningTiming:         reasoningTiming,
			aborted:                 !hasVisibleOutput,
			visibleOutput:           hasVisibleOutput,
			failureCode:             failureCode,
		})
		if err == nil {
			s.logger.InfoContext(ctx, "persisted partial agent result",
				slog.String("bot_id", req.BotID),
				slog.Int("tool_calls", toolCallCount),
				slog.Int("partial_messages", len(partialMessages)),
				slog.Bool("idle_timeout", wasIdleTimeout),
			)
			// Trigger compaction on the failure path so that oversized
			// contexts don't deadlock (where the LLM can never succeed and
			// therefore compaction never fires).
			if rc.estimatedTokens > 0 {
				s.maybeCompact(persistCtx, req, rc, rc.estimatedTokens)
			}
			return persisted
		}
		s.logger.ErrorContext(ctx, "failed to persist partial agent messages",
			slog.String("bot_id", req.BotID),
			slog.Any("error", err),
		)
	}

	if failureCode != "" {
		persisted, err := s.persistTurnFailure(persistCtx, req, rc, failureCode)
		if err == nil {
			return persisted
		}
		s.logger.ErrorContext(ctx, "failed to persist turn-level stream failure",
			slog.String("bot_id", req.BotID),
			slog.Any("error", err),
		)
	}

	s.logger.InfoContext(ctx, "skip persisting failed stream without terminal snapshot",
		slog.String("bot_id", req.BotID),
		slog.Int("tool_calls", toolCallCount),
		slog.Bool("idle_timeout", wasIdleTimeout),
		slog.Bool("visible_output", hasVisibleOutput),
	)

	if rc.estimatedTokens > 0 {
		s.maybeCompact(persistCtx, req, rc, rc.estimatedTokens)
	}
	return nil
}

func (s *Service) persistTurnFailure(ctx context.Context, req ChatRequest, rc resolvedContext, code apperror.Code) ([]messagepkg.Message, error) {
	if code == "" || req.SkipHistoryTurn {
		return nil, nil
	}
	storeReq := req
	if req.ReusePersistedUserMessage {
		storeReq.UserMessagePersisted = true
	}
	output := []ModelMessage{{
		Role:    "assistant",
		Content: newTextContent(""),
	}}
	round := output
	if !storeReq.UserMessagePersisted {
		round = prependTurnUserMessage(storeReq, output)
	}
	assistantIdx := lastAssistantMessageIndex(round)
	if assistantIdx < 0 {
		return nil, nil
	}
	modelID := ""
	if rc.model.ID != "" {
		modelID = rc.model.ID
	}
	persisted, err := s.storeRoundWithOptionsResult(ctx, storeReq, round, modelID, storeRoundOptions{
		AllowEmptyAssistantText: true,
		MessageMetadataByIndex: map[int]map[string]any{
			assistantIdx: {
				messagepkg.AgentStepInterruptedMetadataKey: true,
				messagepkg.HistoryErrorCodeMetadataKey:     string(code),
			},
		},
		ContextLifecycle: rc.runConfig.ContextLifecycle,
	})
	if err != nil {
		return nil, err
	}
	s.logger.InfoContext(ctx, "persisted turn-level stream failure",
		slog.String("bot_id", req.BotID),
		slog.String("chat_id", req.ChatID),
		slog.String("code", string(code)),
	)
	return persisted, nil
}

// interleaveInjectedMessages inserts injected user messages at their correct
// positions within the round. Each record's InsertAfter value indicates how
// many output messages preceded the injection.
//
// round layout: [user_A, output_0, output_1, ..., output_N]
// InsertAfter=K → insert after round[K] (i.e. after the K-th output message).
func interleaveInjectedMessages(round []ModelMessage, injections []InjectedMessageRecord) []ModelMessage {
	if len(injections) == 0 {
		return round
	}
	result := make([]ModelMessage, 0, len(round)+len(injections))
	injIdx := 0
	for i, msg := range round {
		result = append(result, msg)
		for injIdx < len(injections) && injections[injIdx].InsertAfter == i {
			result = append(result, ModelMessage{
				Role:    "user",
				Content: newTextContent(injections[injIdx].HeaderifiedText),
			})
			injIdx++
		}
	}
	for ; injIdx < len(injections); injIdx++ {
		result = append(result, ModelMessage{
			Role:    "user",
			Content: newTextContent(injections[injIdx].HeaderifiedText),
		})
	}
	return result
}

func extractInputTokensFromUsage(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var u struct {
		InputTokens int `json:"inputTokens"`
	}
	if json.Unmarshal(raw, &u) != nil {
		return 0
	}
	return u.InputTokens
}
