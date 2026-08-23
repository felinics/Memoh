package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"
	acpfeedback "github.com/memohai/memoh/internal/agent/decision/feedback"
	"github.com/memohai/memoh/internal/agent/event"
	acpagent "github.com/memohai/memoh/internal/agent/runtime/acp"
	acpclient "github.com/memohai/memoh/internal/agent/runtime/acp/client"
	"github.com/memohai/memoh/internal/agent/runtime/native"
	"github.com/memohai/memoh/internal/apperror"
	attachmentpkg "github.com/memohai/memoh/internal/attachment"
	"github.com/memohai/memoh/internal/bots"
	messagepkg "github.com/memohai/memoh/internal/chat/message"
	session "github.com/memohai/memoh/internal/chat/thread"
	"github.com/memohai/memoh/internal/db"
	"github.com/memohai/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/memohai/memoh/internal/db/store"
)

// acpSinkStallTimeout bounds how long a live-turn event delivery may wait on
// a WS consumer that is connected but not draining. Reaching it cancels the
// stream (client-disconnect semantics) so the adapter callback — and the
// prompt's runtime slot behind it — cannot stall indefinitely.
const acpSinkStallTimeout = 30 * time.Second

type acpPrompter interface {
	Prompt(ctx context.Context, input acpagent.PromptInput) (acpclient.PromptResult, error)
}

type acpPreparedAttachments struct {
	Images                   []acpclient.PromptImage
	Context                  []ChatAttachment
	References               []string
	CanFallbackImagesToFiles bool
}

type ACPSessionExecutionInfo struct {
	IsACP                 bool
	BotID                 string
	Type                  string
	RuntimeType           string
	CreatedByUserID       string
	AgentID               string
	ProjectPath           string
	RuntimeOwnerAccountID string
}

func (s *Service) SetACPSessionPool(pool acpPrompter) {
	s.acpPool = pool
}

func (s *Service) ACPSessionExecutionInfo(ctx context.Context, sessionID string) (ACPSessionExecutionInfo, error) {
	if s == nil || s.sessionService == nil || strings.TrimSpace(sessionID) == "" {
		return ACPSessionExecutionInfo{}, nil
	}
	sess, err := s.sessionService.Get(ctx, sessionID)
	if err != nil {
		return ACPSessionExecutionInfo{}, err
	}
	if !session.IsACPRuntime(sess) {
		return ACPSessionExecutionInfo{}, nil
	}
	acpMeta := mergeACPRuntimeMetadata(sess.Metadata, sess.RuntimeMetadata)
	return ACPSessionExecutionInfo{
		IsACP:                 true,
		BotID:                 sess.BotID,
		Type:                  sess.Type,
		RuntimeType:           sess.RuntimeType,
		CreatedByUserID:       sess.CreatedByUserID,
		AgentID:               metadataString(acpMeta, "acp_agent_id"),
		ProjectPath:           metadataString(acpMeta, "project_path"),
		RuntimeOwnerAccountID: metadataString(acpMeta, "runtime_owner_account_id"),
	}, nil
}

func (s *Service) isACPAgentSession(ctx context.Context, req ChatRequest) (bool, error) {
	if s == nil || s.sessionService == nil || strings.TrimSpace(req.ThreadID) == "" {
		return false, nil
	}
	sess, err := s.sessionService.Get(ctx, req.ThreadID)
	if err != nil {
		return false, err
	}
	if err := validateSessionBot(req.BotID, req.ThreadID, sess.BotID); err != nil {
		return false, err
	}
	return session.IsACPRuntime(sess), nil
}

func (s *Service) streamACPAgentWS(ctx context.Context, req ChatRequest, eventCh chan<- WSStreamEvent, abortCh <-chan struct{}) error {
	if s.acpPool == nil {
		return errors.New("ACP session pool is not configured")
	}
	req.RunID = runIDForChatRequest(req.RunID)
	sess, err := s.sessionService.Get(ctx, req.ThreadID)
	if err != nil {
		return err
	}
	if err := validateSessionBot(req.BotID, req.ThreadID, sess.BotID); err != nil {
		return err
	}
	acpMeta := mergeACPRuntimeMetadata(sess.Metadata, sess.RuntimeMetadata)
	agentID := metadataString(acpMeta, "acp_agent_id")
	projectPath := metadataString(acpMeta, "project_path")
	runtimeOwnerAccountID := metadataString(acpMeta, "runtime_owner_account_id")
	if runtimeOwnerAccountID == "" {
		return acpfeedback.New(
			acpfeedback.CodeRuntimeOwnerMissing,
			"missing_runtime_owner",
			409,
			"chat.acp.runtimeOwnerMissing",
			"ACP runtime owner is missing; recreate or reauthorize the ACP session",
			nil,
		)
	}
	if err := s.requireACPRuntimeOwnerWorkspaceExec(ctx, req.BotID, runtimeOwnerAccountID); err != nil {
		return err
	}
	// A concurrent turn never reaches here: admission holds the session's single
	// active slot, so a second submission is refused with a retryable
	// session_busy before any runtime is asked to prompt.
	preparedAttachments, err := s.prepareACPAttachments(ctx, req)
	if err != nil {
		return err
	}
	contextReq := req
	contextReq.Attachments = preparedAttachments.Context
	contextReq.ReplyAttachments = nil
	contextMarkdown := s.buildACPContextMarkdown(ctx, contextReq, agentID, projectPath)
	contextLifecycle := contextfrag.NewLifecycleHolder()
	contextLifecycle.SetManifest(contextfrag.BuildManifest(nil))

	if req.RawQuery == "" {
		req.RawQuery = strings.TrimSpace(req.Query)
	}
	req.Query = strings.TrimSpace(req.Query)
	var leadingUser *messagepkg.Message
	req, leadingUser, err = s.persistACPLeadingUserMessage(context.WithoutCancel(ctx), req)
	if err != nil {
		return apperror.Wrap(apperror.CodeSessionHistoryInconsistent, err, nil)
	}
	cleanupLeadingUser := func() {
		if leadingUser != nil {
			s.cleanupReplacementMessages(context.WithoutCancel(ctx), []messagepkg.Message{*leadingUser})
		}
	}
	go s.maybeGenerateSessionTitle(context.WithoutCancel(ctx), req, req.RawQuery)

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
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
	activePrompt := s.registerACPActivePrompt(req.BotID, req.ThreadID)
	defer s.unregisterACPActivePrompt(req.BotID, req.ThreadID, activePrompt)
	// userAborted distinguishes an explicit Stop from an ordinary client
	// disconnect: both cancel streamCtx, but only a Stop may downgrade a
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
			// The watcher lost the race for the buffered signal; consume it
			// here so the stop is still honored.
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
		s.cleanupACPDecisionProjections(cleanupCtx, req)
	}
	cleanupProjections := func() { cleanupProjectionsIn(context.WithoutCancel(ctx)) }

	emitWithContext := func(deliveryCtx context.Context, ev native.StreamEvent) {
		if isACPDecisionProjectionEvent(ev) && recordProjection(ev) {
			completeProjection(ev.ToolCallID, s.persistACPDecisionProjection(context.WithoutCancel(ctx), req, ev))
		}
		if activePrompt != nil {
			activePrompt.emit(ev)
		}
		data, err := json.Marshal(ev)
		if err != nil {
			return
		}
		// Prefer an immediately available receiver before consulting cancellation.
		// This keeps a final buffered frame deterministic when cancellation and
		// delivery become ready together.
		select {
		case eventCh <- json.RawMessage(data):
			return
		default:
		}
		stall := time.NewTimer(acpSinkStallTimeout)
		defer stall.Stop()
		select {
		case eventCh <- json.RawMessage(data):
		case <-deliveryCtx.Done():
		case <-stall.C:
			// A live stream that has not accepted a single event for this long
			// has a dead consumer (e.g. a WS client that stopped draining
			// without disconnecting). Cancel the stream so the turn tears down
			// like a client disconnect instead of blocking the adapter callback
			// — and with it the prompt's runtime slot — indefinitely.
			cancel()
		}
	}
	emit := func(ev native.StreamEvent) {
		emitWithContext(streamCtx, ev)
	}

	emit(native.StreamEvent{Type: native.EventStart})
	// No eager text_start here: the UI message converter allocates block IDs
	// in arrival order and the frontend sorts by ID, so pre-creating the text
	// block would pin the answer text above any reasoning that streams first.
	// The first text_delta lazily creates the text block instead.

	result, err := s.acpPool.Prompt(streamCtx, acpagent.PromptInput{
		BotID:                    req.BotID,
		ChatID:                   req.ChatID,
		SessionID:                req.ThreadID,
		RunID:                    req.RunID,
		RouteID:                  req.RouteID,
		AgentID:                  agentID,
		ProjectPath:              projectPath,
		ModelID:                  strings.TrimSpace(req.Model),
		ReasoningEffort:          strings.TrimSpace(req.ReasoningEffort),
		Prompt:                   req.Query,
		Images:                   preparedAttachments.Images,
		AttachmentReferences:     preparedAttachments.References,
		CanFallbackImagesToFiles: preparedAttachments.CanFallbackImagesToFiles,
		ChannelIdentityID:        req.SourceChannelIdentityID,
		SessionToken:             req.Token,
		CurrentPlatform:          req.CurrentChannel,
		ReplyTarget:              req.ReplyTarget,
		ConversationType:         req.ConversationType,
		CanRequestUserInput:      s.canDeliverUserInputWS(eventCh),
		// This flag controls image bytes returned later by the read-media MCP
		// tool. Initial user images use ACP ImageBlock transport above.
		SupportsImageInput:    false,
		ToolOutputLimit:       s.toolOutputLimit(),
		ToolHTTPURL:           req.ToolHTTPURL,
		ContextURI:            acpContextURI,
		ContextMarkdown:       contextMarkdown,
		RuntimeOwnerAccountID: runtimeOwnerAccountID,
		ForceFreshRuntime:     req.ForceFreshRuntime,
		RequiredCommand:       req.AgentCommand,
		Sink:                  acpclient.EventSinkFunc(emit),
	})
	lifecycleCause = err
	if err != nil {
		s.logger.Error("ACP prompt failed",
			slog.String("bot_id", req.BotID),
			slog.String("session_id", req.ThreadID),
			slog.Any("error", err),
		)
		s.cancelPendingACPApprovals(context.WithoutCancel(ctx), req, "tool approval cancelled: the turn ended before a decision arrived")
		if errors.Is(err, acpagent.ErrSessionStateOutOfSync) {
			cleanupProjections()
			cleanupLeadingUser()
			// Wrap before assigning the lifecycle cause so the terminal row
			// records the stable error code, matching the config-error branch.
			wrapped := apperror.Wrap(apperror.CodeSessionHistoryInconsistent, err, nil)
			lifecycleCause = wrapped
			return wrapped
		}
		var feedbackErr *acpfeedback.Error
		if errors.As(err, &feedbackErr) {
			cleanupProjections()
			cleanupLeadingUser()
			return err
		}
		if appErr := acpPromptConfigAppError(err); appErr != nil {
			cleanupProjections()
			lifecycleCause = appErr
			cleanupLeadingUser()
			return appErr
		}
		if feedbackErr := acpPromptInputFeedback(err); feedbackErr != nil {
			cleanupProjections()
			lifecycleCause = feedbackErr
			cleanupLeadingUser()
			return feedbackErr
		}
		result = ensureACPPromptOutput(result)
		if streamCtx.Err() != nil {
			// A user-initiated stop is not an agent failure: keep the partial
			// output unannotated instead of persisting a misleading
			// "agent failed to complete the turn" marker. The native runtime did
			// not complete this turn, so the canonical publication head stays at
			// the previous checkpoint (turnCompleted=false): the warm runtime
			// remains reusable and a cold start resumes the last complete turn.
			abortedReq := req
			abortedReq.SkipMemoryExtraction = true
			if persistErr := s.persistACPRound(context.WithoutCancel(ctx), abortedReq, agentID, projectPath, result, nil, false, contextLifecycle); persistErr != nil {
				lifecycleCause = persistErr
				s.logger.Error("ACP abort persist failed", slog.Any("error", persistErr), slog.String("session_id", req.ThreadID))
				// The runtime stays valid regardless of the resolution: the
				// turn never completed, so the canonical head did not move.
				if s.resolveACPRoundPersistFailure(ctx, req, persistErr, cleanupProjectionsIn) != acpRoundUnresolved {
					cleanupProjections()
				}
			} else {
				cleanupProjections()
			}
			emitWithContext(ctx, native.StreamEvent{Type: native.EventTextEnd})
			emitWithContext(ctx, acpTerminalStreamEvent(native.EventAbort, result))
			return nil
		}
		failedResult, failureDelta := acpFailureResult(result, err)
		if failureDelta != "" {
			emit(native.StreamEvent{Type: native.EventTextDelta, Delta: failureDelta})
		}
		if persistErr := s.persistACPRound(context.WithoutCancel(ctx), req, agentID, projectPath, failedResult, err, false, contextLifecycle); persistErr != nil {
			lifecycleCause = runtimeHistoryError(persistErr)
			s.logger.Error("ACP failure persist failed", slog.Any("error", persistErr), slog.String("session_id", req.ThreadID))
			switch s.resolveACPRoundPersistFailure(ctx, req, persistErr, cleanupProjectionsIn) {
			case acpRoundCommitted:
				// The round committed: the terminal lifecycle cause is the
				// prompt failure, not a history-persistence loss.
				lifecycleCause = err
				cleanupProjections()
				emit(native.StreamEvent{Type: native.EventTextEnd})
				emit(acpTerminalStreamEvent(native.EventAbort, failedResult))
				return nil
			case acpRoundUnresolved:
				return apperror.Wrap(apperror.CodeSessionHistoryInconsistent, persistErr, nil)
			case acpRoundRolledBack:
				cleanupProjections()
			}
		} else {
			cleanupProjections()
		}
		if status, _ := classifyContextLifecycleTerminal(streamCtx, lifecycleCause); status != contextLifecycleStatusAborted {
			emit(acpRuntimeFailureEvent(lifecycleCause))
		}
		emit(native.StreamEvent{Type: native.EventTextEnd})
		emit(acpTerminalStreamEvent(native.EventAbort, failedResult))
		return nil
	}

	result = ensureACPPromptOutput(result)
	if streamCtx.Err() != nil && userStopped() {
		// The prompt finished in the same instant the user stopped it (the
		// SDK's response/ctx select is nondeterministic). Stop wins for
		// presentation: no memory extraction, EventAbort instead of EventEnd -
		// so a user's stop is never presented as a completed turn. But the
		// native runtime did complete and its state is staged, so the
		// publication head still advances (turnCompleted=true); anything else
		// would fork the warm process from canonical history. A mere client
		// disconnect is not a stop: the completed turn persists normally below.
		abortedReq := req
		abortedReq.SkipMemoryExtraction = true
		if persistErr := s.persistACPRound(context.WithoutCancel(ctx), abortedReq, agentID, projectPath, result, nil, true, contextLifecycle); persistErr != nil {
			lifecycleCause = persistErr
			s.logger.Error("ACP abort persist failed", slog.Any("error", persistErr), slog.String("session_id", req.ThreadID))
			switch s.resolveACPRoundPersistFailure(ctx, req, persistErr, cleanupProjectionsIn) {
			case acpRoundCommitted:
				// The round actually committed; nothing diverged.
				lifecycleCause = nil
				cleanupProjections()
			case acpRoundRolledBack:
				// The warm process advanced past canonical history. The next
				// prompt's head comparison would also catch this; closing now
				// just reclaims the process promptly. Safe here (and only
				// here): the run still holds the session's single active slot,
				// so no newer turn can own this session yet.
				if closer, ok := s.acpPool.(interface{ CloseSession(string) error }); ok {
					if closeErr := closer.CloseSession(req.ThreadID); closeErr != nil {
						s.logger.Warn("failed to discard ACP runtime after stop persistence failure",
							slog.String("session_id", req.ThreadID), slog.Any("error", closeErr))
					}
				}
				cleanupProjections()
			case acpRoundUnresolved:
				// Fail closed: the background reconciliation owns the cleanup.
			}
		} else {
			cleanupProjections()
		}
		emitWithContext(ctx, native.StreamEvent{Type: native.EventTextEnd})
		emitWithContext(ctx, acpTerminalStreamEvent(native.EventAbort, result))
		return nil
	}
	emit(native.StreamEvent{Type: native.EventTextEnd})
	if persistErr := s.persistACPRound(context.WithoutCancel(ctx), req, agentID, projectPath, result, nil, true, contextLifecycle); persistErr != nil {
		lifecycleCause = runtimeHistoryError(persistErr)
		s.logger.Error("ACP persist failed", slog.Any("error", persistErr), slog.String("session_id", req.ThreadID))
		switch s.resolveACPRoundPersistFailure(ctx, req, persistErr, cleanupProjectionsIn) {
		case acpRoundCommitted:
			// The round actually committed; the turn terminates cleanly.
			lifecycleCause = nil
			cleanupProjections()
			emit(acpTerminalStreamEvent(native.EventEnd, result))
			return nil
		case acpRoundUnresolved:
			return apperror.Wrap(apperror.CodeSessionHistoryInconsistent, persistErr, nil)
		case acpRoundRolledBack:
		}
		// Definite rollback: the canonical publication head did not move while
		// the warm process already contains this turn. Discard the runtime
		// promptly; even without this close, the next prompt's head check
		// would detect the divergence and restart from the durable head. Safe
		// here (and only here): the run still holds the session's single
		// active slot, so no newer turn can own this session yet.
		if closer, ok := s.acpPool.(interface{ CloseSession(string) error }); ok {
			if closeErr := closer.CloseSession(req.ThreadID); closeErr != nil {
				s.logger.Warn("failed to discard ACP runtime after history persistence failure",
					slog.String("session_id", req.ThreadID), slog.Any("error", closeErr))
			}
		}
		cleanupProjections()
		emit(acpRuntimeFailureEvent(lifecycleCause))
		emit(acpTerminalStreamEvent(native.EventAbort, result))
		return nil
	}
	cleanupProjections()
	emit(acpTerminalStreamEvent(native.EventEnd, result))
	return nil
}

func acpRuntimeFailureEvent(cause error) native.StreamEvent {
	code := string(apperror.CodeOf(cause))
	if strings.TrimSpace(code) == "" {
		code = "acp_runtime_prompt_failed"
	}
	return native.StreamEvent{Type: native.EventError, Error: code}
}

// acpRoundResolution classifies what actually happened to a round whose
// persistACPRound call returned an error.
type acpRoundResolution int

const (
	// acpRoundRolledBack: the round never committed - either the error class
	// guarantees it (the atomic round transaction commits whole or not at
	// all), or the database re-read proved it.
	acpRoundRolledBack acpRoundResolution = iota
	// acpRoundCommitted: the database proved the round committed despite the
	// lost acknowledgement.
	acpRoundCommitted
	// acpRoundUnresolved: the outcome stayed unknown. A bounded background
	// reconciliation keeps retrying and cleans the stream projections when it
	// resolves; callers must fail closed and touch nothing themselves.
	acpRoundUnresolved
)

// resolveACPRoundPersistFailure applies one rule to every terminal branch of
// an ACP turn whose round persistence failed. The eagerly persisted leading
// user message is never deleted here or by any caller: the user watched their
// message send, a visible message must never vanish, and keeping it can never
// corrupt anything - whereas deleting it after a misclassified rollback would
// destroy a committed turn. An unanswered user message simply reads as a
// failed turn to retry.
func (s *Service) resolveACPRoundPersistFailure(
	ctx context.Context,
	req ChatRequest,
	persistErr error,
	cleanupProjectionsIn func(context.Context),
) acpRoundResolution {
	if !errors.Is(persistErr, db.ErrCommitOutcomeUnknown) {
		return acpRoundRolledBack
	}
	outcome, reconcileErr := s.reconcileACPRoundOutcome(context.WithoutCancel(ctx), req)
	if reconcileErr == nil {
		if outcome == "" {
			return acpRoundRolledBack
		}
		return acpRoundCommitted
	}
	s.logger.Error("failed to reconcile uncertain ACP round",
		slog.String("session_id", req.ThreadID), slog.String("run_id", req.RunID), slog.Any("error", reconcileErr))
	s.retryACPRoundOutcome(context.WithoutCancel(ctx), req, func(reconcileCtx context.Context, _ string) {
		cleanupProjectionsIn(reconcileCtx)
	})
	return acpRoundUnresolved
}

type acpRoundReconcileTx interface {
	InTx(context.Context, func(dbstore.Queries) error) error
	SupportsTransactions() bool
}

func (s *Service) reconcileACPRoundOutcome(ctx context.Context, req ChatRequest) (string, error) {
	if s == nil || s.queries == nil {
		return "", errors.New("ACP round reconciliation store is unavailable")
	}
	botID, err := db.ParseUUID(strings.TrimSpace(req.BotID))
	if err != nil {
		return "", err
	}
	sessionID, err := db.ParseUUID(strings.TrimSpace(req.ThreadID))
	if err != nil {
		return "", err
	}
	runID, err := db.ParseUUID(strings.TrimSpace(req.RunID))
	if err != nil {
		return "", err
	}
	txer, ok := s.queries.(acpRoundReconcileTx)
	if !ok || !txer.SupportsTransactions() {
		return "", errors.New("ACP round reconciliation requires transactions")
	}
	var outcome string
	readCompleted := false
	err = txer.InTx(ctx, func(queries dbstore.Queries) error {
		// This first statement waits for the old backend's COMMIT/ROLLBACK. The
		// outcome read stays a second statement so READ COMMITTED takes a fresh
		// snapshot after the wait completes.
		if _, lockErr := queries.LockSessionForCommitReconciliation(ctx, sqlc.LockSessionForCommitReconciliationParams{
			SessionID: sessionID, BotID: botID,
		}); errors.Is(lockErr, pgx.ErrNoRows) {
			readCompleted = true
			return nil
		} else if lockErr != nil {
			return lockErr
		}
		value, readErr := queries.GetACPRoundOutcome(ctx, sqlc.GetACPRoundOutcomeParams{
			BotID: botID, SessionID: sessionID, RunID: runID,
		})
		if errors.Is(readErr, pgx.ErrNoRows) {
			readCompleted = true
			return nil
		}
		if readErr != nil {
			return readErr
		}
		outcome = value
		readCompleted = true
		return nil
	})
	// Once the second READ COMMITTED statement completed, the old writer was
	// already known to have committed or rolled back. Losing the acknowledgement
	// for this read-only reconciliation transaction cannot change that fact.
	if readCompleted {
		return outcome, nil
	}
	if err != nil {
		return "", err
	}
	return outcome, nil
}

// acpRoundReconcileBudget bounds the background reconciliation of one
// uncertain round. Giving up is safe: reconciliation only performs cleanup
// hygiene, while correctness is enforced by the pre-prompt durable-head
// comparison regardless of whether this loop ever resolves.
const acpRoundReconcileBudget = 15 * time.Minute

func (s *Service) retryACPRoundOutcome(ctx context.Context, req ChatRequest, resolved func(context.Context, string)) {
	retryCtx, cancelRetry := context.WithTimeout(context.WithoutCancel(ctx), acpRoundReconcileBudget)
	go func() {
		defer cancelRetry()
		backoff := 100 * time.Millisecond
		timer := time.NewTimer(backoff)
		defer timer.Stop()
		for {
			attemptCtx, cancel := context.WithTimeout(retryCtx, 30*time.Second)
			outcome, err := s.reconcileACPRoundOutcome(attemptCtx, req)
			cancel()
			if err == nil {
				resolved(retryCtx, outcome)
				return
			}
			if retryCtx.Err() != nil {
				s.logger.Error("abandoning uncertain ACP round reconciliation after budget",
					slog.String("session_id", req.ThreadID), slog.String("run_id", req.RunID), slog.Any("error", err))
				return
			}
			s.logger.Error("retrying uncertain ACP round reconciliation",
				slog.String("session_id", req.ThreadID), slog.String("run_id", req.RunID), slog.Any("error", err))
			timer.Reset(backoff)
			select {
			case <-retryCtx.Done():
				s.logger.Error("abandoning uncertain ACP round reconciliation after budget",
					slog.String("session_id", req.ThreadID), slog.String("run_id", req.RunID))
				return
			case <-timer.C:
			}
			if backoff < 30*time.Second {
				backoff *= 2
				if backoff > 30*time.Second {
					backoff = 30 * time.Second
				}
			}
		}
	}()
}

func (s *Service) reconcileACPLeadingUserMessage(ctx context.Context, req ChatRequest) (string, error) {
	if s == nil || s.queries == nil {
		return "", errors.New("ACP leading-user reconciliation store is unavailable")
	}
	botID, err := db.ParseUUID(strings.TrimSpace(req.BotID))
	if err != nil {
		return "", err
	}
	sessionID, err := db.ParseUUID(strings.TrimSpace(req.ThreadID))
	if err != nil {
		return "", err
	}
	runID, err := db.ParseUUID(strings.TrimSpace(req.RunID))
	if err != nil {
		return "", err
	}
	turnID, err := db.ParseUUID(strings.TrimSpace(req.TurnID))
	if err != nil {
		return "", err
	}
	txer, ok := s.queries.(acpRoundReconcileTx)
	if !ok || !txer.SupportsTransactions() {
		return "", errors.New("ACP leading-user reconciliation requires transactions")
	}
	var messageID string
	readCompleted := false
	err = txer.InTx(ctx, func(queries dbstore.Queries) error {
		if _, lockErr := queries.LockSessionForCommitReconciliation(ctx, sqlc.LockSessionForCommitReconciliationParams{
			SessionID: sessionID, BotID: botID,
		}); errors.Is(lockErr, pgx.ErrNoRows) {
			readCompleted = true
			return nil
		} else if lockErr != nil {
			return lockErr
		}
		id, readErr := queries.GetACPLeadingUserMessageID(ctx, sqlc.GetACPLeadingUserMessageIDParams{
			BotID: botID, SessionID: sessionID, RunID: runID, TurnID: turnID,
		})
		if errors.Is(readErr, pgx.ErrNoRows) {
			readCompleted = true
			return nil
		}
		if readErr != nil {
			return readErr
		}
		messageID = id.String()
		readCompleted = true
		return nil
	})
	if readCompleted {
		return messageID, nil
	}
	return "", err
}

func (s *Service) cleanupUncertainACPLeadingUser(ctx context.Context, req ChatRequest) {
	backoff := 100 * time.Millisecond
	for {
		attemptCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		messageID, err := s.reconcileACPLeadingUserMessage(attemptCtx, req)
		cancel()
		if err == nil {
			if messageID != "" {
				s.cleanupReplacementMessages(ctx, []messagepkg.Message{{ID: messageID}})
			}
			return
		}
		s.logger.Error("retrying uncertain ACP leading-user cleanup",
			slog.String("session_id", req.ThreadID), slog.String("run_id", req.RunID), slog.Any("error", err))
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if backoff < 30*time.Second {
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}
}

func (s *Service) cleanupACPDecisionProjections(ctx context.Context, req ChatRequest) {
	if s == nil || s.queries == nil {
		return
	}
	botID, err := db.ParseUUID(strings.TrimSpace(req.BotID))
	if err != nil {
		return
	}
	sessionID, err := db.ParseUUID(strings.TrimSpace(req.ThreadID))
	if err != nil {
		return
	}
	runID, err := db.ParseUUID(strings.TrimSpace(req.RunID))
	if err != nil {
		return
	}
	if _, err := s.queries.DeleteACPDecisionProjectionsByRun(ctx, sqlc.DeleteACPDecisionProjectionsByRunParams{
		BotID: botID, SessionID: sessionID, RunID: runID,
	}); err != nil {
		s.logger.Warn("cleanup ACP decision projections by run failed",
			slog.String("session_id", req.ThreadID), slog.String("run_id", req.RunID), slog.Any("error", err))
	}
}

func (s *Service) prepareACPAttachments(ctx context.Context, req ChatRequest) (acpPreparedAttachments, error) {
	prepared := s.prepareGatewayAttachments(ctx, req)
	result := acpPreparedAttachments{
		Images:                   make([]acpclient.PromptImage, 0, len(prepared)),
		Context:                  make([]ChatAttachment, 0, len(prepared)),
		References:               make([]string, 0, len(prepared)),
		CanFallbackImagesToFiles: true,
	}
	for i, item := range prepared {
		attachmentType := strings.ToLower(strings.TrimSpace(item.Type))
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = fmt.Sprintf("attachment %d", i+1)
		}

		contextAttachment := ChatAttachment{
			Type:        attachmentType,
			ContentHash: strings.TrimSpace(item.ContentHash),
			Name:        strings.TrimSpace(item.Name),
			Mime:        attachmentpkg.NormalizeMime(item.Mime),
			Size:        item.Size,
			Metadata:    item.Metadata,
		}
		reference := strings.TrimSpace(item.FallbackPath)
		if reference == "" && item.Transport == gatewayTransportPublicURL {
			reference = strings.TrimSpace(item.Payload)
		}
		if reference != "" {
			if isLikelyPublicURL(reference) {
				contextAttachment.URL = reference
			} else {
				contextAttachment.Path = reference
			}
			result.References = append(result.References, reference)
		}

		if attachmentType == "image" && item.Transport == gatewayTransportInlineDataURL && strings.TrimSpace(item.Payload) != "" {
			image, imageErr := acpPromptImageFromDataURL(item.Payload, item.Mime)
			if imageErr != nil {
				return acpPreparedAttachments{}, acpfeedback.New(
					acpfeedback.CodeAttachmentInvalid,
					"invalid_image_data",
					http.StatusBadRequest,
					"chat.acp.attachmentInvalid",
					"The attachment is invalid. Please attach it again.",
					map[string]string{"name": name},
				)
			}
			result.Images = append(result.Images, image)
			if reference == "" {
				result.CanFallbackImagesToFiles = false
			}
		} else if reference == "" {
			return acpPreparedAttachments{}, acpfeedback.New(
				acpfeedback.CodeAttachmentUnavailable,
				"attachment_not_reachable",
				http.StatusBadRequest,
				"chat.acp.attachmentUnavailable",
				"The attachment could not be made available to the external agent. Please attach it again.",
				map[string]string{"name": name},
			)
		}

		result.Context = append(result.Context, contextAttachment)
	}
	return result, nil
}

func acpPromptImageFromDataURL(payload, fallbackMime string) (acpclient.PromptImage, error) {
	payload = strings.TrimSpace(payload)
	comma := strings.Index(payload, ",")
	if comma < 0 || !strings.HasPrefix(strings.ToLower(payload), "data:") ||
		!strings.Contains(strings.ToLower(payload[:comma]), ";base64") {
		return acpclient.PromptImage{}, acpclient.ErrInvalidPromptImage
	}
	mimeType := attachmentpkg.MimeFromDataURL(payload)
	if mimeType == "" || mimeType == "application/octet-stream" {
		mimeType = attachmentpkg.NormalizeMime(fallbackMime)
	}
	normalized, err := acpclient.NormalizePromptImages([]acpclient.PromptImage{{
		Data:     strings.TrimSpace(payload[comma+1:]),
		MimeType: mimeType,
	}})
	if err != nil {
		return acpclient.PromptImage{}, err
	}
	return normalized[0], nil
}

func acpPromptInputFeedback(err error) *acpfeedback.Error {
	switch {
	case errors.Is(err, acpagent.ErrAgentCommandUnavailable):
		// The runtime that admission matched was replaced (or updated its
		// command set) before the prompt; the turn fails closed exactly like
		// admission would have.
		return acpfeedback.New(
			acpfeedback.CodeAgentCommandStale,
			"agent_command_stale",
			http.StatusConflict,
			"chat.acp.agentCommandStale",
			"The agent no longer offers this command. Reopen the command picker and try again.",
			nil,
		)
	case errors.Is(err, acpclient.ErrImagePromptUnsupported):
		return acpfeedback.New(
			acpfeedback.CodeImageInputUnsupported,
			"image_input_unsupported",
			http.StatusBadRequest,
			"chat.acp.imageInputUnsupported",
			"This external agent cannot read the attached image.",
			nil,
		)
	case errors.Is(err, acpclient.ErrInvalidPromptImage):
		return acpfeedback.New(
			acpfeedback.CodeAttachmentInvalid,
			"invalid_image_data",
			http.StatusBadRequest,
			"chat.acp.attachmentInvalid",
			"The attachment is invalid. Please attach it again.",
			nil,
		)
	default:
		return nil
	}
}

func acpPromptConfigAppError(err error) error {
	switch {
	case errors.Is(err, acpclient.ErrModelSelectionUnsupported):
		return apperror.New(apperror.CodeACPModelSelectionUnsupported, nil)
	case errors.Is(err, acpclient.ErrModelIDRequired):
		return apperror.New(apperror.CodeACPModelIDRequired, nil)
	case errors.Is(err, acpclient.ErrModelUnavailable):
		return apperror.New(apperror.CodeACPModelUnavailable, nil)
	case errors.Is(err, acpclient.ErrReasoningSelectionUnsupported):
		return apperror.New(apperror.CodeACPReasoningUnsupported, nil)
	case errors.Is(err, acpclient.ErrReasoningEffortRequired):
		return apperror.New(apperror.CodeACPReasoningEffortRequired, nil)
	case errors.Is(err, acpclient.ErrReasoningEffortUnavailable):
		return apperror.New(apperror.CodeACPReasoningUnavailable, nil)
	case errors.Is(err, acpagent.ErrRuntimeConfigUpdateFailed):
		return apperror.Wrap(apperror.CodeACPConfigUpdateFailed, err, nil)
	default:
		return nil
	}
}

func ensureACPPromptOutput(result acpclient.PromptResult) acpclient.PromptResult {
	if len(result.Output) == 0 {
		result.Output = acpclient.TranscriptFromEvents(result.Events, result.Text)
	}
	return result
}

func acpTerminalStreamEvent(eventType native.StreamEventType, result acpclient.PromptResult) native.StreamEvent {
	result = ensureACPPromptOutput(result)
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

func validateSessionBot(botID, sessionID, sessionBotID string) error {
	bid := strings.TrimSpace(botID)
	sid := strings.TrimSpace(sessionID)
	sb := strings.TrimSpace(sessionBotID)
	if bid == "" || sb == "" || bid == sb {
		return nil
	}
	return fmt.Errorf("session %s belongs to bot %s, not %s", sid, sb, bid)
}

func (s *Service) requireACPRuntimeOwnerWorkspaceExec(ctx context.Context, botID, runtimeOwnerAccountID string) error {
	if s == nil || s.botPermissions == nil {
		return errors.New("bot permission checker not configured")
	}
	runtimeOwnerAccountID = strings.TrimSpace(runtimeOwnerAccountID)
	if runtimeOwnerAccountID == "" {
		return acpfeedback.New(
			acpfeedback.CodeRuntimeOwnerMissing,
			"missing_runtime_owner",
			409,
			"chat.acp.runtimeOwnerMissing",
			"ACP runtime owner is missing; recreate or reauthorize the ACP session",
			nil,
		)
	}
	ok, err := s.botPermissions.HasBotPermission(ctx, strings.TrimSpace(botID), runtimeOwnerAccountID, bots.PermissionWorkspaceExec)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	return acpfeedback.New(
		acpfeedback.CodeNoWorkspaceExec,
		"missing_workspace_exec",
		403,
		"chat.acp.missingWorkspaceExec",
		"ACP runtime owner no longer has workspace execution permission for this bot.",
		nil,
	)
}

func mergeACPRuntimeMetadata(metadata, runtimeMetadata map[string]any) map[string]any {
	out := make(map[string]any, len(metadata)+len(runtimeMetadata))
	for key, value := range metadata {
		out[key] = value
	}
	for _, key := range []string{"acp_agent_id", "project_path", "acp_project_mode", "runtime_owner_account_id"} {
		if value, ok := runtimeMetadata[key]; ok {
			out[key] = value
		}
	}
	return out
}

func (s *Service) streamACPAgentChunks(ctx context.Context, req ChatRequest, chunkCh chan<- StreamChunk, errCh chan<- error) {
	eventCh := make(chan WSStreamEvent)
	done := make(chan error, 1)
	go func() {
		defer close(eventCh)
		done <- s.streamACPAgentWS(ctx, req, eventCh, nil)
		close(done)
	}()
	for eventCh != nil || done != nil {
		select {
		case event, ok := <-eventCh:
			if !ok {
				eventCh = nil
				continue
			}
			select {
			case chunkCh <- event:
			case <-ctx.Done():
				errCh <- ctx.Err()
				return
			}
		case err, ok := <-done:
			if !ok {
				done = nil
				continue
			}
			if err != nil {
				errCh <- err
			}
		case <-ctx.Done():
			errCh <- ctx.Err()
			return
		}
	}
}

func isACPDecisionProjectionEvent(ev native.StreamEvent) bool {
	switch ev.Type {
	case native.EventUserInputRequest, native.EventToolApprovalRequest:
		return strings.TrimSpace(ev.ToolCallID) != ""
	default:
		return false
	}
}

func (s *Service) persistACPLeadingUserMessage(ctx context.Context, req ChatRequest) (ChatRequest, *messagepkg.Message, error) {
	if req.UserMessagePersisted || s == nil || s.messageService == nil || strings.TrimSpace(req.BotID) == "" {
		return req, nil, nil
	}
	displayText := strings.TrimSpace(req.RawQuery)
	if displayText == "" {
		displayText = strings.TrimSpace(req.Query)
	}
	if displayText == "" && len(req.Attachments) == 0 {
		return req, nil, nil
	}
	contentText := strings.TrimSpace(req.Query)
	if contentText == "" {
		contentText = displayText
	}
	content, err := json.Marshal(ModelMessage{
		Role:    "user",
		Content: newTextContent(contentText),
	})
	if err != nil {
		s.logger.Warn("persist ACP leading user message: marshal failed", slog.Any("error", err))
		return req, nil, nil
	}
	senderChannelIdentityID, senderUserID := s.resolvePersistSenderIDs(ctx, req)
	sessionMode, runtimeType := s.persistSessionRuntimeSnapshot(ctx, req)
	persisted, err := s.messageService.Persist(ctx, messagepkg.PersistInput{
		BotID:                   req.BotID,
		SessionID:               req.ThreadID,
		SenderChannelIdentityID: senderChannelIdentityID,
		SenderUserID:            senderUserID,
		ExternalMessageID:       req.ExternalMessageID,
		SourceReplyToMessageID:  req.SourceReplyToMessageID,
		Role:                    "user",
		Content:                 content,
		Metadata:                mergeMetadata(buildRouteMetadata(req), buildInteractionMetadata(req)),
		Assets:                  chatAttachmentsToAssetRefs(req.Attachments),
		EventID:                 req.EventID,
		DisplayText:             displayText,
		SessionMode:             sessionMode,
		RuntimeType:             runtimeType,
		RunID:                   req.RunID,
		TurnID:                  req.TurnID,
		TurnPosition:            req.TurnPosition,
	})
	if err != nil {
		s.logger.Warn("persist ACP leading user message failed",
			slog.String("bot_id", req.BotID),
			slog.String("session_id", req.ThreadID),
			slog.Any("error", err))
		if !errors.Is(err, db.ErrCommitOutcomeUnknown) {
			return req, nil, nil
		}
		messageID, reconcileErr := s.reconcileACPLeadingUserMessage(ctx, req)
		if reconcileErr != nil {
			// No native prompt has run yet, so fail closed. A background cleanup
			// removes the eager row if the lost acknowledgement was in fact a
			// commit; retrying the prompt cannot then collide with an orphan.
			go s.cleanupUncertainACPLeadingUser(context.WithoutCancel(ctx), req)
			return req, nil, fmt.Errorf("reconcile uncertain ACP leading user message: %w", reconcileErr)
		}
		if messageID == "" {
			// The lock+fresh read proved rollback. Let the final atomic round
			// persist user and assistant together.
			return req, nil, nil
		}
		persisted = messagepkg.Message{
			ID: messageID, BotID: req.BotID, SessionID: req.ThreadID,
			Role: "user", Content: content, DisplayContent: displayText,
		}
	}
	req.UserMessagePersisted = true
	req.PersistedUserMessageID = persisted.ID
	return req, &persisted, nil
}

func (s *Service) persistACPDecisionProjection(ctx context.Context, req ChatRequest, ev native.StreamEvent) *messagepkg.Message {
	if s == nil || s.messageService == nil || strings.TrimSpace(req.BotID) == "" || strings.TrimSpace(req.ThreadID) == "" {
		return nil
	}
	output := sdkMessagesToModelMessages(acpclient.TranscriptFromEvents([]event.StreamEvent{ev}, ""))
	sessionMode, runtimeType := s.persistSessionRuntimeSnapshot(ctx, req)
	for _, msg := range output {
		if msg.Role != "assistant" {
			continue
		}
		content, err := json.Marshal(msg)
		if err != nil {
			s.logger.Warn("persist ACP decision projection: marshal failed",
				slog.String("tool_call_id", ev.ToolCallID),
				slog.Any("error", err))
			return nil
		}
		metadata := cloneMetadataMap(buildRouteMetadata(req))
		metadata["acp_decision_projection"] = true
		metadata["acp_decision_tool_call_id"] = strings.TrimSpace(ev.ToolCallID)
		persisted, err := s.messageService.Persist(ctx, messagepkg.PersistInput{
			BotID:                   req.BotID,
			SessionID:               req.ThreadID,
			SenderChannelIdentityID: "",
			Role:                    "assistant",
			Content:                 content,
			Metadata:                metadata,
			SessionMode:             sessionMode,
			RuntimeType:             runtimeType,
			TurnRequestMessageID:    req.PersistedUserMessageID,
			RunID:                   req.RunID,
		})
		if err != nil {
			s.logger.Warn("persist ACP decision projection failed",
				slog.String("bot_id", req.BotID),
				slog.String("session_id", req.ThreadID),
				slog.String("tool_call_id", ev.ToolCallID),
				slog.Any("error", err))
			return nil
		}
		return &persisted
	}
	return nil
}

// cancelPendingACPApprovals closes the residual approval window when a turn
// dies abnormally: any pending row for the session belonged to that turn (the
// pool's turn slot guarantees one turn per session), and its waiter is gone -
// left pending, the persisted card would stay actionable forever and a late
// approve would flip a row nobody executes.
func (s *Service) cancelPendingACPApprovals(ctx context.Context, req ChatRequest, reason string) {
	if s == nil || s.toolApproval == nil {
		return
	}
	cancelled, err := s.toolApproval.CancelPendingForSession(ctx, req.BotID, req.ThreadID, reason)
	if err != nil {
		s.logger.Warn("cancel pending ACP approvals failed",
			slog.String("bot_id", req.BotID),
			slog.String("session_id", req.ThreadID),
			slog.Any("error", err))
		return
	}
	if len(cancelled) > 0 {
		s.logger.Info("cancelled pending ACP approvals with their turn",
			slog.String("session_id", req.ThreadID),
			slog.Int("count", len(cancelled)))
	}
}

// persistACPRound persists the round's messages and, when the native runtime
// actually advanced (turnCompleted), moves the session's canonical ACP
// publication head in the same transaction: a staged snapshot publishes a
// resumable checkpoint, an unstaged completion publishes an explicit reset. A
// user-aborted turn (turnCompleted=false) keeps the previous head so the warm
// runtime stays reusable and a cold start resumes the last complete turn.
func (s *Service) persistACPRound(
	ctx context.Context,
	req ChatRequest,
	agentID, projectPath string,
	result acpclient.PromptResult,
	promptErr error,
	turnCompleted bool,
	contextLifecycle *contextfrag.LifecycleHolder,
) error {
	meta := map[string]any{
		"acp_agent_id": agentID,
		"project_path": projectPath,
		"stop_reason":  result.StopReason,
	}
	if promptErr != nil {
		meta["acp_turn_outcome"] = "failed"
		meta["error"] = acpUserFacingFailureMessage(promptErr)
		var feedbackErr *acpfeedback.Error
		if errors.As(promptErr, &feedbackErr) {
			meta["error_code"] = feedbackErr.Code
			meta["error_reason"] = feedbackErr.Reason
			meta["i18n_key"] = feedbackErr.I18nKey
		} else {
			meta["error_code"] = "acp_runtime_prompt_failed"
		}
	}
	// result.Output is already assembled by the ACP client; the application only
	// converts and stores it.
	output := sdkMessagesToModelMessages(result.Output)
	if len(output) == 0 {
		output = []ModelMessage{{Role: "assistant", Content: newTextContent("")}}
	}
	// Normalize the transcript before assigning metadata indexes. The generic
	// store path repairs unclosed tool calls by inserting synthetic tool rows;
	// assigning indexes first can therefore put the checkpoint publication on
	// the inserted tool row instead of the terminal assistant.
	output = repairToolCallClosures(output, syntheticToolClosureError)
	hasAssistant := false
	for _, msg := range output {
		if msg.Role == "assistant" {
			hasAssistant = true
			break
		}
	}
	if !hasAssistant {
		return errors.New("ACP transcript has no assistant message to publish")
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
		// A user-aborted partial round commits as "aborted", not "succeeded":
		// commit-unknown reconciliation needs a durable marker to tell a
		// committed abort from a rollback, and the round genuinely did not
		// complete.
		if turnCompleted {
			outcome["acp_turn_outcome"] = "succeeded"
		} else {
			outcome["acp_turn_outcome"] = "aborted"
		}
		metadataByIndex[lastAssistantIndex+metadataOffset] = outcome
	}
	var publication *messagepkg.ACPPublication
	if promptErr == nil && turnCompleted && lastAssistantIndex >= 0 {
		publication = &messagepkg.ACPPublication{
			RunID:           req.RunID,
			CheckpointReset: !result.CheckpointStaged,
		}
	}
	skipMemory := promptErr != nil || req.UserMessagePersisted || req.ReusePersistedUserMessage || req.SkipMemoryExtraction
	persisted, err := s.storeRoundWithOptionsResult(ctx, req, round, "", storeRoundOptions{
		AllowPendingToolCalls:         true,
		SkipMemory:                    skipMemory,
		AllowEmptyAssistantText:       true,
		MessageMetadataByIndex:        metadataByIndex,
		RequireCompletePersist:        true,
		CleanupACPDecisionProjections: true,
		ACPPublication:                publication,
		ContextLifecycle:              contextLifecycle,
	})
	if err == nil && lastPersistedAssistantMessageID(persisted) == "" {
		// This assertion fires AFTER a committed transaction, so it must never
		// route into the definite-rollback compensation (which would delete
		// the committed round's user row and discard a consistent runtime).
		// Joining the commit-unknown sentinel sends every caller through the
		// database reconciliation path instead: the round is re-read and, being
		// committed, resolves cleanly.
		err = errors.Join(db.ErrCommitOutcomeUnknown, errors.New("ACP assistant output was not persisted"))
	}
	if err == nil && promptErr == nil && (req.UserMessagePersisted || req.ReusePersistedUserMessage) && !req.SkipMemoryExtraction {
		go s.storeMemory(context.WithoutCancel(ctx), req, persisted)
	}
	return err
}

// acpFailureResult appends a short, sanitized failure marker to the partial
// result. Detailed upstream errors can include local paths or auth file names,
// so they stay in logs instead of user-visible chat history.
func acpFailureResult(result acpclient.PromptResult, err error) (acpclient.PromptResult, string) {
	message := acpUserFacingFailureMessage(err)
	if message == "" {
		return result, ""
	}
	if strings.TrimSpace(result.Text) != "" {
		delta := "\n\n" + message
		result.Text = strings.TrimSpace(result.Text + delta)
		result.Events = append(result.Events, event.StreamEvent{Type: event.TextDelta, Delta: delta})
		result.Output = acpclient.AppendTranscriptText(result.Output, message)
		return result, delta
	}
	result.Text = message
	result.Events = append(result.Events, event.StreamEvent{Type: event.TextDelta, Delta: message})
	result.Output = acpclient.AppendTranscriptText(result.Output, message)
	return result, message
}

func acpUserFacingFailureMessage(err error) string {
	if err == nil {
		return ""
	}
	var feedback *acpfeedback.Error
	if errors.As(err, &feedback) {
		return strings.TrimSpace(feedback.Message)
	}
	return "ACP agent failed to complete the turn. Please retry."
}

func metadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}
