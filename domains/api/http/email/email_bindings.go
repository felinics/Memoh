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

type EmailBindingsHandler struct {
	service        *email.Service
	manager        email.Runtime
	botService     *bot.Service
	accountService *account.Service
	logger         *slog.Logger
}

func NewEmailBindingsHandler(log *slog.Logger, service *email.Service, manager email.Runtime, botService *bot.Service, accountService *account.Service) *EmailBindingsHandler {
	return &EmailBindingsHandler{
		service:        service,
		manager:        manager,
		botService:     botService,
		accountService: accountService,
		logger:         log.With(slog.String("handler", "email_bindings")),
	}
}

func (h *EmailBindingsHandler) Register(e *echo.Echo) {
	g := e.Group("/bots/:bot_id/email-bindings")
	g.POST("", h.Create)
	g.GET("", h.List)
	g.PUT("/:id", h.Update)
	g.DELETE("/:id", h.Delete)
}

// Create godoc
// @Summary Bind an email provider to a bot
// @Tags email-bindings
// @Accept json
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param request body email.CreateBindingRequest true "Binding configuration"
// @Success 201 {object} email.BindingResponse
// @Failure 400 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/email-bindings [post].
func (h *EmailBindingsHandler) Create(c echo.Context) error {
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return apperror.Required("bot_id")
	}
	bot, err := h.authorizeBot(c, botID)
	if err != nil {
		return err
	}
	var req email.CreateBindingRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Invalid("bind email binding", err)
	}
	if strings.TrimSpace(req.EmailProviderID) == "" {
		return apperror.Required("email_provider_id")
	}
	if strings.TrimSpace(req.EmailAddress) == "" {
		return apperror.Required("email_address")
	}
	if err := h.ensureProviderOwnedByBot(c, bot, req.EmailProviderID); err != nil {
		return err
	}
	resp, err := h.service.CreateBinding(c.Request().Context(), botID, req)
	if err != nil {
		return apperror.Internal("create email binding", err)
	}
	// Refresh provider connections after binding change
	_ = h.manager.RefreshProvider(c.Request().Context(), req.EmailProviderID)
	return c.JSON(http.StatusCreated, resp)
}

// List godoc
// @Summary List email bindings for a bot
// @Tags email-bindings
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Success 200 {array} email.BindingResponse
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/email-bindings [get].
func (h *EmailBindingsHandler) List(c echo.Context) error {
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return apperror.Required("bot_id")
	}
	if _, err := h.authorizeBot(c, botID); err != nil {
		return err
	}
	items, err := h.service.ListBindings(c.Request().Context(), botID)
	if err != nil {
		return apperror.Internal("list email bindings", err)
	}
	return c.JSON(http.StatusOK, items)
}

// Update godoc
// @Summary Update an email binding
// @Tags email-bindings
// @Accept json
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param id path string true "Binding ID"
// @Param request body email.UpdateBindingRequest true "Updated binding"
// @Success 200 {object} email.BindingResponse
// @Failure 400 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/email-bindings/{id} [put].
func (h *EmailBindingsHandler) Update(c echo.Context) error {
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return apperror.Required("bot_id")
	}
	bot, err := h.authorizeBot(c, botID)
	if err != nil {
		return err
	}
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return apperror.Required("id")
	}
	current, err := h.service.GetBinding(c.Request().Context(), id)
	if err != nil {
		return apperror.NotFound("get email binding", err)
	}
	if current.BotID != botID {
		return apperror.NotFound("get email binding", nil)
	}
	if err := h.ensureProviderOwnedByBot(c, bot, current.EmailProviderID); err != nil {
		return err
	}
	var req email.UpdateBindingRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Invalid("bind email binding", err)
	}
	resp, err := h.service.UpdateBinding(c.Request().Context(), id, req)
	if err != nil {
		return apperror.Internal("update email binding", err)
	}
	_ = h.manager.RefreshProvider(c.Request().Context(), resp.EmailProviderID)
	return c.JSON(http.StatusOK, resp)
}

// Delete godoc
// @Summary Remove an email binding
// @Tags email-bindings
// @Param bot_id path string true "Bot ID"
// @Param id path string true "Binding ID"
// @Success 204 "No Content"
// @Failure 500 {object} apperror.Problem
// @Router /bots/{bot_id}/email-bindings/{id} [delete].
func (h *EmailBindingsHandler) Delete(c echo.Context) error {
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return apperror.Required("bot_id")
	}
	bot, err := h.authorizeBot(c, botID)
	if err != nil {
		return err
	}
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return apperror.Required("id")
	}
	// Get binding info before delete for refresh
	binding, err := h.service.GetBinding(c.Request().Context(), id)
	if err != nil {
		return apperror.NotFound("get email binding", err)
	}
	if binding.BotID != botID {
		return apperror.NotFound("get email binding", nil)
	}
	if err := h.ensureProviderOwnedByBot(c, bot, binding.EmailProviderID); err != nil {
		return err
	}
	if err := h.service.DeleteBinding(c.Request().Context(), id); err != nil {
		return apperror.Internal("delete email binding", err)
	}
	if binding.EmailProviderID != "" {
		_ = h.manager.RefreshProvider(c.Request().Context(), binding.EmailProviderID)
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *EmailBindingsHandler) authorizeBot(c echo.Context, botID string) (bot.Bot, error) {
	userID, err := auth.UserIDFromContext(c)
	if err != nil {
		return bot.Bot{}, err
	}
	return httpx.AuthorizeBotAccess(c.Request().Context(), h.botService, h.accountService, userID, botID)
}

func (h *EmailBindingsHandler) ensureProviderOwnedByBot(c echo.Context, record bot.Bot, providerID string) error {
	if strings.TrimSpace(record.OwnerUserID) == "" {
		return apperror.Forbidden("authorize email provider owner", nil)
	}
	if _, err := h.service.GetProvider(c.Request().Context(), record.OwnerUserID, providerID); err != nil {
		return apperror.Forbidden("authorize email provider owner", err)
	}
	return nil
}
