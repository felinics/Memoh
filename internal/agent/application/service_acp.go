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
)

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

	if req.RawQuery == "" {
		req.RawQuery = strings.TrimSpace(req.Query)
	}
	req.Query = strings.TrimSpace(req.Query)
	var leadingUser *messagepkg.Message
	req, leadingUser = s.persistACPLeadingUserMessage(context.WithoutCancel(ctx), req)
	cleanupLeadingUser := func() {
		if leadingUser != nil {
			s.cleanupReplacementMessages(context.WithoutCancel(ctx), []messagepkg.Message{*leadingUser})
		}
	}
	go s.maybeGenerateSessionTitle(context.WithoutCancel(ctx), req, req.RawQuery)

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	activePrompt := s.registerACPActivePrompt(req.BotID, req.ThreadID)
	defer s.unregisterACPActivePrompt(req.BotID, req.ThreadID, activePrompt)
	go func() {
		select {
		case <-abortCh:
			cancel()
		case <-streamCtx.Done():
		}
	}()

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
	cleanupProjections := func() {
		s.cleanupReplacementMessages(context.WithoutCancel(ctx), projectedSnapshot())
	}

	emit := func(ev native.StreamEvent) {
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
		select {
		case eventCh <- json.RawMessage(data):
		case <-streamCtx.Done():
		}
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
		Sink:                  acpclient.EventSinkFunc(emit),
	})
	if err != nil {
		s.logger.Error("ACP prompt failed",
			slog.String("bot_id", req.BotID),
			slog.String("session_id", req.ThreadID),
			slog.Any("error", err),
		)
		s.cancelPendingACPApprovals(context.WithoutCancel(ctx), req, "tool approval cancelled: the turn ended before a decision arrived")
		var feedbackErr *acpfeedback.Error
		if errors.As(err, &feedbackErr) {
			cleanupProjections()
			cleanupLeadingUser()
			return err
		}
		if appErr := acpPromptConfigAppError(err); appErr != nil {
			cleanupProjections()
			cleanupLeadingUser()
			return appErr
		}
		if feedbackErr := acpPromptInputFeedback(err); feedbackErr != nil {
			cleanupProjections()
			cleanupLeadingUser()
			return feedbackErr
		}
		result = ensureACPPromptOutput(result)
		failedResult, failureDelta := acpFailureResult(result, err)
		if failureDelta != "" {
			emit(native.StreamEvent{Type: native.EventTextDelta, Delta: failureDelta})
		}
		if err := s.persistACPRound(context.WithoutCancel(ctx), req, agentID, projectPath, failedResult, err); err != nil {
			s.logger.Error("ACP failure persist failed", slog.Any("error", err), slog.String("session_id", req.ThreadID))
		} else {
			cleanupProjections()
		}
		emit(native.StreamEvent{Type: native.EventTextEnd})
		emit(acpTerminalStreamEvent(native.EventAbort, failedResult))
		return nil
	}

	emit(native.StreamEvent{Type: native.EventTextEnd})
	result = ensureACPPromptOutput(result)
	if err := s.persistACPRound(context.WithoutCancel(ctx), req, agentID, projectPath, result, nil); err != nil {
		s.logger.Error("ACP persist failed", slog.Any("error", err), slog.String("session_id", req.ThreadID))
	} else {
		cleanupProjections()
	}
	emit(acpTerminalStreamEvent(native.EventEnd, result))
	return nil
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

func (s *Service) persistACPLeadingUserMessage(ctx context.Context, req ChatRequest) (ChatRequest, *messagepkg.Message) {
	if req.UserMessagePersisted || s == nil || s.messageService == nil || strings.TrimSpace(req.BotID) == "" {
		return req, nil
	}
	displayText := strings.TrimSpace(req.RawQuery)
	if displayText == "" {
		displayText = strings.TrimSpace(req.Query)
	}
	if displayText == "" && len(req.Attachments) == 0 {
		return req, nil
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
		return req, nil
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
		return req, nil
	}
	req.UserMessagePersisted = true
	req.PersistedUserMessageID = persisted.ID
	return req, &persisted
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
		persisted, err := s.messageService.Persist(ctx, messagepkg.PersistInput{
			BotID:                   req.BotID,
			SessionID:               req.ThreadID,
			SenderChannelIdentityID: "",
			Role:                    "assistant",
			Content:                 content,
			Metadata:                buildRouteMetadata(req),
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

func (s *Service) persistACPRound(ctx context.Context, req ChatRequest, agentID, projectPath string, result acpclient.PromptResult, promptErr error) error {
	meta := map[string]any{
		"acp_agent_id": agentID,
		"project_path": projectPath,
		"stop_reason":  result.StopReason,
	}
	if promptErr != nil {
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
	if req.UserMessagePersisted {
		metadataOffset = 0
	}
	for idx, msg := range output {
		if msg.Role == "assistant" {
			metadataByIndex[idx+metadataOffset] = meta
		}
	}
	skipMemory := promptErr != nil || req.UserMessagePersisted || req.SkipMemoryExtraction
	err := s.storeRoundWithOptions(ctx, req, round, "", storeRoundOptions{
		SkipMemory:              skipMemory,
		AllowEmptyAssistantText: true,
		MessageMetadataByIndex:  metadataByIndex,
		RequireCompletePersist:  true,
	})
	if err == nil && promptErr == nil && req.UserMessagePersisted && !req.SkipMemoryExtraction {
		go s.storeMemory(context.WithoutCancel(ctx), req, round)
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
