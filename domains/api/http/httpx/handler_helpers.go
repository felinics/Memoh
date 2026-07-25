package httpx

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/memohai/memoh/domains/api/auth"
	"github.com/memohai/memoh/domains/api/bot"
	"github.com/memohai/memoh/domains/api/identity"
	"github.com/memohai/memoh/domains/iam/account"
)

// RequireChannelIdentityID extracts and validates the channel identity ID from the request context.
func RequireChannelIdentityID(c echo.Context) (string, error) {
	channelIdentityID, err := auth.UserIDFromContext(c)
	if err != nil {
		return "", err
	}
	if err := identity.ValidateChannelIdentityID(channelIdentityID); err != nil {
		return "", echo.NewHTTPError(http.StatusBadRequest, err.Error())
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
	if botService == nil || accountService == nil {
		return bot.Bot{}, echo.NewHTTPError(http.StatusInternalServerError, "bot services not configured")
	}
	isAdmin, err := accountService.IsAdmin(ctx, channelIdentityID)
	if err != nil {
		return bot.Bot{}, echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	record, err := botService.AuthorizeAccessWithPermission(ctx, channelIdentityID, botID, isAdmin, requiredPermission)
	if err != nil {
		if errors.Is(err, bot.ErrBotNotFound) {
			return bot.Bot{}, echo.NewHTTPError(http.StatusNotFound, "bot not found")
		}
		if errors.Is(err, bot.ErrBotAccessDenied) {
			return bot.Bot{}, echo.NewHTTPError(http.StatusForbidden, "bot access denied")
		}
		return bot.Bot{}, echo.NewHTTPError(http.StatusInternalServerError, err.Error())
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
