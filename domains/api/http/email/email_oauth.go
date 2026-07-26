package email

import (
	"context"
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

	"github.com/labstack/echo/v4"

	httpx "github.com/memohai/memoh/domains/api/http"
	"github.com/memohai/memoh/domains/api/identity/auth"
	"github.com/memohai/memoh/domains/channel/email"
	"github.com/memohai/memoh/internal/apperror"
)

const emailOAuthCallbackPath = "/api/email/oauth/callback"

// EmailOAuthHandler handles the OAuth2 authorization flow for Gmail providers.
type EmailOAuthHandler struct {
	service     *email.Service
	tokenStore  email.OAuthTokenStore
	gmail       gmailOAuth
	callbackURL string
	logger      *slog.Logger
}

type gmailOAuth interface {
	HasOAuthClient() bool
	EffectiveRedirectURI(string) string
	AuthorizeURL(redirectURI, state string) (string, error)
	ExchangeCode(context.Context, map[string]any, string, string, string) error
}

type emailOAuthStatusResponse struct {
	Provider     string     `json:"provider"`
	Configured   bool       `json:"configured"`
	HasToken     bool       `json:"has_token"`
	Expired      bool       `json:"expired"`
	EmailAddress string     `json:"email_address,omitempty"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
}

func NewEmailOAuthHandler(log *slog.Logger, service *email.Service, tokenStore email.OAuthTokenStore, gmail gmailOAuth, callbackURL string) *EmailOAuthHandler {
	return &EmailOAuthHandler{
		service:     service,
		tokenStore:  tokenStore,
		gmail:       gmail,
		callbackURL: callbackURL,
		logger:      log.With(slog.String("handler", "email_oauth")),
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
// @Failure 400 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Router /email-providers/{id}/oauth/authorize [get].
func (h *EmailOAuthHandler) Authorize(c echo.Context) error {
	userID, err := auth.UserIDFromContext(c)
	if err != nil {
		return err
	}
	providerID := strings.TrimSpace(c.Param("id"))
	if providerID == "" {
		return apperror.Required("id")
	}

	provider, err := h.service.GetProvider(c.Request().Context(), userID, providerID)
	if err != nil {
		return apperror.NotFound("get email provider", err)
	}

	callbackURL := h.gmail.EffectiveRedirectURI(h.effectiveCallbackURL(c))
	state, err := generateState(callbackURL)
	if err != nil {
		return apperror.Internal("generate oauth state", err)
	}

	if err := h.tokenStore.SetPendingState(c.Request().Context(), providerID, state); err != nil {
		return apperror.Internal("store oauth state", err)
	}

	var authURL string
	if email.ProviderName(provider.Provider) == email.ProviderGmail {
		if !isProviderConfigured(provider, h.gmail) {
			return apperror.Invalid("authorize gmail oauth", nil)
		}
		authURL, err = h.gmail.AuthorizeURL(callbackURL, state)
		if err != nil {
			return apperror.Invalid("build gmail authorize url", err)
		}
	}
	if authURL == "" {
		return apperror.Field("provider", apperror.FieldUnsupported)
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
// @Failure 400 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
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

	if email.ProviderName(provider.Provider) != email.ProviderGmail {
		return renderEmailOAuthCallbackResult(c, http.StatusBadRequest, stored.ProviderID, "error", "provider does not support OAuth2")
	}
	redirectURI := callbackURLFromState(state)
	if redirectURI == "" {
		redirectURI = h.gmail.EffectiveRedirectURI(h.effectiveCallbackURL(c))
	}
	if err := h.gmail.ExchangeCode(ctx, provider.Config, stored.ProviderID, code, redirectURI); err != nil {
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
// @Failure 400 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Router /email-providers/{id}/oauth/status [get].
func (h *EmailOAuthHandler) Status(c echo.Context) error {
	userID, err := auth.UserIDFromContext(c)
	if err != nil {
		return err
	}
	providerID := strings.TrimSpace(c.Param("id"))
	if providerID == "" {
		return apperror.Required("id")
	}

	ctx := c.Request().Context()
	provider, err := h.service.GetProvider(ctx, userID, providerID)
	if err != nil {
		return apperror.NotFound("get email provider", err)
	}
	if !supportsEmailOAuth(email.ProviderName(provider.Provider)) {
		return apperror.Field("provider", apperror.FieldUnsupported)
	}

	resp := emailOAuthStatusResponse{
		Provider:   provider.Provider,
		Configured: isProviderConfigured(provider, h.gmail),
	}

	token, err := h.tokenStore.Get(ctx, providerID)
	if err != nil {
		if errors.Is(err, email.ErrNotFound) {
			return c.JSON(http.StatusOK, resp)
		}
		h.logger.Error("email oauth status failed", slog.Any("error", err))
		return apperror.Internal("load oauth status", err)
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
// @Failure 400 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Router /email-providers/{id}/oauth/token [delete].
func (h *EmailOAuthHandler) Revoke(c echo.Context) error {
	userID, err := auth.UserIDFromContext(c)
	if err != nil {
		return err
	}
	providerID := strings.TrimSpace(c.Param("id"))
	if providerID == "" {
		return apperror.Required("id")
	}

	ctx := c.Request().Context()
	provider, err := h.service.GetProvider(ctx, userID, providerID)
	if err != nil {
		return apperror.NotFound("get email provider", err)
	}
	if !supportsEmailOAuth(email.ProviderName(provider.Provider)) {
		return apperror.Field("provider", apperror.FieldUnsupported)
	}

	if err := h.tokenStore.Delete(ctx, providerID); err != nil {
		h.logger.Error("email oauth revoke failed", slog.Any("error", err))
		return apperror.Internal("revoke oauth token", err)
	}

	return c.NoContent(http.StatusNoContent)
}

func supportsEmailOAuth(name email.ProviderName) bool {
	return name == email.ProviderGmail
}

func isProviderConfigured(provider email.ProviderResponse, gmail gmailOAuth) bool {
	config := provider.Config
	if config == nil {
		config = map[string]any{}
	}
	if email.ProviderName(provider.Provider) != email.ProviderGmail {
		return false
	}
	emailAddress, _ := config["email_address"].(string)
	return strings.TrimSpace(emailAddress) != "" && gmail != nil && gmail.HasOAuthClient()
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

	host := httpx.FirstHeaderValue(req.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = strings.TrimSpace(req.Host)
	}
	if host == "" {
		return ""
	}
	proto := httpx.FirstHeaderValue(req.Header.Get(echo.HeaderXForwardedProto))
	if proto == "" {
		if req.TLS != nil {
			proto = "https"
		} else {
			proto = "http"
		}
	}
	if port := httpx.FirstHeaderValue(req.Header.Get("X-Forwarded-Port")); port != "" &&
		!strings.Contains(host, ":") &&
		(proto != "https" || port != "443") &&
		(proto != "http" || port != "80") {
		host += ":" + port
	}
	return proto + "://" + host
}

func normalizeOrigin(raw string) string {
	origin := httpx.FirstHeaderValue(raw)
	if origin == "" {
		return ""
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
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

	return c.HTML(statusCode, httpx.ExecuteHTMLTemplate(page, map[string]string{
		"ProviderID": providerID,
		"Status":     status,
		"Error":      errorMessage,
	}))
}
