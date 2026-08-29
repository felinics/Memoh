package handlers

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/auth"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/identity"
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
func AuthorizeBotAccess(ctx context.Context, botService *bots.Service, accountService *accounts.Service, channelIdentityID, botID string) (bots.Bot, error) {
	return AuthorizeBotAccessWithPermission(ctx, botService, accountService, channelIdentityID, botID, bots.PermissionManage)
}

// AuthorizeBotAccessWithPermission validates that the given identity holds the
// required permission scope on the specified bot.
func AuthorizeBotAccessWithPermission(ctx context.Context, botService *bots.Service, accountService *accounts.Service, channelIdentityID, botID, requiredPermission string) (bots.Bot, error) {
	if botService == nil || accountService == nil {
		return bots.Bot{}, echo.NewHTTPError(http.StatusInternalServerError, "bot services not configured")
	}
	isAdmin, err := accountService.IsAdmin(ctx, channelIdentityID)
	if err != nil {
		return bots.Bot{}, echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	bot, err := botService.AuthorizeAccessWithPermission(ctx, channelIdentityID, botID, isAdmin, requiredPermission)
	if err != nil {
		if errors.Is(err, bots.ErrBotNotFound) {
			return bots.Bot{}, echo.NewHTTPError(http.StatusNotFound, "bot not found")
		}
		if errors.Is(err, bots.ErrBotAccessDenied) {
			return bots.Bot{}, echo.NewHTTPError(http.StatusForbidden, "bot access denied")
		}
		return bots.Bot{}, echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return bot, nil
}

// parseOffsetLimit extracts limit and offset query parameters with defaults.
func parseOffsetLimit(c echo.Context) (limit, offset int) {
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
