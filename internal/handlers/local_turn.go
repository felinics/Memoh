package handlers

import (
	"context"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/agent/application"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/channel"
)

// localTurns builds the turn a local client's text starts. The Web chat
// socket and the Web queue API both build their turns here, so a text sent
// while the session is idle and one queued behind a running turn start the
// same turn: the queued run renders its prompt and tools, and acts for the
// same account, as the run it waited for.
type localTurns struct {
	channel        channel.ChannelType
	agentService   *application.Service
	botService     *bots.Service
	accountService *accounts.Service
}

// text authorizes the user's text on a session and returns the turn it
// starts: who sends it, the account it runs for, the conversation it answers
// in, and the tool gateway an external agent reaches Memoh through. The
// session's runtime is returned for callers with checks of their own.
func (t localTurns) text(ctx context.Context, c echo.Context, identityID, botID, sessionID, text string) (turn.StartTurnCommand, application.RuntimeSessionExecutionInfo, error) {
	info, err := t.authorizeExecution(ctx, identityID, botID, sessionID)
	if err != nil {
		return turn.StartTurnCommand{}, info, err
	}
	// The run acts for the signed-in user, or for the runtime owner of a
	// session that executes in a workspace.
	actor := identityID
	if owner := strings.TrimSpace(info.RuntimeOwnerAccountID); info.RequiresWorkspaceExec && owner != "" {
		actor = owner
	}
	text = strings.TrimSpace(text)
	return turn.StartTurnCommand{
		SchemaVersion:           1,
		Mode:                    turn.ModeChat,
		BotID:                   botID,
		ChatID:                  botID,
		ThreadID:                sessionID,
		ActorAccountID:          actor,
		UserID:                  identityID,
		SourceChannelIdentityID: identityID,
		Query:                   text,
		UserVisibleText:         text,
		ReplyTarget:             botID,
		ConversationType:        channel.ConversationTypePrivate,
		CurrentChannel:          t.channel.String(),
		Channels:                []string{t.channel.String()},
		ToolHTTPURL:             buildExternalAgentToolsURL(c, botID),
	}, info, nil
}

// authorizeExecution checks that the user may run a turn in the session's
// runtime. Every external agent executes in the workspace, so its sessions
// need the user's workspace_exec grant and a runtime owner.
func (t localTurns) authorizeExecution(ctx context.Context, identityID, botID, sessionID string) (application.RuntimeSessionExecutionInfo, error) {
	if t.agentService == nil {
		return application.RuntimeSessionExecutionInfo{}, nil
	}
	info, err := t.agentService.RuntimeSessionExecutionInfo(ctx, sessionID)
	if err != nil || !info.RequiresWorkspaceExec {
		return info, err
	}
	if strings.TrimSpace(info.RuntimeOwnerAccountID) == "" {
		return info, apperror.New(apperror.CodeACPRuntimeOwnerMissing, nil)
	}
	bot, err := AuthorizeBotAccessWithPermission(ctx, t.botService, t.accountService, identityID, botID, bots.PermissionWorkspaceExec)
	if err != nil {
		if isHTTPStatus(err, http.StatusForbidden) {
			return info, apperror.New(apperror.CodeNoWorkspaceExec, nil)
		}
		return info, err
	}
	if strings.TrimSpace(info.BotID) != "" && info.BotID != bot.ID {
		return info, apperror.New(apperror.CodeSessionNotFound, nil)
	}
	perms, err := resolveUserPermissions(ctx, t.botService, t.accountService, identityID, bot.ID)
	if err != nil {
		return info, err
	}
	return info, authorizeExternalAgentSessionAccess(identityID, perms, info.RuntimeOwnerAccountID)
}
