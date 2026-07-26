package http

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/memohai/memoh/domains/api/bot"
	botpersistence "github.com/memohai/memoh/domains/api/bot/persistence"
	"github.com/memohai/memoh/domains/api/identity"
	"github.com/memohai/memoh/domains/api/identity/auth"
	"github.com/memohai/memoh/domains/iam/account"
	"github.com/memohai/memoh/internal/apperror"
)

// RequireChannelIdentityID extracts and validates the channel identity ID from the request context.
func RequireChannelIdentityID(c echo.Context) (string, error) {
	channelIdentityID, err := auth.UserIDFromContext(c)
	if err != nil {
		return "", err
	}
	if err := identity.ValidateChannelIdentityID(channelIdentityID); err != nil {
		return "", apperror.Invalid("require channel identity", err)
	}
	return channelIdentityID, nil
}

// AuthorizeBotAccess validates that the given identity has manage-level access to
// the specified bot (owner, workspace admin, or a user grant carrying manage).
func AuthorizeBotAccess(ctx context.Context, botService *bot.Service, accountService *account.Service, channelIdentityID, botID string) (bot.Bot, error) {
	return AuthorizeBotAccessWithPermission(ctx, botService, accountService, channelIdentityID, botID, bot.PermissionManage)
}

// AuthorizeBotAccessWithPermission validates that the given identity holds the
// required permission scope on the specified bot.
func AuthorizeBotAccessWithPermission(ctx context.Context, botService *bot.Service, accountService *account.Service, channelIdentityID, botID, requiredPermission string) (bot.Bot, error) {
	const op = "authorize bot access"
	if botService == nil || accountService == nil {
		return bot.Bot{}, apperror.Internal(op, errors.New("bot services not configured"))
	}
	isAdmin, err := accountService.IsAdmin(ctx, channelIdentityID)
	if err != nil {
		return bot.Bot{}, apperror.Internal(op, err)
	}
	record, err := botService.AuthorizeAccessWithPermission(ctx, channelIdentityID, botID, isAdmin, requiredPermission)
	if err != nil {
		switch {
		case errors.Is(err, botpersistence.ErrBotNotFound):
			return bot.Bot{}, apperror.NotFound(op, err)
		case errors.Is(err, bot.ErrBotAccessDenied):
			return bot.Bot{}, apperror.Forbidden(op, err)
		default:
			return bot.Bot{}, apperror.Internal(op, err)
		}
	}
	return record, nil
}

// parseOffsetLimit extracts limit and offset query parameters with defaults.
func ParseOffsetLimit(c echo.Context) (limit, offset int) {
	limit = 50
	if raw := strings.TrimSpace(c.QueryParam("limit")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			limit = v
		}
	}
	if raw := strings.TrimSpace(c.QueryParam("offset")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v >= 0 {
			offset = v
		}
	}
	return limit, offset
}
