package handlers

import (
	"context"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/mcp"
)

// MCPOAuthHandler handles OAuth-related endpoints for MCP connections.
type MCPOAuthHandler struct {
	oauthService   *mcp.OAuthService
	connService    *mcp.ConnectionService
	botService     *bots.Service
	accountService *accounts.Service
	logger         *slog.Logger
}

func NewMCPOAuthHandler(log *slog.Logger, oauthService *mcp.OAuthService, connService *mcp.ConnectionService, botService *bots.Service, accountService *accounts.Service) *MCPOAuthHandler {
	return &MCPOAuthHandler{
		oauthService:   oauthService,
		connService:    connService,
		botService:     botService,
		accountService: accountService,
		logger:         log.With(slog.String("handler", "mcp_oauth")),
	}
}

func (h *MCPOAuthHandler) Register(e *echo.Echo) {
	group := e.Group("/bots/:bot_id/mcp/:id/oauth")
	group.POST("/discover", h.Discover)
	group.POST("/authorize", h.Authorize)
	group.GET("/status", h.Status)
	group.DELETE("/token", h.RevokeToken)
	group.POST("/exchange", h.Exchange)
	e.GET("/oauth/mcp/callback", h.Callback)
	e.GET("/api/oauth/mcp/callback", h.Callback)
}

type oauthDiscoverRequest struct {
	URL string `json:"url"`
}

// Discover godoc
// @Summary Discover OAuth configuration for MCP server
// @Description Probe MCP server URL for OAuth requirements and discover authorization server metadata
// @Tags mcp
// @Param id path string true "MCP connection ID"
// @Param payload body oauthDiscoverRequest false "Optional URL override"
// @Success 200 {object} mcp.DiscoveryResult
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /bots/{bot_id}/mcp/{id}/oauth/discover [post].
func (h *MCPOAuthHandler) Discover(c echo.Context) error {
	userID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	connID := strings.TrimSpace(c.Param("id"))
	if botID == "" || connID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot_id and id are required")
	}
	if _, err := h.authorizeBotAccess(c.Request().Context(), userID, botID); err != nil {
		return err
	}

	conn, err := h.connService.Get(c.Request().Context(), botID, connID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return echo.NewHTTPError(http.StatusNotFound, "mcp connection not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	var req oauthDiscoverRequest
	_ = c.Bind(&req)

	serverURL := strings.TrimSpace(req.URL)
	if serverURL == "" {
		if configURL, ok := conn.Config["url"].(string); ok {
			serverURL = strings.TrimSpace(configURL)
		}
	}
	if serverURL == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "MCP server URL is required for OAuth discovery")
	}

	result, err := h.oauthService.Discover(c.Request().Context(), serverURL)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	if err := h.oauthService.SaveDiscovery(c.Request().Context(), connID, result); err != nil {
		h.logger.Error("failed to save discovery result", slog.Any("error", err))
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to save discovery result")
	}

	return c.JSON(http.StatusOK, result)
}

type oauthAuthorizeRequest struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"` //nolint:gosec // intentional: OAuth client_secret is a required API parameter
	CallbackURL  string `json:"callback_url"`
}

// Authorize godoc
// @Summary Start OAuth authorization flow
// @Description Generate PKCE and return authorization URL for the user to authorize
// @Tags mcp
// @Param id path string true "MCP connection ID"
// @Param payload body oauthAuthorizeRequest false "Optional client_id"
// @Success 200 {object} mcp.AuthorizeResult
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /bots/{bot_id}/mcp/{id}/oauth/authorize [post].
func (h *MCPOAuthHandler) Authorize(c echo.Context) error {
	userID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	connID := strings.TrimSpace(c.Param("id"))
	if botID == "" || connID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot_id and id are required")
	}
	if _, err := h.authorizeBotAccess(c.Request().Context(), userID, botID); err != nil {
		return err
	}

	var req oauthAuthorizeRequest
	_ = c.Bind(&req)

	result, err := h.oauthService.StartAuthorization(c.Request().Context(), connID, req.ClientID, req.ClientSecret, req.CallbackURL)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	return c.JSON(http.StatusOK, result)
}

type oauthExchangeRequest struct {
	Code  string `json:"code"`
	State string `json:"state"`
}

// Exchange godoc
// @Summary Exchange OAuth authorization code for tokens
// @Description Frontend callback page calls this to exchange the authorization code for access/refresh tokens
// @Tags mcp
// @Param payload body oauthExchangeRequest true "Authorization code and state"
// @Success 200 {object} map[string]bool
// @Failure 400 {object} ErrorResponse
// @Router /bots/{bot_id}/mcp/{id}/oauth/exchange [post].
func (h *MCPOAuthHandler) Exchange(c echo.Context) error {
	var req oauthExchangeRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}

	code := strings.TrimSpace(req.Code)
	state := strings.TrimSpace(req.State)
	if code == "" || state == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "code and state are required")
	}

	_, err := h.oauthService.HandleCallback(c.Request().Context(), state, code)
	if err != nil {
		h.logger.Warn("oauth exchange failed", slog.Any("error", err))
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	return c.JSON(http.StatusOK, map[string]bool{"success": true})
}

// Callback godoc
// @Summary OAuth callback for MCP connections
// @Description Exchanges the authorization code and renders a small completion page
// @Tags mcp
// @Param code query string false "Authorization code"
// @Param state query string false "State parameter"
// @Param error query string false "OAuth error"
// @Param error_description query string false "OAuth error description"
// @Success 200 {string} string "HTML result page"
// @Failure 400 {string} string "HTML error page"
// @Router /oauth/mcp/callback [get].
func (h *MCPOAuthHandler) Callback(c echo.Context) error {
	if errorParam := strings.TrimSpace(c.QueryParam("error")); errorParam != "" {
		errorDesc := strings.TrimSpace(c.QueryParam("error_description"))
		message := errorParam
		if errorDesc != "" {
			message += ": " + errorDesc
		}
		return renderMCPOAuthCallbackResult(c, http.StatusBadRequest, "error", message)
	}

	code := strings.TrimSpace(c.QueryParam("code"))
	state := strings.TrimSpace(c.QueryParam("state"))
	if code == "" {
		return renderMCPOAuthCallbackResult(c, http.StatusBadRequest, "error", "code is required")
	}
	if state == "" {
		return renderMCPOAuthCallbackResult(c, http.StatusBadRequest, "error", "state is required")
	}

	if _, err := h.oauthService.HandleCallback(c.Request().Context(), state, code); err != nil {
		h.logger.Warn("oauth callback failed", slog.Any("error", err))
		return renderMCPOAuthCallbackResult(c, http.StatusBadRequest, "error", err.Error())
	}
	return renderMCPOAuthCallbackResult(c, http.StatusOK, "success", "")
}

// Status godoc
// @Summary Get OAuth status for MCP connection
// @Description Returns the current OAuth status including whether tokens are available
// @Tags mcp
// @Param id path string true "MCP connection ID"
// @Success 200 {object} mcp.OAuthStatus
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /bots/{bot_id}/mcp/{id}/oauth/status [get].
func (h *MCPOAuthHandler) Status(c echo.Context) error {
	userID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	connID := strings.TrimSpace(c.Param("id"))
	if botID == "" || connID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot_id and id are required")
	}
	if _, err := h.authorizeBotAccess(c.Request().Context(), userID, botID); err != nil {
		return err
	}

	status, err := h.oauthService.GetStatus(c.Request().Context(), connID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return c.JSON(http.StatusOK, status)
}

// RevokeToken godoc
// @Summary Revoke OAuth tokens for MCP connection
// @Description Clears stored OAuth tokens
// @Tags mcp
// @Param id path string true "MCP connection ID"
// @Success 204 "No Content"
// @Failure 400 {object} ErrorResponse
// @Router /bots/{bot_id}/mcp/{id}/oauth/token [delete].
func (h *MCPOAuthHandler) RevokeToken(c echo.Context) error {
	userID, err := h.requireChannelIdentityID(c)
	if err != nil {
		return err
	}
	botID := strings.TrimSpace(c.Param("bot_id"))
	connID := strings.TrimSpace(c.Param("id"))
	if botID == "" || connID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot_id and id are required")
	}
	if _, err := h.authorizeBotAccess(c.Request().Context(), userID, botID); err != nil {
		return err
	}

	if err := h.oauthService.RevokeToken(c.Request().Context(), connID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return c.NoContent(http.StatusNoContent)
}

func (*MCPOAuthHandler) requireChannelIdentityID(c echo.Context) (string, error) {
	return RequireChannelIdentityID(c)
}

func (h *MCPOAuthHandler) authorizeBotAccess(ctx context.Context, channelIdentityID, botID string) (bots.Bot, error) {
	return AuthorizeBotAccess(ctx, h.botService, h.accountService, channelIdentityID, botID)
}

func renderMCPOAuthCallbackResult(c echo.Context, status int, result string, message string) error {
	page := template.Must(template.New("mcp-oauth-result").Parse(`<!doctype html>
<html>
  <head>
    <meta charset="utf-8">
    <title>MCP OAuth</title>
  </head>
  <body style="font-family: sans-serif; padding: 24px;">
    <h2>{{if eq .Result "success"}}MCP server connected{{else}}MCP authorization failed{{end}}</h2>
    {{if .Message}}<p>{{.Message}}</p>{{else}}<p>You can close this window and return to Memoh.</p>{{end}}
    <script>
      window.opener?.postMessage({ type: "mcp-oauth-callback", status: "{{.Result}}", error: "{{.Message}}" }, "*");
      if (window.opener) window.close();
    </script>
  </body>
</html>`))
	return c.HTML(status, executeHTMLTemplate(page, map[string]string{
		"Result":  result,
		"Message": message,
	}))
}
