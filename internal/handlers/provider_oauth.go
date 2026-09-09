package handlers

import (
	"html/template"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/auth"
	"github.com/felinics/memoh/internal/oauthctx"
	"github.com/felinics/memoh/internal/providers"
)

type ProviderOAuthHandler struct {
	service *providers.Service
}

func NewProviderOAuthHandler(service *providers.Service) *ProviderOAuthHandler {
	return &ProviderOAuthHandler{service: service}
}

func (h *ProviderOAuthHandler) Register(e *echo.Echo) {
	e.GET("/providers/:id/oauth/authorize", h.Authorize)
	e.POST("/providers/:id/oauth/poll", h.Poll)
	e.GET("/providers/:id/oauth/status", h.Status)
	e.DELETE("/providers/:id/oauth/token", h.Revoke)
	e.GET("/auth/callback", h.Callback)
	e.GET("/providers/oauth/callback", h.Callback)
}

// Authorize godoc
// @Summary Start OAuth2 authorization for an LLM provider
// @Tags providers-oauth
// @Param id path string true "Provider ID (UUID)"
// @Success 200 {object} providers.OAuthAuthorizeResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /providers/{id}/oauth/authorize [get].
func (h *ProviderOAuthHandler) Authorize(c echo.Context) error {
	providerID := strings.TrimSpace(c.Param("id"))
	if providerID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "id is required")
	}
	ctx := c.Request().Context()
	if userID, err := auth.UserIDFromContext(c); err == nil {
		ctx = oauthctx.WithUserID(ctx, userID)
	}
	resp, err := h.service.StartOAuthAuthorization(ctx, providerID)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	return c.JSON(http.StatusOK, resp)
}

// Poll godoc
// @Summary Poll OAuth device authorization for an LLM provider
// @Tags providers-oauth
// @Param id path string true "Provider ID (UUID)"
// @Success 200 {object} providers.OAuthStatus
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /providers/{id}/oauth/poll [post].
func (h *ProviderOAuthHandler) Poll(c echo.Context) error {
	providerID := strings.TrimSpace(c.Param("id"))
	if providerID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "id is required")
	}
	ctx := c.Request().Context()
	if userID, err := auth.UserIDFromContext(c); err == nil {
		ctx = oauthctx.WithUserID(ctx, userID)
	}
	status, err := h.service.PollOAuthAuthorization(ctx, providerID)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	return c.JSON(http.StatusOK, status)
}

// Status godoc
// @Summary Get OAuth2 status for an LLM provider
// @Tags providers-oauth
// @Param id path string true "Provider ID (UUID)"
// @Success 200 {object} providers.OAuthStatus
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /providers/{id}/oauth/status [get].
func (h *ProviderOAuthHandler) Status(c echo.Context) error {
	providerID := strings.TrimSpace(c.Param("id"))
	if providerID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "id is required")
	}
	ctx := c.Request().Context()
	if userID, err := auth.UserIDFromContext(c); err == nil {
		ctx = oauthctx.WithUserID(ctx, userID)
	}
	status, err := h.service.GetOAuthStatus(ctx, providerID)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	return c.JSON(http.StatusOK, status)
}

// Revoke godoc
// @Summary Revoke stored OAuth2 tokens for an LLM provider
// @Tags providers-oauth
// @Param id path string true "Provider ID (UUID)"
// @Success 204 "No Content"
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /providers/{id}/oauth/token [delete].
func (h *ProviderOAuthHandler) Revoke(c echo.Context) error {
	providerID := strings.TrimSpace(c.Param("id"))
	if providerID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "id is required")
	}
	ctx := c.Request().Context()
	if userID, err := auth.UserIDFromContext(c); err == nil {
		ctx = oauthctx.WithUserID(ctx, userID)
	}
	if err := h.service.RevokeOAuthToken(ctx, providerID); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	return c.NoContent(http.StatusNoContent)
}

// Callback godoc
// @Summary OAuth2 callback for LLM providers
// @Tags providers-oauth
// @Param code query string true "Authorization code"
// @Param state query string true "State parameter"
// @Success 200 {string} string "HTML success page"
// @Failure 400 {object} ErrorResponse
// @Router /providers/oauth/callback [get].
func (h *ProviderOAuthHandler) Callback(c echo.Context) error {
	code := strings.TrimSpace(c.QueryParam("code"))
	state := strings.TrimSpace(c.QueryParam("state"))
	if code == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "code is required")
	}
	if state == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "state is required")
	}
	providerID, err := h.service.HandleOAuthCallback(c.Request().Context(), state, code)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	page := template.Must(template.New("oauth-success").Parse(`<!doctype html>
<html>
  <head>
    <meta charset="utf-8">
    <title>Provider Connected</title>
  </head>
  <body style="font-family: sans-serif; padding: 24px;">
    <h2>Provider connected</h2>
    <p>Your current Memoh account is now connected.</p>
    <script>
      window.opener?.postMessage({ type: "memoh-provider-oauth-success", providerId: "{{.ProviderID}}" }, "*");
      window.close();
    </script>
  </body>
</html>`))
	return c.HTML(http.StatusOK, executeHTMLTemplate(page, map[string]string{"ProviderID": providerID}))
}

func executeHTMLTemplate(tpl *template.Template, data any) string {
	var b strings.Builder
	_ = tpl.Execute(&b, data)
	return b.String()
}
