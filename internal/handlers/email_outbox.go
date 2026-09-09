package handlers

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/auth"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/email"
)

type EmailOutboxHandler struct {
	outbox         *email.OutboxService
	botService     *bots.Service
	accountService *accounts.Service
	logger         *slog.Logger
}

func NewEmailOutboxHandler(log *slog.Logger, outbox *email.OutboxService, botService *bots.Service, accountService *accounts.Service) *EmailOutboxHandler {
	return &EmailOutboxHandler{
		outbox:         outbox,
		botService:     botService,
		accountService: accountService,
		logger:         log.With(slog.String("handler", "email_outbox")),
	}
}

func (h *EmailOutboxHandler) Register(e *echo.Echo) {
	g := e.Group("/bots/:bot_id/email-outbox")
	g.GET("", h.List)
	g.GET("/:id", h.Get)
}

// List godoc
// @Summary List outbox emails for a bot (audit)
// @Tags email-outbox
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param limit query int false "Limit" default(20)
// @Param offset query int false "Offset" default(0)
// @Success 200 {object} map[string]any
// @Failure 500 {object} ErrorResponse
// @Router /bots/{bot_id}/email-outbox [get].
func (h *EmailOutboxHandler) List(c echo.Context) error {
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot_id is required")
	}
	if _, err := h.authorizeBot(c, botID); err != nil {
		return err
	}
	limit, err := parseInt32Query(c.QueryParam("limit"), 20)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	offset, err := parseInt32Query(c.QueryParam("offset"), 0)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	items, total, err := h.outbox.ListByBot(c.Request().Context(), botID, limit, offset)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, map[string]any{
		"items": items,
		"total": total,
	})
}

func parseInt32Query(raw string, defaultValue int32) (int32, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultValue, nil
	}
	parsed, err := strconv.ParseInt(raw, 10, 32)
	if err != nil {
		return 0, echo.NewHTTPError(http.StatusBadRequest, "invalid integer query parameter")
	}
	value := int32(parsed)
	if value < 0 {
		return 0, nil
	}
	return value, nil
}

// Get godoc
// @Summary Get outbox email detail
// @Tags email-outbox
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param id path string true "Email ID"
// @Success 200 {object} email.OutboxItemResponse
// @Failure 404 {object} ErrorResponse
// @Router /bots/{bot_id}/email-outbox/{id} [get].
func (h *EmailOutboxHandler) Get(c echo.Context) error {
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot_id is required")
	}
	if _, err := h.authorizeBot(c, botID); err != nil {
		return err
	}
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "id is required")
	}
	resp, err := h.outbox.Get(c.Request().Context(), id)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, err.Error())
	}
	if resp.BotID != botID {
		return echo.NewHTTPError(http.StatusNotFound, "email outbox item not found")
	}
	return c.JSON(http.StatusOK, resp)
}

func (h *EmailOutboxHandler) authorizeBot(c echo.Context, botID string) (bots.Bot, error) {
	userID, err := auth.UserIDFromContext(c)
	if err != nil {
		return bots.Bot{}, err
	}
	return AuthorizeBotAccess(c.Request().Context(), h.botService, h.accountService, userID, botID)
}
