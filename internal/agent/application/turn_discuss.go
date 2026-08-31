package application

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/apperror"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	sessionpkg "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/chat/timeline"
	"github.com/felinics/memoh/internal/contextview"
	"github.com/felinics/memoh/internal/models"
)

// turnRuntimeHooks are test seams for the transport-facing turn lifecycle.
// Production leaves them nil and calls the Service's own orchestration methods
// and native Agent directly.
type turnRuntimeHooks struct {
	streamChat       func(context.Context, ChatRequest) (<-chan StreamChunk, <-chan error)
	streamAgent      func(context.Context, native.RunConfig) <-chan native.StreamEvent
	resolveRunConfig func(context.Context, string, string, string, string, string, string, string) (ResolveRunConfigResult, error)
	inlineImages     func(context.Context, string, []timeline.ImageAttachmentRef) []sdk.ImagePart
	storeRound       func(context.Context, string, string, string, string, string, []sdk.Message, string, *contextfrag.LifecycleHolder) error
}

// startDiscussTurn orchestrates one discuss turn: resolve the run config,
// emit a synthetic run-resolved event, then either stream the native agent
// (persisting the round) or the external ACP runtime. The participation
// gate for ACP runtimes lives here because it is a property of runtime
// cost, not of channel policy: the caller supplies DiscussAddressed and
// the runtime decides whether starting is worth it.
// The run context and its admission are established by StartTurn, so a discuss
// turn occupies the thread's single slot on the same terms as a chat turn.
func (s *Service) startDiscussTurn(runCtx context.Context, cmd turn.StartTurnCommand, cancel context.CancelFunc, admission sessionruntime.Admission) (turn.RunHandle, error) {
	if !s.discussRuntimeConfigured() {
		return nil, errors.New("turn: discuss runtime not configured")
	}
	h := newDiscussHandle(runCtx, cmd, cancel, admission.RunID, s.turnRunFinisher(runCtx, admission))
	h.publishAgentEvent = s.turnAgentEventPublisher(admission.Handle)
	go s.pumpDiscuss(runCtx, cmd, h)
	return h, nil
}

func newDiscussHandle(ctx context.Context, cmd turn.StartTurnCommand, cancel context.CancelFunc, runID string, finishRun func(status string, cause error)) *discussHandle {
	return &discussHandle{
		runHandle: runHandle{
			id:        runID,
			events:    make(chan turn.Event, 16),
			errs:      make(chan error, 1),
			ctx:       ctx,
			cancel:    cancel,
			inject:    make(chan turn.InjectMessage), // unused in discuss mode
			addAssets: func([]turn.OutboundAssetRef) {},
			finishRun: finishRun,
		},
		teamID:    cmd.TeamID,
		sessionID: cmd.ThreadID,
	}
}

// Inject is not supported in discuss mode: no reader consumes the inject
// channel, so blocking until the run ends would just wedge the caller.
// Shadowing runHandle.Inject fails fast instead.
func (*discussHandle) Inject(context.Context, turn.InjectMessage) error {
	return errors.New("turn: discuss turns do not accept injected messages")
}

// discussHandle reuses runHandle's channel pair with manual event emission.
type discussHandle struct {
	runHandle
	teamID               string
	sessionID            string
	seq                  int64
	contentLightTerminal bool
}

// emit delivers one event, giving up when the run context is canceled so
// a stalled consumer can never wedge the pump (Cancel must always unblock).
func (h *discussHandle) emit(kind string, payload []byte) bool {
	h.seq++
	select {
	case h.events <- turn.Event{
		RunID:    h.id,
		TeamID:   h.teamID,
		ThreadID: h.sessionID,
		Seq:      h.seq,
		Kind:     kind,
		Payload:  payload,
	}:
		return true
	case <-h.ctx.Done():
		h.failed.Store(true)
		return false
	}
}

// emitErr mirrors emit for the error channel. Any reported error marks the run
// failed; recordStreamFailure preserves explicit cancellation as an abort.
func (h *discussHandle) emitErr(err error) bool {
	if !h.recordStreamFailure(err) {
		return false
	}
	select {
	case h.errs <- err:
		return true
	case <-h.ctx.Done():
		return false
	}
}

func (s *Service) pumpDiscuss(ctx context.Context, cmd turn.StartTurnCommand, h *discussHandle) {
	defer close(h.events)
	defer close(h.errs)
	defer func() {
		if h.contentLightTerminal && !h.failed.Load() && h.streamErr == nil && !s.usesDurableTerminalObserver() {
			s.EnsureTerminalContextLifecycle(ctx, h.id, cmd.BotID, cmd.ThreadID, nil)
		}
	}()
	defer h.finish()
	defer func() {
		// External cancellation can surface as a cleanly closed agent
		// stream; record it before cancel() masks the distinction.
		if h.ctx.Err() != nil {
			h.failed.Store(true)
		}
		h.cancel()
	}()

	resolved, err := s.resolveDiscussRunConfig(ctx,
		cmd.BotID, cmd.ThreadID, cmd.SourceChannelIdentityID,
		cmd.CurrentChannel, cmd.ReplyTarget, cmd.ConversationType, cmd.SessionToken)
	if err != nil {
		h.emitErr(err)
		return
	}
	resolvedPayload, _ := json.Marshal(turn.DiscussRunResolvedPayload{RuntimeType: resolved.RuntimeType})
	if !h.emit(turn.DiscussEventRunResolved, resolvedPayload) {
		return
	}

	if runtimeType := strings.TrimSpace(resolved.RuntimeType); runtimeType == sessionpkg.RuntimeACPAgent ||
		sessionpkg.IsDirectRuntimeType(runtimeType) {
		if !cmd.DiscussAddressed {
			if h.emit(turn.DiscussEventSkipped, nil) {
				h.contentLightTerminal = true
			}
			return
		}
		s.pumpDiscussAgent(ctx, cmd, h)
		return
	}
	s.pumpDiscussNative(ctx, cmd, h, resolved)
}

func (s *Service) pumpDiscussNative(ctx context.Context, cmd turn.StartTurnCommand, h *discussHandle, resolved ResolveRunConfigResult) {
	runConfig := resolved.RunConfig
	runConfig.RunID = h.id
	budgetTokens := resolved.ContextBudgetMaxTokens
	if budgetTokens <= 0 {
		budgetTokens = s.contextAbsoluteMaxTokens()
	}
	admitted, admission := admitDiscussMessages(cmd.DiscussMessages, budgetTokens)
	if admission.ProtectedOverflow {
		s.logger.Error("context_admission_rejected",
			slog.String("path", "discuss_turn"),
			slog.String("bot_id", cmd.BotID),
			slog.String("session_id", cmd.ThreadID),
			slog.Int("estimated_tokens", admission.EstimatedTokens),
			slog.Int("budget_tokens", admission.BudgetTokens))
		cause := apperror.New(apperror.CodeContextProtectedOverflow, nil)
		if runConfig.ContextLifecycle == nil {
			runConfig.ContextLifecycle = contextfrag.NewLifecycleHolder()
		}
		runConfig.ContextLifecycle.SetManifest(contextfrag.BuildManifest(nil))
		s.contextLifecycleTerminal(ctx, runConfig)(cause)
		h.emitErr(cause)
		return
	}
	if admission.DroppedMessages > 0 {
		s.logger.Info("context_admission",
			slog.String("path", "discuss_turn"),
			slog.String("bot_id", cmd.BotID),
			slog.String("session_id", cmd.ThreadID),
			slog.Int("estimated_tokens", admission.EstimatedTokens),
			slog.Int("selected_tokens", admission.SelectedTokens),
			slog.Int("budget_tokens", admission.BudgetTokens),
			slog.Int("dropped_messages", admission.DroppedMessages))
	}
	runConfig.Messages = discussMessagesToSDK(admitted)
	runConfig.SessionType = sessionpkg.TypeDiscuss
	runConfig.Query = ""
	runConfig.ContextCurrentUserMessageIndex = nil
	runConfig.ContextMemoryMessageIndex = nil
	if runConfig.ContextLifecycle == nil {
		runConfig.ContextLifecycle = contextfrag.NewLifecycleHolder()
	}
	runConfig.ContextBudgetMaxTokens = resolved.ContextBudgetMaxTokens
	if runConfig.ContextToolExchangePolicy == nil {
		runConfig.ContextToolExchangePolicy = defaultToolExchangePolicy()
	}

	// Inline image attachments from new RC segments so the model receives
	// them as native vision input (ImagePart) on the first encounter.
	var imageParts []sdk.ImagePart
	if runConfig.SupportsImageInput && len(cmd.DiscussImageRefs) > 0 {
		refs := make([]timeline.ImageAttachmentRef, len(cmd.DiscussImageRefs))
		for i, r := range cmd.DiscussImageRefs {
			refs[i] = timeline.ImageAttachmentRef{ContentHash: r.ContentHash, Mime: r.Mime}
		}
		imageParts = s.inlineDiscussImages(ctx, cmd.BotID, refs)
		injectImagePartsIntoLastUserMessage(runConfig.Messages, imageParts)
	}
	runConfig.ContextSourceFrags = s.collectDiscussSourceFrags(ctx, runConfig, admitted, imageParts)
	runConfig = runConfig.RefreshContextFrag()
	terminal := s.contextLifecycleTerminal(ctx, runConfig)
	var lifecycleCause error
	var lifecycleDeferred bool
	defer func() {
		if !lifecycleDeferred {
			terminal(lifecycleCause)
		}
	}()

	reasoningTiming := newReasoningTimingTracker(nil)
	configureNativeReasoningTiming(&runConfig, reasoningTiming, nil)
	idleCtx, idleCancel := s.withStreamIdleTimeout(ctx, reasoningEffortForIdle(runConfig))
	defer idleCancel.Stop()
	eventCh := s.streamDiscussAgent(idleCtx, runConfig)

	var finalMessages json.RawMessage
	var finalReasoningTiming []messagepkg.ReasoningTimingSegment
	var terminalEvent native.StreamEvent
	var terminalPayload []byte
	var hasTerminalEvent bool
	for event := range eventCh {
		idleCancel.Reset()
		if event.Type == native.EventToolCallStart {
			idleCancel.RecordToolCall()
		}
		if eventErr := agentStreamLifecycleError(event); eventErr != nil && lifecycleCause == nil {
			lifecycleCause = eventErr
		}
		terminal := event.Type == native.EventAgentEnd || event.Type == native.EventAgentAbort
		if terminal {
			finalMessages = event.Messages
			finalReasoningTiming = takeTerminalReasoningTiming(reasoningTiming, event.Type)
			terminalEvent = event
			terminalPayload, _ = json.Marshal(event)
			hasTerminalEvent = true
			lifecycleDeferred = strings.TrimSpace(event.ApprovalID) != ""
			if !lifecycleDeferred {
				switch event.Type {
				case native.EventAgentEnd:
					lifecycleCause = nil
				case native.EventAgentAbort:
					if idleCancel.DidFire() {
						lifecycleCause = context.Cause(idleCtx)
					} else if context.Cause(ctx) != nil || lifecycleCause == nil {
						lifecycleCause = agentAbortCause(ctx)
					}
				}
			}
			continue
		}
		if h.publishAgentEvent != nil {
			if publishErr := h.publishAgentEvent(ctx, publicAgentStreamEvent(event)); publishErr != nil {
				lifecycleCause = publishErr
				lifecycleDeferred = false
				h.emitErr(publishErr)
				return
			}
		}
		payload, marshalErr := json.Marshal(publicAgentStreamEvent(event))
		if marshalErr != nil {
			continue
		}
		if !h.emit(string(event.Type), payload) {
			if lifecycleCause == nil {
				lifecycleCause = context.Cause(ctx)
			}
			return
		}
	}
	timedOut := idleCancel.DidFire()
	if timedOut {
		lifecycleCause = context.Cause(idleCtx)
		lifecycleDeferred = false
	}
	if !hasTerminalEvent && !timedOut {
		if lifecycleCause == nil {
			if ctx.Err() != nil {
				lifecycleCause = context.Cause(ctx)
			} else {
				lifecycleCause = errors.New("native discuss stream ended without a terminal event")
			}
		}
		if ctx.Err() == nil {
			h.emitErr(lifecycleCause)
		}
		return
	}

	// The native stream deliberately drains to a terminal event after external
	// cancellation. Preserve the admitted run's values and fencing token while
	// detaching cancellation so that terminal history and runtime publication
	// can cross the same durable boundary as the main chat stream.
	terminalCtx := context.WithoutCancel(ctx)
	if hasTerminalEvent || timedOut {
		var failureCode apperror.Code
		if timedOut {
			failureCode = snapshotFailureCode(true, lifecycleCause)
		}
		if storeErr := s.persistDiscussTerminalSnapshot(terminalCtx, runConfig, cmd, resolved.ModelID, finalMessages, finalReasoningTiming, failureCode); storeErr != nil {
			historyErr := runtimeHistoryError(storeErr)
			lifecycleCause = historyErr
			lifecycleDeferred = false
			h.emitErr(historyErr)
			return
		}
	}

	if timedOut {
		failureEvent := agentFailureStreamEvent(lifecycleCause)
		if h.publishAgentEvent != nil {
			if publishErr := h.publishAgentEvent(terminalCtx, failureEvent); publishErr != nil {
				h.emitErr(publishErr)
				return
			}
		}
		if payload, marshalErr := json.Marshal(failureEvent); marshalErr == nil {
			h.emit(string(failureEvent.Type), payload)
		}
		h.emitErr(lifecycleCause)
		return
	}
	if hasTerminalEvent && h.publishAgentEvent != nil {
		if publishErr := h.publishAgentEvent(terminalCtx, terminalEvent); publishErr != nil {
			lifecycleCause = publishErr
			lifecycleDeferred = false
			h.emitErr(publishErr)
			return
		}
		h.terminalPublished = true
	}
	if len(terminalPayload) > 0 && !h.emit(string(terminalEvent.Type), terminalPayload) {
		return
	}

	// Compute pressure on this goroutine so the detached trigger holds a few
	// scalars instead of pinning the whole composed context until it runs.
	if compactable := discussCompactableTokens(cmd.DiscussMessages); compactable > 0 && s.compactionService != nil && s.settingsService != nil {
		// Pressure is measured on the full composed context, not the admitted
		// window: what admission dropped is exactly what compaction must cover.
		go s.maybeCompactDiscuss(context.WithoutCancel(ctx), cmd.BotID, cmd.ThreadID, resolved.ModelID, compactable)
	}
}

func (s *Service) persistDiscussTerminalSnapshot(
	ctx context.Context,
	runConfig native.RunConfig,
	cmd turn.StartTurnCommand,
	modelID string,
	finalMessages json.RawMessage,
	reasoningTiming []messagepkg.ReasoningTimingSegment,
	failureCode apperror.Code,
) error {
	var sdkMsgs []sdk.Message
	if len(finalMessages) > 0 && json.Unmarshal(finalMessages, &sdkMsgs) == nil && len(sdkMsgs) > 0 {
		return s.storeDiscussRound(
			ctx,
			runConfig.RunID,
			cmd.BotID,
			cmd.ThreadID,
			cmd.SourceChannelIdentityID,
			cmd.CurrentChannel,
			sdkMsgs,
			modelID,
			runConfig.ContextLifecycle,
			reasoningTiming,
		)
	}
	if failureCode == "" {
		return nil
	}
	if s.turnHooks != nil && s.turnHooks.storeRound != nil {
		return s.turnHooks.storeRound(
			ctx,
			runConfig.RunID,
			cmd.BotID,
			cmd.ThreadID,
			cmd.SourceChannelIdentityID,
			cmd.CurrentChannel,
			[]sdk.Message{sdk.AssistantMessage("")},
			modelID,
			runConfig.ContextLifecycle,
		)
	}
	return s.storeRoundWithOptions(ctx, ChatRequest{
		RunID:                   runConfig.RunID,
		BotID:                   cmd.BotID,
		ChatID:                  cmd.BotID,
		ThreadID:                cmd.ThreadID,
		SourceChannelIdentityID: cmd.SourceChannelIdentityID,
		CurrentChannel:          cmd.CurrentChannel,
		UserMessagePersisted:    true,
	}, []ModelMessage{{
		Role:    "assistant",
		Content: newTextContent(""),
	}}, modelID, storeRoundOptions{
		AllowEmptyAssistantText: true,
		MessageMetadataByIndex: map[int]map[string]any{
			0: {
				messagepkg.AgentStepInterruptedMetadataKey: true,
				messagepkg.HistoryErrorCodeMetadataKey:     string(failureCode),
			},
		},
		ContextLifecycle: runConfig.ContextLifecycle,
		ReasoningTiming:  reasoningTiming,
	})
}

func (s *Service) collectDiscussSourceFrags(
	ctx context.Context,
	runConfig native.RunConfig,
	messages []turn.DiscussMessage,
	inlineImages []sdk.ImagePart,
) []contextfrag.ContextFrag {
	var systemFrags []contextfrag.ContextFrag
	var memoryFrags []contextfrag.ContextFrag
	var hookFrags []contextfrag.ContextFrag
	for _, frag := range runConfig.ContextSourceFrags {
		switch {
		case frag.Slot == contextfrag.SlotSystem:
			systemFrags = append(systemFrags, frag)
		case frag.Kind == contextfrag.KindMemoryRecall:
			memoryFrags = append(memoryFrags, frag)
		case frag.Kind == contextfrag.KindHookContext:
			hookFrags = append(hookFrags, frag)
		}
	}
	frags, err := (&contextview.DiscussSDKContextBuilder{}).CollectDiscussSourceFrags(
		ctx,
		runConfig.ContextScope,
		runConfig.System,
		contextview.DiscussContextInput{
			ComposedMessages: discussMessagesToTimeline(messages),
			InlineImages:     inlineImages,
			SystemFrags:      systemFrags,
		},
	)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("collect typed discuss context failed", slog.Any("error", err))
		}
		return nil
	}
	dynamic := make([]contextfrag.ContextFrag, 0, len(memoryFrags)+len(hookFrags))
	dynamic = append(dynamic, memoryFrags...)
	dynamic = append(dynamic, hookFrags...)
	if len(dynamic) == 0 {
		return frags
	}
	currentIndex := len(frags)
	for i, frag := range frags {
		if frag.Kind == contextfrag.KindCurrentUserMessage {
			currentIndex = i
			break
		}
	}
	out := make([]contextfrag.ContextFrag, 0, len(frags)+len(dynamic))
	out = append(out, frags[:currentIndex]...)
	out = append(out, dynamic...)
	out = append(out, frags[currentIndex:]...)
	return out
}

// maybeCompactDiscuss re-evaluates compaction pressure after a native discuss
// turn with the same trigger policy as the chat path. ACP discuss turns run
// through streamTurnChat and inherit its trigger directly.
func (s *Service) maybeCompactDiscuss(ctx context.Context, botID, threadID, modelID string, compactable int) {
	// The absolute cap keeps compaction triggers alive even when the model
	// has no configured context window; a zero budget would disable them.
	budget := s.contextAbsoluteMaxTokens()
	var turnModel models.GetResponse
	if s.modelsService != nil && strings.TrimSpace(modelID) != "" {
		if model, err := s.modelsService.GetByID(ctx, modelID); err == nil {
			turnModel = model
			budget = s.effectiveContextTokenBudget(model)
		}
	}
	s.maybeCompact(ctx, ChatRequest{BotID: botID, ThreadID: threadID}, resolvedContext{
		model:                  turnModel,
		compactableTokens:      compactable,
		compactableTokensKnown: true,
		contextTokenBudget:     budget,
	}, compactable)
}

// discussCompactableTokens estimates the raw history share of a discuss
// context, excluding artifact summaries, in the shared estimator's unit.
func discussCompactableTokens(messages []turn.DiscussMessage) int {
	total := 0
	for _, message := range messages {
		if message.CompactionArtifactID != "" {
			continue
		}
		total += discussMessageTokens(message)
	}
	return total
}

func (s *Service) pumpDiscussAgent(ctx context.Context, cmd turn.StartTurnCommand, h *discussHandle) {
	// ACP resolution carries no model window, so the prompt is budgeted by
	// the absolute cap before any concatenation (CM-ADM-001).
	admitted, admission := admitDiscussMessages(cmd.DiscussMessages, s.contextAbsoluteMaxTokens())
	if admission.ProtectedOverflow {
		s.logger.Error("context_admission_rejected",
			slog.String("path", "discuss_agent"),
			slog.String("bot_id", cmd.BotID),
			slog.String("session_id", cmd.ThreadID),
			slog.Int("estimated_tokens", admission.EstimatedTokens),
			slog.Int("budget_tokens", admission.BudgetTokens))
		cause := apperror.New(apperror.CodeContextProtectedOverflow, nil)
		lifecycle := contextfrag.NewLifecycleHolder()
		lifecycle.SetManifest(contextfrag.BuildManifest(nil))
		s.contextLifecycleTerminal(ctx, native.RunConfig{
			RunID: h.id,
			Identity: native.SessionContext{
				BotID:     cmd.BotID,
				SessionID: cmd.ThreadID,
			},
			ContextLifecycle: lifecycle,
		})(cause)
		h.emitErr(cause)
		return
	}
	if admission.DroppedMessages > 0 {
		s.logger.Info("context_admission",
			slog.String("path", "discuss_agent"),
			slog.String("bot_id", cmd.BotID),
			slog.String("session_id", cmd.ThreadID),
			slog.Int("estimated_tokens", admission.EstimatedTokens),
			slog.Int("selected_tokens", admission.SelectedTokens),
			slog.Int("budget_tokens", admission.BudgetTokens),
			slog.Int("dropped_messages", admission.DroppedMessages))
	}
	prompt := discussAgentFullContextPrompt(admitted)
	if strings.TrimSpace(prompt) == "" {
		// No composable context: end without a skip marker so the caller
		// does not advance its consumed cursor (pre-port semantics).
		h.contentLightTerminal = true
		return
	}
	chunks, errs := s.streamTurnChat(ctx, ChatRequest{
		BotID:                   cmd.BotID,
		ChatID:                  cmd.BotID,
		ThreadID:                cmd.ThreadID,
		RunID:                   h.id,
		RouteID:                 cmd.RouteID,
		SourceChannelIdentityID: cmd.SourceChannelIdentityID,
		CurrentChannel:          cmd.CurrentChannel,
		ReplyTarget:             cmd.ReplyTarget,
		ConversationType:        cmd.ConversationType,
		Token:                   cmd.SessionToken,
		ChatToken:               cmd.ChatToken,
		ToolHTTPURL:             cmd.ToolHTTPURL,
		Query:                   prompt,
		RawQuery:                prompt,
		UserMessagePersisted:    true,
		SkipMemoryExtraction:    true,
		ForceFreshRuntime:       true,
	})
	for chunks != nil || errs != nil {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				chunks = nil
				continue
			}
			if err := h.publishChunk(chunk); err != nil {
				h.emitErr(err)
				return
			}
			if !h.emit(parseKind(chunk), chunk) {
				return
			}
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if err != nil {
				if !h.emitErr(err) {
					return
				}
			}
		case <-ctx.Done():
			h.failed.Store(true)
			return
		}
	}
}

func (s *Service) discussRuntimeConfigured() bool {
	if s == nil {
		return false
	}
	if s.turnHooks != nil && s.turnHooks.resolveRunConfig != nil {
		return s.turnHooks.streamAgent != nil
	}
	return s.agent != nil
}

func (s *Service) resolveDiscussRunConfig(
	ctx context.Context,
	botID, sessionID, channelIdentityID, currentPlatform, replyTarget, conversationType, chatToken string,
) (ResolveRunConfigResult, error) {
	if s.turnHooks != nil && s.turnHooks.resolveRunConfig != nil {
		return s.turnHooks.resolveRunConfig(
			ctx,
			botID,
			sessionID,
			channelIdentityID,
			currentPlatform,
			replyTarget,
			conversationType,
			chatToken,
		)
	}
	return s.ResolveRunConfig(
		ctx,
		botID,
		sessionID,
		channelIdentityID,
		currentPlatform,
		replyTarget,
		conversationType,
		chatToken,
	)
}

func (s *Service) inlineDiscussImages(ctx context.Context, botID string, refs []timeline.ImageAttachmentRef) []sdk.ImagePart {
	if s.turnHooks != nil && s.turnHooks.inlineImages != nil {
		return s.turnHooks.inlineImages(ctx, botID, refs)
	}
	return s.InlineImageAttachments(ctx, botID, refs)
}

func (s *Service) streamDiscussAgent(ctx context.Context, cfg native.RunConfig) <-chan native.StreamEvent {
	if s.turnHooks != nil && s.turnHooks.streamAgent != nil {
		return s.turnHooks.streamAgent(ctx, cfg)
	}
	return s.agent.Stream(ctx, cfg)
}

func (s *Service) storeDiscussRound(
	ctx context.Context,
	runID string,
	botID, sessionID, channelIdentityID, currentPlatform string,
	messages []sdk.Message,
	modelID string,
	lifecycle *contextfrag.LifecycleHolder,
	reasoningTiming []messagepkg.ReasoningTimingSegment,
) error {
	if s.turnHooks != nil && s.turnHooks.storeRound != nil {
		return s.turnHooks.storeRound(
			ctx,
			runID,
			botID,
			sessionID,
			channelIdentityID,
			currentPlatform,
			messages,
			modelID,
			lifecycle,
		)
	}
	return s.storeRoundWithOptions(ctx, ChatRequest{
		RunID:                   runID,
		BotID:                   botID,
		ChatID:                  botID,
		ThreadID:                sessionID,
		SourceChannelIdentityID: channelIdentityID,
		CurrentChannel:          currentPlatform,
		UserMessagePersisted:    true,
	}, sdkMessagesToModelMessages(messages), modelID, storeRoundOptions{
		ContextLifecycle: lifecycle,
		ReasoningTiming:  reasoningTiming,
	})
}

// discussMessagesToSDK converts composed context messages into SDK
// messages, preserving structured raw content when present.
func discussMessagesToSDK(messages []turn.DiscussMessage) []sdk.Message {
	result := make([]sdk.Message, 0, len(messages))
	for _, m := range messages {
		if len(m.RawContent) > 0 {
			raw, err := json.Marshal(struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			}{
				Role:    m.Role,
				Content: m.RawContent,
			})
			if err == nil {
				var msg sdk.Message
				if json.Unmarshal(raw, &msg) == nil {
					result = append(result, msg)
					continue
				}
			}
		}
		switch m.Role {
		case "assistant":
			result = append(result, sdk.AssistantMessage(m.Content))
		default:
			result = append(result, sdk.UserMessage(m.Content))
		}
	}
	return result
}

func discussMessagesToTimeline(messages []turn.DiscussMessage) []timeline.ContextMessage {
	result := make([]timeline.ContextMessage, len(messages))
	for i, message := range messages {
		result[i] = timeline.ContextMessage{
			Role:                 message.Role,
			Content:              message.Content,
			RawContent:           message.RawContent,
			CompactionArtifactID: message.CompactionArtifactID,
		}
	}
	return result
}

// injectImagePartsIntoLastUserMessage appends ImageParts to the last user
// message in msgs so the model receives inline vision input.
func injectImagePartsIntoLastUserMessage(msgs []sdk.Message, parts []sdk.ImagePart) {
	if len(parts) == 0 {
		return
	}
	extra := make([]sdk.MessagePart, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p.Image) != "" {
			extra = append(extra, p)
		}
	}
	if len(extra) == 0 {
		return
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == sdk.MessageRoleUser {
			msgs[i].Content = append(msgs[i].Content, extra...)
			return
		}
	}
}

// discussAgentFullContextPrompt renders the composed context into the single
// reset-each-turn prompt used by external ACP runtimes. ACP does not receive
// native ToolUsage, so its stable preamble owns the send-only output contract.
func discussAgentFullContextPrompt(messages []turn.DiscussMessage) string {
	var b strings.Builder
	b.WriteString("You are replying in a discuss-mode conversation. The runtime is reset each turn, so use the complete context below as the source of truth.\n\n")
	b.WriteString("IMPORTANT: You MUST use the `send` tool to speak in the observed conversation. Ordinary text output is internal and invisible to everyone.\n\n")
	for _, msg := range messages {
		role := strings.TrimSpace(msg.Role)
		if role == "" {
			role = "user"
		}
		content := strings.TrimSpace(msg.Content)
		if content == "" {
			continue
		}
		b.WriteString("[")
		b.WriteString(role)
		b.WriteString("]\n")
		b.WriteString(content)
		b.WriteString("\n\n")
	}
	b.WriteString("Reply to the latest user-visible message when a response is appropriate.")
	return b.String()
}
