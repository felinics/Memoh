package handlers

import (
	"context"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/providers"
)

type ACPCredentialsHandler struct {
	botService     *bots.Service
	accountService *accounts.Service
	httpClient     *http.Client
}

func NewACPCredentialsHandler(botService *bots.Service, accountService *accounts.Service) *ACPCredentialsHandler {
	return &ACPCredentialsHandler{botService: botService, accountService: accountService}
}

func (h *ACPCredentialsHandler) Register(e *echo.Echo) {
	e.POST("/bots/:bot_id/acp/agents/:agent_id/credentials/test", h.Test)
}

// Test godoc
// @Summary Test ACP agent managed API key credentials
// @Description Probe the provider endpoint behind an ACP agent's API key setup to verify reachability and authentication without starting the agent
// @Tags acp
// @Param bot_id path string true "Bot ID"
// @Param agent_id path string true "ACP agent ID"
// @Success 200 {object} providers.TestResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /bots/{bot_id}/acp/agents/{agent_id}/credentials/test [post].
func (h *ACPCredentialsHandler) Test(c echo.Context) error {
	bot, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}
	resp, err := testACPManagedCredentials(c.Request().Context(), bot.Metadata, c.Param("agent_id"), h.httpClient)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, resp)
}

func (h *ACPCredentialsHandler) requireBotAccess(c echo.Context) (bots.Bot, error) {
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return bots.Bot{}, echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	channelIdentityID, err := RequireChannelIdentityID(c)
	if err != nil {
		return bots.Bot{}, err
	}
	return AuthorizeBotAccess(c.Request().Context(), h.botService, h.accountService, channelIdentityID, botID)
}

func testACPManagedCredentials(ctx context.Context, metadata map[string]any, agentID string, httpClient *http.Client) (providers.TestResponse, error) {
	if _, ok := acpprofile.Lookup(agentID); !ok {
		return providers.TestResponse{}, echo.NewHTTPError(http.StatusNotFound, "unknown acp agent")
	}
	target, err := acpprofile.APIKeyProbeTarget(acpprofile.ParseAgentSetup(metadata, agentID))
	if err != nil {
		return providers.TestResponse{}, echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	sdkProvider := models.NewSDKProvider(target.BaseURL, target.APIKey, "", models.ClientType(target.ClientType), models.DefaultProviderProbeTimeout, httpClient)
	return providers.TestSDKCredentials(ctx, sdkProvider), nil
}
