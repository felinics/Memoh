package application

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
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
	"github.com/felinics/memoh/internal/messageconv"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/runtimekind"
)

// turnRuntimeHooks are test seams for the transport-facing turn lifecycle.
// Production leaves them nil and calls the Service's own orchestration methods
// and native Agent directly.
type turnRuntimeHooks struct {
	streamChat       func(context.Context, ChatRequest) (<-chan StreamChunk, <-chan error)
	streamAgent      func(context.Context, native.RunConfig) <-chan native.StreamEvent
	resolveRunConfig func(context.Context, RunConfigRequest) (ResolveRunConfigResult, error)
	inlineImages     func(context.Context, string, []timeline.ImageAttachmentRef) [][]sdk.ImagePart
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
	h := newDiscussHandle(runCtx, cmd, cancel, admission.RunID, s.turnRunFinisher(runCtx, admission, string(cmd.Mode)))
	h.publishAgentEvent = s.turnAgentEventPublisher(admission.Handle)
	go s.pumpDiscuss(runCtx, cmd, h)
	return h, nil
}

func newDiscussHandle(ctx context.Context, cmd turn.StartTurnCommand, cancel context.CancelFunc, runID string, finishRun func(RunOutcome) sessionruntime.TerminalRun) *discussHandle {
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
	// Registered first so it runs last: the span covers the run's terminal
	// write and reports what the run finished with.
	runCtx := ctx
	ctx, endTurn := startSelfCanceledTurnSpan(ctx, ChatRequest{BotID: cmd.BotID, ThreadID: cmd.ThreadID})
	defer func() { endTurn(discussTurnSpanCause(runCtx, h)) }()
	consumerGone := false
	defer close(h.events)
	defer close(h.errs)
	defer func() {
		// A consumer that canceled the run does not read tail events.
		if !consumerGone {
			h.sendTerminal(h.teamID, h.sessionID, h.seq+1)
		}
	}()
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
			consumerGone = true
		}
		h.cancel()
	}()

	// A Discuss batch speaks for the group rather than one account, so no user
	// scopes its config; every step of the turn resolves the same way.
	resolved, err := s.resolveDiscussRunConfig(ctx, RunConfigRequest{
		BotID:             cmd.BotID,
		SessionID:         cmd.ThreadID,
		ChannelIdentityID: cmd.SourceChannelIdentityID,
		CurrentPlatform:   cmd.CurrentChannel,
		ReplyTarget:       cmd.ReplyTarget,
		ConversationType:  cmd.ConversationType,
		ChatToken:         cmd.SessionToken,
	})
	if err != nil {
		h.emitErr(err)
		return
	}
	resolvedPayload, _ := json.Marshal(turn.DiscussRunResolvedPayload{RuntimeType: resolved.RuntimeType})
	if !h.emit(turn.DiscussEventRunResolved, resolvedPayload) {
		return
	}

	external := runtimekind.IsExternal(resolved.RuntimeType)
	if external && !cmd.DiscussAddressed {
		if h.emit(turn.DiscussEventSkipped, nil) {
			h.contentLightTerminal = true
		}
		return
	}
	recompose, compactErr := s.maybeSyncCompactDiscuss(ctx, cmd, resolved, h.id)
	if recompose {
		if h.emit(turn.DiscussEventRecompose, nil) {
			h.contentLightTerminal = true
		}
		return
	}
	if cmd.DiscussRecoveryExhausted && len(cmd.DiscussOmittedSources) > 0 {
		compactErr = errors.New("channel omitted input after compaction")
	}
	if compactErr != nil || cmd.DiscussContextOverflow {
		cause := apperror.New(apperror.CodeContextProtectedOverflow, nil)
		if compactErr != nil {
			cause = apperror.Wrap(budgetFailureCode(compactErr), compactErr, nil)
		}
		cfg := resolved.RunConfig
		cfg.RunID = h.id
		cfg.Identity.BotID, cfg.Identity.SessionID = cmd.BotID, cmd.ThreadID
		cfg.ContextLifecycle = contextfrag.NewLifecycleHolder()
		cfg.ContextLifecycle.SetManifest(contextfrag.BuildManifest(nil))
		s.contextLifecycleTerminal(ctx, cfg)(cause)
		h.emitErr(cause)
		return
	}
	if external {
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
	// What the history checkpoint holds is compacted by the run's budget
	// check like any history; only input that cannot fit on its own stops the
	// turn here.
	unpinned := make([]turn.DiscussMessage, 0, len(cmd.DiscussMessages))
	for _, message := range cmd.DiscussMessages {
		if !message.Pinned {
			unpinned = append(unpinned, message)
		}
	}
	_, admission := admitDiscussMessages(unpinned, budgetTokens)
	if admission.ProtectedOverflow {
		s.logger.ErrorContext(ctx, "context_admission_rejected",
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
	if runConfig.ContextMutations == nil {
		runConfig.ContextMutations = contextfrag.NewMutationLedger()
	}
	recordOmittedCurrentInput(runConfig.ContextMutations, cmd.DiscussOmittedSources)
	// The whole batch runs; what does not fit is compacted by the run's
	// budget check, never dropped here.
	admitted := cmd.DiscussMessages
	runConfig.Messages = discussMessagesToSDK(admitted)
	runConfig.SessionType = sessionpkg.TypeDiscuss
	runConfig.Query = ""
	runConfig.ContextCurrentUserMessageIndex = nil
	runConfig.ContextMemoryMessageIndex = nil
	if runConfig.ContextLifecycle == nil {
		runConfig.ContextLifecycle = contextfrag.NewLifecycleHolder()
	}
	runConfig.ContextBudgetMaxTokens = resolved.ContextBudgetMaxTokens

	// Inline image attachments from new RC segments so the model receives
	// them as native vision input (ImagePart) on the first encounter.
	var currentImages []sdk.ImagePart
	var sourceImages map[string][]sdk.ImagePart
	if runConfig.SupportsImageInput && len(cmd.DiscussImageRefs) > 0 {
		var withheld []string
		currentImages, sourceImages, withheld = s.discussImageParts(ctx, cmd, admitted)
		if len(withheld) > 0 {
			runConfig.ContextMutations.Record(contextfrag.MutationCurrentInputImagesOmitted, "sources="+strings.Join(withheld, ","))
		}
	}
	runConfig.ContextSourceFrags = s.collectDiscussSourceFrags(ctx, runConfig, admitted, currentImages, sourceImages)
	for _, frag := range runConfig.ContextSourceFrags {
		if frag.Provenance.Collector == discussContextCollector &&
			(frag.Kind == contextfrag.KindCurrentUserMessage || len(sourceImages[frag.Provenance.SourceID]) > 0) {
			if message := contextfrag.FragMessage(frag); message != nil {
				runConfig.Messages[frag.Provenance.Index] = *message
			}
		}
	}
	runConfig.ContextProvenance = discussContextProvenance(admitted, runConfig.Messages)
	runConfig = runConfig.RefreshContextFrag()
	terminal := s.contextLifecycleTerminal(ctx, runConfig)
	outcome := newOutcomeRecorder(ctx)
	// A failure without a catalogued code is named on the terminal event, so
	// the run's record and its failure row name the same failure.
	outcome.defaultCode = apperror.CodeAgentResponseInterrupted
	defer outcome.finishLifecycle(terminal)

	reasoningTiming := newReasoningTimingTracker(nil)
	rc := resolvedContext{runConfig: runConfig, model: models.GetResponse{ID: resolved.ModelID}}
	committer := s.bindNativeRun(ctx, discussRunRequest(cmd, h, admitted), rc, &runConfig, reasoningTiming)
	idleCtx, idleCancel := s.withStreamIdleTimeout(ctx, reasoningEffortForIdle(runConfig))
	defer idleCancel.Stop()
	outcome.watchIdle(idleCtx, idleCancel)
	// Budget recovery compacts at most once per run; the last attempt a
	// recompose budget allows only sheds images.
	compacted := cmd.DiscussRecoveryExhausted
	runConfig.RecoverContextBudget = func(ctx context.Context, cfg native.RunConfig) (native.RunConfig, bool, error) {
		return s.recoverDiscussContextBudget(ctx, cmd, admitted, resolved.ModelID, cfg, &compacted)
	}
	runConfig = pauseIdleDuringBudgetRecovery(runConfig, idleCancel)
	streamCtx, cancelStream := context.WithCancelCause(idleCtx)
	defer cancelStream(nil)
	// Stopping the stream decides how the run ends; the watchdog has nothing
	// left to time while the engine winds down.
	stopStream := func(cause error) {
		idleCancel.Stop()
		cancelStream(cause)
	}
	eventCh := s.streamDiscussAgent(streamCtx, runConfig)

	var finalMessages json.RawMessage
	var finalReasoningTiming []messagepkg.ReasoningTimingSegment
	var terminalEvent native.StreamEvent
	var terminalPayload []byte
	var uncommittedText uncommittedStepText
events:
	for event := range eventCh {
		if event.Type == native.EventContextRecompose {
			if h.emit(turn.DiscussEventRecompose, nil) {
				h.contentLightTerminal = true
				outcome.handOff()
			} else {
				outcome.recordCause(context.Cause(ctx))
			}
			return
		}
		idleCancel.Observe(event)
		_ = outcome.observe(event)
		uncommittedText.observe(event)
		if event.IsTerminal() {
			finalMessages = event.Messages
			finalReasoningTiming = takeTerminalReasoningTiming(reasoningTiming, event.Type)
			terminalEvent = outcome.stampTerminal(event)
			terminalPayload, _ = json.Marshal(terminalEvent)
			continue
		}
		if h.publishAgentEvent != nil {
			if publishErr := h.publishAgentEvent(ctx, publicAgentStreamEvent(event)); publishErr != nil {
				outcome.setCause(publishErr)
				stopStream(publishErr)
				break events
			}
		}
		payload, marshalErr := json.Marshal(publicAgentStreamEvent(event))
		if marshalErr != nil {
			continue
		}
		if !h.emit(string(event.Type), payload) {
			outcome.recordCause(context.Cause(ctx))
			stopStream(context.Cause(ctx))
			break events
		}
	}
	// The engine may still be committing a step; history is written once it
	// has stopped.
	for event := range eventCh {
		uncommittedText.observe(event)
	}
	hasTerminalEvent := outcome.terminalSeen
	timedOut := idleCancel.DidFire()
	if timedOut {
		outcome.setCause(context.Cause(idleCtx))
	}
	if !hasTerminalEvent && !timedOut {
		if ctx.Err() != nil {
			outcome.recordCause(context.Cause(ctx))
		} else {
			outcome.recordCause(errors.New("native discuss stream ended without a terminal event"))
		}
	}

	// The native stream deliberately drains to a terminal event after external
	// cancellation. Preserve the admitted run's values and fencing token while
	// detaching cancellation so that terminal history and runtime publication
	// can cross the same durable boundary as the main chat stream.
	terminalCtx := context.WithoutCancel(ctx)
	// Steps are already history. A failure is written after them before the
	// terminal event proposes the run's end, after which no row can be written.
	if committer != nil && !runOwnershipLost(ctx) {
		s.recordStepRunFailure(terminalCtx, committer, uncommittedText.String(), outcome.failureCode())
		if storeErr := committer.finish(terminalCtx); storeErr != nil {
			historyErr := runtimeHistoryError(storeErr)
			outcome.setCause(historyErr)
			h.emitErr(historyErr)
			return
		}
	}
	if !hasTerminalEvent && !timedOut {
		if ctx.Err() == nil {
			h.emitErr(outcome.cause)
		}
		return
	}
	if committer == nil {
		var failureCode apperror.Code
		if timedOut {
			failureCode = snapshotFailureCode(true, outcome.cause)
		}
		if storeErr := s.persistDiscussTerminalSnapshot(terminalCtx, runConfig, cmd, resolved.ModelID, finalMessages, finalReasoningTiming, failureCode); storeErr != nil {
			historyErr := runtimeHistoryError(storeErr)
			outcome.setCause(historyErr)
			h.emitErr(historyErr)
			return
		}
	}

	if timedOut {
		failureEvent := agentFailureStreamEvent(outcome.cause)
		if h.publishAgentEvent != nil {
			if publishErr := h.publishAgentEvent(terminalCtx, failureEvent); publishErr != nil {
				h.emitErr(publishErr)
				return
			}
		}
		if payload, marshalErr := json.Marshal(failureEvent); marshalErr == nil {
			h.emit(string(failureEvent.Type), payload)
		}
		h.emitErr(outcome.cause)
		return
	}
	if hasTerminalEvent && h.publishAgentEvent != nil {
		if publishErr := h.publishAgentEvent(terminalCtx, terminalEvent); publishErr != nil {
			outcome.setCause(publishErr)
			h.emitErr(publishErr)
			return
		}
		h.terminalPublished = true
	}
	if len(terminalPayload) > 0 && !h.emit(string(terminalEvent.Type), terminalPayload) {
		return
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

// discussRunRequest is the request a Discuss run writes its steps under. Its
// input is the rows ingress already wrote, so its steps join the history as
// replies without a request row of their own; the inputs of the batch it
// answers are what a fold keeps in place.
func discussRunRequest(cmd turn.StartTurnCommand, h *discussHandle, admitted []turn.DiscussMessage) ChatRequest {
	req := ChatRequest{
		RunID:                   h.id,
		BotID:                   cmd.BotID,
		ChatID:                  cmd.BotID,
		ThreadID:                cmd.ThreadID,
		SourceChannelIdentityID: cmd.SourceChannelIdentityID,
		CurrentChannel:          cmd.CurrentChannel,
		SessionType:             sessionpkg.TypeDiscuss,
		UserMessagePersisted:    true,
	}
	for _, message := range admitted {
		if source := message.Source; source != nil && source.Current {
			req.discussCurrentSources = append(req.discussCurrentSources, turn.ContextMessageSource{Kind: source.Kind, ID: source.ID})
		}
	}
	return req
}

// discussContextProvenance names what each composed message renders: the
// inbound message or stored reply Channel read it from, at the version it
// read, or the summary it renders. The inputs of the batch are anchors.
func discussContextProvenance(admitted []turn.DiscussMessage, messages []sdk.Message) []native.MessageProvenance {
	var out []native.MessageProvenance
	for i, message := range admitted {
		if i >= len(messages) {
			break
		}
		var source turn.ContextMessageSource
		switch {
		case message.CompactionArtifactID != "":
			source = turn.ContextMessageSource{Kind: "summary", ID: message.CompactionArtifactID}
		case message.Source != nil && message.Source.ID != "":
			source = *message.Source
		default:
			continue
		}
		anchor := source.Current
		source.Current = false
		out = append(out, native.MessageProvenance{Message: messages[i], Sources: []turn.ContextMessageSource{source}, Anchor: anchor})
	}
	return out
}

// discussContextCollector names the collector of composed discuss messages.
const discussContextCollector = "discuss_context"

// discussImageParts inlines the batch's new images, each on the admitted
// message it arrived with; a ref naming no message rides on the current input.
// A message admission left out takes its images with it, so older input of
// the batch carries its own images as history instead of growing the
// protected current input. The vision budget is spent newest first; the
// messages whose images are not delivered are reported.
func (s *Service) discussImageParts(ctx context.Context, cmd turn.StartTurnCommand, admitted []turn.DiscussMessage) ([]sdk.ImagePart, map[string][]sdk.ImagePart, []string) {
	admittedIDs := make(map[string]bool, len(admitted))
	currentID := ""
	for _, message := range admitted {
		if message.Source != nil && message.Source.Kind == "external" {
			admittedIDs[message.Source.ID] = true
			if message.Source.Current {
				currentID = message.Source.ID
			}
		}
	}
	var refs []timeline.ImageAttachmentRef
	var owners, withheld []string
	for _, ref := range cmd.DiscussImageRefs {
		switch {
		case ref.MessageID == "" || admittedIDs[ref.MessageID]:
			refs = append(refs, timeline.ImageAttachmentRef{ContentHash: ref.ContentHash, Mime: ref.Mime})
			owners = append(owners, ref.MessageID)
		case !slices.Contains(withheld, ref.MessageID):
			// Admission left the message out or compaction summarized it; no
			// summary carries an image.
			withheld = append(withheld, ref.MessageID)
		}
	}
	if len(refs) == 0 {
		return nil, nil, withheld
	}
	slices.Reverse(refs)
	inlined := s.inlineDiscussImages(ctx, cmd.BotID, refs)
	var current []sdk.ImagePart
	bySource := make(map[string][]sdk.ImagePart)
	delivered := make(map[string]bool)
	for i := len(inlined) - 1; i >= 0; i-- {
		if len(inlined[i]) > 0 {
			delivered[refs[i].ContentHash] = true
		}
		if owner := owners[len(owners)-1-i]; owner == "" {
			current = append(current, inlined[i]...)
		} else {
			bySource[owner] = append(bySource[owner], inlined[i]...)
		}
	}
	for i, owner := range owners {
		if owner == "" {
			owner = currentID
		}
		if hash := refs[len(refs)-1-i].ContentHash; owner != "" && !delivered[hash] && !slices.Contains(withheld, owner) {
			withheld = append(withheld, owner)
		}
	}
	return current, bySource, withheld
}

func (s *Service) collectDiscussSourceFrags(
	ctx context.Context,
	runConfig native.RunConfig,
	messages []turn.DiscussMessage,
	inlineImages []sdk.ImagePart,
	sourceImages map[string][]sdk.ImagePart,
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
			SourceImages:     sourceImages,
			SystemFrags:      systemFrags,
		},
	)
	if err != nil {
		if s.logger != nil {
			s.logger.WarnContext(ctx, "collect typed discuss context failed", slog.Any("error", err))
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

func (s *Service) pumpDiscussAgent(ctx context.Context, cmd turn.StartTurnCommand, h *discussHandle) {
	// ACP resolution carries no model window, so the prompt is budgeted by
	// the absolute cap before any concatenation (CM-ADM-001).
	admitted, admission := admitDiscussAgentMessages(cmd.DiscussMessages, s.contextAbsoluteMaxTokens())
	if admission.ProtectedOverflow {
		s.logger.ErrorContext(ctx, "context_admission_rejected",
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
		s.logger.InfoContext(ctx, "context_admission",
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
		BotID:                    cmd.BotID,
		ChatID:                   cmd.BotID,
		ThreadID:                 cmd.ThreadID,
		RunID:                    h.id,
		RouteID:                  cmd.RouteID,
		SourceChannelIdentityID:  cmd.SourceChannelIdentityID,
		CurrentChannel:           cmd.CurrentChannel,
		ReplyTarget:              cmd.ReplyTarget,
		ConversationType:         cmd.ConversationType,
		Token:                    cmd.SessionToken,
		ChatToken:                cmd.ChatToken,
		ToolHTTPURL:              cmd.ToolHTTPURL,
		Query:                    prompt,
		RawQuery:                 prompt,
		UserMessagePersisted:     true,
		SkipMemoryExtraction:     true,
		ForceFreshRuntime:        true,
		discussMessages:          cmd.DiscussMessages,
		discussCurrentSources:    cmd.DiscussCurrentSources,
		discussOmittedSources:    cmd.DiscussOmittedSources,
		discussContextTokens:     discussContextPressure(cmd),
		discussRecoveryExhausted: cmd.DiscussRecoveryExhausted,
	})
	for chunks != nil || errs != nil {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				chunks = nil
				continue
			}
			if parseKind(chunk) == string(native.EventContextRecompose) {
				if h.emit(turn.DiscussEventRecompose, nil) {
					h.contentLightTerminal = true
				}
				return
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

func (s *Service) resolveDiscussRunConfig(ctx context.Context, req RunConfigRequest) (ResolveRunConfigResult, error) {
	if s.turnHooks != nil && s.turnHooks.resolveRunConfig != nil {
		return s.turnHooks.resolveRunConfig(ctx, req)
	}
	resolved, err := s.ResolveRunConfig(ctx, req)
	if err == nil {
		resolved.RunConfig = s.prepareRunConfig(ctx, resolved.RunConfig)
	}
	return resolved, err
}

func (s *Service) inlineDiscussImages(ctx context.Context, botID string, refs []timeline.ImageAttachmentRef) [][]sdk.ImagePart {
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
			// RawContent is the stored content shape (arguments object, output
			// value, nested annotations); the codec types it for the SDK.
			msg := messageconv.ModelMessageToSDKMessage(turn.ModelMessage{Role: m.Role, Content: m.RawContent})
			if msg.Role != "" && len(msg.Content) > 0 {
				result = append(result, msg)
				continue
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
			Source:               message.Source,
			Content:              message.Content,
			RawContent:           message.RawContent,
			CompactionArtifactID: message.CompactionArtifactID,
			Pinned:               message.Pinned,
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
