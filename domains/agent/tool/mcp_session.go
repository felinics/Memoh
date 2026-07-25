package tool

import "github.com/memohai/memoh/domains/agent/mcp"

func toMCPSession(s SessionContext) mcp.ToolSessionContext {
	return mcp.ToolSessionContext{
		BotID:             s.BotID,
		ChatID:            s.ChatID,
		SessionID:         s.SessionID,
		SessionType:       s.SessionType,
		ChannelIdentityID: s.ChannelIdentityID,
		SessionToken:      s.SessionToken,
		CurrentPlatform:   s.CurrentPlatform,
		ReplyTarget:       s.ReplyTarget,
		ConversationType:  s.ConversationType,
		IsSubagent:        s.IsSubagent,
	}
}
