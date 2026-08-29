package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/botagents"
	"github.com/felinics/memoh/internal/bots"
)

type BotAgentsHandler struct {
	service        *botagents.Service
	botService     *bots.Service
	accountService *accounts.Service
	logger         *slog.Logger
}

func NewBotAgentsHandler(log *slog.Logger, service *botagents.Service, botService *bots.Service, accountService *accounts.Service) *BotAgentsHandler {
	return &BotAgentsHandler{
		service:        service,
		botService:     botService,
		accountService: accountService,
		logger:         log.With(slog.String("handler", "bot_agents")),
	}
}

func (h *BotAgentsHandler) Register(e *echo.Echo) {
	group := e.Group("/bots/:bot_id/agents")
	group.POST("", h.Create)
	group.GET("", h.List)
	group.GET("/:id", h.Get)
	group.PATCH("/:id", h.Update)
	group.DELETE("/:id", h.Delete)
}

// Create godoc
// @Summary Add an Agent to a bot
// @Description Add a named Agent backed by a runtime descriptor
// @Tags bot-agents
// @Accept json
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param payload body botagents.CreateRequest true "Agent payload"
// @Success 201 {object} botagents.BotAgent
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} ErrorResponse
// @Failure 409 {object} apperror.Problem
// @Router /bots/{bot_id}/agents [post].
func (h *BotAgentsHandler) Create(c echo.Context) error {
	botID, err := h.authorize(c, bots.PermissionManage)
	if err != nil {
		return err
	}
	var req botagents.CreateRequest
	if err := c.Bind(&req); err != nil {
		return apperror.New(apperror.CodeBotAgentInvalidMetadata, nil)
	}
	agent, err := h.service.Create(c.Request().Context(), botID, req)
	if err != nil {
		return h.publicError("create", err)
	}
	return c.JSON(http.StatusCreated, agent)
}

// List godoc
// @Summary List a bot's Agents
// @Description List active and disabled non-deleted Agents attached to a bot
// @Tags bot-agents
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Success 200 {object} botagents.ListResponse
// @Failure 403 {object} ErrorResponse
// @Router /bots/{bot_id}/agents [get].
func (h *BotAgentsHandler) List(c echo.Context) error {
	botID, err := h.authorize(c, bots.PermissionChat)
	if err != nil {
		return err
	}
	items, err := h.service.List(c.Request().Context(), botID)
	if err != nil {
		return h.publicError("list", err)
	}
	return c.JSON(http.StatusOK, botagents.ListResponse{Items: items})
}

// Get godoc
// @Summary Get a bot Agent
// @Description Get one Agent attached to a bot
// @Tags bot-agents
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param id path string true "Agent ID"
// @Success 200 {object} botagents.BotAgent
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} apperror.Problem
// @Router /bots/{bot_id}/agents/{id} [get].
func (h *BotAgentsHandler) Get(c echo.Context) error {
	botID, err := h.authorize(c, bots.PermissionChat)
	if err != nil {
		return err
	}
	agent, err := h.service.Get(c.Request().Context(), botID, strings.TrimSpace(c.Param("id")))
	if err != nil {
		return h.publicError("get", err)
	}
	return c.JSON(http.StatusOK, agent)
}

// Update godoc
// @Summary Update a bot Agent
// @Description Rename, enable, or disable an Agent; runtime metadata is immutable
// @Tags bot-agents
// @Accept json
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param id path string true "Agent ID"
// @Param payload body botagents.UpdateRequest true "Agent changes"
// @Success 200 {object} botagents.BotAgent
// @Failure 400 {object} apperror.Problem
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} apperror.Problem
// @Failure 409 {object} apperror.Problem
// @Router /bots/{bot_id}/agents/{id} [patch].
func (h *BotAgentsHandler) Update(c echo.Context) error {
	botID, err := h.authorize(c, bots.PermissionManage)
	if err != nil {
		return err
	}
	var req botagents.UpdateRequest
	if err := c.Bind(&req); err != nil {
		return apperror.New(apperror.CodeBotAgentInvalidMetadata, nil)
	}
	agent, err := h.service.Update(c.Request().Context(), botID, strings.TrimSpace(c.Param("id")), req)
	if err != nil {
		return h.publicError("update", err)
	}
	return c.JSON(http.StatusOK, agent)
}

// Delete godoc
// @Summary Delete a bot Agent
// @Description Soft-delete an Agent while preserving existing session bindings
// @Tags bot-agents
// @Param bot_id path string true "Bot ID"
// @Param id path string true "Agent ID"
// @Success 204 "No Content"
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} apperror.Problem
// @Failure 409 {object} apperror.Problem
// @Router /bots/{bot_id}/agents/{id} [delete].
func (h *BotAgentsHandler) Delete(c echo.Context) error {
	botID, err := h.authorize(c, bots.PermissionManage)
	if err != nil {
		return err
	}
	if err := h.service.Delete(c.Request().Context(), botID, strings.TrimSpace(c.Param("id"))); err != nil {
		return h.publicError("delete", err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *BotAgentsHandler) authorize(c echo.Context, permission string) (string, error) {
	channelIdentityID, err := RequireChannelIdentityID(c)
	if err != nil {
		return "", err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return "", apperror.New(apperror.CodeBotAgentNotFound, nil)
	}
	if _, err := AuthorizeBotAccessWithPermission(c.Request().Context(), h.botService, h.accountService, channelIdentityID, botID, permission); err != nil {
		return "", err
	}
	return botID, nil
}

func (h *BotAgentsHandler) publicError(operation string, err error) error {
	if publicErr := botAgentHTTPError(err); publicErr != nil {
		return publicErr
	}
	h.logger.Error("bot Agent operation failed", slog.String("operation", operation), slog.Any("error", err))
	return echo.NewHTTPError(http.StatusInternalServerError, "bot Agent operation failed")
}

func botAgentHTTPError(err error) error {
	switch {
	case errors.Is(err, botagents.ErrNotFound):
		return apperror.New(apperror.CodeBotAgentNotFound, nil)
	case errors.Is(err, botagents.ErrNameTaken):
		return apperror.New(apperror.CodeBotAgentNameTaken, nil)
	case errors.Is(err, botagents.ErrInvalidRuntime):
		return apperror.New(apperror.CodeBotAgentInvalidRuntime, nil)
	case errors.Is(err, botagents.ErrInvalidMetadata):
		return apperror.New(apperror.CodeBotAgentInvalidMetadata, nil)
	case errors.Is(err, botagents.ErrDefaultInUse):
		return apperror.New(apperror.CodeBotAgentDefaultInUse, nil)
	case errors.Is(err, botagents.ErrUnavailable):
		return apperror.New(apperror.CodeBotAgentUnavailable, nil)
	}
	var configErr *botagents.ConfigurationError
	if errors.As(err, &configErr) {
		return apperror.New(apperror.CodeBotAgentUnavailable, map[string]string{"field": configErr.Field})
	}
	return nil
}
