package application

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	agentfeedback "github.com/felinics/memoh/internal/agent/decision/feedback"
	"github.com/felinics/memoh/internal/agent/event"
	acpagent "github.com/felinics/memoh/internal/agent/runtime/acp"
	acpclient "github.com/felinics/memoh/internal/agent/runtime/acp/client"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	attachmentpkg "github.com/felinics/memoh/internal/attachment"
	"github.com/felinics/memoh/internal/bots"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// runtimeSinkStallTimeout bounds how long a live-turn event delivery may wait on
// a WS consumer that is connected but not draining. Reaching it cancels the
// stream (client-disconnect semantics) so the adapter callback — and the
// prompt's runtime slot behind it — cannot stall indefinitely.
const runtimeSinkStallTimeout = 30 * time.Second

type acpPrompter interface {
	Prompt(ctx context.Context, input acpagent.PromptInput) (acpclient.PromptResult, error)
}

type runtimePreparedAttachments struct {
	Images                   []external.Image
	Context                  []ChatAttachment
	References               []string
	CanFallbackImagesToFiles bool
}

type RuntimeSessionExecutionInfo struct {
	RequiresWorkspaceExec bool
	BotID                 string
	RuntimeOwnerAccountID string
}

func (s *Service) SetACPSessionPool(pool acpPrompter) {
	s.acpPool = pool
	s.SetExternalRuntimes(acpagent.NewDriver(pool))
}

func (s *Service) RuntimeSessionExecutionInfo(ctx context.Context, sessionID string) (RuntimeSessionExecutionInfo, error) {
	if s == nil || s.sessionService == nil || strings.TrimSpace(sessionID) == "" {
		return RuntimeSessionExecutionInfo{}, nil
	}
	sess, err := s.sessionService.Get(ctx, sessionID)
	if err != nil {
		return RuntimeSessionExecutionInfo{}, err
	}
	// Direct external runtimes execute in the workspace exactly like ACP
	// agents; the WS execution gate must cover both, or a
	// session reached by other means (a fork, a shared session) executes
	// with only the inherited runtime owner ever checked.
	if !session.IsACPRuntime(sess) && !session.IsDirectRuntime(sess) {
		return RuntimeSessionExecutionInfo{}, nil
	}
	runtimeMeta := runtimeSessionMeta(sess)
	return RuntimeSessionExecutionInfo{
		RequiresWorkspaceExec: true,
		BotID:                 sess.BotID,
		RuntimeOwnerAccountID: metadataString(runtimeMeta, "runtime_owner_account_id"),
	}, nil
}

// runtimeRoundResolution classifies what actually happened to a round whose
// persistence call returned an error.
type runtimeRoundResolution int

const (
	// runtimeRoundRolledBack: the round never committed - either the error class
	// guarantees it (the atomic round transaction commits whole or not at
	// all), or the database re-read proved it.
	runtimeRoundRolledBack runtimeRoundResolution = iota
	// runtimeRoundCommitted: the database proved the round committed despite the
	// lost acknowledgement.
	runtimeRoundCommitted
	// runtimeRoundUnresolved: the outcome stayed unknown. A bounded background
	// reconciliation keeps retrying and cleans the stream projections when it
	// resolves; callers must fail closed and touch nothing themselves.
	runtimeRoundUnresolved
)

// resolveRuntimeRoundPersistFailure applies one rule to every terminal branch of
// an External Agent turn whose round persistence failed. The eagerly persisted leading
// user message is never deleted here or by any caller: the user watched their
// message send, a visible message must never vanish, and keeping it can never
// corrupt anything - whereas deleting it after a misclassified rollback would
// destroy a committed turn. An unanswered user message simply reads as a
// failed turn to retry.
func (s *Service) resolveRuntimeRoundPersistFailure(
	ctx context.Context,
	req ChatRequest,
	persistErr error,
	cleanupProjectionsIn func(context.Context),
) runtimeRoundResolution {
	if !errors.Is(persistErr, db.ErrCommitOutcomeUnknown) {
		return runtimeRoundRolledBack
	}
	outcome, reconcileErr := s.reconcileRuntimeRoundOutcome(context.WithoutCancel(ctx), req)
	if reconcileErr == nil {
		if outcome == "" {
			return runtimeRoundRolledBack
		}
		return runtimeRoundCommitted
	}
	s.logger.Error("failed to reconcile uncertain External Agent round",
		slog.String("session_id", req.ThreadID), slog.String("run_id", req.RunID), slog.Any("error", reconcileErr))
	s.retryRuntimeRoundOutcome(context.WithoutCancel(ctx), req, func(reconcileCtx context.Context, _ string) {
		cleanupProjectionsIn(reconcileCtx)
	})
	return runtimeRoundUnresolved
}

type runtimeRoundReconcileTx interface {
	InTx(context.Context, func(dbstore.Queries) error) error
	SupportsTransactions() bool
}

func (s *Service) reconcileRuntimeRoundOutcome(ctx context.Context, req ChatRequest) (string, error) {
	if s == nil || s.queries == nil {
		return "", errors.New("external agent round reconciliation store is unavailable")
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
	txer, ok := s.queries.(runtimeRoundReconcileTx)
	if !ok || !txer.SupportsTransactions() {
		return "", errors.New("external agent round reconciliation requires transactions")
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
		value, readErr := queries.GetRuntimeRoundOutcome(ctx, sqlc.GetRuntimeRoundOutcomeParams{
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

// runtimeRoundReconcileBudget bounds the background reconciliation of one
// uncertain round. Giving up is safe: reconciliation only performs cleanup
// hygiene, while correctness is enforced by the pre-prompt durable-head
// comparison regardless of whether this loop ever resolves.
const runtimeRoundReconcileBudget = 15 * time.Minute

func (s *Service) retryRuntimeRoundOutcome(ctx context.Context, req ChatRequest, resolved func(context.Context, string)) {
	retryCtx, cancelRetry := context.WithTimeout(context.WithoutCancel(ctx), runtimeRoundReconcileBudget)
	go func() {
		defer cancelRetry()
		backoff := 100 * time.Millisecond
		timer := time.NewTimer(backoff)
		defer timer.Stop()
		for {
			attemptCtx, cancel := context.WithTimeout(retryCtx, 30*time.Second)
			outcome, err := s.reconcileRuntimeRoundOutcome(attemptCtx, req)
			cancel()
			if err == nil {
				resolved(retryCtx, outcome)
				return
			}
			if retryCtx.Err() != nil {
				s.logger.Error("abandoning uncertain External Agent round reconciliation after budget",
					slog.String("session_id", req.ThreadID), slog.String("run_id", req.RunID), slog.Any("error", err))
				return
			}
			s.logger.Error("retrying uncertain External Agent round reconciliation",
				slog.String("session_id", req.ThreadID), slog.String("run_id", req.RunID), slog.Any("error", err))
			timer.Reset(backoff)
			select {
			case <-retryCtx.Done():
				s.logger.Error("abandoning uncertain External Agent round reconciliation after budget",
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

func (s *Service) reconcileRuntimeLeadingUserMessage(ctx context.Context, req ChatRequest) (string, error) {
	if s == nil || s.queries == nil {
		return "", errors.New("external agent leading-user reconciliation store is unavailable")
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
	txer, ok := s.queries.(runtimeRoundReconcileTx)
	if !ok || !txer.SupportsTransactions() {
		return "", errors.New("external agent leading-user reconciliation requires transactions")
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
		id, readErr := queries.GetRuntimeLeadingUserMessageID(ctx, sqlc.GetRuntimeLeadingUserMessageIDParams{
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

func (s *Service) cleanupUncertainRuntimeLeadingUser(ctx context.Context, req ChatRequest) {
	backoff := 100 * time.Millisecond
	for {
		attemptCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		messageID, err := s.reconcileRuntimeLeadingUserMessage(attemptCtx, req)
		cancel()
		if err == nil {
			if messageID != "" {
				s.cleanupReplacementMessages(ctx, []messagepkg.Message{{ID: messageID}})
			}
			return
		}
		s.logger.Error("retrying uncertain External Agent leading-user cleanup",
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

func (s *Service) cleanupRuntimeDecisionProjectionRows(ctx context.Context, req ChatRequest) {
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
	if _, err := s.queries.DeleteRuntimeDecisionProjectionsByRun(ctx, sqlc.DeleteRuntimeDecisionProjectionsByRunParams{
		BotID: botID, SessionID: sessionID, RunID: runID,
	}); err != nil {
		s.logger.Warn("cleanup External Agent decision projections by run failed",
			slog.String("session_id", req.ThreadID), slog.String("run_id", req.RunID), slog.Any("error", err))
	}
}

func (s *Service) prepareRuntimeAttachments(ctx context.Context, req ChatRequest) (runtimePreparedAttachments, error) {
	prepared := s.prepareGatewayAttachments(ctx, req)
	result := runtimePreparedAttachments{
		Images:                   make([]external.Image, 0, len(prepared)),
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
			image, imageErr := runtimePromptImageFromDataURL(item.Payload, item.Mime)
			if imageErr != nil {
				return runtimePreparedAttachments{}, agentfeedback.New(
					agentfeedback.CodeAttachmentInvalid,
					"invalid_image_data",
					http.StatusBadRequest,
					"chat.externalAgent.attachmentInvalid",
					"The attachment is invalid. Please attach it again.",
					map[string]string{"name": name},
				)
			}
			result.Images = append(result.Images, image)
			if reference == "" {
				result.CanFallbackImagesToFiles = false
			}
		} else if reference == "" {
			return runtimePreparedAttachments{}, agentfeedback.New(
				agentfeedback.CodeAttachmentUnavailable,
				"attachment_not_reachable",
				http.StatusBadRequest,
				"chat.externalAgent.attachmentUnavailable",
				"The attachment could not be made available to the external agent. Please attach it again.",
				map[string]string{"name": name},
			)
		}

		result.Context = append(result.Context, contextAttachment)
	}
	return result, nil
}

func runtimePromptImageFromDataURL(payload, fallbackMime string) (external.Image, error) {
	payload = strings.TrimSpace(payload)
	comma := strings.Index(payload, ",")
	if comma < 0 || !strings.HasPrefix(strings.ToLower(payload), "data:") ||
		!strings.Contains(strings.ToLower(payload[:comma]), ";base64") {
		return external.Image{}, errors.New("invalid image data URL")
	}
	mimeType := attachmentpkg.MimeFromDataURL(payload)
	if mimeType == "" || mimeType == "application/octet-stream" {
		mimeType = attachmentpkg.NormalizeMime(fallbackMime)
	}
	if !strings.HasPrefix(mimeType, "image/") {
		return external.Image{}, errors.New("attachment is not an image")
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(payload[comma+1:]))
	if err != nil || len(data) == 0 || len(data) > 20*1024*1024 {
		return external.Image{}, errors.New("invalid image payload")
	}
	return external.Image{MimeType: mimeType, Data: data}, nil
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

func (s *Service) requireRuntimeOwnerWorkspaceExec(ctx context.Context, botID, runtimeOwnerAccountID string) error {
	if s == nil || s.botPermissions == nil {
		return errors.New("bot permission checker not configured")
	}
	runtimeOwnerAccountID = strings.TrimSpace(runtimeOwnerAccountID)
	if runtimeOwnerAccountID == "" {
		return agentfeedback.New(
			agentfeedback.CodeRuntimeOwnerMissing,
			"missing_runtime_owner",
			409,
			"chat.externalAgent.runtimeOwnerMissing",
			"External Agent runtime owner is missing; start a new External Agent session",
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
	return agentfeedback.New(
		agentfeedback.CodeNoWorkspaceExec,
		"missing_workspace_exec",
		403,
		"chat.externalAgent.noWorkspaceExec",
		"External Agent runtime owner no longer has workspace execution permission for this bot.",
		nil,
	)
}

func isRuntimeDecisionProjectionEvent(ev native.StreamEvent) bool {
	switch ev.Type {
	case native.EventUserInputRequest, native.EventToolApprovalRequest:
		return strings.TrimSpace(ev.ToolCallID) != ""
	default:
		return false
	}
}

func (s *Service) persistRuntimeLeadingUserMessage(ctx context.Context, req ChatRequest) (ChatRequest, *messagepkg.Message, error) {
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
	content, err := historyfrag.MarshalStoredModelMessage(ModelMessage{
		Role:    "user",
		Content: newTextContent(contentText),
	})
	if err != nil {
		s.logger.Warn("persist External Agent leading user message: marshal failed", slog.Any("error", err))
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
		s.logger.Warn("persist External Agent leading user message failed",
			slog.String("bot_id", req.BotID),
			slog.String("session_id", req.ThreadID),
			slog.Any("error", err))
		if !errors.Is(err, db.ErrCommitOutcomeUnknown) {
			return req, nil, nil
		}
		messageID, reconcileErr := s.reconcileRuntimeLeadingUserMessage(ctx, req)
		if reconcileErr != nil {
			// No native prompt has run yet, so fail closed. A background cleanup
			// removes the eager row if the lost acknowledgement was in fact a
			// commit; retrying the prompt cannot then collide with an orphan.
			go s.cleanupUncertainRuntimeLeadingUser(context.WithoutCancel(ctx), req)
			return req, nil, fmt.Errorf("reconcile uncertain External Agent leading user message: %w", reconcileErr)
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

func (s *Service) persistRuntimeDecisionProjection(ctx context.Context, req ChatRequest, ev native.StreamEvent) *messagepkg.Message {
	if s == nil || s.messageService == nil || strings.TrimSpace(req.BotID) == "" || strings.TrimSpace(req.ThreadID) == "" {
		return nil
	}
	output := sdkMessagesToModelMessages(external.TranscriptFromEvents([]event.StreamEvent{ev}, ""))
	sessionMode, runtimeType := s.persistSessionRuntimeSnapshot(ctx, req)
	for _, msg := range output {
		if msg.Role != "assistant" {
			continue
		}
		content, err := historyfrag.MarshalStoredModelMessage(msg)
		if err != nil {
			s.logger.Warn("persist External Agent decision projection: marshal failed",
				slog.String("tool_call_id", ev.ToolCallID),
				slog.Any("error", err))
			return nil
		}
		metadata := cloneMetadataMap(buildRouteMetadata(req))
		metadata["agent_decision_projection"] = true
		metadata["agent_decision_tool_call_id"] = strings.TrimSpace(ev.ToolCallID)
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
			s.logger.Warn("persist External Agent decision projection failed",
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

// cancelPendingRuntimeApprovals closes the residual approval window when a turn
// dies abnormally: any pending row for the session belonged to that turn (the
// pool's turn slot guarantees one turn per session), and its waiter is gone -
// left pending, the persisted card would stay actionable forever and a late
// approve would flip a row nobody executes.
func (s *Service) cancelPendingRuntimeApprovals(ctx context.Context, req ChatRequest, reason string) {
	if s == nil || s.toolApproval == nil {
		return
	}
	cancelled, err := s.toolApproval.CancelPendingForSession(ctx, req.BotID, req.ThreadID, reason)
	if err != nil {
		s.logger.Warn("cancel pending External Agent approvals failed",
			slog.String("bot_id", req.BotID),
			slog.String("session_id", req.ThreadID),
			slog.Any("error", err))
		return
	}
	if len(cancelled) > 0 {
		s.logger.Info("cancelled pending External Agent approvals with their turn",
			slog.String("session_id", req.ThreadID),
			slog.Int("count", len(cancelled)))
	}
}

func metadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}
