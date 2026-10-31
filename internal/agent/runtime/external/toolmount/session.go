package toolmount

import (
	"context"
	"strings"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/sessionmode"
	"github.com/felinics/memoh/internal/mcp"
	"github.com/felinics/memoh/internal/runtimefence"
)

// IdleSession is the identity of a mount between turns: the bot alone. The
// gateway lists tools for it but refuses to run them, since no run owns the
// call.
func IdleSession(botID string) mcp.ToolSessionContext {
	return mcp.ToolSessionContext{
		BotID:            botID,
		ChatID:           botID,
		SessionType:      sessionmode.Chat,
		CanListUserInput: true,
		RequireActiveRun: true,
	}
}

// TurnSession is the trusted identity of the tool calls one turn makes. It
// comes from the turn's input and context only, never from the HTTP request.
func TurnSession(ctx context.Context, input external.PromptInput) mcp.ToolSessionContext {
	botID := strings.TrimSpace(input.BotID)
	session := IdleSession(botID)
	session.ChatID = external.FirstNonEmpty(input.ChatID, botID)
	session.SessionID = strings.TrimSpace(input.ThreadID)
	session.RunID = strings.TrimSpace(input.RunID)
	session.SessionType = external.FirstNonEmpty(input.SessionMode, sessionmode.Chat)
	session.RouteID = strings.TrimSpace(input.RouteID)
	session.CurrentPlatform = strings.TrimSpace(input.CurrentPlatform)
	session.ReplyTarget = strings.TrimSpace(input.ReplyTarget)
	session.ConversationType = strings.TrimSpace(input.ConversationType)
	session.ChannelIdentityID = strings.TrimSpace(input.ChannelIdentityID)
	session.SessionToken = strings.TrimSpace(input.SessionToken)
	session.CanRequestUserInput = input.CanRequestUserInput
	session.RuntimeActive = true
	session.ContextBudgetMaxTokens = input.ContextBudgetMaxTokens
	if fence, ok := runtimefence.FromContext(ctx); ok {
		session.RuntimeFence = fence
	}
	session.RunContext = ctx
	return session
}
