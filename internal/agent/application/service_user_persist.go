package application

import (
	"context"
	"errors"
	"strings"

	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
)

// ApplyUserMessageHookAndPersistUserTurn applies the normal user-message hook
// before writing a user turn ahead of agent execution. Web skill activations
// use this so a denied hook cannot leave a persisted user-only special turn.
func (s *Service) ApplyUserMessageHookAndPersistUserTurn(ctx context.Context, req ChatRequest) (ChatRequest, messagepkg.Message, error) {
	if req.WorkspaceTarget == nil {
		var err error
		ctx, req, err = s.prepareWorkspaceRequest(ctx, req)
		if err != nil {
			return req, messagepkg.Message{}, err
		}
	}
	if req.RawQuery == "" {
		req.RawQuery = strings.TrimSpace(req.Query)
	}
	nextReq, err := s.applyUserMessageHook(ctx, req)
	if err != nil {
		return req, messagepkg.Message{}, err
	}
	persisted, err := s.persistUserTurn(ctx, nextReq)
	if err != nil {
		return nextReq, messagepkg.Message{}, err
	}
	nextReq.UserMessagePersisted = true
	nextReq.PersistedUserMessageID = persisted.ID
	return nextReq, persisted, nil
}

// persistUserTurn writes a user turn before agent execution. It is used after
// the user-message hook has already accepted the request.
func (s *Service) persistUserTurn(ctx context.Context, req ChatRequest) (messagepkg.Message, error) {
	if s == nil || s.messageService == nil {
		return messagepkg.Message{}, errors.New("message service not configured")
	}
	if strings.TrimSpace(req.BotID) == "" {
		return messagepkg.Message{}, errors.New("bot id is required")
	}
	if strings.TrimSpace(req.ThreadID) == "" {
		return messagepkg.Message{}, errors.New("session id is required")
	}
	modelMessage := ModelMessage{
		Role:    "user",
		Content: newTextContent(persistedUserTurnText(req)),
	}
	modelMessage = normalizeUserMessageContent(modelMessage)
	content, err := historyfrag.MarshalStoredModelMessage(modelMessage)
	if err != nil {
		return messagepkg.Message{}, err
	}
	displayText := strings.TrimSpace(req.Query)
	if strings.TrimSpace(req.UserVisibleText) != "" || req.UserMessageKind == UserMessageKindSkillActivation {
		displayText = strings.TrimSpace(req.UserVisibleText)
	} else if strings.TrimSpace(req.RawQuery) != "" {
		displayText = strings.TrimSpace(req.RawQuery)
	}
	senderChannelIdentityID, senderUserID := s.resolvePersistSenderIDs(ctx, req)
	sessionMode, runtimeType := s.persistSessionRuntimeSnapshot(ctx, req)
	return s.messageService.Persist(ctx, messagepkg.PersistInput{
		BotID:                   req.BotID,
		SessionID:               req.ThreadID,
		SenderChannelIdentityID: senderChannelIdentityID,
		SenderUserID:            senderUserID,
		ExternalMessageID:       req.ExternalMessageID,
		SourceReplyToMessageID:  req.SourceReplyToMessageID,
		Role:                    "user",
		Content:                 content,
		Metadata:                mergeMetadata(mergeMetadata(buildRouteMetadata(req), buildInteractionMetadata(req)), workspaceTargetMetadata(req.WorkspaceTarget)),
		Assets:                  chatAttachmentsToAssetRefs(req.Attachments),
		EventID:                 req.EventID,
		DisplayText:             displayText,
		SessionMode:             sessionMode,
		RuntimeType:             runtimeType,
		RunID:                   req.RunID,
		TurnID:                  req.TurnID,
		TurnPosition:            req.TurnPosition,
	})
}

func persistedUserTurnText(req ChatRequest) string {
	if req.UserMessageKind == UserMessageKindSkillActivation {
		return strings.TrimSpace(req.UserVisibleText)
	}
	return req.Query
}
