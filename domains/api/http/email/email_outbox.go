package email

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/memohai/memoh/domains/api/bot"
	httpx "github.com/memohai/memoh/domains/api/http"
	"github.com/memohai/memoh/domains/api/identity/auth"
	"github.com/memohai/memoh/domains/channel/email"
	"github.com/memohai/memoh/domains/iam/account"
	"github.com/memohai/memoh/internal/apperror"
)

type EmailOutboxHandler struct {
	outbox         *email.OutboxService
	botService     *bot.Service
	accountService *account.Service
	logger         *slog.Logger
}

func NewEmailOutboxHandler(log *slog.Logger, outbox *email.OutboxService, botService *bot.Service, accountService *account.Service) *EmailOutboxHandler {
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
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/email-outbox [get].
func (h *EmailOutboxHandler) List(c echo.Context) error {
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return apperror.Required("bot_id")
	}
	if _, err := h.authorizeBot(c, botID); err != nil {
		return err
	}
	limit, err := httpx.ParseInt32Query(c.QueryParam("limit"), 20)
	if err != nil {
		return apperror.Invalid("parse limit", err)
	}
	offset, err := httpx.ParseInt32Query(c.QueryParam("offset"), 0)
	if err != nil {
		return apperror.Invalid("parse offset", err)
	}

	items, total, err := h.outbox.ListByBot(c.Request().Context(), botID, limit, offset)
	if err != nil {
		return apperror.Internal("list email outbox", err)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"items": items,
		"total": total,
	})
}

// Get godoc
// @Summary Get outbox email detail
// @Tags email-outbox
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param id path string true "Email ID"
// @Success 200 {object} email.OutboxItemResponse
// @Failure 404 {object} apperror.Problem
// @Router /bots/{bot_id}/email-outbox/{id} [get].
func (h *EmailOutboxHandler) Get(c echo.Context) error {
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return apperror.Required("bot_id")
	}
	if _, err := h.authorizeBot(c, botID); err != nil {
		return err
	}
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return apperror.Required("id")
	}
	resp, err := h.outbox.Get(c.Request().Context(), id)
	if err != nil {
		return apperror.NotFound("get email outbox", err)
	}
	if resp.BotID != botID {
		return apperror.NotFound("get email outbox", nil)
	}
	return c.JSON(http.StatusOK, resp)
}

func (h *EmailOutboxHandler) authorizeBot(c echo.Context, botID string) (bot.Bot, error) {
	userID, err := auth.UserIDFromContext(c)
	if err != nil {
		return bot.Bot{}, err
	}
	return httpx.AuthorizeBotAccess(c.Request().Context(), h.botService, h.accountService, userID, botID)
}
