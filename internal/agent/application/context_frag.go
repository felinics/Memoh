package application

import (
	"context"
	"strings"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/agent/sessionmode"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/contextview"
)

func buildProviderSourceFrags(
	ctx context.Context,
	cfg native.RunConfig,
	sections []native.SystemSection,
	hookSystemFrags []contextfrag.ContextFrag,
) []contextfrag.ContextFrag {
	frags := native.SystemSectionFrags(sections, cfg.ContextScope)
	frags = append(frags, hookSystemFrags...)
	return append(frags, contextview.CollectNonSystemProviderSourceFrags(ctx, cfg)...)
}

func buildContextFragScope(req ChatRequest, displayName string, identity native.SessionContext) contextfrag.Scope {
	channelIdentityID := firstNonEmpty(req.SourceChannelIdentityID, identity.ChannelIdentityID)
	scope := contextfrag.Scope{
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

func contextFragAttentionReasons(req ChatRequest) []contextfrag.AttentionReason {
	var reasons []contextfrag.AttentionReason
	add := func(reason contextfrag.AttentionReason) {
		for _, existing := range reasons {
			if existing == reason {
				return
			}
		}
		reasons = append(reasons, reason)
	}

	if strings.TrimSpace(req.SessionType) == sessionmode.Schedule {
		add(contextfrag.AttentionSchedule)
	}
	if req.MentionsBot {
		add(contextfrag.AttentionMention)
	}
	if req.RepliesToBot {
		add(contextfrag.AttentionReply)
	}
	query := strings.TrimSpace(firstNonEmpty(req.RawQuery, req.Query))
	if strings.HasPrefix(query, "/") {
		add(contextfrag.AttentionCommand)
	}
	switch turn.NormalizeConversationType(req.ConversationType) {
	case turn.ConversationTypePrivate:
		add(contextfrag.AttentionDirect)
	case turn.ConversationTypeGroup, turn.ConversationTypeThread:
		if len(reasons) == 0 {
			add(contextfrag.AttentionPassive)
		}
	}
	if len(reasons) == 0 {
		add(contextfrag.AttentionPassive)
	}
	return reasons
}
