package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/agent/application"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/botagents"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/errs"
)

type BotAgentsHandler struct {
	service        *botagents.Service
	botService     *bots.Service
	accountService *accounts.Service
	logger         *slog.Logger
	runtimes       external.Drivers
}

func NewBotAgentsHandler(log *slog.Logger, service *botagents.Service, botService *bots.Service, accountService *accounts.Service, runtimes external.Drivers) *BotAgentsHandler {
	return &BotAgentsHandler{
		service:        service,
		botService:     botService,
		accountService: accountService,
		logger:         log.With(slog.String("handler", "bot_agents")),
		runtimes:       runtimes,
	}
}

func (h *BotAgentsHandler) Register(e *echo.Echo) {
	group := e.Group("/bots/:bot_id/agents")
	group.POST("", h.Create)
	group.GET("", h.List)
	group.GET("/:id", h.Get)
	group.GET("/:id/models", h.ListModels)
	group.GET("/:id/runtime-controls", h.RuntimeControls)
	group.PATCH("/:id", h.Update)
	group.DELETE("/:id", h.Delete)
}

// ListModels godoc
// @Summary List models available to a bot Agent
// @Tags bot-agents
// @Param bot_id path string true "Bot ID"
// @Param id path string true "Agent ID"
// @Param model_id query string false "Model whose effective defaults should be displayed"
// @Param project_path query string false "Workspace project path for runtime model settings"
// @Success 200 {object} external.ModelCatalog
// @Failure 403 {object} server.Problem
// @Failure 404 {object} server.Problem
// @Failure 503 {object} server.Problem
// @Router /bots/{bot_id}/agents/{id}/models [get].
func (h *BotAgentsHandler) ListModels(c echo.Context) error {
	botID, err := h.authorize(c, bots.PermissionWorkspaceExec)
	if err != nil {
		return err
	}
	agent, err := h.service.GetActive(c.Request().Context(), botID, strings.TrimSpace(c.Param("id")))
	if err != nil {
		return h.publicError("list models", err)
	}
	catalog, err := h.runtimes.ModelCatalog(c.Request().Context(), agent.Runtime, external.ModelCatalogRequest{
		BotID: botID, BotAgentID: agent.ID, ProjectPath: strings.TrimSpace(c.QueryParam("project_path")),
		ModelID: strings.TrimSpace(c.QueryParam("model_id")), ResolveDefaults: true,
	})
	if err != nil {
		// An External Agent error the user can act on (agent_dependency_missing
		// and friends) keeps its own code and args; wrapping it as
		// runtime-unavailable would lose both and hide the install task from
		// the web.
		if translated := application.ExternalAgentError(err); apperror.CodeOf(translated) != "" {
			return translated
		}
		return apperror.Wrap(apperror.CodeExternalRuntimeUnavailable, err, map[string]string{"runtime": agent.Runtime})
	}
	return c.JSON(http.StatusOK, catalog)
}

// Create godoc
// @Summary Add an Agent to a bot
// @Description Add a named Agent backed by a runtime descriptor. Omit enabled to create it enabled; pass enabled=false to hold a direct-runtime Agent back until its workspace dependency preflight passes. The response reports that dependency (dependency_id) when the runtime declares one.
// @Tags bot-agents
// @Accept json
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param payload body botagents.CreateRequest true "Agent payload"
// @Success 201 {object} botagents.BotAgent
// @Failure 400 {object} server.Problem
// @Failure 403 {object} server.Problem
// @Failure 409 {object} server.Problem
// @Router /bots/{bot_id}/agents [post].
func (h *BotAgentsHandler) Create(c echo.Context) error {
	bot, err := h.authorizeBot(c, bots.PermissionManage)
	if err != nil {
		return err
	}
	var req botagents.CreateRequest
	if err := c.Bind(&req); err != nil {
		return apperror.New(apperror.CodeBotAgentInvalidMetadata, nil)
	}
	agent, err := h.service.Create(c.Request().Context(), bot.ID, req)
	if err != nil {
		return h.publicError("create", err)
	}
	return c.JSON(http.StatusCreated, h.present(agent, bot))
}

// List godoc
// @Summary List a bot's Agents
// @Description List active and disabled non-deleted Agents attached to a bot. Direct-runtime Agents carry the workspace dependency their runtime declares.
// @Tags bot-agents
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Success 200 {object} botagents.ListResponse
// @Failure 403 {object} server.Problem
// @Router /bots/{bot_id}/agents [get].
func (h *BotAgentsHandler) List(c echo.Context) error {
	bot, err := h.authorizeBot(c, bots.PermissionChat)
	if err != nil {
		return err
	}
	items, err := h.service.List(c.Request().Context(), bot.ID)
	if err != nil {
		return h.publicError("list", err)
	}
	for i := range items {
		items[i] = h.present(items[i], bot)
	}
	return c.JSON(http.StatusOK, botagents.ListResponse{Items: items})
}

// Get godoc
// @Summary Get a bot Agent
// @Description Get one Agent attached to a bot, including the workspace dependency its runtime declares (omitted for runtimes without one).
// @Tags bot-agents
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param id path string true "Agent ID"
// @Success 200 {object} botagents.BotAgent
// @Failure 403 {object} server.Problem
// @Failure 404 {object} server.Problem
// @Router /bots/{bot_id}/agents/{id} [get].
func (h *BotAgentsHandler) Get(c echo.Context) error {
	bot, err := h.authorizeBot(c, bots.PermissionChat)
	if err != nil {
		return err
	}
	agent, err := h.service.Get(c.Request().Context(), bot.ID, strings.TrimSpace(c.Param("id")))
	if err != nil {
		return h.publicError("get", err)
	}
	return c.JSON(http.StatusOK, h.present(agent, bot))
}

// Update godoc
// @Summary Update a bot Agent
// @Description Update an Agent's name, availability, or runtime configuration
// @Tags bot-agents
// @Accept json
// @Produce json
// @Param bot_id path string true "Bot ID"
// @Param id path string true "Agent ID"
// @Param payload body botagents.UpdateRequest true "Agent changes"
// @Success 200 {object} botagents.BotAgent
// @Failure 400 {object} server.Problem
// @Failure 403 {object} server.Problem
// @Failure 404 {object} server.Problem
// @Failure 409 {object} server.Problem
// @Router /bots/{bot_id}/agents/{id} [patch].
func (h *BotAgentsHandler) Update(c echo.Context) error {
	bot, err := h.authorizeBot(c, bots.PermissionManage)
	if err != nil {
		return err
	}
	var req botagents.UpdateRequest
	if err := c.Bind(&req); err != nil {
		return apperror.New(apperror.CodeBotAgentInvalidMetadata, nil)
	}
	agent, err := h.service.Update(c.Request().Context(), bot.ID, strings.TrimSpace(c.Param("id")), req)
	if err != nil {
		return h.publicError("update", err)
	}
	if req.Metadata != nil {
		h.runtimes.ResetBotAgent(botagents.SessionRuntime(agent.Runtime), bot.ID, agent.ID)
	}
	return c.JSON(http.StatusOK, h.present(agent, bot))
}

// Delete godoc
// @Summary Delete a bot Agent
// @Description Soft-delete an Agent while preserving existing session bindings
// @Tags bot-agents
// @Param bot_id path string true "Bot ID"
// @Param id path string true "Agent ID"
// @Success 204 "No Content"
// @Failure 403 {object} server.Problem
// @Failure 404 {object} server.Problem
// @Failure 409 {object} server.Problem
// @Router /bots/{bot_id}/agents/{id} [delete].
func (h *BotAgentsHandler) Delete(c echo.Context) error {
	botID, err := h.authorize(c, bots.PermissionManage)
	if err != nil {
		return err
	}
	botAgentID := strings.TrimSpace(c.Param("id"))
	err = h.service.Delete(c.Request().Context(), botID, botAgentID, func(agent botagents.BotAgent) error {
		purgeErr := application.ExternalRuntimeError(h.runtimes.PurgeBotAgentAuth(c.Request().Context(), agent.Runtime, botID, botAgentID))
		if purgeErr != nil && apperror.CodeOf(purgeErr) == "" {
			return apperror.Wrap(apperror.CodeAgentCredentialMaterializationFailed, purgeErr, nil)
		}
		return purgeErr
	})
	if err != nil {
		if apperror.CodeOf(err) != "" {
			return err
		}
		return h.publicError("delete", err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *BotAgentsHandler) authorize(c echo.Context, permission string) (string, error) {
	bot, err := h.authorizeBot(c, permission)
	if err != nil {
		return "", err
	}
	return bot.ID, nil
}

func (h *BotAgentsHandler) authorizeBot(c echo.Context, permission string) (bots.Bot, error) {
	channelIdentityID, err := RequireChannelIdentityID(c)
	if err != nil {
		return bots.Bot{}, err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return bots.Bot{}, apperror.New(apperror.CodeBotAgentNotFound, nil)
	}
	return AuthorizeBotAccessWithPermission(c.Request().Context(), h.botService, h.accountService, channelIdentityID, botID, permission)
}

func (*BotAgentsHandler) publicError(operation string, err error) error {
	if publicErr := botAgentHTTPError(err); publicErr != nil {
		return publicErr
	}
	return errs.Wrap(err, "bot Agent operation", slog.String("operation", operation))
}

func botAgentHTTPError(err error) error {
	switch {
	case errors.Is(err, botagents.ErrNotFound):
		return apperror.Wrap(apperror.CodeBotAgentNotFound, err, nil)
	case errors.Is(err, botagents.ErrNameTaken):
		return apperror.Wrap(apperror.CodeBotAgentNameTaken, err, nil)
	case errors.Is(err, botagents.ErrProviderDirectRuntime):
		var direct *botagents.ProviderDirectRuntimeError
		args := map[string]string{}
		if errors.As(err, &direct) {
			args["runtime"] = direct.Runtime
		}
		return apperror.Wrap(apperror.CodeBotAgentProviderDirectRuntime, err, args)
	case errors.Is(err, botagents.ErrInvalidRuntime):
		return apperror.Wrap(apperror.CodeBotAgentInvalidRuntime, err, nil)
	case errors.Is(err, botagents.ErrInvalidMetadata):
		return apperror.Wrap(apperror.CodeBotAgentInvalidMetadata, err, nil)
	case errors.Is(err, botagents.ErrDefaultInUse):
		return apperror.Wrap(apperror.CodeBotAgentDefaultInUse, err, nil)
	case errors.Is(err, botagents.ErrUnavailable):
		return apperror.Wrap(apperror.CodeBotAgentUnavailable, err, nil)
	}
	var configErr *botagents.ConfigurationError
	if errors.As(err, &configErr) {
		return apperror.Wrap(apperror.CodeBotAgentUnavailable, err, map[string]string{"field": configErr.Field})
	}
	return nil
}

// present decorates a stored Agent with what clients read but the row does
// not hold. The driver-declared workspace dependency lets the web run the
// install preflight before enabling it: direct agents share the runtimekind
// vocabulary with their driver (botagents.RuntimeCodex == codex.RuntimeType),
// so the agent's runtime is the lookup key and runtimes without a declaration
// leave it nil. An ACP instance that predates per-instance setups gets the
// setup it currently launches with.
func (h *BotAgentsHandler) present(agent botagents.BotAgent, bot bots.Bot) botagents.BotAgent {
	agent.Dependency = dependencyFor(h.runtimes.RequiredDependencies(), agent.Runtime)
	return botagents.WithACPSetup(agent, bot.Metadata)
}

func dependencyFor(requirements map[string]external.DependencyRequirement, runtime string) *botagents.DependencyRequirement {
	requirement, ok := requirements[strings.TrimSpace(runtime)]
	if !ok {
		return nil
	}
	return &botagents.DependencyRequirement{DependencyID: requirement.DependencyID}
}

// RuntimeControls godoc
// @Summary Get bot Agent default runtime controls
// @Tags bot-agents
// @Param bot_id path string true "Bot ID"
// @Param id path string true "Agent ID"
// @Success 200 {object} external.Controls
// @Failure 403 {object} server.Problem
// @Failure 404 {object} server.Problem
// @Failure 500 {object} server.Problem
// @Router /bots/{bot_id}/agents/{id}/runtime-controls [get].
func (h *BotAgentsHandler) RuntimeControls(c echo.Context) error {
	botID, err := h.authorize(c, bots.PermissionWorkspaceExec)
	if err != nil {
		return err
	}
	agent, err := h.service.Get(c.Request().Context(), botID, strings.TrimSpace(c.Param("id")))
	if err != nil {
		return h.publicError("runtime controls", err)
	}
	var driver external.Driver
	for _, candidate := range h.runtimes {
		if candidate != nil && candidate.RuntimeType() == agent.Runtime {
			driver = candidate
			break
		}
	}
	controls, err := external.ReadControls(c.Request().Context(), driver, external.PromptInput{BotID: botID, BotAgentID: agent.ID, RuntimeMetadata: agent.Metadata})
	if err != nil {
		return runtimeControlError(err)
	}
	return c.JSON(http.StatusOK, controls)
}
