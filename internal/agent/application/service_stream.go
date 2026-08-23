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

const streamRecoveryRecordPrefix = "\x1e"

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

// recordVisibleAgentText is the compact recovery log used when the native
// runtime cannot produce a terminal Messages snapshot after cancellation. A
// text-only stream stays as plain text. As soon as a non-text visible event is
// observed, the buffer switches to record mode and preserves the full visible
// event sequence so recovery can reconstruct persistence-safe SDK state.
func recordVisibleAgentText(dst *strings.Builder, event native.StreamEvent) {
	if dst == nil || !hasVisibleAgentStreamOutput(event) {
		return
	}
	recordMode := strings.HasPrefix(dst.String(), streamRecoveryRecordPrefix)
	if event.Type == native.EventTextDelta && !recordMode {
		dst.WriteString(event.Delta)
		return
	}
	if !recordMode {
		if priorText := dst.String(); priorText != "" {
			dst.Reset()
			writeStreamRecoveryEvent(dst, native.StreamEvent{Type: native.EventTextDelta, Delta: priorText})
		}
	}
	writeStreamRecoveryEvent(dst, event)
}

func writeStreamRecoveryEvent(dst *strings.Builder, event native.StreamEvent) {
	if dst == nil {
		return
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return
	}
	dst.WriteString(streamRecoveryRecordPrefix)
	dst.Write(raw)
	dst.WriteByte('\n')
}

func restoreVisibleTextSnapshot(snap *terminalSnapshot, recovery string) {
	if snap == nil || !snap.aborted || !snap.visibleOutput || len(snap.sdkMessages) > 0 || strings.TrimSpace(recovery) == "" {
		return
	}
	if !strings.HasPrefix(recovery, streamRecoveryRecordPrefix) {
		snap.sdkMessages = []sdk.Message{sdk.AssistantMessage(recovery)}
		return
	}
	if recovered := recoverStreamedSDKMessages(recovery); len(recovered) > 0 {
		snap.sdkMessages = recovered
	}
}

func recoverStreamedSDKMessages(recovery string) []sdk.Message {
	var messages []sdk.Message
	seenToolCalls := make(map[string]bool)
	for _, record := range strings.Split(recovery, streamRecoveryRecordPrefix) {
		record = strings.TrimSpace(record)
		if record == "" {
			continue
		}
		var event native.StreamEvent
		if err := json.Unmarshal([]byte(record), &event); err != nil {
			continue
		}
		switch event.Type {
		case native.EventTextDelta:
			appendRecoveredAssistantText(&messages, event.Delta)
		case native.EventReasoningDelta:
			appendRecoveredAssistantReasoning(&messages, event.Delta)
		case native.EventToolCallInputStart, native.EventToolCallStart:
			key := strings.TrimSpace(event.ToolCallID)
			if key == "" {
				key = strings.TrimSpace(event.ToolName)
			}
			if key != "" && !seenToolCalls[key] {
				seenToolCalls[key] = true
				appendRecoveredAssistantPart(&messages, sdk.ToolCallPart{
					ToolCallID: event.ToolCallID,
					ToolName:   event.ToolName,
					Input:      event.Input,
				})
			}
		case native.EventToolCallProgress:
			if len(messages) == 0 {
				appendRecoveredAssistantText(&messages, streamedEventTrace("tool progress", firstNonEmpty(event.ToolName, event.ToolCallID)))
			}
		case native.EventToolCallEnd:
			key := strings.TrimSpace(event.ToolCallID)
			if key == "" {
				key = strings.TrimSpace(event.ToolName)
			}
			if key != "" && !seenToolCalls[key] {
				seenToolCalls[key] = true
				appendRecoveredAssistantPart(&messages, sdk.ToolCallPart{
					ToolCallID: event.ToolCallID,
					ToolName:   event.ToolName,
					Input:      event.Input,
				})
			}
			if strings.TrimSpace(event.ToolCallID) != "" || strings.TrimSpace(event.ToolName) != "" {
				result := event.Result
				isError := strings.TrimSpace(event.Error) != ""
				if result == nil && isError {
					result = event.Error
				}
				messages = append(messages, sdk.ToolMessage(sdk.ToolResultPart{
					ToolCallID: event.ToolCallID,
					ToolName:   event.ToolName,
					Result:     result,
					IsError:    isError,
				}))
			} else if len(messages) == 0 {
				appendRecoveredAssistantText(&messages, streamedEventTrace("tool completed", ""))
			}
		case native.EventToolApprovalRequest:
			if len(messages) == 0 {
				appendRecoveredAssistantText(&messages, streamedEventTrace("tool approval requested", firstNonEmpty(event.ToolName, event.ToolCallID)))
			}
		case native.EventUserInputRequest:
			if len(messages) == 0 {
				appendRecoveredAssistantText(&messages, streamedEventTrace("user input requested", firstNonEmpty(event.ToolName, event.UserInputID)))
			}
		case native.EventAttachment:
			for _, attachment := range event.Attachments {
				label := firstNonEmpty(attachment.Name, attachment.Path, attachment.URL, attachment.ContentHash, attachment.Type)
				appendRecoveredAssistantText(&messages, streamedEventTrace("attachment", label))
			}
		case native.EventReaction:
			for _, reaction := range event.Reactions {
				appendRecoveredAssistantText(&messages, streamedEventTrace("reaction", reaction.Emoji))
			}
		case native.EventSpeech:
			for _, speech := range event.Speeches {
				appendRecoveredAssistantText(&messages, streamedEventTrace("speech", speech.Text))
			}
		}
	}
	return messages
}

func appendRecoveredAssistantPart(messages *[]sdk.Message, part sdk.MessagePart) {
	if messages == nil || part == nil {
		return
	}
	if len(*messages) > 0 && (*messages)[len(*messages)-1].Role == sdk.MessageRoleAssistant {
		last := &(*messages)[len(*messages)-1]
		last.Content = append(last.Content, part)
		return
	}
	*messages = append(*messages, sdk.Message{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{part}})
}

func appendRecoveredAssistantText(messages *[]sdk.Message, text string) {
	if strings.TrimSpace(text) == "" || messages == nil {
		return
	}
	if len(*messages) > 0 {
		last := &(*messages)[len(*messages)-1]
		if last.Role == sdk.MessageRoleAssistant && len(last.Content) > 0 {
			if part, ok := last.Content[len(last.Content)-1].(sdk.TextPart); ok {
				part.Text += text
				last.Content[len(last.Content)-1] = part
				return
			}
		}
	}
	appendRecoveredAssistantPart(messages, sdk.TextPart{Text: text})
}

func appendRecoveredAssistantReasoning(messages *[]sdk.Message, text string) {
	if strings.TrimSpace(text) == "" || messages == nil {
		return
	}
	if len(*messages) > 0 {
		last := &(*messages)[len(*messages)-1]
		if last.Role == sdk.MessageRoleAssistant && len(last.Content) > 0 {
			if part, ok := last.Content[len(last.Content)-1].(sdk.ReasoningPart); ok {
				part.Text += text
				last.Content[len(last.Content)-1] = part
				return
			}
		}
	}
	appendRecoveredAssistantPart(messages, sdk.ReasoningPart{Text: text})
}

func streamedEventTrace(kind, detail string) string {
	kind = strings.TrimSpace(kind)
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return "[streamed " + kind + "]"
	}
	return "[streamed " + kind + ": " + detail + "]"
}

// providerTimedOut is variadic so older continuation call sites that do not
// carry provider-timeout state remain source-compatible. Callers that can
// distinguish a provider timeout pass it explicitly.
func shouldPersistTerminalEvent(event native.StreamEvent, idle *idleCancel, visibleOutput bool, providerTimedOut ...bool) bool {
	if !event.IsTerminal() {
		return false
	}
	interrupted := idle != nil && idle.DidFire()
	if len(providerTimedOut) > 0 && providerTimedOut[0] {
		interrupted = true
	}
	if len(event.Messages) > 0 {
		return true
	}
	// Empty explicit cancellation still leaves no synthetic row when nothing
	// was shown. If the user already saw output, however, persist its recovery
	// snapshot even when cancellation rather than a timeout caused the abort.
	return event.Type == native.EventAgentAbort && (interrupted || visibleOutput)
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
		var visibleText strings.Builder
		var providerTimedOut bool
		var terminalEventSeen bool
		var agentStreamErr error
		for event := range eventCh {
			idleCancel.Reset() // each event resets the idle timer

			// Track tool calls for adaptive idle timeout and progress events.
			if event.Type == native.EventToolCallStart {
				toolCallCount++
				idleCancel.RecordToolCall()
			}

			if eventErr := agentStreamEventError(event); eventErr != nil {
				if native.IsTimeoutStreamError(eventErr) {
					providerTimedOut = true
				}
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
						providerTimedOut = false
					case native.EventAgentAbort:
						if context.Cause(streamCtx) != nil || lifecycleCause == nil {
							lifecycleCause = agentAbortCause(streamCtx)
						}
					}
				}
			}
			recordVisibleAgentText(&visibleText, event)
			if hasVisibleAgentStreamOutput(event) {
				hasVisibleOutput = true
			}

			data, err := json.Marshal(event)
			if err != nil {
				continue
			}
			if shouldPersistTerminalEvent(event, idleCancel, hasVisibleOutput, providerTimedOut) {
				if snap, ok := extractTerminalSnapshot(data); ok {
					snap.visibleOutput = hasVisibleOutput
					restoreVisibleTextSnapshot(&snap, visibleText.String())
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
						// Use WithoutCancel so persistence still succeeds even when the
						// parent ctx has already been cancelled by a client disconnect or timeout.
						persisted, storeErr := s.persistTerminalSnapshotResult(context.WithoutCancel(streamCtx), streamReq, rc, snap)
						if storeErr != nil {
							if lifecycleCause == nil {
								lifecycleCause = storeErr
							}
							s.logger.Error("stream persist failed", slog.Any("error", storeErr))
						} else {
							stored = len(persisted) > 0
						}
					}
				}
			}

			// Forward to the client unless the client is already gone. Once the
			// client disconnects we keep draining eventCh so the terminal event can
			// still be captured for persistence above.
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

		interruptedByTimeout := idleCancel.DidFire() || providerTimedOut
		// Intermediate persistence on abort/error. If the step committer already
		// finalized an empty interrupted step, explicitly fall back to the terminal
		// snapshot (including recovered visible text) before synthesizing a marker.
		if !stored && stepCommitter != nil && !runOwnershipLost(streamCtx) {
			if storeErr := stepCommitter.finish(streamCtx, rc.estimatedTokens); storeErr != nil {
				if lifecycleCause == nil {
					lifecycleCause = storeErr
				}
				s.logger.Error("stream step finalization failed", slog.Any("error", storeErr))
			} else if len(stepCommitter.persistedMessages()) > 0 {
				stored = true
			} else if hasSnapshot && len(lastSnapshot.sdkMessages) > 0 {
				persisted := s.persistPartialResult(streamCtx, streamReq, rc, lastSnapshot.sdkMessages, toolCallCount, interruptedByTimeout, hasVisibleOutput)
				stored = len(persisted) > 0
			} else if interruptedByTimeout && !hasVisibleOutput {
				persisted := s.persistPartialResult(streamCtx, streamReq, rc, nil, toolCallCount, true, false)
				stored = len(persisted) > 0
			}
		} else if !stored {
			switch {
			case runOwnershipLost(streamCtx):
				s.logger.Warn("skip persisting stream after run ownership loss",
					slog.String("bot_id", streamReq.BotID),
					slog.String("chat_id", streamReq.ChatID),
				)
			case hasSnapshot:
				_ = s.persistPartialResult(streamCtx, streamReq, rc, lastSnapshot.sdkMessages, toolCallCount, interruptedByTimeout, hasVisibleOutput)
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
		// Hooks currently mean retry/edit turn replacement. ACP runtimes have no
		// rewind primitive, so running the turn would leave their in-process
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
	var visibleText strings.Builder
	var providerTimedOut bool
	var persistedMessages []messagepkg.Message
	terminalEventSeen := false
	for event := range agentEventCh {
		idleCancel.Reset() // each event resets the idle timer

		if event.Type == native.EventToolCallStart {
			toolCallCount++
			idleCancel.RecordToolCall()
		}

		if eventErr := agentStreamEventError(event); eventErr != nil {
			if native.IsTimeoutStreamError(eventErr) {
				providerTimedOut = true
			}
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
					providerTimedOut = false
				case native.EventAgentAbort:
					if context.Cause(streamCtx) != nil || lifecycleCause == nil {
						lifecycleCause = agentAbortCause(streamCtx)
					}
				}
			}
		}
		recordVisibleAgentText(&visibleText, event)
		if hasVisibleAgentStreamOutput(event) {
			hasVisibleOutput = true
		}

		data, err := json.Marshal(event)
		if err != nil {
			continue
		}

		if shouldPersistTerminalEvent(event, idleCancel, hasVisibleOutput, providerTimedOut) {
			if snap, ok := extractTerminalSnapshot(data); ok {
				snap.visibleOutput = hasVisibleOutput
				restoreVisibleTextSnapshot(&snap, visibleText.String())
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
						stored = len(persisted) > 0
					}
				}
			}
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

	interruptedByTimeout := idleCancel.DidFire() || providerTimedOut
	if !stored && stepCommitter != nil && !runOwnershipLost(ctx) {
		if storeErr := stepCommitter.finish(ctx, rc.estimatedTokens); storeErr != nil {
			if lifecycleCause == nil {
				lifecycleCause = storeErr
			}
			s.logger.Error("ws step finalization failed", slog.Any("error", storeErr))
		} else if len(stepCommitter.persistedMessages()) > 0 {
			persistedMessages = stepCommitter.persistedMessages()
			stored = true
		} else if hasSnapshot && len(lastSnapshot.sdkMessages) > 0 {
			persistedMessages = s.persistPartialResult(ctx, req, rc, lastSnapshot.sdkMessages, toolCallCount, interruptedByTimeout, hasVisibleOutput)
			stored = len(persistedMessages) > 0
		} else if interruptedByTimeout && !hasVisibleOutput {
			persistedMessages = s.persistPartialResult(ctx, req, rc, nil, toolCallCount, true, false)
			stored = len(persistedMessages) > 0
		}
	} else if !stored {
		switch {
		case runOwnershipLost(ctx):
			s.logger.Warn("skip persisting ws stream after run ownership loss",
				slog.String("bot_id", req.BotID),
				slog.String("chat_id", req.ChatID),
			)
		case hasSnapshot:
			persistedMessages = s.persistPartialResult(ctx, req, rc, lastSnapshot.sdkMessages, toolCallCount, interruptedByTimeout, hasVisibleOutput)
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

	// Retry/edit replacement is a post-persistence operation. Run it only after
	// terminal fallback persistence has produced the final replacement slice;
	// otherwise an empty pre-fallback slice can delete/fail the old turn before
	// an interruption marker or recovered partial output is durable.
	if postPersist != nil && len(persistedMessages) > 0 {
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
		// no trace and the user only saw silence. Persist a stable marker instead.
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
// was interrupted (provider error, user abort, timeout) and partial SDK messages
// are available, those are persisted via the normal pipeline so orphaned
// tool_calls get repaired with synthetic error tool_results.
func (s *Service) persistPartialResult(
	ctx context.Context,
	req ChatRequest,
	rc resolvedContext,
	partialMessages []sdk.Message,
	toolCallCount int,
	wasTimeout bool,
	hasVisibleOutput bool,
) []messagepkg.Message {
	persistCtx := context.WithoutCancel(ctx)

	if len(partialMessages) > 0 {
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
				slog.Bool("timeout", wasTimeout),
			)
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
	if wasTimeout && !hasVisibleOutput {
		persisted, err := s.persistTerminalSnapshotResult(persistCtx, req, rc, terminalSnapshot{
			aborted: true,
		})
		if err == nil {
			s.logger.Info("persisted interrupted marker after timeout",
				slog.String("bot_id", req.BotID),
				slog.Int("tool_calls", toolCallCount),
			)
			return persisted
		}
		s.logger.Error("failed to persist interrupted marker after timeout",
			slog.String("bot_id", req.BotID),
			slog.Any("error", err),
		)
	}

	s.logger.Info("skip persisting failed stream without terminal snapshot",
		slog.String("bot_id", req.BotID),
		slog.Int("tool_calls", toolCallCount),
		slog.Bool("timeout", wasTimeout),
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
