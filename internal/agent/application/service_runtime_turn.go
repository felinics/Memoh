package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	agentfeedback "github.com/felinics/memoh/internal/agent/decision/feedback"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/sessionmode"
	"github.com/felinics/memoh/internal/apperror"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/schedule"
)

// SetExternalRuntimes registers out-of-process runtime drivers (codex,
// claude-code, the ACP pool's adapter). Sessions whose runtime type has no
// registered driver fail with a stable "runtime unavailable" error instead of
// silently degrading to the built-in model runtime.
func (s *Service) SetExternalRuntimes(drivers ...external.Driver) {
	if s.externalDrivers == nil {
		s.externalDrivers = map[string]external.Driver{}
	}
	for _, driver := range drivers {
		if driver == nil {
			continue
		}
		s.externalDrivers[strings.TrimSpace(driver.RuntimeType())] = driver
	}
}

type runtimeDispatchKind int

const (
	dispatchNative runtimeDispatchKind = iota
	dispatchExternal
)

// runtimeDispatch is the resolved execution route for one chat request.
type runtimeDispatch struct {
	kind   runtimeDispatchKind
	driver external.Driver
}

// resolveRuntimeDispatch decides which runtime executes a request's turn:
// the in-process model runtime, the ACP pool, or a direct external driver.
func (s *Service) resolveRuntimeDispatch(ctx context.Context, req ChatRequest) (runtimeDispatch, error) {
	if s == nil || s.sessionService == nil || strings.TrimSpace(req.ThreadID) == "" {
		return runtimeDispatch{kind: dispatchNative}, nil
	}
	sess, err := s.sessionService.Get(ctx, req.ThreadID)
	if err != nil {
		return runtimeDispatch{}, err
	}
	if err := validateSessionBot(req.BotID, req.ThreadID, sess.BotID); err != nil {
		return runtimeDispatch{}, err
	}
	runtimeType := ""
	switch {
	case session.IsACPRuntime(sess):
		runtimeType = session.RuntimeACPAgent
	case session.IsDirectRuntime(sess):
		runtimeType = strings.TrimSpace(sess.RuntimeType)
	default:
		return runtimeDispatch{kind: dispatchNative}, nil
	}
	driver, ok := s.externalDrivers[runtimeType]
	if !ok {
		return runtimeDispatch{}, apperror.New(apperror.CodeExternalRuntimeUnavailable, map[string]string{"runtime": runtimeType})
	}
	return runtimeDispatch{kind: dispatchExternal, driver: driver}, nil
}

// streamRuntimeChunks adapts the WS turn to the chunk-channel surface.
// Cancellation reports the error immediately but does NOT return until
// streamRuntimeWS does: the caller's FinishRun frees the session's single
// active slot, and the driver's interrupt handshake (codex turn/interrupt,
// claude SIGINT grace) can still be executing against the workspace for
// several seconds. Returning early let a stopped turn overlap the next one.
// The drivers bound their own interrupt windows, so this wait is bounded.
func (s *Service) streamRuntimeChunks(ctx context.Context, driver external.Driver, req ChatRequest, chunkCh chan<- StreamChunk, errCh chan<- error) {
	eventCh := make(chan WSStreamEvent)
	done := make(chan error, 1)
	go func() {
		defer close(eventCh)
		done <- s.streamRuntimeWS(ctx, driver, req, eventCh, nil)
		close(done)
	}()
	ctxDone := ctx.Done()
	cancelled := false
	for eventCh != nil || done != nil {
		select {
		case event, ok := <-eventCh:
			if !ok {
				eventCh = nil
				continue
			}
			if cancelled {
				// The consumer stopped reading on cancellation; keep
				// draining so the producer can wind down.
				continue
			}
			select {
			case chunkCh <- event:
			case <-ctxDone:
				cancelled = true
				ctxDone = nil
				errCh <- ctx.Err()
			}
		case err, ok := <-done:
			if !ok {
				done = nil
				continue
			}
			if err != nil && !cancelled {
				errCh <- err
			}
		case <-ctxDone:
			cancelled = true
			ctxDone = nil
			errCh <- ctx.Err()
		}
	}
}

// runtimeSessionMeta overlays session metadata with runtime metadata
// (runtime wins), giving drivers one merged view of their keys.
func runtimeSessionMeta(sess session.Thread) map[string]any {
	out := make(map[string]any, len(sess.Metadata)+len(sess.RuntimeMetadata))
	for key, value := range sess.Metadata {
		out[key] = value
	}
	for key, value := range sess.RuntimeMetadata {
		out[key] = value
	}
	return out
}

// streamRuntimeWS runs one turn on a runtime driver (ACP, codex,
// claude-code) and streams events to the WS surface. The runtime owns its
// durable session state (keyed through runtime metadata), Memoh's history is
// a projection persisted per round, and runtimes that checkpoint report the
// staging outcome on the result so the round publishes the matching head.
func (s *Service) streamRuntimeWS(ctx context.Context, driver external.Driver, req ChatRequest, eventCh chan<- WSStreamEvent, abortCh <-chan struct{}) error {
	req.RunID = runIDForChatRequest(req.RunID)
	if s.sessionRuntime != nil {
		// External drivers block their turn inline on decisions; declare it
		// before the first prompt so the manager resumes on terminal decision
		// statuses instead of waiting for a native-style re-entry.
		s.sessionRuntime.MarkInlineDecisionRun(req.BotID, req.ThreadID, req.RunID)
	}
	reasoningTiming := newReasoningTimingTracker(nil)
	sess, err := s.sessionService.Get(ctx, req.ThreadID)
	if err != nil {
		return err
	}
	if err := validateSessionBot(req.BotID, req.ThreadID, sess.BotID); err != nil {
		return err
	}
	runtimeType := strings.TrimSpace(driver.RuntimeType())
	runtimeMeta := runtimeSessionMeta(sess)
	projectPath := metadataString(runtimeMeta, "project_path")
	runtimeOwnerAccountID := metadataString(runtimeMeta, "runtime_owner_account_id")
	if err := s.requireRuntimeOwnerWorkspaceExec(ctx, req.BotID, runtimeOwnerAccountID); err != nil {
		return err
	}
	// A concurrent turn never reaches here: admission holds the session's
	// single active slot upstream.
	preparedAttachments, err := s.prepareRuntimeAttachments(ctx, req)
	if err != nil {
		return err
	}
	contextReq := req
	contextReq.Attachments = preparedAttachments.Context
	contextReq.ReplyAttachments = nil
	if req.RawQuery == "" {
		req.RawQuery = strings.TrimSpace(req.Query)
	}
	req.Query = strings.TrimSpace(req.Query)
	contextSections, memoryTrace := s.buildRuntimeContextSections(ctx, contextReq, runtimeContextAgentID(runtimeType, runtimeMeta), projectPath)
	contextMarkdown, contextURI, contextManifest := runtimeContextViaContextView(ctx, s.logger, contextSections, req.Query)
	contextLifecycle := contextfrag.NewLifecycleHolder()
	if contextManifest != nil {
		contextLifecycle.SetManifest(*contextManifest)
	}
	if memoryTrace != nil {
		contextLifecycle.SetMemoryRecall(*memoryTrace)
	}
	var leadingUser *messagepkg.Message
	req, leadingUser, err = s.persistRuntimeLeadingUserMessage(context.WithoutCancel(ctx), req)
	if err != nil {
		return apperror.Wrap(apperror.CodeSessionHistoryInconsistent, err, nil)
	}
	// Once a round persists, the leading user message is never deleted: the
	// user watched it send. Only the config-error early exit below — where no
	// round exists at all — may clean it up.
	cleanupLeadingUser := func() {
		if leadingUser != nil {
			s.cleanupReplacementMessages(context.WithoutCancel(ctx), []messagepkg.Message{*leadingUser})
		}
	}
	go s.maybeGenerateSessionTitle(context.WithoutCancel(ctx), req, req.RawQuery)

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	idleCtx, idleCancel := s.withStreamIdleTimeout(streamCtx, strings.TrimSpace(req.ReasoningEffort))
	defer idleCancel.Stop()
	terminal := s.contextLifecycleTerminal(streamCtx, native.RunConfig{
		RunID: req.RunID,
		Identity: native.SessionContext{
			BotID:     req.BotID,
			SessionID: req.ThreadID,
		},
		ContextLifecycle: contextLifecycle,
	})
	var lifecycleCause error
	defer func() { terminal(lifecycleCause) }()
	activePrompt := s.registerExternalAgentActivePrompt(req.BotID, req.ThreadID)
	defer s.unregisterExternalAgentActivePrompt(req.BotID, req.ThreadID, activePrompt)

	// userAborted distinguishes an explicit Stop from an ordinary client
	// disconnect: both cancel streamCtx, but only a Stop downgrades a
	// completed prompt to an aborted round.
	var userAborted atomic.Bool
	go func() {
		select {
		case <-abortCh:
			userAborted.Store(true)
			cancel()
		case <-streamCtx.Done():
		}
	}()
	userStopped := func() bool {
		if userAborted.Load() {
			return true
		}
		select {
		case <-abortCh:
			userAborted.Store(true)
			return true
		default:
			return false
		}
	}

	var (
		projectedMu       sync.Mutex
		projectedMessages = map[string]*messagepkg.Message{}
	)
	recordProjection := func(ev native.StreamEvent) bool {
		toolCallID := strings.TrimSpace(ev.ToolCallID)
		if toolCallID == "" {
			return false
		}
		projectedMu.Lock()
		defer projectedMu.Unlock()
		if _, exists := projectedMessages[toolCallID]; exists {
			return false
		}
		projectedMessages[toolCallID] = nil
		return true
	}
	completeProjection := func(toolCallID string, message *messagepkg.Message) {
		toolCallID = strings.TrimSpace(toolCallID)
		if toolCallID == "" {
			return
		}
		projectedMu.Lock()
		if message == nil {
			delete(projectedMessages, toolCallID)
		} else {
			projectedMessages[toolCallID] = message
		}
		projectedMu.Unlock()
	}
	projectedSnapshot := func() []messagepkg.Message {
		projectedMu.Lock()
		defer projectedMu.Unlock()
		if len(projectedMessages) == 0 {
			return nil
		}
		out := make([]messagepkg.Message, 0, len(projectedMessages))
		for _, message := range projectedMessages {
			if message != nil {
				out = append(out, *message)
			}
		}
		return out
	}
	cleanupProjectionsIn := func(cleanupCtx context.Context) {
		s.cleanupReplacementMessages(cleanupCtx, projectedSnapshot())
		s.cleanupRuntimeDecisionProjectionRows(cleanupCtx, req)
	}
	cleanupProjections := func() { cleanupProjectionsIn(context.WithoutCancel(ctx)) }

	emitWithContext := func(deliveryCtx context.Context, ev native.StreamEvent) {
		reasoningTiming.observe(ev)
		if isRuntimeDecisionProjectionEvent(ev) && recordProjection(ev) {
			completeProjection(ev.ToolCallID, s.persistRuntimeDecisionProjection(context.WithoutCancel(ctx), req, ev))
		}
		if activePrompt != nil {
			activePrompt.emit(ev)
		}
		data, err := json.Marshal(ev)
		if err != nil {
			return
		}
		select {
		case eventCh <- json.RawMessage(data):
			return
		default:
		}
		stall := time.NewTimer(runtimeSinkStallTimeout)
		defer stall.Stop()
		select {
		case eventCh <- json.RawMessage(data):
		case <-deliveryCtx.Done():
		case <-stall.C:
			// A live stream that has not accepted an event for this long has a
			// dead consumer; tear the turn down like a client disconnect.
			cancel()
		}
	}
	emit := func(ev native.StreamEvent) {
		idleCancel.Reset()
		if ev.Type == native.EventToolCallStart {
			idleCancel.RecordToolCall()
		}
		emitWithContext(streamCtx, ev)
	}

	emit(native.StreamEvent{Type: native.EventStart})

	result, err := driver.Prompt(idleCtx, external.PromptInput{
		BotID:                     req.BotID,
		BotAgentID:                sess.BotAgentID,
		ChatID:                    req.ChatID,
		ThreadID:                  req.ThreadID,
		RunID:                     req.RunID,
		RouteID:                   req.RouteID,
		Prompt:                    req.Query,
		ContextMarkdown:           contextMarkdown,
		ContextURI:                contextURI,
		ContextBudgetMaxTokens:    s.runtimeContextBudgetDefault(streamCtx, req.BotID),
		ContextToolExchangePolicy: defaultToolExchangePolicy(),
		Images:                    preparedAttachments.Images,
		AttachmentReferences:      preparedAttachments.References,
		CanFallbackImagesToFiles:  preparedAttachments.CanFallbackImagesToFiles,
		ModelID:                   strings.TrimSpace(req.Model),
		ReasoningEffort:           strings.TrimSpace(req.ReasoningEffort),
		SessionMode:               firstNonEmpty(req.SessionType, sessionmode.Chat),
		CurrentPlatform:           req.CurrentChannel,
		ReplyTarget:               req.ReplyTarget,
		ConversationType:          req.ConversationType,
		Command:                   req.AgentCommand,
		ForceFreshRuntime:         req.ForceFreshRuntime,
		RuntimeMetadata:           runtimeMeta,
		RuntimeOwnerAccountID:     runtimeOwnerAccountID,
		ChannelIdentityID:         req.SourceChannelIdentityID,
		SessionToken:              req.Token,
		ToolHTTPURL:               req.ToolHTTPURL,
		// Elicitation rides each runtime's own protocol (ACP elicitation,
		// codex requestUserInput), independent of the MCP tool gateway.
		ToolOutputLimit:     s.toolOutputLimit(),
		CanRequestUserInput: s.canDeliverUserInputWS(eventCh),
		Sink:                external.EventSinkFunc(emit),
	})
	lifecycleCause = err
	if idleCancel.DidFire() {
		// Drivers normalize cancellation into a partial nil-error result so the
		// application can persist interrupted output. Restore the watchdog's
		// stable cause here; unlike a user stop or client disconnect, an idle
		// timeout is a failed turn and must carry agent.response_timeout through
		// history, lifecycle, and the live stream.
		err = context.Cause(idleCtx)
		lifecycleCause = err
	}

	cancelPending := func() {
		s.cancelPendingRuntimeApprovals(context.WithoutCancel(ctx), req, "tool approval cancelled: the turn ended before a decision arrived")
	}

	// Persist driver-owned session keys (e.g. the runtime's thread id) before
	// anything else. History must never advance past this anchor: committing
	// the round while its new thread/session id was lost makes every later
	// turn resume the pre-round context, forking the conversation permanently.
	// Failing the round keeps state consistent instead — nothing is committed,
	// and a retry runs against the stored anchor.
	if len(result.RuntimeMetadata) > 0 {
		if _, mergeErr := s.sessionService.MergeRuntimeMetadata(context.WithoutCancel(ctx), req.ThreadID, runtimeType, result.RuntimeMetadata); mergeErr != nil {
			s.logger.Error("external runtime metadata merge failed",
				slog.String("session_id", req.ThreadID), slog.String("runtime", runtimeType), slog.Any("error", mergeErr))
			cancelPending()
			cleanupProjections()
			cleanupLeadingUser()
			anchorErr := fmt.Errorf("persist runtime session anchor: %w", mergeErr)
			if err != nil {
				anchorErr = errors.Join(err, anchorErr)
			}
			lifecycleCause = anchorErr
			return anchorErr
		}
	}

	if err != nil {
		s.logger.Error("external runtime prompt failed",
			slog.String("bot_id", req.BotID),
			slog.String("session_id", req.ThreadID),
			slog.String("runtime", runtimeType),
			slog.Any("error", err),
		)
		cancelPending()
		if isRuntimeConfigurationError(err) {
			// Configuration-class failure: nothing ran, so persist nothing.
			// Repeated attempts must not pile failure rounds into history.
			cleanupProjections()
			cleanupLeadingUser()
			return err
		}
		if streamCtx.Err() != nil {
			// A user stop or client disconnect: keep the partial output
			// unannotated; the turn simply did not complete.
			abortedReq := req
			abortedReq.SkipMemoryExtraction = true
			if persistErr := s.persistRuntimeRound(context.WithoutCancel(ctx), abortedReq, runtimeType, projectPath, result, nil, false, contextLifecycle, takeTerminalReasoningTiming(reasoningTiming, native.EventAgentAbort)); persistErr != nil {
				lifecycleCause = persistErr
				s.logger.Error("external abort persist failed", slog.Any("error", persistErr), slog.String("session_id", req.ThreadID))
				if s.resolveRuntimeRoundPersistFailure(ctx, req, persistErr, cleanupProjectionsIn) != runtimeRoundUnresolved {
					cleanupProjections()
				}
			} else {
				cleanupProjections()
			}
			emitWithContext(ctx, native.StreamEvent{Type: native.EventTextEnd})
			emitWithContext(ctx, runtimeTerminalStreamEvent(native.EventAbort, result))
			return nil
		}
		failedResult, failureDelta := runtimeFailureResult(result, err)
		if failureDelta != "" {
			emit(native.StreamEvent{Type: native.EventTextDelta, Delta: failureDelta})
		}
		if persistErr := s.persistRuntimeRound(context.WithoutCancel(ctx), req, runtimeType, projectPath, failedResult, err, false, contextLifecycle, takeTerminalReasoningTiming(reasoningTiming, native.EventAgentAbort)); persistErr != nil {
			lifecycleCause = runtimeHistoryError(persistErr)
			s.logger.Error("external failure persist failed", slog.Any("error", persistErr), slog.String("session_id", req.ThreadID))
			switch s.resolveRuntimeRoundPersistFailure(ctx, req, persistErr, cleanupProjectionsIn) {
			case runtimeRoundCommitted:
				lifecycleCause = err
				cleanupProjections()
				emit(native.StreamEvent{Type: native.EventTextEnd})
				emit(runtimeTerminalStreamEvent(native.EventAbort, failedResult))
				return nil
			case runtimeRoundUnresolved:
				return apperror.Wrap(apperror.CodeSessionHistoryInconsistent, persistErr, nil)
			case runtimeRoundRolledBack:
				cleanupProjections()
			}
		} else {
			cleanupProjections()
		}
		lifecycleSnapshot, _ := contextLifecycle.Snapshot()
		if status, _ := classifyContextLifecycleTerminal(streamCtx, lifecycleSnapshot, lifecycleCause); status != contextLifecycleStatusAborted {
			emit(runtimeFailureEvent(lifecycleCause))
		}
		emit(native.StreamEvent{Type: native.EventTextEnd})
		emit(runtimeTerminalStreamEvent(native.EventAbort, failedResult))
		return nil
	}

	if !result.TurnCompleted || (streamCtx.Err() != nil && userStopped()) {
		// Either the runtime did not finish the turn (interrupt landed), or it
		// finished in the same instant the user stopped it. Both present as an
		// abort with no memory extraction; only a genuinely completed turn
		// persists a "succeeded" outcome marker.
		if lifecycleCause == nil && streamCtx.Err() != nil {
			// The driver's interrupt contract swallows the cancellation error;
			// restore it so the terminal lifecycle records an abort, not a
			// completion.
			lifecycleCause = context.Cause(streamCtx)
		}
		cancelPending()
		abortedReq := req
		abortedReq.SkipMemoryExtraction = true
		if persistErr := s.persistRuntimeRound(context.WithoutCancel(ctx), abortedReq, runtimeType, projectPath, result, nil, result.TurnCompleted, contextLifecycle, takeTerminalReasoningTiming(reasoningTiming, native.EventAgentAbort)); persistErr != nil {
			lifecycleCause = persistErr
			s.logger.Error("external abort persist failed", slog.Any("error", persistErr), slog.String("session_id", req.ThreadID))
			switch s.resolveRuntimeRoundPersistFailure(ctx, req, persistErr, cleanupProjectionsIn) {
			case runtimeRoundCommitted:
				lifecycleCause = nil
				cleanupProjections()
			case runtimeRoundRolledBack:
				// A completed-then-stopped turn moved the runtime past the
				// canonical head; a merely interrupted one did not, so only the
				// former needs driver-side repair.
				s.handleRuntimeRoundRollback(ctx, driver, req, runtimeType, result.TurnCompleted)
				cleanupProjections()
			case runtimeRoundUnresolved:
			}
		} else {
			cleanupProjections()
		}
		emitWithContext(ctx, native.StreamEvent{Type: native.EventTextEnd})
		emitWithContext(ctx, runtimeTerminalStreamEvent(native.EventAbort, result))
		return nil
	}

	emit(native.StreamEvent{Type: native.EventTextEnd})
	if persistErr := s.persistRuntimeRound(context.WithoutCancel(ctx), req, runtimeType, projectPath, result, nil, true, contextLifecycle, takeTerminalReasoningTiming(reasoningTiming, native.EventAgentEnd)); persistErr != nil {
		lifecycleCause = runtimeHistoryError(persistErr)
		s.logger.Error("external persist failed", slog.Any("error", persistErr), slog.String("session_id", req.ThreadID))
		switch s.resolveRuntimeRoundPersistFailure(ctx, req, persistErr, cleanupProjectionsIn) {
		case runtimeRoundCommitted:
			lifecycleCause = nil
			cleanupProjections()
			emit(runtimeTerminalStreamEvent(native.EventEnd, result))
			return nil
		case runtimeRoundUnresolved:
			return apperror.Wrap(apperror.CodeSessionHistoryInconsistent, persistErr, nil)
		case runtimeRoundRolledBack:
		}
		// Definite rollback of a completed turn: the runtime remembers a round
		// the visible history lost. Drivers with a repair policy (the ACP pool
		// discards the warm process) take the callback; runtimes whose durable
		// state is authoritative on their own side keep the session — dropping
		// it would lose the whole conversation context — and the ghost turn is
		// logged.
		s.handleRuntimeRoundRollback(ctx, driver, req, runtimeType, true)
		cleanupProjections()
		emit(runtimeFailureEvent(lifecycleCause))
		emit(runtimeTerminalStreamEvent(native.EventAbort, result))
		return nil
	}
	cleanupProjections()
	emit(runtimeTerminalStreamEvent(native.EventEnd, result))
	return nil
}

// triggerScheduleRuntime runs one scheduled External Agent turn without an
// interactive stream and returns the persisted round's plain-text result.
func (s *Service) triggerScheduleRuntime(ctx context.Context, botID string, payload schedule.TriggerPayload, token, runID string, driver external.Driver) (schedule.TriggerResult, error) {
	if s.sessionRuntime != nil {
		// Same inline-decision declaration as streamRuntimeWS.
		s.sessionRuntime.MarkInlineDecisionRun(botID, payload.SessionID, runID)
	}
	sess, err := s.sessionService.Get(ctx, payload.SessionID)
	if err != nil {
		return schedule.TriggerResult{}, err
	}
	runtimeType := strings.TrimSpace(driver.RuntimeType())
	runtimeMeta := runtimeSessionMeta(sess)
	projectPath := metadataString(runtimeMeta, "project_path")
	runtimeOwner := strings.TrimSpace(metadataString(runtimeMeta, "runtime_owner_account_id"))
	// The helper fails closed on a missing owner with the stable feedback
	// error the interactive path uses.
	if err := s.requireRuntimeOwnerWorkspaceExec(ctx, botID, runtimeOwner); err != nil {
		return schedule.TriggerResult{}, err
	}

	req := ChatRequest{
		BotID:           botID,
		ChatID:          botID,
		ThreadID:        payload.SessionID,
		RunID:           runID,
		Query:           payload.Command,
		RawQuery:        payload.Command,
		UserID:          payload.OwnerUserID,
		Token:           token,
		Model:           payload.ACPModelID,
		ReasoningEffort: payload.ReasoningEffort,
		SessionType:     sessionmode.Schedule,
	}

	schedulePrompt := native.GenerateSchedulePrompt(native.Schedule{
		ID:          payload.ID,
		Name:        payload.Name,
		Description: payload.Description,
		Pattern:     payload.Pattern,
		MaxCalls:    payload.MaxCalls,
		Command:     payload.Command,
	})
	contextSections, memoryTrace := s.buildRuntimeContextSections(ctx, req, runtimeContextAgentID(runtimeType, runtimeMeta), projectPath)
	contextMarkdown, contextURI, contextManifest := runtimeContextViaContextView(ctx, s.logger, contextSections, req.Query)
	contextLifecycle := contextfrag.NewLifecycleHolder()
	if contextManifest != nil {
		contextLifecycle.SetManifest(*contextManifest)
	}
	if memoryTrace != nil {
		contextLifecycle.SetMemoryRecall(*memoryTrace)
	}
	terminal := s.contextLifecycleTerminal(ctx, native.RunConfig{
		RunID: runID,
		Identity: native.SessionContext{
			BotID:     botID,
			SessionID: payload.SessionID,
		},
		ContextLifecycle: contextLifecycle,
	})
	var lifecycleCause error
	defer func() { terminal(lifecycleCause) }()

	// Fail closed like the chat path: proceeding after an uncertain eager
	// insert would race cleanup against this round's own user message.
	var leadingUser *messagepkg.Message
	req, leadingUser, err = s.persistRuntimeLeadingUserMessage(context.WithoutCancel(ctx), req)
	if err != nil {
		return schedule.TriggerResult{}, fmt.Errorf("persist scheduled external user message: %w", err)
	}

	reasoningTiming := newReasoningTimingTracker(nil)
	idleCtx, idleCancel := s.withStreamIdleTimeout(ctx, strings.TrimSpace(payload.ReasoningEffort))
	defer idleCancel.Stop()
	result, promptErr := driver.Prompt(idleCtx, external.PromptInput{
		BotID:                     botID,
		BotAgentID:                sess.BotAgentID,
		ChatID:                    botID,
		ThreadID:                  payload.SessionID,
		RunID:                     runID,
		Prompt:                    schedulePrompt,
		ContextMarkdown:           contextMarkdown,
		ContextURI:                contextURI,
		ContextBudgetMaxTokens:    s.runtimeContextBudgetDefault(ctx, botID),
		ContextToolExchangePolicy: defaultToolExchangePolicy(),
		ModelID:                   strings.TrimSpace(payload.ACPModelID),
		ReasoningEffort:           strings.TrimSpace(payload.ReasoningEffort),
		SessionMode:               sessionmode.Schedule,
		RuntimeMetadata:           runtimeMeta,
		RuntimeOwnerAccountID:     runtimeOwner,
		ChannelIdentityID:         strings.TrimSpace(payload.OwnerUserID),
		SessionToken:              token,
		ToolOutputLimit:           s.toolOutputLimit(),
		// Nobody is on the other end of a scheduled run.
		CanRequestUserInput: false,
		Sink: external.EventSinkFunc(func(ev native.StreamEvent) {
			idleCancel.Reset()
			if ev.Type == native.EventToolCallStart {
				idleCancel.RecordToolCall()
			}
			reasoningTiming.observe(ev)
		}),
	})
	if idleCancel.DidFire() {
		promptErr = context.Cause(idleCtx)
	}
	lifecycleCause = promptErr
	// Same contract as the chat path: the round must not commit past a lost
	// runtime session anchor, or later fires resume the pre-round context.
	if len(result.RuntimeMetadata) > 0 {
		if _, mergeErr := s.sessionService.MergeRuntimeMetadata(context.WithoutCancel(ctx), payload.SessionID, runtimeType, result.RuntimeMetadata); mergeErr != nil {
			s.logger.Error("external runtime metadata merge failed",
				slog.String("session_id", payload.SessionID), slog.String("runtime", runtimeType), slog.Any("error", mergeErr))
			s.cancelPendingRuntimeApprovals(context.WithoutCancel(ctx), req, "tool approval cancelled: the scheduled run ended before a decision arrived")
			if leadingUser != nil {
				s.cleanupReplacementMessages(context.WithoutCancel(ctx), []messagepkg.Message{*leadingUser})
			}
			anchorErr := fmt.Errorf("persist runtime session anchor: %w", mergeErr)
			if promptErr != nil {
				anchorErr = errors.Join(promptErr, anchorErr)
			}
			lifecycleCause = anchorErr
			return schedule.TriggerResult{}, anchorErr
		}
	}
	if !result.TurnCompleted && (promptErr == nil || ctx.Err() != nil) {
		cause := promptErr
		if cause == nil {
			cause = context.Cause(ctx)
		}
		if cause == nil {
			cause = errors.New("external schedule runtime ended before completing the turn")
		}
		lifecycleCause = cause
		s.cancelPendingRuntimeApprovals(context.WithoutCancel(ctx), req, "tool approval cancelled: the scheduled run ended before a decision arrived")
		abortedReq := req
		abortedReq.SkipMemoryExtraction = true
		if persistErr := s.persistRuntimeRound(context.WithoutCancel(ctx), abortedReq, runtimeType, projectPath, result, nil, false, contextLifecycle, takeTerminalReasoningTiming(reasoningTiming, native.EventAgentAbort)); persistErr != nil {
			lifecycleCause = runtimeHistoryError(persistErr)
			s.logger.Error("external schedule abort persist failed", slog.Any("error", persistErr), slog.String("session_id", payload.SessionID))
			return schedule.TriggerResult{}, persistErr
		}
		return schedule.TriggerResult{}, cause
	}
	if promptErr != nil {
		s.cancelPendingRuntimeApprovals(context.WithoutCancel(ctx), req, "tool approval cancelled: the scheduled run ended before a decision arrived")
		if isRuntimeConfigurationError(promptErr) {
			// Configuration-class failure: nothing ran; the schedule log keeps
			// the failure record without polluting the session history.
			if leadingUser != nil {
				s.cleanupReplacementMessages(context.WithoutCancel(ctx), []messagepkg.Message{*leadingUser})
			}
			return schedule.TriggerResult{}, promptErr
		}
		failedResult, _ := runtimeFailureResult(result, promptErr)
		if err := s.persistRuntimeRound(context.WithoutCancel(ctx), req, runtimeType, projectPath, failedResult, promptErr, false, contextLifecycle, takeTerminalReasoningTiming(reasoningTiming, native.EventAgentAbort)); err != nil {
			lifecycleCause = runtimeHistoryError(err)
			s.logger.Error("external schedule failure persist failed", slog.Any("error", err), slog.String("session_id", payload.SessionID))
		}
		return schedule.TriggerResult{}, promptErr
	}

	if err := s.persistRuntimeRound(context.WithoutCancel(ctx), req, runtimeType, projectPath, result, nil, result.TurnCompleted, contextLifecycle, takeTerminalReasoningTiming(reasoningTiming, native.EventAgentEnd)); err != nil {
		lifecycleCause = runtimeHistoryError(err)
		s.logger.Error("external schedule persist failed", slog.Any("error", err), slog.String("session_id", payload.SessionID))
		return schedule.TriggerResult{}, err
	}

	var usageJSON []byte
	if result.Usage != nil {
		usageJSON, _ = json.Marshal(result.Usage)
	}
	return schedule.TriggerResult{
		Status:     "ok",
		Text:       strings.TrimSpace(result.Text),
		UsageBytes: usageJSON,
	}, nil
}

// abortSettleWait bounds how long AbortSessionRuns waits for the aborted run
// to reach a terminal state. It must outlast the drivers' own interrupt
// grace windows (codex settle + recycle, claude interrupt grace) so a
// successful return means the runtime has actually stopped.
const abortSettleWait = 30 * time.Second

// AbortSessionRuns interrupts the session's active run, if any, and waits —
// bounded — until it actually stops. Deleting or resetting a session on a
// direct external runtime must not leave its turn executing (and mutating
// the workspace) behind the deletion; cancel alone only signals the driver,
// so callers that destroy the session next need the settle, not the signal.
// A run that cannot settle inside the window fails the call rather than
// letting the caller proceed over a still-running turn.
func (s *Service) AbortSessionRuns(ctx context.Context, botID, sessionID string) error {
	if s == nil || s.decisionRuntime == nil {
		return nil
	}
	snapshot, err := s.decisionRuntime.Snapshot(ctx, botID, sessionID)
	if err != nil {
		return err
	}
	run := snapshot.CurrentRunView
	if run == nil || strings.TrimSpace(run.RunID) == "" {
		return nil
	}
	runID := run.RunID
	if _, err = s.decisionRuntime.Abort(ctx, botID, sessionID, runID); err != nil {
		return err
	}
	deadline := time.Now().Add(abortSettleWait)
	for {
		snapshot, err := s.decisionRuntime.Snapshot(ctx, botID, sessionID)
		if err != nil {
			return err
		}
		current := snapshot.CurrentRunView
		// The ledger going terminal is not enough: a waiting_decision run is
		// stamped aborted the moment its cancel is dispatched, while the
		// driver's interrupt handshake is still tearing the turn down. The
		// active-prompt registration lives exactly as long as the driver
		// call, so wait for both.
		if (current == nil || current.RunID != runID || !activeRuntimeRunStatus(current.Status)) &&
			!s.hasExternalAgentActivePrompt(botID, sessionID) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("aborted run %s did not stop within %s", runID, abortSettleWait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func activeRuntimeRunStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case sessionruntime.RunStatusAdmitting, sessionruntime.RunStatusRunning,
		sessionruntime.RunStatusWaitingDecision, sessionruntime.RunStatusAborting:
		return true
	default:
		return false
	}
}

// handleRuntimeRoundRollback resolves a definite round rollback: a driver
// that repairs its runtime state (the ACP pool discards the warm process so
// the next prompt restarts from the durable head) gets the callback for
// completed turns; every other case keeps the runtime session and logs the
// ghost turn.
func (s *Service) handleRuntimeRoundRollback(ctx context.Context, driver external.Driver, req ChatRequest, runtimeType string, turnCompleted bool) {
	if turnCompleted {
		if handler, ok := driver.(external.RoundRollbackHandler); ok {
			handler.OnRoundRolledBack(context.WithoutCancel(ctx), req.BotID, req.ThreadID)
			return
		}
	}
	s.logRuntimeRoundDivergence(req, runtimeType)
}

func (s *Service) logRuntimeRoundDivergence(req ChatRequest, runtimeType string) {
	s.logger.Warn("external runtime remembers a round the visible history lost",
		slog.String("session_id", req.ThreadID),
		slog.String("run_id", req.RunID),
		slog.String("runtime", runtimeType),
	)
}

// persistRuntimeRound stores one external turn as a normal history round.
// Shared outcome markers and reconciliation keep commit-unknown handling
// runtime-neutral. Runtimes that implement
// checkpoint stage their snapshot at turn end and report the outcome on the
// result; the round commit publishes the matching head in the same
// transaction as the messages. Runtimes without checkpoints publish nothing.
func (s *Service) persistRuntimeRound(
	ctx context.Context,
	req ChatRequest,
	runtimeType, projectPath string,
	result external.PromptResult,
	promptErr error,
	turnCompleted bool,
	contextLifecycle *contextfrag.LifecycleHolder,
	reasoningTiming []messagepkg.ReasoningTimingSegment,
) error {
	meta := map[string]any{
		"runtime_type": runtimeType,
		"project_path": projectPath,
		"stop_reason":  result.StopReason,
	}
	for key, value := range result.RoundMetadata {
		meta[key] = value
	}
	if promptErr != nil {
		meta["agent_turn_outcome"] = "failed"
		meta["error"] = runtimeUserFacingFailureMessage(promptErr)
		var feedbackErr *agentfeedback.Error
		if errors.As(promptErr, &feedbackErr) {
			meta["error_code"] = feedbackErr.Code
			meta["error_reason"] = feedbackErr.Reason
			meta["i18n_key"] = feedbackErr.I18nKey
		} else if code := strings.TrimSpace(string(apperror.CodeOf(promptErr))); code != "" {
			meta["error_code"] = code
		} else {
			meta["error_code"] = "runtime_prompt_failed"
		}
	}
	output := sdkMessagesToModelMessages(result.Output)
	if len(output) == 0 {
		output = []ModelMessage{{Role: "assistant", Content: newTextContent("")}}
	}
	output = repairToolCallClosures(output, syntheticToolClosureError)
	hasAssistant := false
	for _, msg := range output {
		if msg.Role == "assistant" {
			hasAssistant = true
			break
		}
	}
	if !hasAssistant {
		return errors.New("external transcript has no assistant message to publish")
	}
	if result.Usage != nil {
		for idx := len(output) - 1; idx >= 0; idx-- {
			if output[idx].Role == "assistant" {
				usage, _ := json.Marshal(result.Usage)
				output[idx].Usage = usage
				break
			}
		}
	}
	round := make([]ModelMessage, 0, 1+len(output))
	round = append(round, ModelMessage{Role: "user", Content: newTextContent(req.Query)})
	round = append(round, output...)

	metadataByIndex := make(map[int]map[string]any, len(output))
	metadataOffset := 1
	if req.UserMessagePersisted || req.ReusePersistedUserMessage {
		metadataOffset = 0
	}
	lastAssistantIndex := -1
	for idx, msg := range output {
		if msg.Role == "assistant" {
			lastAssistantIndex = idx
			entryMeta := make(map[string]any, len(meta))
			for key, value := range meta {
				entryMeta[key] = value
			}
			metadataByIndex[idx+metadataOffset] = entryMeta
		}
	}
	if promptErr == nil && lastAssistantIndex >= 0 {
		outcome := make(map[string]any, len(meta)+1)
		for key, value := range meta {
			outcome[key] = value
		}
		// Aborted partial rounds commit as "aborted", not "succeeded": the
		// commit-unknown reconciliation needs the durable marker.
		if turnCompleted {
			outcome["agent_turn_outcome"] = "succeeded"
		} else {
			outcome["agent_turn_outcome"] = "aborted"
		}
		metadataByIndex[lastAssistantIndex+metadataOffset] = outcome
	}
	var publication *messagepkg.AgentPublication
	if promptErr == nil && turnCompleted && lastAssistantIndex >= 0 && result.Checkpoint != external.CheckpointNone {
		publication = &messagepkg.AgentPublication{
			RunID:           req.RunID,
			CheckpointReset: result.Checkpoint != external.CheckpointStaged,
		}
	}
	skipMemory := promptErr != nil || req.UserMessagePersisted || req.ReusePersistedUserMessage || req.SkipMemoryExtraction
	persisted, err := s.storeRoundWithOptionsResult(ctx, req, round, "", storeRoundOptions{
		AllowPendingToolCalls:             true,
		SkipMemory:                        skipMemory,
		AllowEmptyAssistantText:           true,
		MessageMetadataByIndex:            metadataByIndex,
		ReasoningTiming:                   reasoningTiming,
		RequireCompletePersist:            true,
		CleanupRuntimeDecisionProjections: true,
		AgentPublication:                  publication,
		// The runtime's turn id lands on the run row in the round transaction;
		// it anchors turn-level operations such as codex thread/fork.
		AgentTurnID:      strings.TrimSpace(result.AgentTurnID),
		ContextLifecycle: contextLifecycle,
	})
	if err == nil && lastPersistedAssistantMessageID(persisted) == "" {
		// Fires after a committed transaction: join the commit-unknown
		// sentinel so callers route into database reconciliation instead of
		// the definite-rollback compensation.
		err = errors.Join(db.ErrCommitOutcomeUnknown, errors.New("external assistant output was not persisted"))
	}
	if err == nil && promptErr == nil && (req.UserMessagePersisted || req.ReusePersistedUserMessage) && !req.SkipMemoryExtraction {
		go s.storeMemory(context.WithoutCancel(ctx), req, persisted)
	}
	return err
}

// runtimeFailureResult appends a short, sanitized failure marker to the
// partial result; detailed errors stay in logs, not user-visible history.
func runtimeFailureResult(result external.PromptResult, err error) (external.PromptResult, string) {
	message := runtimeUserFacingFailureMessage(err)
	if message == "" {
		return result, ""
	}
	result.Output = external.AppendTranscriptText(result.Output, message)
	return result, "\n\n" + message
}

func runtimeUserFacingFailureMessage(err error) string {
	if err == nil {
		return ""
	}
	var feedbackErr *agentfeedback.Error
	if errors.As(err, &feedbackErr) {
		if message := strings.TrimSpace(feedbackErr.Message); message != "" {
			return message
		}
	}
	if code := strings.TrimSpace(string(apperror.CodeOf(err))); code != "" {
		return "The external agent could not complete this turn (" + code + ")."
	}
	return "The external agent could not complete this turn."
}

// isRuntimeConfigurationError reports failures where nothing ran:
// the turn must not persist a round, and the caller surfaces the error
// directly. Drivers normalize their configuration-class errors into stable
// feedback or apperror shapes before returning them.
func isRuntimeConfigurationError(err error) bool {
	var feedbackErr *agentfeedback.Error
	if errors.As(err, &feedbackErr) {
		return true
	}
	switch apperror.CodeOf(err) {
	case apperror.CodeExternalRuntimeAuthRequired, apperror.CodeExternalRuntimeUnavailable,
		apperror.CodeSessionHistoryInconsistent,
		apperror.CodeACPModelSelectionUnsupported, apperror.CodeACPModelIDRequired,
		apperror.CodeACPModelUnavailable, apperror.CodeACPReasoningUnsupported,
		apperror.CodeACPReasoningEffortRequired, apperror.CodeACPReasoningUnavailable,
		apperror.CodeACPConfigUpdateFailed:
		return true
	default:
		return false
	}
}

func runtimeFailureEvent(cause error) native.StreamEvent {
	code := string(apperror.CodeOf(cause))
	if strings.TrimSpace(code) == "" {
		code = "runtime_prompt_failed"
	}
	return native.StreamEvent{Type: native.EventError, Error: code}
}

func runtimeTerminalStreamEvent(eventType native.StreamEventType, result external.PromptResult) native.StreamEvent {
	ev := native.StreamEvent{Type: eventType}
	if data, err := json.Marshal(result.Output); err == nil {
		ev.Messages = data
	}
	if result.Usage != nil {
		if data, err := json.Marshal(result.Usage); err == nil {
			ev.Usage = data
		}
	}
	return ev
}
