package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	sdk "github.com/memohai/twilight-ai/sdk"

	"github.com/memohai/memoh/internal/agent/runtime/native"
	"github.com/memohai/memoh/internal/apperror"
	messagepkg "github.com/memohai/memoh/internal/chat/message"
)

// WSStreamEvent represents a raw JSON event forwarded from the agent.
type WSStreamEvent = json.RawMessage

// terminalSnapshot captures the partial state extracted from a terminal
// agent event. It is used both for the success-path persistence and for the
// interrupted-path fallback so that real partial messages get saved instead
// of a synthetic placeholder.
type terminalSnapshot struct {
	sdkMessages    []sdk.Message
	usage          json.RawMessage
	deferredToolID string
	aborted        bool
	visibleOutput  bool
}

// interruptedTurnMarker is a stable code persisted when a turn aborts before
// producing any visible output (e.g. a provider timeout on the very first
// response). The Web UI localizes this code for display; keeping the stored
// value language-neutral also keeps it safe when replayed into model context.
const interruptedTurnMarker = "[turn-interrupted]"

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

func shouldPersistTerminalEvent(event native.StreamEvent, idle *idleCancel, visibleOutput bool) bool {
	if !event.IsTerminal() {
		return false
	}
	if event.Type == native.EventAgentAbort && !visibleOutput && (idle == nil || !idle.DidFire()) {
		return false
	}
	if len(event.Messages) > 0 {
		return true
	}
	// An idle timeout can produce an aborted terminal event before the SDK has
	// emitted its first message. Explicit user cancellation should not create a
	// synthetic history row for an unsent turn.
	return event.Type == native.EventAgentAbort && idle != nil && idle.DidFire()
}

func agentStreamEventError(event native.StreamEvent) error {
	if event.Type != native.EventError {
		return nil
	}
	detail := strings.TrimSpace(event.Error)
	if detail == "" {
		detail = "agent stream failed"
	}
	return errors.New(detail)
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
// raw SDK messages plus auxiliary metadata. Empty message arrays are accepted
// for abort events because a provider can time out before emitting its first
// SDK message; other terminal events still require usable messages.
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
	var sdkMsgs []sdk.Message
	if len(envelope.Messages) > 0 {
		if err := json.Unmarshal(envelope.Messages, &sdkMsgs); err != nil {
			return terminalSnapshot{}, false
		}
	}
	if len(sdkMsgs) == 0 && envelope.Type != string(native.EventAgentAbort) {
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
		if ok, err := s.isACPAgentSession(ctx, streamReq); err != nil {
			s.logger.Error("StreamChat: ACP session check failed",
				slog.String("bot_id", streamReq.BotID),
				slog.String("session_id", streamReq.ThreadID),
				slog.Any("error", err),
			)
			errCh <- err
			return
		} else if ok {
			if err := rejectACPWorkspaceTarget(streamReq); err != nil {
				errCh <- err
				return
			}
			s.streamACPAgentChunks(ctx, streamReq, chunkCh, errCh)
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
		var err error
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
		stepCommitter := s.newAgentStepCommitter(streamCtx, streamReq, rc)
		if stepCommitter != nil {
			cfg.OnStepCommitted = stepCommitter.commit
			cfg.OnStepInterrupted = stepCommitter.interrupt
		}
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
		idleCtx, idleCancel := withIdleTimeout(streamCtx)
		defer idleCancel.Stop()

		eventCh := s.agent.Stream(idleCtx, cfg)
		stored := false
		clientGone := false
		var lastSnapshot terminalSnapshot
		var hasSnapshot bool
		var toolCallCount int
		var hasVisibleOutput bool
		var terminalEventSeen bool
		for event := range eventCh {
			idleCancel.Reset() // each event resets the idle timer

			// Track tool calls for adaptive idle timeout and progress events
			if event.Type == native.EventToolCallStart {
				toolCallCount++
				idleCancel.RecordToolCall()
			}

			if eventErr := agentStreamEventError(event); eventErr != nil {
				if lifecycleCause == nil {
					lifecycleCause = eventErr
				}
				s.logger.Error("agent stream error",
					slog.String("bot_id", streamReq.BotID),
					slog.String("chat_id", streamReq.ChatID),
					slog.String("model_id", rc.model.ID),
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
						if context.Cause(streamCtx) != nil || lifecycleCause == nil {
							lifecycleCause = agentAbortCause(streamCtx)
						}
					}
				}
			}
			if hasVisibleAgentStreamOutput(event) {
				hasVisibleOutput = true
			}

			data, err := json.Marshal(event)
			if err != nil {
				continue
			}
			if shouldPersistTerminalEvent(event, idleCancel, hasVisibleOutput) {
				if snap, ok := extractTerminalSnapshot(data); ok {
					snap.visibleOutput = hasVisibleOutput
					lastSnapshot = snap
					hasSnapshot = true
					lifecycleDeferred = lifecycleDeferred || snap.deferredToolID != ""
					if snap.aborted && !lifecycleDeferred && lifecycleCause == nil {
						lifecycleCause = agentAbortCause(streamCtx)
					}
					if !stored && !runOwnershipLost(streamCtx) && stepCommitter != nil {
						if storeErr := stepCommitter.finish(streamCtx, extractInputTokensFromUsage(snap.usage)); storeErr != nil {
							if lifecycleCause == nil {
								lifecycleCause = storeErr
							}
							s.logger.Error("stream step finalization failed", slog.Any("error", storeErr))
						} else if len(stepCommitter.persistedMessages()) > 0 {
							stored = true
						}
					} else if !stored && !runOwnershipLost(streamCtx) {
						// Use WithoutCancel so persistence still succeeds even
						// when the parent ctx has already been cancelled by a
						// client disconnect or idle timeout.
						if storeErr := s.persistTerminalSnapshot(context.WithoutCancel(streamCtx), streamReq, rc, snap); storeErr != nil {
							if lifecycleCause == nil {
								lifecycleCause = storeErr
							}
							s.logger.Error("stream persist failed", slog.Any("error", storeErr))
						} else {
							stored = true
						}
					}
				}
			}

			// Forward to the client unless the client is already gone. Once
			// the client disconnects we keep draining eventCh so the agent
			// goroutine can finish and the terminal event (with partial
			// messages) is captured for persistence above.
			if !clientGone {
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
				lifecycleCause = context.DeadlineExceeded
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
				_ = s.persistPartialResult(streamCtx, streamReq, rc, lastSnapshot.sdkMessages, toolCallCount, idleCancel.DidFire(), hasVisibleOutput)
			default:
				s.logger.Info("skip persisting failed startup stream",
					slog.String("bot_id", streamReq.BotID),
					slog.String("chat_id", streamReq.ChatID),
				)
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
			// Notify the client that the stream was terminated due to idle timeout.
			if !clientGone {
				timeoutEvent := native.StreamEvent{
					Type:  native.EventError,
					Error: fmt.Sprintf("stream timeout: no response from model provider (after %d tool calls)", toolCallCount),
				}
				if data, err := json.Marshal(timeoutEvent); err == nil {
					select {
					case chunkCh <- StreamChunk(data):
					case <-streamCtx.Done():
					}
				}
			}
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
	if ok, err := s.isACPAgentSession(ctx, req); err != nil {
		s.logger.Error("StreamChatWS: ACP session check failed",
			slog.String("bot_id", req.BotID),
			slog.String("session_id", req.ThreadID),
			slog.Any("error", err),
		)
		return nil, err
	} else if ok {
		if err := rejectACPWorkspaceTarget(req); err != nil {
			return nil, err
		}
		// Hooks currently mean retry/edit turn replacement. ACP runtimes have
		// no rewind primitive, so running the turn would leave their in-process
		// context inconsistent with the visible history.
		if preflight != nil || postPersist != nil {
			return nil, apperror.New(apperror.CodeACPTurnReplacementUnsupported, nil)
		}
		return nil, s.streamACPAgentWS(ctx, req, eventCh, abortCh)
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
	var err error
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
	stepCommitter := s.newAgentStepCommitter(streamCtx, req, rc)
	if stepCommitter != nil {
		cfg.OnStepCommitted = stepCommitter.commit
		cfg.OnStepInterrupted = stepCommitter.interrupt
	}
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
	idleCtx, idleCancel := withIdleTimeout(streamCtx)
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
	for event := range agentEventCh {
		idleCancel.Reset() // each event resets the idle timer

		// Track tool calls for adaptive idle timeout
		if event.Type == native.EventToolCallStart {
			toolCallCount++
			idleCancel.RecordToolCall()
		}

		if eventErr := agentStreamEventError(event); eventErr != nil {
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
					if context.Cause(streamCtx) != nil || lifecycleCause == nil {
						lifecycleCause = agentAbortCause(streamCtx)
					}
				}
			}
		}
		if hasVisibleAgentStreamOutput(event) {
			hasVisibleOutput = true
		}

		data, err := json.Marshal(event)
		if err != nil {
			continue
		}

		if shouldPersistTerminalEvent(event, idleCancel, hasVisibleOutput) {
			if snap, ok := extractTerminalSnapshot(data); ok {
				snap.visibleOutput = hasVisibleOutput
				lastSnapshot = snap
				hasSnapshot = true
				lifecycleDeferred = lifecycleDeferred || snap.deferredToolID != ""
				if snap.aborted && !lifecycleDeferred && lifecycleCause == nil {
					lifecycleCause = agentAbortCause(streamCtx)
				}
				if !stored && !runOwnershipLost(ctx) && stepCommitter != nil {
					if storeErr := stepCommitter.finish(ctx, extractInputTokensFromUsage(snap.usage)); storeErr != nil {
						if lifecycleCause == nil {
							lifecycleCause = storeErr
						}
						s.logger.Error("ws step finalization failed", slog.Any("error", storeErr))
					} else if len(stepCommitter.persistedMessages()) > 0 {
						persistedMessages = stepCommitter.persistedMessages()
						stored = true
					}
				} else if !stored && !runOwnershipLost(ctx) {
					persisted, storeErr := s.persistTerminalSnapshotResult(context.WithoutCancel(ctx), req, rc, snap)
					if storeErr != nil {
						if lifecycleCause == nil {
							lifecycleCause = storeErr
						}
						s.logger.Error("ws persist failed", slog.Any("error", storeErr))
					} else {
						persistedMessages = persisted
						stored = true
					}
				}
			}
		}

		if event.IsTerminal() && postPersist != nil && !postPersistApplied {
			if err := postPersist(context.WithoutCancel(ctx), persistedMessages); err != nil {
				lifecycleCause = err
				lifecycleDeferred = false
				return persistedMessages, err
			}
			postPersistApplied = true
		}

		if !clientGone {
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
			lifecycleCause = context.DeadlineExceeded
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
			persistedMessages = s.persistPartialResult(ctx, req, rc, lastSnapshot.sdkMessages, toolCallCount, idleCancel.DidFire(), hasVisibleOutput)
		default:
			s.logger.Info("skip persisting failed startup ws stream",
				slog.String("bot_id", req.BotID),
				slog.String("chat_id", req.ChatID),
			)
		}
	}

	if idleCancel.DidFire() {
		s.logger.Warn("agent ws stream aborted: idle timeout (no events from provider)",
			slog.String("bot_id", req.BotID),
			slog.String("chat_id", req.ChatID),
			slog.String("model_id", modelID),
			slog.Int("tool_calls", toolCallCount),
		)
		// Notify the client that the stream was terminated due to idle timeout.
		if !clientGone {
			timeoutEvent := native.StreamEvent{
				Type:  native.EventError,
				Error: fmt.Sprintf("stream timeout: no response from model provider (after %d tool calls)", toolCallCount),
			}
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
	if snap.aborted && !snap.visibleOutput {
		// Issue #1010 family: a turn that aborted before producing any visible
		// output used to be dropped here, so the turn vanished from history with
		// no trace and the user only saw silence. Instead of returning empty,
		// replace the (empty) partial output with a stable, human-visible marker
		// so the interruption leaves an auditable trace in the session history.
		// The [turn-interrupted] prefix lets UI/history tooling recognize the row
		// as an interrupted-turn marker rather than a normal assistant reply.
		s.logger.Info("persisting interrupted-turn marker (aborted before visible output)",
			slog.String("bot_id", req.BotID),
			slog.String("chat_id", req.ChatID),
			slog.Int("messages", len(outputMessages)),
		)
		outputMessages = []ModelMessage{
			{
				Role:    "assistant",
				Content: newTextContent(interruptedTurnMarker),
			},
		}
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
		AllowPendingToolCalls: snap.deferredToolID != "",
		ContextLifecycle:      rc.runConfig.ContextLifecycle,
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
// When no partial messages are available, failures are not persisted. The UI
// can show temporary errors without committing a user-only history row for a
// send that did not successfully produce an assistant turn.
func (s *Service) persistPartialResult(
	ctx context.Context,
	req ChatRequest,
	rc resolvedContext,
	partialMessages []sdk.Message,
	toolCallCount int,
	wasIdleTimeout bool,
	hasVisibleOutput bool,
) []messagepkg.Message {
	persistCtx := context.WithoutCancel(ctx)

	if len(partialMessages) > 0 {
		// AllowPendingToolCalls=false → repairToolCallClosures will inject
		// synthetic error tool_results for any tool_calls that never received
		// a real result, preserving the assistant ↔ tool pairing required by
		// downstream provider serializers (especially Anthropic).
		persisted, err := s.persistTerminalSnapshotResult(persistCtx, req, rc, terminalSnapshot{
			sdkMessages:   partialMessages,
			aborted:       !hasVisibleOutput,
			visibleOutput: hasVisibleOutput,
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
	if wasIdleTimeout && !hasVisibleOutput {
		persisted, err := s.persistTerminalSnapshotResult(persistCtx, req, rc, terminalSnapshot{
			aborted: true,
		})
		if err == nil {
			s.logger.Info("persisted interrupted marker after idle timeout",
				slog.String("bot_id", req.BotID),
				slog.Int("tool_calls", toolCallCount),
			)
			return persisted
		}
		s.logger.Error("failed to persist interrupted marker after idle timeout",
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
