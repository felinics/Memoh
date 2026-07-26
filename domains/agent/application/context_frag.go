package application

import (
	"strings"

	agentdomain "github.com/memohai/memoh/domains/agent"
	"github.com/memohai/memoh/domains/agent/chat/context/fragment"
	"github.com/memohai/memoh/domains/agent/engine"
	sessionmode "github.com/memohai/memoh/domains/agent/chat/session/mode"
)

func buildContextFragScope(req ChatRequest, displayName string, identity engine.SessionContext) fragment.Scope {
	channelIdentityID := firstNonEmpty(req.SourceChannelIdentityID, identity.ChannelIdentityID)
	scope := fragment.Scope{
		BotID:                     firstNonEmpty(req.BotID, identity.BotID),
		ChatID:                    firstNonEmpty(req.ChatID, identity.ChatID),
		SessionID:                 firstNonEmpty(req.ThreadID, identity.SessionID),
		ChannelIdentityID:         strings.TrimSpace(channelIdentityID),
		DisplayName:               strings.TrimSpace(displayName),
		Platform:                  firstNonEmpty(req.CurrentChannel, identity.CurrentPlatform),
		ConversationType:          firstNonEmpty(req.ConversationType, identity.ConversationType),
		ConversationName:          strings.TrimSpace(req.ConversationName),
		ReplyTarget:               firstNonEmpty(req.ReplyTarget, identity.ReplyTarget),
		CurrentMessageID:          strings.TrimSpace(req.ExternalMessageID),
		EventID:                   strings.TrimSpace(req.EventID),
		ReplyToMessageID:          strings.TrimSpace(req.SourceReplyToMessageID),
		ReplySender:               strings.TrimSpace(req.ReplySender),
		MentionsBot:               req.MentionsBot,
		RepliesToBot:              req.RepliesToBot,
		ForwardMessageID:          strings.TrimSpace(req.ForwardMessageID),
		ForwardFromUserID:         strings.TrimSpace(req.ForwardFromUserID),
		ForwardFromConversationID: strings.TrimSpace(req.ForwardFromConversationID),
	}
	scope.Attention = contextFragAttentionReasons(req)
	return scope
}

func contextFragAttentionReasons(req ChatRequest) []fragment.AttentionReason {
	var reasons []fragment.AttentionReason
	add := func(reason fragment.AttentionReason) {
		for _, existing := range reasons {
			if existing == reason {
				return
			}
		}
		reasons = append(reasons, reason)
	}

	switch strings.TrimSpace(req.SessionType) {
	case sessionmode.Schedule:
		add(fragment.AttentionSchedule)
	case sessionmode.Heartbeat:
		add(fragment.AttentionHeartbeat)
	}
	if req.MentionsBot {
		add(fragment.AttentionMention)
	}
	if req.RepliesToBot {
		add(fragment.AttentionReply)
	}
	query := strings.TrimSpace(firstNonEmpty(req.RawQuery, req.Query))
	if strings.HasPrefix(query, "/") {
		add(fragment.AttentionCommand)
	}
	switch agentdomain.NormalizeConversationType(req.ConversationType) {
	case agentdomain.ConversationTypePrivate:
		add(fragment.AttentionDirect)
	case agentdomain.ConversationTypeGroup, agentdomain.ConversationTypeThread:
		if len(reasons) == 0 {
			add(fragment.AttentionPassive)
		}
	}
	if len(reasons) == 0 {
		add(fragment.AttentionPassive)
	}
	return reasons
}
