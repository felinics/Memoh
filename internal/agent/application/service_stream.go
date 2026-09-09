package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/apperror"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
)

// WSStreamEvent represents a raw JSON event forwarded from the agent.
type WSStreamEvent = json.RawMessage

// terminalSnapshot captures the partial state extracted from a terminal
// agent event. It is used both for the success-path persistence and for the
// interrupted-path fallback so that real partial messages get saved instead
// of a synthetic placeholder.
type terminalSnapshot struct {
	sdkMessages     []sdk.Message
	usage           json.RawMessage
	reasoningTiming []messagepkg.ReasoningTimingSegment
	deferredToolID  string
	aborted         bool
	visibleOutput   bool
	failureCode     apperror.Code
}

func snapshotFailureCode(idleFired bool, cause error) apperror.Code {
	if idleFired {
		return apperror.CodeAgentResponseTimeout
	}
	switch code := apperror.CodeOf(cause); code {
	case apperror.CodeAgentResponseTimeout, apperror.CodeAgentResponseInterrupted:
		return code
	default:
		return ""
	}
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

func agentStreamEventError(event native.StreamEvent) error {
	if event.Type != native.EventError {
		return nil
	}
	if code := apperror.Code(strings.TrimSpace(event.Code)); code != "" {
		if _, ok := apperror.Lookup(code); ok {
			return apperror.New(code, nil)
		}
	}
	detail := strings.TrimSpace(event.Error)
	if detail == "" {
		detail = "agent stream failed"
	}
	return errors.New(detail)
}

func agentStreamLifecycleError(event native.StreamEvent) error {
	err := agentStreamEventError(event)
	if err == nil || apperror.CodeOf(err) != "" {
		return err
	}
	return apperror.Wrap(apperror.CodeAgentResponseInterrupted, err, nil)
}

func agentFailureStreamEvent(cause error) native.StreamEvent {
	public, ok := apperror.PublicFrom(cause, "")
	if !ok {
		public, _ = apperror.PublicFrom(
			apperror.Wrap(apperror.CodeAgentResponseInterrupted, cause, nil),
			"",
		)
	}
	return native.StreamEvent{
		Type:  native.EventError,
		Code:  string(public.Code),
		Error: public.Detail,
	}
}

func publicAgentStreamEvent(event native.StreamEvent) native.StreamEvent {
	if cause := agentStreamLifecycleError(event); cause != nil {
		return agentFailureStreamEvent(cause)
	}
	return event
}

func agentAbortCause(ctx context.Context) error {
	if ctx != nil {
		if cause := context.Cause(ctx); cause != nil {
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
		Type       string          `json:"type"`
		Messages   json.RawMessage `json:"messages"`
		Usage      json.RawMessage `json:"usage,omitempty"`
		ApprovalID string          `json:"approvalId,omitempty"`
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
		sdkMessages:    sdkMsgs,
		usage:          envelope.Usage,
		deferredToolID: strings.TrimSpace(envelope.ApprovalID),
		aborted:        envelope.Type == string(native.EventAgentAbort),
	}, true
}

// StreamChat runs a streaming chat via the internal agent.
func (s *Service) StreamChat(ctx context.Context, req ChatRequest) (<-chan StreamChunk, <-chan error) {
	chunkCh := make(chan StreamChunk)
	errCh := make(chan error, 1)
	go func() {
		defer close(chunkCh)
		defer close(errCh)
		streamReq := req
		if streamReq.RawQuery == "" {
			streamReq.RawQuery = strings.TrimSpace(streamReq.Query)
		}
		if err := rejectReservedSkillMetadataIfPresent(streamReq); err != nil {
			errCh <- err
			return
		}
		if err := s.rejectRequestedSkillsIfUnsupportedContext(ctx, streamReq); err != nil {
			errCh <- err
			return
		}
		dispatch, err := s.resolveRuntimeDispatch(ctx, streamReq)
		if err != nil {
			s.logger.Error("StreamChat: runtime dispatch failed",
				slog.String("bot_id", streamReq.BotID),
				slog.String("session_id", streamReq.ThreadID),
				slog.Any("error", err),
			)
			errCh <- err
			return
		}
		if dispatch.kind == dispatchExternal {
			if err := rejectExternalAgentWorkspaceTarget(streamReq); err != nil {
				errCh <- err
				return
			}
			s.streamRuntimeChunks(ctx, dispatch.driver, streamReq, chunkCh, errCh)
			return
		}
		streamCtx, preparedReq, prepareErr := s.prepareWorkspaceRequest(ctx, streamReq)
		if prepareErr != nil {
			errCh <- prepareErr
			return
		}
		streamReq = preparedReq

		if streamReq.RawQuery == "" {
			streamReq.RawQuery = strings.TrimSpace(streamReq.Query)
		}
		if !streamReq.UserMessagePersisted {
			streamReq, err = s.applyUserMessageHook(streamCtx, streamReq)
			if err != nil {
				s.logger.Error("agent stream user message hook failed",
					slog.String("bot_id", streamReq.BotID),
					slog.String("chat_id", streamReq.ChatID),
					slog.Any("error", err),
				)
				errCh <- err
				return
			}
		}
		rc, streamReq, err := s.resolve(streamCtx, streamReq)
		if err != nil {
			s.logger.Error("agent stream resolve failed",
				slog.String("bot_id", streamReq.BotID),
				slog.String("chat_id", streamReq.ChatID),
				slog.Any("error", err),
			)
			errCh <- err
			return
		}
		streamReq.Query = rc.query
		streamReq.RunID = rc.runConfig.RunID

		go s.maybeGenerateSessionTitle(context.WithoutCancel(streamCtx), streamReq, streamReq.RawQuery)

		cfg := rc.runConfig
		cfg.LiveToolStream = true
		cfg.CanRequestUserInput = s.canDeliverUserInputStream()
		reasoningTiming := newReasoningTimingTracker(nil)
		stepCommitter := s.newAgentStepCommitter(streamCtx, streamReq, rc)
		configureNativeReasoningTiming(&cfg, reasoningTiming, stepCommitter)
		cfg = s.prepareRunConfig(streamCtx, cfg)
		terminal := s.contextLifecycleTerminal(streamCtx, cfg)
		var lifecycleCause error
		var lifecycleDeferred bool
		defer func() {
			if !lifecycleDeferred {
				terminal(lifecycleCause)
			}
		}()

		// Wrap with idle timeout: if no events arrive within the adaptive timeout, cancel the stream.
		idleCtx, idleCancel := s.withStreamIdleTimeout(streamCtx, reasoningEffortForIdle(cfg))
		defer idleCancel.Stop()

		eventCh := s.agent.Stream(idleCtx, cfg)
		stored := false
		clientGone := false
		var lastSnapshot terminalSnapshot
		var hasSnapshot bool
		var toolCallCount int
		var hasVisibleOutput bool
		var terminalEventSeen bool
		var agentStreamErr error
		var failureEventForwarded bool
		for event := range eventCh {
			idleCancel.Reset() // each event resets the idle timer

			// Track tool calls for adaptive idle timeout and progress events
			if event.Type == native.EventToolCallStart {
				toolCallCount++
				idleCancel.RecordToolCall()
			}

			if eventErr := agentStreamLifecycleError(event); eventErr != nil {
				if lifecycleCause == nil {
					lifecycleCause = eventErr
				}
				if agentStreamErr == nil {
					agentStreamErr = eventErr
				}
				s.logger.Error("agent stream error",
					slog.String("bot_id", streamReq.BotID),
					slog.String("chat_id", streamReq.ChatID),
					slog.String("model_id", rc.model.ID),
					slog.String("code", event.Code),
					slog.String("error", event.Error),
				)
			}
			if event.IsTerminal() {
				terminalEventSeen = true
				lifecycleDeferred = strings.TrimSpace(event.ApprovalID) != ""
				if !lifecycleDeferred {
					switch event.Type {
					case native.EventAgentEnd:
						// A terminal success means an earlier retryable stream error recovered.
						lifecycleCause = nil
					case native.EventAgentAbort:
						if idleCancel.DidFire() {
							lifecycleCause = context.Cause(idleCtx)
						} else if context.Cause(streamCtx) != nil || lifecycleCause == nil {
							lifecycleCause = agentAbortCause(streamCtx)
						}
					}
				}
			}
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

			data, err := json.Marshal(publicAgentStreamEvent(event))
			if err != nil {
				continue
			}
			var terminalPersistErr error
			if event.IsTerminal() && len(event.Messages) > 0 {
				if snap, ok := extractTerminalSnapshot(data); ok {
					if stepCommitter == nil {
						snap.reasoningTiming = takeTerminalReasoningTiming(reasoningTiming, event.Type)
					}
					snap.visibleOutput = hasVisibleOutput
					snap.failureCode = snapshotFailureCode(idleCancel.DidFire(), lifecycleCause)
					lastSnapshot = snap
					hasSnapshot = true
					lifecycleDeferred = lifecycleDeferred || snap.deferredToolID != ""
					if snap.aborted && !lifecycleDeferred && lifecycleCause == nil {
						lifecycleCause = agentAbortCause(streamCtx)
					}
					if !stored && !runOwnershipLost(streamCtx) && stepCommitter != nil {
						if storeErr := stepCommitter.finish(streamCtx, extractInputTokensFromUsage(snap.usage)); storeErr != nil {
							terminalPersistErr = runtimeHistoryError(storeErr)
							if lifecycleCause == nil {
								lifecycleCause = terminalPersistErr
							}
							s.logger.Error("stream step finalization failed", slog.Any("error", storeErr))
						} else {
							stored = true
						}
					} else if !stored && !runOwnershipLost(streamCtx) {
						// Use WithoutCancel so persistence still succeeds even
						// when the parent ctx has already been cancelled by a
						// client disconnect or idle timeout.
						if storeErr := s.persistTerminalSnapshot(context.WithoutCancel(streamCtx), streamReq, rc, snap); storeErr != nil {
							terminalPersistErr = runtimeHistoryError(storeErr)
							if lifecycleCause == nil {
								lifecycleCause = terminalPersistErr
							}
							s.logger.Error("stream persist failed", slog.Any("error", storeErr))
						} else {
							stored = true
						}
					}
				}
			}
			if event.IsTerminal() && !stored && !runOwnershipLost(streamCtx) && terminalPersistErr == nil {
				switch {
				case !hasVisibleOutput:
					// A terminal event before any visible assistant output has no
					// output row to persist. The admitted user message is already
					// durable for both clean completion and cancellation.
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
				if terminalPersistErr != nil && lifecycleCause == nil {
					lifecycleCause = terminalPersistErr
				}
			}
			if event.IsTerminal() && (terminalPersistErr != nil || runOwnershipLost(streamCtx)) {
				// A terminal runtime proposal is recoverable only after the output
				// it names is durable. Withhold the terminal event on persistence
				// failure; the error channel finishes the run as failed instead.
				if terminalPersistErr != nil && agentStreamErr == nil {
					agentStreamErr = terminalPersistErr
				}
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
		if lifecycleCause == nil && !lifecycleDeferred {
			switch {
			case idleCancel.DidFire():
				lifecycleCause = context.Cause(idleCtx)
			case streamCtx.Err() != nil:
				lifecycleCause = context.Cause(streamCtx)
			case !terminalEventSeen:
				lifecycleCause = errors.New("agent stream ended without a terminal event")
			}
		}

		// Intermediate persistence on abort/error: persist only concrete
		// partial assistant/tool state. Failed sends without a terminal
		// snapshot are treated as unsent so the Web UI can restore the draft
		// without polluting history.
		if !stored && stepCommitter != nil && !runOwnershipLost(streamCtx) {
			if storeErr := stepCommitter.finish(streamCtx, rc.estimatedTokens); storeErr != nil {
				if lifecycleCause == nil {
					lifecycleCause = storeErr
				}
				s.logger.Error("stream step finalization failed", slog.Any("error", storeErr))
			}
		} else if !stored {
			switch {
			case runOwnershipLost(streamCtx):
				s.logger.Warn("skip persisting stream after run ownership loss",
					slog.String("bot_id", streamReq.BotID),
					slog.String("chat_id", streamReq.ChatID),
				)
			case hasSnapshot:
				_ = s.persistPartialResult(streamCtx, streamReq, rc, lastSnapshot.sdkMessages, lastSnapshot.reasoningTiming, toolCallCount, idleCancel.DidFire(), hasVisibleOutput, lastSnapshot.failureCode)
			default:
				if code := snapshotFailureCode(idleCancel.DidFire(), lifecycleCause); code != "" {
					if _, storeErr := s.persistTurnFailure(context.WithoutCancel(streamCtx), streamReq, rc, code); storeErr != nil {
						s.logger.Error("stream timeout persist failed", slog.Any("error", storeErr))
					}
				} else {
					s.logger.Info("skip persisting failed startup stream",
						slog.String("bot_id", streamReq.BotID),
						slog.String("chat_id", streamReq.ChatID),
					)
				}
			}
		}
		if commitErr := stepCommitter.err(); commitErr != nil && streamCtx.Err() == nil {
			if lifecycleCause == nil {
				lifecycleCause = commitErr
			}
			errCh <- commitErr
		}

		if idleCancel.DidFire() {
			s.logger.Warn("agent stream aborted: idle timeout (no events from provider)",
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
			agentStreamErr = context.Cause(idleCtx)
		}
		if agentStreamErr != nil {
			errCh <- agentStreamErr
		}
	}()
	return chunkCh, errCh
}

// StreamChatWS resolves the agent context and streams agent events.
// Events are sent on eventCh. When abortCh is closed, the context is cancelled.
func (s *Service) StreamChatWS(
	ctx context.Context,
	req ChatRequest,
	eventCh chan<- WSStreamEvent,
	abortCh <-chan struct{},
) error {
	_, err := s.streamChatWSResult(ctx, req, eventCh, abortCh)
	return err
}

func (s *Service) streamChatWSResult(
	ctx context.Context,
	req ChatRequest,
	eventCh chan<- WSStreamEvent,
	abortCh <-chan struct{},
) ([]messagepkg.Message, error) {
	return s.streamChatWSResultWithHooks(ctx, req, eventCh, abortCh, nil, nil)
}

func (s *Service) streamChatWSResultWithHooks(
	ctx context.Context,
	req ChatRequest,
	eventCh chan<- WSStreamEvent,
	abortCh <-chan struct{},
	preflight func(context.Context) error,
	postPersist func(context.Context, []messagepkg.Message) error,
) ([]messagepkg.Message, error) {
	if err := rejectReservedSkillMetadataIfPresent(req); err != nil {
		return nil, err
	}
	if err := s.rejectRequestedSkillsIfUnsupportedContext(ctx, req); err != nil {
		return nil, err
	}
	dispatch, err := s.resolveRuntimeDispatch(ctx, req)
	if err != nil {
		s.logger.Error("StreamChatWS: runtime dispatch failed",
			slog.String("bot_id", req.BotID),
			slog.String("session_id", req.ThreadID),
			slog.Any("error", err),
		)
		return nil, err
	}
	if dispatch.kind == dispatchExternal {
		if err := rejectExternalAgentWorkspaceTarget(req); err != nil {
			return nil, err
		}
		// Hooks currently mean retry/edit turn replacement. Runtimes that own
		// their conversation context have no rewind primitive, so running the
		// turn would leave that context inconsistent with the visible history.
		if preflight != nil || postPersist != nil {
			return nil, apperror.New(apperror.CodeExternalAgentTurnReplacementUnsupported, nil)
		}
		return nil, s.streamRuntimeWS(ctx, dispatch.driver, req, eventCh, abortCh)
	}
	var prepareErr error
	ctx, req, prepareErr = s.prepareWorkspaceRequest(ctx, req)
	if prepareErr != nil {
		return nil, prepareErr
	}

	if preflight != nil {
		if err := preflight(ctx); err != nil {
			return nil, err
		}
	}

	if req.RawQuery == "" {
		req.RawQuery = strings.TrimSpace(req.Query)
	}
	if !req.UserMessagePersisted && !req.ReusePersistedUserMessage {
		req, err = s.applyUserMessageHook(ctx, req)
		if err != nil {
			s.logger.Error("StreamChatWS: user message hook failed",
				slog.String("bot_id", req.BotID),
				slog.Any("error", err),
			)
			return nil, err
		}
	}
	rc, req, err := s.resolve(ctx, req)
	if err != nil {
		s.logger.Error("StreamChatWS: resolve failed",
			slog.String("bot_id", req.BotID),
			slog.Any("error", err),
		)
		return nil, fmt.Errorf("resolve: %w", err)
	}
	req.Query = rc.query
	req.RunID = rc.runConfig.RunID

	go s.maybeGenerateSessionTitle(context.WithoutCancel(ctx), req, req.RawQuery)

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
	cfg = s.prepareRunConfig(streamCtx, cfg)
	terminal := s.contextLifecycleTerminal(streamCtx, cfg)
	var lifecycleCause error
	var lifecycleDeferred bool
	defer func() {
		if !lifecycleDeferred {
			terminal(lifecycleCause)
		}
	}()

	// Wrap with idle timeout: if no events arrive within the adaptive timeout, cancel the stream.
	idleCtx, idleCancel := s.withStreamIdleTimeout(streamCtx, reasoningEffortForIdle(cfg))
	defer idleCancel.Stop()

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
	terminalEventSeen := false
	failureEventForwarded := false
	for event := range agentEventCh {
		idleCancel.Reset() // each event resets the idle timer

		// Track tool calls for adaptive idle timeout
		if event.Type == native.EventToolCallStart {
			toolCallCount++
			idleCancel.RecordToolCall()
		}

		if eventErr := agentStreamLifecycleError(event); eventErr != nil {
			if lifecycleCause == nil {
				lifecycleCause = eventErr
			}
			s.logger.Error("agent stream error",
				slog.String("bot_id", req.BotID),
				slog.String("chat_id", req.ChatID),
				slog.String("model_id", modelID),
				slog.String("error", event.Error),
			)
		}
		if event.IsTerminal() {
			terminalEventSeen = true
			lifecycleDeferred = strings.TrimSpace(event.ApprovalID) != ""
			if !lifecycleDeferred {
				switch event.Type {
				case native.EventAgentEnd:
					lifecycleCause = nil
				case native.EventAgentAbort:
					if idleCancel.DidFire() {
						lifecycleCause = context.Cause(idleCtx)
					} else if context.Cause(streamCtx) != nil || lifecycleCause == nil {
						lifecycleCause = agentAbortCause(streamCtx)
					}
				}
			}
		}
		if hasVisibleAgentStreamOutput(event) {
			hasVisibleOutput = true
		}
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

		data, err := json.Marshal(publicAgentStreamEvent(event))
		if err != nil {
			continue
		}

		var terminalPersistErr error
		if event.IsTerminal() && len(event.Messages) > 0 {
			if snap, ok := extractTerminalSnapshot(data); ok {
				if stepCommitter == nil {
					snap.reasoningTiming = takeTerminalReasoningTiming(reasoningTiming, event.Type)
				}
				snap.visibleOutput = hasVisibleOutput
				snap.failureCode = snapshotFailureCode(idleCancel.DidFire(), lifecycleCause)
				lastSnapshot = snap
				hasSnapshot = true
				lifecycleDeferred = lifecycleDeferred || snap.deferredToolID != ""
				if snap.aborted && !lifecycleDeferred && lifecycleCause == nil {
					lifecycleCause = agentAbortCause(streamCtx)
				}
				if !stored && !runOwnershipLost(ctx) && stepCommitter != nil {
					if storeErr := stepCommitter.finish(ctx, extractInputTokensFromUsage(snap.usage)); storeErr != nil {
						terminalPersistErr = runtimeHistoryError(storeErr)
						if lifecycleCause == nil {
							lifecycleCause = terminalPersistErr
						}
						s.logger.Error("ws step finalization failed", slog.Any("error", storeErr))
					} else {
						persistedMessages = stepCommitter.persistedMessages()
						stored = true
					}
				} else if !stored && !runOwnershipLost(ctx) {
					persisted, storeErr := s.persistTerminalSnapshotResult(context.WithoutCancel(ctx), req, rc, snap)
					if storeErr != nil {
						terminalPersistErr = runtimeHistoryError(storeErr)
						if lifecycleCause == nil {
							lifecycleCause = terminalPersistErr
						}
						s.logger.Error("ws persist failed", slog.Any("error", storeErr))
					} else {
						persistedMessages = persisted
						stored = true
					}
				}
			}
		}
		if event.IsTerminal() && !stored && !runOwnershipLost(ctx) && terminalPersistErr == nil {
			switch {
			case !hasVisibleOutput:
				// A terminal event before any visible assistant output has no
				// output row to persist. The admitted user message is already
				// durable for both clean completion and cancellation.
				stored = true
			case stepCommitter != nil:
				if storeErr := stepCommitter.finish(ctx, rc.estimatedTokens); storeErr != nil {
					terminalPersistErr = runtimeHistoryError(storeErr)
				} else {
					persistedMessages = stepCommitter.persistedMessages()
					stored = true
				}
			default:
				terminalPersistErr = runtimeHistoryError(errors.New("agent terminal event has no persistable snapshot"))
			}
			if terminalPersistErr != nil && lifecycleCause == nil {
				lifecycleCause = terminalPersistErr
			}
		}
		if event.IsTerminal() && terminalPersistErr != nil {
			// postPersist and terminal publication both require the canonical
			// history write. Returning here keeps the runtime recoverable instead
			// of proposing completion for output that never committed.
			lifecycleDeferred = false
			return persistedMessages, terminalPersistErr
		}
		if event.IsTerminal() && runOwnershipLost(ctx) {
			return persistedMessages, sessionruntime.ErrRunOwnershipLost
		}

		if event.IsTerminal() && postPersist != nil && !postPersistApplied {
			if err := postPersist(context.WithoutCancel(ctx), persistedMessages); err != nil {
				lifecycleCause = err
				lifecycleDeferred = false
				return persistedMessages, err
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
	if lifecycleCause == nil && !lifecycleDeferred {
		switch {
		case idleCancel.DidFire():
			lifecycleCause = context.Cause(idleCtx)
		case streamCtx.Err() != nil:
			lifecycleCause = context.Cause(streamCtx)
		case !terminalEventSeen:
			lifecycleCause = errors.New("agent stream ended without a terminal event")
		}
	}

	// Intermediate persistence on abort/error
	if !stored && stepCommitter != nil && !runOwnershipLost(ctx) {
		if storeErr := stepCommitter.finish(ctx, rc.estimatedTokens); storeErr != nil {
			if lifecycleCause == nil {
				lifecycleCause = storeErr
			}
			s.logger.Error("ws step finalization failed", slog.Any("error", storeErr))
		} else {
			persistedMessages = stepCommitter.persistedMessages()
		}
	} else if !stored {
		switch {
		case runOwnershipLost(ctx):
			s.logger.Warn("skip persisting ws stream after run ownership loss",
				slog.String("bot_id", req.BotID),
				slog.String("chat_id", req.ChatID),
			)
		case hasSnapshot:
			persistedMessages = s.persistPartialResult(ctx, req, rc, lastSnapshot.sdkMessages, lastSnapshot.reasoningTiming, toolCallCount, idleCancel.DidFire(), hasVisibleOutput, lastSnapshot.failureCode)
		default:
			if code := snapshotFailureCode(idleCancel.DidFire(), lifecycleCause); code != "" {
				persisted, storeErr := s.persistTurnFailure(context.WithoutCancel(ctx), req, rc, code)
				if storeErr != nil {
					s.logger.Error("ws timeout persist failed", slog.Any("error", storeErr))
				} else {
					persistedMessages = persisted
				}
			} else {
				s.logger.Info("skip persisting failed startup ws stream",
					slog.String("bot_id", req.BotID),
					slog.String("chat_id", req.ChatID),
				)
			}
		}
	}

	if idleCancel.DidFire() {
		s.logger.Warn("agent ws stream aborted: idle timeout (no events from provider)",
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

	if postPersist != nil && !postPersistApplied {
		if err := postPersist(context.WithoutCancel(ctx), persistedMessages); err != nil {
			lifecycleCause = err
			lifecycleDeferred = false
			return persistedMessages, err
		}
	}
	if commitErr := stepCommitter.err(); commitErr != nil && ctx.Err() == nil {
		if lifecycleCause == nil {
			lifecycleCause = commitErr
		}
		return persistedMessages, commitErr
	}

	if idleCancel.DidFire() {
		return persistedMessages, context.Cause(idleCtx)
	}
	return persistedMessages, nil
}

// persistTerminalSnapshot stores the SDK messages produced by an agent run
// (or partial run) into bot history. Triggers compaction when usage data
// indicates the context is large.
func (s *Service) persistTerminalSnapshot(ctx context.Context, req ChatRequest, rc resolvedContext, snap terminalSnapshot) error {
	_, err := s.persistTerminalSnapshotResult(ctx, req, rc, snap)
	return err
}

func (s *Service) persistTerminalSnapshotResult(ctx context.Context, req ChatRequest, rc resolvedContext, snap terminalSnapshot) ([]messagepkg.Message, error) {
	outputMessages := sdkMessagesToModelMessages(snap.sdkMessages)
	if snap.failureCode != "" && (!snap.visibleOutput || !hasPersistableAssistantOutput(outputMessages)) {
		return s.persistTurnFailure(ctx, req, rc, snap.failureCode)
	}
	if snap.aborted && !snap.visibleOutput {
		s.logger.Info("skip persisting aborted terminal snapshot before visible output",
			slog.String("bot_id", req.BotID),
			slog.String("chat_id", req.ChatID),
			slog.Int("messages", len(outputMessages)),
		)
		return nil, nil
	}
	if !hasPersistableAssistantOutput(outputMessages) {
		s.logger.Info("skip persisting terminal snapshot without assistant output",
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
		go s.maybeCompact(context.WithoutCancel(ctx), req, rc, inputTokens)
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
			sdkMessages:     partialMessages,
			reasoningTiming: reasoningTiming,
			aborted:         !hasVisibleOutput,
			visibleOutput:   hasVisibleOutput,
			failureCode:     failureCode,
		})
		if err == nil {
			s.logger.Info("persisted partial agent result",
				slog.String("bot_id", req.BotID),
				slog.Int("tool_calls", toolCallCount),
				slog.Int("partial_messages", len(partialMessages)),
				slog.Bool("idle_timeout", wasIdleTimeout),
			)
			// Trigger compaction on the failure path so that oversized
			// contexts don't deadlock (where the LLM can never succeed and
			// therefore compaction never fires).
			if rc.estimatedTokens > 0 {
				go s.maybeCompact(persistCtx, req, rc, rc.estimatedTokens)
			}
			return persisted
		}
		s.logger.Error("failed to persist partial agent messages",
			slog.String("bot_id", req.BotID),
			slog.Any("error", err),
		)
	}

	if failureCode != "" {
		persisted, err := s.persistTurnFailure(persistCtx, req, rc, failureCode)
		if err == nil {
			return persisted
		}
		s.logger.Error("failed to persist turn-level stream failure",
			slog.String("bot_id", req.BotID),
			slog.Any("error", err),
		)
	}

	s.logger.Info("skip persisting failed stream without terminal snapshot",
		slog.String("bot_id", req.BotID),
		slog.Int("tool_calls", toolCallCount),
		slog.Bool("idle_timeout", wasIdleTimeout),
		slog.Bool("visible_output", hasVisibleOutput),
	)

	if rc.estimatedTokens > 0 {
		go s.maybeCompact(persistCtx, req, rc, rc.estimatedTokens)
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
	s.logger.Info("persisted turn-level stream failure",
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
