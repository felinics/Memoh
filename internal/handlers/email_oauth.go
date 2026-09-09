package handlers

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/auth"
	"github.com/felinics/memoh/internal/email"
	emailgmail "github.com/felinics/memoh/internal/email/adapters/gmail"
	"github.com/felinics/memoh/internal/oauthclients"
)

const emailOAuthCallbackPath = "/api/email/oauth/callback"

// EmailOAuthHandler handles the OAuth2 authorization flow for Gmail providers.
type EmailOAuthHandler struct {
	service      *email.Service
	tokenStore   email.OAuthTokenStore
	oauthClients oauthclients.Resolver
	callbackURL  string
	logger       *slog.Logger
}

type emailOAuthStatusResponse struct {
	Provider     string     `json:"provider"`
	Configured   bool       `json:"configured"`
	HasToken     bool       `json:"has_token"`
	Expired      bool       `json:"expired"`
	EmailAddress string     `json:"email_address,omitempty"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
}

func NewEmailOAuthHandler(log *slog.Logger, service *email.Service, tokenStore email.OAuthTokenStore, oauthClients oauthclients.Resolver, callbackURL string) *EmailOAuthHandler {
	return &EmailOAuthHandler{
		service:      service,
		tokenStore:   tokenStore,
		oauthClients: oauthClients,
		callbackURL:  callbackURL,
		logger:       log.With(slog.String("handler", "email_oauth")),
	}
}

func (h *EmailOAuthHandler) Register(e *echo.Echo) {
	e.GET("/email-providers/:id/oauth/authorize", h.Authorize)
	e.GET("/email-providers/:id/oauth/status", h.Status)
	e.DELETE("/email-providers/:id/oauth/token", h.Revoke)
	e.GET("/email/oauth/callback", h.Callback)
	e.GET(emailOAuthCallbackPath, h.Callback)
}

// Authorize godoc
// @Summary Start OAuth2 authorization for an email provider
// @Description Returns the authorization URL to redirect the user to
// @Tags email-oauth
// @Param id path string true "Email provider ID"
// @Success 200 {object} map[string]string
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /email-providers/{id}/oauth/authorize [get].
func (h *EmailOAuthHandler) Authorize(c echo.Context) error {
	userID, err := auth.UserIDFromContext(c)
	if err != nil {
		return err
	}
	providerID := strings.TrimSpace(c.Param("id"))
	if providerID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "id is required")
	}

	provider, err := h.service.GetProvider(c.Request().Context(), userID, providerID)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "provider not found")
	}

	adapter := emailgmail.New(h.logger, h.tokenStore, h.oauthClients)
	callbackURL := adapter.EffectiveRedirectURI(h.effectiveCallbackURL(c))
	state, err := generateState(callbackURL)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to generate state")
	}

	if err := h.tokenStore.SetPendingState(c.Request().Context(), providerID, state); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to store state")
	}

	var authURL string
	if email.ProviderName(provider.Provider) == emailgmail.ProviderName {
		if !isProviderConfigured(provider, adapter) {
			return echo.NewHTTPError(http.StatusBadRequest, "gmail oauth is not configured")
		}
		authURL, err = adapter.AuthorizeURL(callbackURL, state)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
	}
	if authURL == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "provider does not support OAuth2")
	}

	return c.JSON(http.StatusOK, map[string]string{"auth_url": authURL})
}

// Callback godoc
// @Summary OAuth2 callback for email providers
// @Description Handles the OAuth2 callback, exchanges the code for tokens
// @Tags email-oauth
// @Param code query string true "Authorization code"
// @Param state query string true "State parameter"
// @Success 200 {object} map[string]string
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /email/oauth/callback [get].
func (h *EmailOAuthHandler) Callback(c echo.Context) error {
	code := strings.TrimSpace(c.QueryParam("code"))
	state := strings.TrimSpace(c.QueryParam("state"))

	if code == "" {
		return renderEmailOAuthCallbackResult(c, http.StatusBadRequest, "", "error", "code is required")
	}
	if state == "" {
		return renderEmailOAuthCallbackResult(c, http.StatusBadRequest, "", "error", "state is required")
	}

	ctx := c.Request().Context()

	stored, err := h.tokenStore.GetByState(ctx, state)
	if err != nil {
		h.logger.Error("oauth callback: state not found", slog.String("state", state), slog.Any("error", err))
		return renderEmailOAuthCallbackResult(c, http.StatusBadRequest, "", "error", "invalid or expired state")
	}

	provider, err := h.service.GetProviderInternal(ctx, stored.ProviderID)
	if err != nil {
		return renderEmailOAuthCallbackResult(c, http.StatusInternalServerError, stored.ProviderID, "error", "provider not found")
	}

	if email.ProviderName(provider.Provider) != emailgmail.ProviderName {
		return renderEmailOAuthCallbackResult(c, http.StatusBadRequest, stored.ProviderID, "error", "provider does not support OAuth2")
	}
	adapter := emailgmail.New(h.logger, h.tokenStore, h.oauthClients)
	redirectURI := callbackURLFromState(state)
	if redirectURI == "" {
		redirectURI = adapter.EffectiveRedirectURI(h.effectiveCallbackURL(c))
	}
	if err := adapter.ExchangeCode(ctx, provider.Config, stored.ProviderID, code, redirectURI); err != nil {
		h.logger.Error("gmail code exchange failed", slog.Any("error", err))
		return renderEmailOAuthCallbackResult(c, http.StatusInternalServerError, stored.ProviderID, "error", "token exchange failed")
	}

	h.logger.Info("email oauth authorized", slog.String("provider_id", stored.ProviderID), slog.String("provider", provider.Provider))
	return renderEmailOAuthCallbackResult(c, http.StatusOK, stored.ProviderID, "success", "")
}

// Status godoc
// @Summary Get OAuth2 status for an email provider
// @Tags email-oauth
// @Param id path string true "Email provider ID"
// @Success 200 {object} emailOAuthStatusResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /email-providers/{id}/oauth/status [get].
func (h *EmailOAuthHandler) Status(c echo.Context) error {
	userID, err := auth.UserIDFromContext(c)
	if err != nil {
		return err
	}
	providerID := strings.TrimSpace(c.Param("id"))
	if providerID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "id is required")
	}

	ctx := c.Request().Context()
	provider, err := h.service.GetProvider(ctx, userID, providerID)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "provider not found")
	}
	if !supportsEmailOAuth(email.ProviderName(provider.Provider)) {
		return echo.NewHTTPError(http.StatusBadRequest, "provider does not support OAuth2")
	}

	resp := emailOAuthStatusResponse{
		Provider:   provider.Provider,
		Configured: isProviderConfigured(provider, emailgmail.New(h.logger, h.tokenStore, h.oauthClients)),
	}

	token, err := h.tokenStore.Get(ctx, providerID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusOK, resp)
		}
		h.logger.Error("email oauth status failed", slog.Any("error", err))
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to load oauth status")
	}

	resp.HasToken = token.AccessToken != ""
	resp.EmailAddress = token.EmailAddress
	if !token.ExpiresAt.IsZero() {
		expiresAt := token.ExpiresAt
		resp.ExpiresAt = &expiresAt
		resp.Expired = time.Now().After(token.ExpiresAt)
	}

	return c.JSON(http.StatusOK, resp)
}

// Revoke godoc
// @Summary Revoke stored OAuth2 tokens for an email provider
// @Tags email-oauth
// @Param id path string true "Email provider ID"
// @Success 204 "No Content"
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /email-providers/{id}/oauth/token [delete].
func (h *EmailOAuthHandler) Revoke(c echo.Context) error {
	userID, err := auth.UserIDFromContext(c)
	if err != nil {
		return err
	}
	providerID := strings.TrimSpace(c.Param("id"))
	if providerID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "id is required")
	}

	ctx := c.Request().Context()
	provider, err := h.service.GetProvider(ctx, userID, providerID)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "provider not found")
	}
	if !supportsEmailOAuth(email.ProviderName(provider.Provider)) {
		return echo.NewHTTPError(http.StatusBadRequest, "provider does not support OAuth2")
	}

	if err := h.tokenStore.Delete(ctx, providerID); err != nil {
		h.logger.Error("email oauth revoke failed", slog.Any("error", err))
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to revoke oauth token")
	}

	return c.NoContent(http.StatusNoContent)
}

func supportsEmailOAuth(name email.ProviderName) bool {
	return name == emailgmail.ProviderName
}

func isProviderConfigured(provider email.ProviderResponse, adapter *emailgmail.Adapter) bool {
	config := provider.Config
	if config == nil {
		config = map[string]any{}
	}
	if email.ProviderName(provider.Provider) != emailgmail.ProviderName {
		return false
	}
	emailAddress, _ := config["email_address"].(string)
	return strings.TrimSpace(emailAddress) != "" && adapter.HasOAuthClient()
}

func generateState(callbackURL string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	state := hex.EncodeToString(b)
	if callbackURL == "" {
		return state, nil
	}
	return state + "." + base64.RawURLEncoding.EncodeToString([]byte(callbackURL)), nil
}

func (h *EmailOAuthHandler) effectiveCallbackURL(c echo.Context) string {
	if baseURL := requestBaseURL(c.Request()); baseURL != "" {
		return strings.TrimRight(baseURL, "/") + emailOAuthCallbackPath
	}
	return h.callbackURL
}

func requestBaseURL(req *http.Request) string {
	if origin := normalizeOrigin(req.Header.Get(echo.HeaderOrigin)); origin != "" {
		return origin
	}
	if referer := normalizeOrigin(req.Referer()); referer != "" {
		return referer
	}

	host := firstHeaderValue(req.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = strings.TrimSpace(req.Host)
	}
	if host == "" {
		return ""
	}
	proto := firstHeaderValue(req.Header.Get(echo.HeaderXForwardedProto))
	if proto == "" {
		if req.TLS != nil {
			proto = "https"
		} else {
			proto = "http"
		}
	}
	if port := firstHeaderValue(req.Header.Get("X-Forwarded-Port")); port != "" &&
		!strings.Contains(host, ":") &&
		(proto != "https" || port != "443") &&
		(proto != "http" || port != "80") {
		host += ":" + port
	}
	return proto + "://" + host
}

func normalizeOrigin(raw string) string {
	origin := firstHeaderValue(raw)
	if origin == "" {
		return ""
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}

func firstHeaderValue(raw string) string {
	if raw == "" {
		return ""
	}
	parts := strings.Split(raw, ",")
	return strings.TrimSpace(parts[0])
}

func callbackURLFromState(state string) string {
	_, encoded, ok := strings.Cut(state, ".")
	if !ok || encoded == "" {
		return ""
	}
	callbackURL, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(callbackURL))
}

func renderEmailOAuthCallbackResult(c echo.Context, statusCode int, providerID, status, errorMessage string) error {
	page := template.Must(template.New("email-oauth-result").Parse(`<!doctype html>
<html>
  <head>
    <meta charset="utf-8">
    <title>{{if eq .Status "success"}}Gmail OAuth Connected{{else}}Gmail OAuth Failed{{end}}</title>
  </head>
  <body style="font-family: sans-serif; padding: 24px;">
    {{if eq .Status "success"}}
      <h2>Gmail OAuth connected</h2>
      <p>You can close this window and return to Memoh.</p>
    {{else}}
      <h2>Gmail OAuth failed</h2>
      <p>{{.Error}}</p>
    {{end}}
    <script>
      window.opener?.postMessage({
        type: "memoh-email-oauth-callback",
        status: "{{.Status}}",
        providerId: "{{.ProviderID}}",
        error: "{{.Error}}"
      }, "*");
      setTimeout(() => window.close(), 300);
    </script>
  </body>
</html>`))

	return c.HTML(statusCode, executeHTMLTemplate(page, map[string]string{
		"ProviderID": providerID,
		"Status":     status,
		"Error":      errorMessage,
	}))
}
