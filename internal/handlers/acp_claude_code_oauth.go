package handlers

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/memohai/memoh/internal/accounts"
	acpprofile "github.com/memohai/memoh/internal/agent/runtime/acp/profile"
	sessionruntime "github.com/memohai/memoh/internal/agent/runtime/session"
	"github.com/memohai/memoh/internal/apperror"
	"github.com/memohai/memoh/internal/bots"
)

const (
	acpClaudeCodeOAuthStateTTL    = 30 * time.Minute
	acpClaudeCodeOAuthStatePrefix = "acp_claude_code_"

	claudeCodeOAuthClientID     = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	claudeCodeOAuthAuthorizeURL = "https://claude.ai/oauth/authorize"
	claudeCodeOAuthTokenURL     = "https://platform.claude.com/v1/oauth/token" //nolint:gosec // OAuth endpoint URL, not a credential.
	claudeCodeOAuthRedirectURI  = "https://platform.claude.com/oauth/code/callback"
	claudeCodeOAuthScope        = "user:inference"
	claudeCodeOAuthExpiresIn    = 31536000
)

type ACPClaudeCodeOAuthAuthorizeResponse struct {
	AuthURL   string `json:"auth_url"`
	SessionID string `json:"session_id"`
}

type ACPClaudeCodeOAuthExchangeRequest struct {
	SessionID string `json:"session_id"`
	Code      string `json:"code"`
}

type ACPClaudeCodeOAuthStatus struct {
	Configured bool `json:"configured"`
	HasToken   bool `json:"has_token"`
}

type ACPClaudeCodeOAuthHandler struct {
	botService     *bots.Service
	accountService *accounts.Service
	acpWorkspace   acpWorkspaceConfigProvider
	runtimeResets  oauthRuntimeResetService
	tokenURL       string
	httpClient     *http.Client

	mu     sync.Mutex
	states map[string]acpClaudeCodeOAuthState
}

type oauthRuntimeResetService interface {
	BeginBotHistoryReset(ctx context.Context, botID string) (resetCtx context.Context, release func(), err error)
}

type acpClaudeCodeOAuthState struct {
	State             string
	BotID             string
	ChannelIdentityID string
	CodeVerifier      string
	ExpiresAt         time.Time
}

type claudeCodeOAuthTokenResponse struct {
	AccessToken string `json:"access_token"` //nolint:gosec // token response from Claude OAuth exchange.
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
	Scope       string `json:"scope"`
}

func NewACPClaudeCodeOAuthHandler(botService *bots.Service, accountService *accounts.Service, acpWorkspace acpWorkspaceConfigProvider) *ACPClaudeCodeOAuthHandler {
	return &ACPClaudeCodeOAuthHandler{
		botService:     botService,
		accountService: accountService,
		acpWorkspace:   acpWorkspace,
		tokenURL:       claudeCodeOAuthTokenURL,
		httpClient:     http.DefaultClient,
		states:         map[string]acpClaudeCodeOAuthState{},
	}
}

// SetRuntimeResetService installs the distributed operation boundary used when
// OAuth exchange changes the bot's ACP launch configuration.
func (h *ACPClaudeCodeOAuthHandler) SetRuntimeResetService(closer oauthRuntimeResetService) {
	h.runtimeResets = closer
}

func (h *ACPClaudeCodeOAuthHandler) Register(e *echo.Echo) {
	e.GET("/bots/:bot_id/acp/claude-code/oauth/authorize", h.Authorize)
	e.POST("/bots/:bot_id/acp/claude-code/oauth/exchange", h.Exchange)
	e.GET("/bots/:bot_id/acp/claude-code/oauth/status", h.Status)
}

// Authorize godoc
// @Summary Start Claude Code ACP OAuth authorization
// @Tags acp
// @Param bot_id path string true "Bot ID"
// @Success 200 {object} ACPClaudeCodeOAuthAuthorizeResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /bots/{bot_id}/acp/claude-code/oauth/authorize [get].
func (h *ACPClaudeCodeOAuthHandler) Authorize(c echo.Context) error {
	bot, channelIdentityID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}
	if h.acpWorkspace == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "workspace manager is not configured")
	}
	if err := h.ensureManagedWorkspace(c.Request().Context(), bot.ID); err != nil {
		return err
	}

	state, err := generateACPClaudeCodeOAuthState()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	codeVerifier, err := generateACPClaudeCodeOAuthCodeVerifier()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	authURL := buildACPClaudeCodeOAuthAuthorizeURL(state, generateACPClaudeCodeOAuthCodeChallenge(codeVerifier))

	h.mu.Lock()
	h.pruneExpiredLocked(time.Now())
	h.states[state] = acpClaudeCodeOAuthState{
		State:             state,
		BotID:             bot.ID,
		ChannelIdentityID: channelIdentityID,
		CodeVerifier:      codeVerifier,
		ExpiresAt:         time.Now().Add(acpClaudeCodeOAuthStateTTL),
	}
	h.mu.Unlock()

	return c.JSON(http.StatusOK, ACPClaudeCodeOAuthAuthorizeResponse{AuthURL: authURL, SessionID: state})
}

// Exchange godoc
// @Summary Exchange Claude Code OAuth code for an ACP token
// @Tags acp
// @Param bot_id path string true "Bot ID"
// @Param request body ACPClaudeCodeOAuthExchangeRequest true "OAuth exchange request"
// @Success 200 {object} ACPClaudeCodeOAuthStatus
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /bots/{bot_id}/acp/claude-code/oauth/exchange [post].
func (h *ACPClaudeCodeOAuthHandler) Exchange(c echo.Context) error {
	bot, _, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}
	var req ACPClaudeCodeOAuthExchangeRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "session_id is required")
	}
	code := strings.TrimSpace(req.Code)
	if code == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "code is required")
	}
	oauthState, err := h.takeState(sessionID)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if oauthState.BotID != bot.ID {
		return echo.NewHTTPError(http.StatusBadRequest, "oauth session does not match bot")
	}
	if err := h.ensureManagedWorkspace(c.Request().Context(), bot.ID); err != nil {
		return err
	}

	authCode, codeState := parseACPClaudeCodeOAuthCode(code)
	if authCode == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "code is required")
	}
	if codeState != "" && codeState != oauthState.State {
		return echo.NewHTTPError(http.StatusBadRequest, "oauth state does not match")
	}
	tokenResp, err := exchangeACPClaudeCodeOAuthToken(c.Request().Context(), h.httpClient, h.tokenURL, authCode, oauthState.CodeVerifier, codeState)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	token := strings.TrimSpace(tokenResp.AccessToken)
	if token == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "oauth token response did not include an access token")
	}
	if err := h.saveOAuthToken(c.Request().Context(), bot, token); err != nil {
		if apperror.CodeOf(err) != "" {
			return err
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, ACPClaudeCodeOAuthStatus{Configured: true, HasToken: true})
}

// Status godoc
// @Summary Get Claude Code ACP OAuth status
// @Tags acp
// @Param bot_id path string true "Bot ID"
// @Success 200 {object} ACPClaudeCodeOAuthStatus
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /bots/{bot_id}/acp/claude-code/oauth/status [get].
func (h *ACPClaudeCodeOAuthHandler) Status(c echo.Context) error {
	bot, _, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}
	status := ACPClaudeCodeOAuthStatus{
		Configured: h.acpWorkspace != nil,
		HasToken:   claudeCodeOAuthTokenConfigured(bot.Metadata),
	}
	if !status.Configured {
		return c.JSON(http.StatusOK, status)
	}
	if err := h.ensureManagedWorkspace(c.Request().Context(), bot.ID); err != nil {
		var httpErr *echo.HTTPError
		if errors.As(err, &httpErr) && httpErr.Code == http.StatusBadRequest {
			status.Configured = false
			return c.JSON(http.StatusOK, status)
		}
		return err
	}
	return c.JSON(http.StatusOK, status)
}

func (h *ACPClaudeCodeOAuthHandler) requireBotAccess(c echo.Context) (bots.Bot, string, error) {
	botID := strings.TrimSpace(c.Param("bot_id"))
	if botID == "" {
		return bots.Bot{}, "", echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}
	channelIdentityID, err := RequireChannelIdentityID(c)
	if err != nil {
		return bots.Bot{}, "", err
	}
	bot, err := AuthorizeBotAccess(c.Request().Context(), h.botService, h.accountService, channelIdentityID, botID)
	if err != nil {
		return bots.Bot{}, "", err
	}
	return bot, channelIdentityID, nil
}

func (h *ACPClaudeCodeOAuthHandler) ensureManagedWorkspace(ctx context.Context, botID string) error {
	// The OAuth token is persisted in bot metadata and injected via
	// CLAUDE_CODE_OAUTH_TOKEN when the workspace session starts.
	if _, err := h.acpWorkspace.WorkspaceInfo(ctx, botID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return nil
}

func (h *ACPClaudeCodeOAuthHandler) saveOAuthToken(ctx context.Context, bot bots.Bot, token string) error {
	if h.botService == nil {
		return errors.New("bot service is not configured")
	}
	if h.runtimeResets == nil {
		return apperror.Wrap(
			apperror.CodeSessionHistoryInconsistent,
			errors.New("runtime reset is not configured"),
			nil,
		)
	}
	resetCtx, release, err := h.runtimeResets.BeginBotHistoryReset(ctx, bot.ID)
	if err != nil {
		return apperror.Wrap(apperror.CodeSessionHistoryInconsistent, err, nil)
	}
	defer release()

	_, updateErr := h.botService.Update(resetCtx, bot.ID, bots.UpdateBotRequest{
		Metadata: upsertClaudeCodeOAuthMetadata(bot.Metadata, token),
	})
	if errors.Is(updateErr, sessionruntime.ErrHistoryResetLeaseLost) {
		return apperror.Wrap(apperror.CodeSessionHistoryInconsistent, updateErr, nil)
	}
	if cause := context.Cause(resetCtx); errors.Is(cause, sessionruntime.ErrHistoryResetLeaseLost) {
		if updateErr != nil {
			cause = errors.Join(cause, updateErr)
		}
		return apperror.Wrap(apperror.CodeSessionHistoryInconsistent, cause, nil)
	}
	return updateErr
}

func (h *ACPClaudeCodeOAuthHandler) takeState(state string) (acpClaudeCodeOAuthState, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneExpiredLocked(time.Now())
	oauthState, ok := h.states[state]
	if !ok {
		return acpClaudeCodeOAuthState{}, errors.New("oauth session is invalid or expired")
	}
	delete(h.states, state)
	return oauthState, nil
}

func (h *ACPClaudeCodeOAuthHandler) pruneExpiredLocked(now time.Time) {
	for state, value := range h.states {
		if !value.ExpiresAt.IsZero() && now.After(value.ExpiresAt) {
			delete(h.states, state)
		}
	}
}

func exchangeACPClaudeCodeOAuthToken(ctx context.Context, client *http.Client, tokenURL, code, codeVerifier, codeState string) (claudeCodeOAuthTokenResponse, error) {
	tokenURL = strings.TrimSpace(tokenURL)
	if tokenURL == "" {
		tokenURL = claudeCodeOAuthTokenURL
	}
	if client == nil {
		client = http.DefaultClient
	}
	body := map[string]any{
		"code":          strings.TrimSpace(code),
		"grant_type":    "authorization_code",
		"client_id":     claudeCodeOAuthClientID,
		"redirect_uri":  claudeCodeOAuthRedirectURI,
		"code_verifier": strings.TrimSpace(codeVerifier),
		"expires_in":    claudeCodeOAuthExpiresIn,
	}
	if strings.TrimSpace(codeState) != "" {
		body["state"] = strings.TrimSpace(codeState)
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return claudeCodeOAuthTokenResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, bytes.NewReader(payload))
	if err != nil {
		return claudeCodeOAuthTokenResponse{}, err
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "axios/1.13.6")
	resp, err := client.Do(req) //nolint:gosec // Production callers pass the fixed Claude token endpoint; tests inject httptest URLs.
	if err != nil {
		return claudeCodeOAuthTokenResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return claudeCodeOAuthTokenResponse{}, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return claudeCodeOAuthTokenResponse{}, fmt.Errorf("claude code oauth token exchange failed: status %d, body: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var out claudeCodeOAuthTokenResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return claudeCodeOAuthTokenResponse{}, err
	}
	return out, nil
}

func upsertClaudeCodeOAuthMetadata(metadata map[string]any, token string) map[string]any {
	next := cloneMetadataRecord(metadata)
	acp := cloneMetadataRecord(recordValue(next[acpprofile.MetadataKeyACP]))
	agents := cloneMetadataRecord(recordValue(acp["agents"]))
	agent := cloneMetadataRecord(recordValue(agents[acpprofile.AgentClaudeCodeID]))
	managed := cloneMetadataRecord(recordValue(agent["managed"]))

	managed["oauth_token"] = strings.TrimSpace(token)
	agent["enabled"] = true
	agent["setup_mode"] = "oauth"
	agent["managed"] = managed
	agents[acpprofile.AgentClaudeCodeID] = agent
	acp["agents"] = agents
	next[acpprofile.MetadataKeyACP] = acp
	return next
}

func claudeCodeOAuthTokenConfigured(metadata map[string]any) bool {
	acp := recordValue(metadata[acpprofile.MetadataKeyACP])
	agents := recordValue(acp["agents"])
	agent := recordValue(agents[acpprofile.AgentClaudeCodeID])
	managed := recordValue(agent["managed"])
	token, _ := managed["oauth_token"].(string)
	return strings.TrimSpace(token) != ""
}

func buildACPClaudeCodeOAuthAuthorizeURL(state, codeChallenge string) string {
	params := url.Values{}
	params.Set("code", "true")
	params.Set("client_id", claudeCodeOAuthClientID)
	params.Set("response_type", "code")
	params.Set("redirect_uri", claudeCodeOAuthRedirectURI)
	params.Set("scope", claudeCodeOAuthScope)
	params.Set("code_challenge", codeChallenge)
	params.Set("code_challenge_method", "S256")
	params.Set("state", state)
	return claudeCodeOAuthAuthorizeURL + "?" + params.Encode()
}

func parseACPClaudeCodeOAuthCode(code string) (authCode string, state string) {
	code = strings.TrimSpace(code)
	if parsed, err := url.Parse(code); err == nil {
		if queryCode := strings.TrimSpace(parsed.Query().Get("code")); queryCode != "" {
			return queryCode, strings.TrimSpace(parsed.Query().Get("state"))
		}
		if values, err := url.ParseQuery(parsed.Fragment); err == nil {
			if queryCode := strings.TrimSpace(values.Get("code")); queryCode != "" {
				return queryCode, strings.TrimSpace(values.Get("state"))
			}
		}
	}
	if values, err := url.ParseQuery(code); err == nil {
		if queryCode := strings.TrimSpace(values.Get("code")); queryCode != "" {
			return queryCode, strings.TrimSpace(values.Get("state"))
		}
	}
	if idx := strings.Index(code, "#"); idx >= 0 {
		return strings.TrimSpace(code[:idx]), strings.TrimSpace(code[idx+1:])
	}
	return code, ""
}

func generateACPClaudeCodeOAuthState() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return acpClaudeCodeOAuthStatePrefix + hex.EncodeToString(raw[:]), nil
}

func generateACPClaudeCodeOAuthCodeVerifier() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func generateACPClaudeCodeOAuthCodeChallenge(codeVerifier string) string {
	sum := sha256.Sum256([]byte(codeVerifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func recordValue(value any) map[string]any {
	if record, ok := value.(map[string]any); ok {
		return record
	}
	return map[string]any{}
}

func cloneMetadataRecord(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
