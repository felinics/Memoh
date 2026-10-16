package handlers

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/agent/application"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/httpx"
)

// StarterPromptsHandler serves the new-chat suggestions of a bot. It owns
// authorization and response mapping; the material, hashing and generation
// live in the application service.
type StarterPromptsHandler struct {
	agentService   *application.Service
	botService     *bots.Service
	accountService *accounts.Service
}

func NewStarterPromptsHandler(agentService *application.Service, botService *bots.Service, accountService *accounts.Service) *StarterPromptsHandler {
	return &StarterPromptsHandler{agentService: agentService, botService: botService, accountService: accountService}
}

func (h *StarterPromptsHandler) Register(e *echo.Echo) {
	e.GET("/bots/:bot_id/starter-prompts", h.Get)
	e.POST("/bots/:bot_id/starter-prompts/refresh", h.Refresh)
}

// StarterPromptsRefreshRequest asks for a different set than the one on screen.
type StarterPromptsRefreshRequest struct {
	// Locale is the BCP 47 language tag the suggestions are written in. Defaults to en.
	Locale string `json:"locale"`
	// Avoid lists the prompts currently shown, which the new set must not repeat.
	Avoid []string `json:"avoid"`
}

// StarterPrompt is one suggested message for the new-chat screen.
type StarterPrompt struct {
	Title  string `json:"title" validate:"required"`
	Prompt string `json:"prompt" validate:"required"`
}

// StarterPromptsResponse is either `unchanged` (the set the client holds is
// still current) or a fresh set with its `hash`. An empty `hash` with no
// items means no set could be generated this time.
type StarterPromptsResponse struct {
	Unchanged bool            `json:"unchanged" validate:"required"`
	Hash      string          `json:"hash" validate:"required"`
	Items     []StarterPrompt `json:"items" validate:"required"`
}

// Get godoc
// @Summary Get new-chat starter prompts
// @Description Suggestions for the current user's new chat with a bot, generated from the bot's memory, the user's recent session titles, the bot's installed Apps and its name. The server keeps nothing: pass the hash of the set you hold as known_hash, and the response says unchanged while that set is still current; otherwise a new set is generated, which can take a while.
// @Tags bots
// @Param bot_id path string true "Bot ID"
// @Param locale query string false "BCP 47 language tag the suggestions are written in. Defaults to en."
// @Param known_hash query string false "Hash of the set the client already holds."
// @Success 200 {object} StarterPromptsResponse
// @Failure 400 {object} server.Problem
// @Failure 403 {object} server.Problem
// @Failure 500 {object} server.Problem
// @Router /bots/{bot_id}/starter-prompts [get].
func (h *StarterPromptsHandler) Get(c echo.Context) error {
	return h.respond(c, func(botID, userID string) application.StarterPromptsRequest {
		return application.StarterPromptsRequest{
			BotID:     botID,
			UserID:    userID,
			Locale:    c.QueryParam("locale"),
			KnownHash: c.QueryParam("known_hash"),
		}
	})
}

// Refresh godoc
// @Summary Generate a different set of new-chat starter prompts
// @Description Generates a new set for the current user's new chat with a bot, steering away from the prompts already on screen. The returned hash is the same one Get reports for the current material, so later Get calls answer unchanged for the refreshed set.
// @Tags bots
// @Param bot_id path string true "Bot ID"
// @Param payload body StarterPromptsRefreshRequest true "Locale and the prompts on screen"
// @Success 200 {object} StarterPromptsResponse
// @Failure 400 {object} server.Problem
// @Failure 403 {object} server.Problem
// @Failure 500 {object} server.Problem
// @Router /bots/{bot_id}/starter-prompts/refresh [post].
func (h *StarterPromptsHandler) Refresh(c echo.Context) error {
	var body StarterPromptsRefreshRequest
	if err := c.Bind(&body); err != nil {
		return apperror.FieldInvalid("body", err)
	}
	return h.respond(c, func(botID, userID string) application.StarterPromptsRequest {
		return application.StarterPromptsRequest{
			BotID:   botID,
			UserID:  userID,
			Locale:  body.Locale,
			Refresh: true,
			Avoid:   body.Avoid,
		}
	})
}

// respond authorizes the caller to chat with the bot, runs the request the
// build function makes for it, and writes the response.
func (h *StarterPromptsHandler) respond(c echo.Context, build func(botID, userID string) application.StarterPromptsRequest) error {
	userID, err := RequireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID, err := httpx.RequiredParam(c, "bot_id")
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	if _, err := AuthorizeBotAccessWithPermission(ctx, h.botService, h.accountService, userID, botID, bots.PermissionChat); err != nil {
		return err
	}
	result, err := h.agentService.StarterPrompts(ctx, build(botID, userID))
	if err != nil {
		return errs.Wrap(err, "starter prompts")
	}
	items := make([]StarterPrompt, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, StarterPrompt{Title: item.Title, Prompt: item.Prompt})
	}
	return c.JSON(http.StatusOK, StarterPromptsResponse{Unchanged: result.Unchanged, Hash: result.Hash, Items: items})
}
