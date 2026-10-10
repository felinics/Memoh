package handlers

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/auth"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/identity"
)

// RequireChannelIdentityID extracts and validates the channel identity ID from the request context.
func RequireChannelIdentityID(c echo.Context) (string, error) {
	channelIdentityID, err := auth.UserIDFromContext(c)
	if err != nil {
		return "", err
	}
	if err := identity.ValidateChannelIdentityID(channelIdentityID); err != nil {
		return "", errs.Wrap(err, "channel identity id")
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
		return bots.Bot{}, errs.Wrap(err, "check bot admin")
	}
	bot, err := botService.AuthorizeAccessWithPermission(ctx, channelIdentityID, botID, isAdmin, requiredPermission)
	if err != nil {
		if errors.Is(err, bots.ErrBotNotFound) {
			return bots.Bot{}, apperror.Wrap(apperror.CodeBotNotFound, err, nil)
		}
		if errors.Is(err, bots.ErrBotAccessDenied) {
			return bots.Bot{}, echo.NewHTTPError(http.StatusForbidden, "bot access denied")
		}
		return bots.Bot{}, errs.Wrap(err, "authorize bot access")
	}
	return bot, nil
}

// resourceLookupError answers a failed lookup by resource id. A malformed id
// is the caller's mistake, a row that is not there is that resource's
// not_found, and every other failure — a database that is down, a query that
// did not compile — stays a server fault. Collapsing them into not_found
// reports an outage as a client mistake and hides the cause.
func resourceLookupError(err error, field string, code apperror.Code, op string) error {
	switch {
	case errors.Is(err, db.ErrInvalidUUID):
		return apperror.FieldInvalid(field, err)
	case errors.Is(err, pgx.ErrNoRows), errors.Is(err, db.ErrNotFound):
		return apperror.Wrap(code, err, nil)
	default:
		return errs.Wrap(err, op)
	}
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

func firstHeaderValue(raw string) string {
	if raw == "" {
		return ""
	}
	parts := strings.Split(raw, ",")
	return strings.TrimSpace(parts[0])
}
