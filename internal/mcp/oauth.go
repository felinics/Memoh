package mcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/textutil"
)

// OAuthService manages OAuth flows for MCP connections.
type OAuthService struct {
	queries     dbstore.Queries
	logger      *slog.Logger
	httpClient  *http.Client
	callbackURL string
}

func NewOAuthService(log *slog.Logger, queries dbstore.Queries, callbackURL string) *OAuthService {
	if log == nil {
		log = slog.Default()
	}
	return &OAuthService{
		queries:     queries,
		logger:      log.With(slog.String("service", "mcp_oauth")),
		httpClient:  &http.Client{Timeout: 15 * time.Second},
		callbackURL: callbackURL,
	}
}

// WithQueries returns a shallow service copy bound to the supplied queries.
func (s *OAuthService) WithQueries(queries dbstore.Queries) *OAuthService {
	if s == nil {
		return nil
	}
	clone := *s
	clone.queries = queries
	return &clone
}

// DiscoveryResult holds the result of an OAuth discovery flow.
type DiscoveryResult struct {
	ResourceMetadataURL    string   `json:"resource_metadata_url"`
	AuthorizationServerURL string   `json:"authorization_server_url"`
	AuthorizationEndpoint  string   `json:"authorization_endpoint"`
	TokenEndpoint          string   `json:"token_endpoint"`
	RegistrationEndpoint   string   `json:"registration_endpoint,omitempty"`
	ScopesSupported        []string `json:"scopes_supported,omitempty"`
	ResourceURI            string   `json:"resource_uri"`
}

// OAuthStatus describes the current OAuth state of a connection.
type OAuthStatus struct {
	Configured  bool       `json:"configured"`
	HasToken    bool       `json:"has_token"`
	Expired     bool       `json:"expired"`
	Scopes      string     `json:"scopes,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	AuthServer  string     `json:"auth_server,omitempty"`
	CallbackURL string     `json:"callback_url"`
}

// AuthorizeResult holds the authorization URL to redirect the user to.
type AuthorizeResult struct {
	AuthorizationURL string `json:"authorization_url"`
}

// Discover performs the MCP OAuth discovery flow:
// 1. Send request to MCP server, expect 401 with WWW-Authenticate
// 2. Fetch Protected Resource Metadata
// 3. Fetch Authorization Server Metadata.
func (s *OAuthService) Discover(ctx context.Context, serverURL string) (*DiscoveryResult, error) {
	serverURL = strings.TrimSpace(serverURL)
	if serverURL == "" {
		return nil, errors.New("server URL is required")
	}

	resourceURI := canonicalResourceURI(serverURL)

	// Step 1: Probe the MCP server for 401 + WWW-Authenticate
	resourceMetaURL, challengeScope, err := s.probeForAuth(ctx, serverURL)
	if err != nil {
		return nil, fmt.Errorf("oauth probe failed: %w", err)
	}

	// Step 2: Fetch Protected Resource Metadata
	if resourceMetaURL == "" {
		resourceMetaURL = s.guessResourceMetadataURL(serverURL)
	}
	prm, prmErr := s.fetchProtectedResourceMetadata(ctx, resourceMetaURL)

	var authServerURL string
	var scopes []string

	if prmErr == nil && len(prm.AuthorizationServers) > 0 {
		authServerURL = prm.AuthorizationServers[0]
		scopes = prm.ScopesSupported
	} else {
		// Fallback: some MCP servers (e.g. Linear) don't serve PRM.
		// Try the server origin directly as the authorization server.
		s.logger.Info("PRM unavailable, falling back to direct ASM discovery",
			slog.String("server_url", serverURL),
			slog.Any("prm_error", prmErr),
		)
		parsed, _ := url.Parse(serverURL)
		if parsed != nil {
			authServerURL = parsed.Scheme + "://" + parsed.Host
			if parsed.Path != "" && parsed.Path != "/" {
				authServerURL += parsed.Path
			}
		}
	}

	if authServerURL == "" {
		if prmErr != nil {
			return nil, fmt.Errorf("failed to fetch protected resource metadata: %w", prmErr)
		}
		return nil, errors.New("no authorization servers found in protected resource metadata")
	}

	// Step 3: Fetch Authorization Server Metadata
	asm, err := s.fetchAuthServerMetadata(ctx, authServerURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch authorization server metadata: %w", err)
	}

	if len(scopes) == 0 && challengeScope != "" {
		scopes = strings.Split(challengeScope, " ")
	}
	if scopes == nil {
		scopes = []string{}
	}

	return &DiscoveryResult{
		ResourceMetadataURL:    resourceMetaURL,
		AuthorizationServerURL: authServerURL,
		AuthorizationEndpoint:  asm.AuthorizationEndpoint,
		TokenEndpoint:          asm.TokenEndpoint,
		RegistrationEndpoint:   asm.RegistrationEndpoint,
		ScopesSupported:        scopes,
		ResourceURI:            resourceURI,
	}, nil
}

// SaveDiscovery persists the discovery result for a connection.
func (s *OAuthService) SaveDiscovery(ctx context.Context, connectionID string, result *DiscoveryResult) error {
	if result == nil {
		return errors.New("OAuth discovery result is required")
	}
	connUUID, err := db.ParseUUID(connectionID)
	if err != nil {
		return err
	}
	scopesSupported := result.ScopesSupported
	if scopesSupported == nil {
		scopesSupported = []string{}
	}
	_, err = s.queries.UpsertMCPOAuthDiscovery(ctx, sqlc.UpsertMCPOAuthDiscoveryParams{
		ConnectionID:           connUUID,
		ResourceMetadataUrl:    result.ResourceMetadataURL,
		AuthorizationServerUrl: result.AuthorizationServerURL,
		AuthorizationEndpoint:  result.AuthorizationEndpoint,
		TokenEndpoint:          result.TokenEndpoint,
		RegistrationEndpoint:   result.RegistrationEndpoint,
		ScopesSupported:        scopesSupported,
		ResourceUri:            result.ResourceURI,
	})
	return err
}

// StartAuthorization generates PKCE parameters and returns the authorization URL.
// Client ID resolution follows MCP spec priority:
//  1. User-provided client_id
//  2. Previously stored client_id (from prior registration or user input)
//  3. Dynamic Client Registration (RFC 7591) if registration_endpoint is available
//  4. Error — user must provide a client_id
func (s *OAuthService) StartAuthorization(ctx context.Context, connectionID, clientID, clientSecret, callbackURL string) (*AuthorizeResult, error) {
	if callbackURL == "" {
		callbackURL = s.callbackURL
	}
	connUUID, err := db.ParseUUID(connectionID)
	if err != nil {
		return nil, err
	}

	token, err := s.queries.GetMCPOAuthToken(ctx, connUUID)
	if err != nil {
		return nil, fmt.Errorf("oauth not discovered for this connection: %w", err)
	}

	if token.AuthorizationEndpoint == "" {
		return nil, errors.New("authorization endpoint not configured")
	}

	// Resolve client_id via priority chain
	if clientID == "" {
		clientID = token.ClientID
	}
	if clientSecret == "" {
		clientSecret = token.ClientSecret
	}
	if clientID == "" && token.RegistrationEndpoint != "" {
		// Attempt Dynamic Client Registration (RFC 7591)
		regResult, regErr := s.registerClient(ctx, token.RegistrationEndpoint, callbackURL)
		if regErr != nil {
			s.logger.Warn("dynamic client registration failed", slog.Any("error", regErr))
		} else {
			clientID = regResult.ClientID
			dcrSecret := regResult.ClientSecret
			if err := s.queries.UpdateMCPOAuthPKCEState(ctx, sqlc.UpdateMCPOAuthPKCEStateParams{
				ConnectionID:     connUUID,
				PkceCodeVerifier: "", // will be set below
				StateParam:       "", // will be set below
				ClientID:         clientID,
				RedirectUri:      callbackURL,
			}); err != nil {
				s.logger.Warn("failed to save DCR client_id", slog.Any("error", err))
			}
			if dcrSecret != "" {
				clientSecret = dcrSecret
				_ = s.queries.UpdateMCPOAuthClientSecret(ctx, sqlc.UpdateMCPOAuthClientSecretParams{
					ConnectionID: connUUID,
					ClientSecret: dcrSecret,
				})
			}
			s.logger.Info("dynamic client registration succeeded", slog.String("client_id", clientID))
		}
	}
	if clientID == "" {
		return nil, errors.New("client_id is required: the authorization server does not support automatic registration, please provide a client_id from a registered OAuth application")
	}

	// Persist client_secret if provided by the user
	if clientSecret != "" && clientSecret != token.ClientSecret {
		_ = s.queries.UpdateMCPOAuthClientSecret(ctx, sqlc.UpdateMCPOAuthClientSecretParams{
			ConnectionID: connUUID,
			ClientSecret: clientSecret,
		})
	}

	codeVerifier, err := generateCodeVerifier()
	if err != nil {
		return nil, fmt.Errorf("failed to generate PKCE code verifier: %w", err)
	}
	codeChallenge := computeCodeChallenge(codeVerifier)
	state, err := generateState()
	if err != nil {
		return nil, fmt.Errorf("failed to generate state: %w", err)
	}

	if err := s.queries.UpdateMCPOAuthPKCEState(ctx, sqlc.UpdateMCPOAuthPKCEStateParams{
		ConnectionID:     connUUID,
		PkceCodeVerifier: codeVerifier,
		StateParam:       state,
		ClientID:         clientID,
		RedirectUri:      callbackURL,
	}); err != nil {
		return nil, fmt.Errorf("failed to save PKCE state: %w", err)
	}

	params := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {callbackURL},
		"state":                 {state},
		"code_challenge":        {codeChallenge},
		"code_challenge_method": {"S256"},
	}

	if token.ResourceUri != "" {
		params.Set("resource", token.ResourceUri)
	}

	scopes := token.ScopesSupported
	if len(scopes) > 0 {
		params.Set("scope", strings.Join(scopes, " "))
	}

	authURL := token.AuthorizationEndpoint + "?" + params.Encode()
	return &AuthorizeResult{AuthorizationURL: authURL}, nil
}

// HandleCallback exchanges the authorization code for tokens.
func (s *OAuthService) HandleCallback(ctx context.Context, state, code string) (string, error) {
	if state == "" || code == "" {
		return "", errors.New("state and code are required")
	}

	// The auth code is single-use, but the browser may abort this request
	// mid-flight (popup closed, or re-navigated by a retry). Everything after
	// the state lookup runs on a detached context so an abort cannot burn the
	// code — the frontend polls status and completes the flow regardless of
	// what this HTTP response does.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()

	token, err := s.queries.GetMCPOAuthTokenByState(ctx, state)
	if err != nil {
		return "", fmt.Errorf("invalid or expired state parameter: %w", err)
	}

	if token.TokenEndpoint == "" || token.PkceCodeVerifier == "" {
		return "", errors.New("invalid OAuth state: missing token endpoint or code verifier")
	}

	redirectURI := token.RedirectUri
	if redirectURI == "" {
		redirectURI = s.callbackURL
	}
	tokenResp, err := s.exchangeCode(ctx, token.TokenEndpoint, code, token.PkceCodeVerifier, token.ClientID, token.ClientSecret, token.ResourceUri, redirectURI)
	if err != nil {
		return "", fmt.Errorf("token exchange failed: %w", err)
	}

	var expiresAt pgtype.Timestamptz
	if tokenResp.ExpiresIn > 0 {
		t := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
		expiresAt = pgtype.Timestamptz{Time: t, Valid: true}
	}

	if err := s.queries.UpdateMCPOAuthTokens(ctx, sqlc.UpdateMCPOAuthTokensParams{
		ConnectionID: token.ConnectionID,
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		TokenType:    tokenResp.TokenType,
		ExpiresAt:    expiresAt,
		Scope:        tokenResp.Scope,
	}); err != nil {
		return "", fmt.Errorf("failed to save tokens: %w", err)
	}

	_ = s.queries.UpdateMCPConnectionAuthType(ctx, sqlc.UpdateMCPConnectionAuthTypeParams{
		ID:       token.ConnectionID,
		AuthType: "oauth",
	})

	return token.ConnectionID.String(), nil
}

// GetValidToken returns a valid access token, refreshing if expired.
func (s *OAuthService) GetValidToken(ctx context.Context, connectionID string) (string, error) {
	connUUID, err := db.ParseUUID(connectionID)
	if err != nil {
		return "", err
	}

	token, err := s.queries.GetMCPOAuthToken(ctx, connUUID)
	if err != nil {
		return "", fmt.Errorf("no oauth token found: %w", err)
	}

	if token.AccessToken == "" {
		return "", errors.New("no access token available, authorization required")
	}

	if token.ExpiresAt.Valid && time.Now().After(token.ExpiresAt.Time.Add(-30*time.Second)) {
		if token.RefreshToken == "" {
			return "", errors.New("access token expired and no refresh token available")
		}
		refreshed, err := s.refreshToken(ctx, token.TokenEndpoint, token.RefreshToken, token.ClientID, token.ResourceUri)
		if err != nil {
			return "", fmt.Errorf("token refresh failed: %w", err)
		}

		var expiresAt pgtype.Timestamptz
		if refreshed.ExpiresIn > 0 {
			t := time.Now().Add(time.Duration(refreshed.ExpiresIn) * time.Second)
			expiresAt = pgtype.Timestamptz{Time: t, Valid: true}
		}

		refreshTokenValue := refreshed.RefreshToken
		if refreshTokenValue == "" {
			refreshTokenValue = token.RefreshToken
		}

		if err := s.queries.UpdateMCPOAuthTokens(ctx, sqlc.UpdateMCPOAuthTokensParams{
			ConnectionID: connUUID,
			AccessToken:  refreshed.AccessToken,
			RefreshToken: refreshTokenValue,
			TokenType:    refreshed.TokenType,
			ExpiresAt:    expiresAt,
			Scope:        refreshed.Scope,
		}); err != nil {
			s.logger.Warn("failed to save refreshed tokens", slog.Any("error", err))
		}
		return refreshed.AccessToken, nil
	}

	return token.AccessToken, nil
}

// GetStatus returns the OAuth status for a connection.
func (s *OAuthService) GetStatus(ctx context.Context, connectionID string) (*OAuthStatus, error) {
	connUUID, err := db.ParseUUID(connectionID)
	if err != nil {
		return nil, err
	}

	token, err := s.queries.GetMCPOAuthToken(ctx, connUUID)
	if err != nil {
		return &OAuthStatus{Configured: false, CallbackURL: s.callbackURL}, nil
	}

	status := &OAuthStatus{
		Configured:  token.AuthorizationEndpoint != "",
		HasToken:    token.AccessToken != "",
		AuthServer:  token.AuthorizationServerUrl,
		Scopes:      token.Scope,
		CallbackURL: s.callbackURL,
	}

	if token.ExpiresAt.Valid {
		t := db.TimeFromPg(token.ExpiresAt)
		status.ExpiresAt = &t
		status.Expired = time.Now().After(token.ExpiresAt.Time)
	}

	return status, nil
}

// RevokeToken clears stored tokens for a connection.
func (s *OAuthService) RevokeToken(ctx context.Context, connectionID string) error {
	connUUID, err := db.ParseUUID(connectionID)
	if err != nil {
		return err
	}
	return s.queries.ClearMCPOAuthTokens(ctx, connUUID)
}

// --- internal helpers ---

type protectedResourceMetadata struct {
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported"`
}

type authServerMetadata struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	RegistrationEndpoint  string   `json:"registration_endpoint"`
	ScopesSupported       []string `json:"scopes_supported"`
}

type tokenResponse struct {
	AccessToken      string `json:"access_token"`  //nolint:gosec // intentional: OAuth token response field
	RefreshToken     string `json:"refresh_token"` //nolint:gosec // intentional: OAuth token response field
	TokenType        string `json:"token_type"`
	ExpiresIn        int    `json:"expires_in"`
	Scope            string `json:"scope"`
	Error            string `json:"error,omitempty"`
	ErrorDescription string `json:"error_description,omitempty"`
}

type mcpOAuthProbeRequest struct {
	JSONRPC string                  `json:"jsonrpc"`
	ID      string                  `json:"id"`
	Method  string                  `json:"method"`
	Params  mcpOAuthProbeParameters `json:"params"`
}

type mcpOAuthProbeParameters struct {
	ProtocolVersion string                  `json:"protocolVersion"`
	Capabilities    map[string]any          `json:"capabilities"`
	ClientInfo      mcpOAuthProbeClientInfo `json:"clientInfo"`
}

type mcpOAuthProbeClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func (s *OAuthService) probeForAuth(ctx context.Context, serverURL string) (resourceMetaURL, scope string, err error) {
	payload, err := json.Marshal(mcpOAuthProbeRequest{
		JSONRPC: "2.0",
		ID:      "oauth-probe",
		Method:  "initialize",
		Params: mcpOAuthProbeParameters{
			ProtocolVersion: "2025-06-18",
			Capabilities:    map[string]any{},
			ClientInfo: mcpOAuthProbeClientInfo{
				Name:    "Memoh",
				Version: "dev",
			},
		},
	})
	if err != nil {
		return "", "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL, strings.NewReader(string(payload)))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := s.httpClient.Do(req) //nolint:gosec // G704: URL is from OAuth server discovery metadata or operator config, not user input
	if err != nil {
		return "", "", err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusUnauthorized {
		return "", "", fmt.Errorf("expected 401 Unauthorized, got %d (server may not require OAuth)", resp.StatusCode)
	}

	wwwAuth := resp.Header.Get("WWW-Authenticate")
	if wwwAuth == "" {
		return "", "", nil
	}

	resourceMetaURL = extractWWWAuthParam(wwwAuth, "resource_metadata")
	scope = extractWWWAuthParam(wwwAuth, "scope")
	return resourceMetaURL, scope, nil
}

func (*OAuthService) guessResourceMetadataURL(serverURL string) string {
	parsed, err := url.Parse(serverURL)
	if err != nil {
		return ""
	}
	base := parsed.Scheme + "://" + parsed.Host
	if parsed.Path != "" && parsed.Path != "/" {
		return base + "/.well-known/oauth-protected-resource" + parsed.Path
	}
	return base + "/.well-known/oauth-protected-resource"
}

func (s *OAuthService) fetchProtectedResourceMetadata(ctx context.Context, metadataURL string) (*protectedResourceMetadata, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.httpClient.Do(req) //nolint:gosec // G704: URL is from OAuth server discovery metadata or operator config, not user input
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("resource metadata returned %d: %s", resp.StatusCode, string(body))
	}

	var meta protectedResourceMetadata
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

func (s *OAuthService) fetchAuthServerMetadata(ctx context.Context, issuerURL string) (*authServerMetadata, error) {
	parsed, err := url.Parse(issuerURL)
	if err != nil {
		return nil, err
	}

	// Try multiple well-known endpoints per MCP spec (RFC 8414 Section 3.1).
	// For issuer URLs with path components (e.g., https://github.com/login/oauth):
	//   1. Path appending: https://github.com/login/oauth/.well-known/openid-configuration
	//   2. Path insertion (OIDC): https://github.com/.well-known/openid-configuration/login/oauth
	//   3. Path insertion (OAuth): https://github.com/.well-known/oauth-authorization-server/login/oauth
	base := parsed.Scheme + "://" + parsed.Host
	var candidates []string
	if parsed.Path != "" && parsed.Path != "/" {
		candidates = []string{
			base + "/.well-known/oauth-authorization-server" + parsed.Path,
			base + "/.well-known/oauth-authorization-server",
			base + "/.well-known/openid-configuration" + parsed.Path,
			base + "/.well-known/openid-configuration",
			strings.TrimRight(issuerURL, "/") + "/.well-known/openid-configuration",
		}
	} else {
		candidates = []string{
			base + "/.well-known/oauth-authorization-server",
			base + "/.well-known/openid-configuration",
		}
	}

	var lastErr error
	for _, candidate := range candidates {
		meta, err := s.tryFetchASMetadata(ctx, candidate)
		if err == nil {
			return meta, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("could not fetch authorization server metadata: %w", lastErr)
}

func (s *OAuthService) tryFetchASMetadata(ctx context.Context, metadataURL string) (*authServerMetadata, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.httpClient.Do(req) //nolint:gosec // G704: URL is from OAuth server discovery metadata or operator config, not user input
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metadata endpoint %s returned %d", metadataURL, resp.StatusCode)
	}

	var meta authServerMetadata
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return nil, err
	}
	if meta.AuthorizationEndpoint == "" || meta.TokenEndpoint == "" {
		return nil, errors.New("metadata missing required endpoints")
	}
	return &meta, nil
}

func (s *OAuthService) exchangeCode(ctx context.Context, tokenEndpoint, code, codeVerifier, clientID, clientSecret, resourceURI, redirectURI string) (*tokenResponse, error) {
	data := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {clientID},
		"code_verifier": {codeVerifier},
	}
	if clientSecret != "" {
		data.Set("client_secret", clientSecret)
	}
	if resourceURI != "" {
		data.Set("resource", resourceURI)
	}

	s.logger.Info("exchangeCode request",
		slog.String("token_endpoint", tokenEndpoint),
		slog.String("redirect_uri", redirectURI),
		slog.String("client_id", clientID),
		slog.Bool("has_secret", clientSecret != ""),
		slog.Bool("has_verifier", codeVerifier != ""),
		slog.String("resource_uri", resourceURI),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req) //nolint:gosec // G704: URL is from OAuth server discovery metadata or operator config, not user input
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange returned %d: %s", resp.StatusCode, string(body))
	}

	tok, err := parseTokenResponse(body)
	if err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w (body: %s)", err, truncate(string(body), 256))
	}
	return tok, nil
}

// parseTokenResponse tries JSON first, then falls back to form-encoded
// (GitHub's token endpoint returns form-encoded by default).
func parseTokenResponse(body []byte) (*tokenResponse, error) {
	var tok tokenResponse
	if err := json.Unmarshal(body, &tok); err == nil {
		if tok.Error != "" {
			if tok.ErrorDescription != "" {
				return nil, fmt.Errorf("%s: %s", tok.Error, tok.ErrorDescription)
			}
			return nil, fmt.Errorf("%s", tok.Error)
		}
		if tok.AccessToken == "" {
			return nil, errors.New("no access_token in response")
		}
		if tok.TokenType == "" {
			tok.TokenType = "Bearer"
		}
		return &tok, nil
	}

	vals, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, fmt.Errorf("response is neither JSON nor form-encoded: %w", err)
	}

	if errCode := vals.Get("error"); errCode != "" {
		desc := vals.Get("error_description")
		if desc != "" {
			return nil, fmt.Errorf("%s: %s", errCode, desc)
		}
		return nil, fmt.Errorf("%s", errCode)
	}

	tok.AccessToken = vals.Get("access_token")
	tok.RefreshToken = vals.Get("refresh_token")
	tok.TokenType = vals.Get("token_type")
	tok.Scope = vals.Get("scope")
	if tok.TokenType == "" {
		tok.TokenType = "Bearer"
	}
	if tok.AccessToken == "" {
		return nil, errors.New("no access_token in response")
	}
	return &tok, nil
}

func truncate(s string, maxLen int) string {
	return textutil.TruncateRunesWithSuffix(s, maxLen, "...")
}

func (s *OAuthService) refreshToken(ctx context.Context, tokenEndpoint, refreshToken, clientID, resourceURI string) (*tokenResponse, error) {
	data := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {clientID},
	}
	if resourceURI != "" {
		data.Set("resource", resourceURI)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req) //nolint:gosec // G704: URL is from OAuth server discovery metadata or operator config, not user input
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read refresh response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token refresh returned %d: %s", resp.StatusCode, string(body))
	}

	tok, err := parseTokenResponse(body)
	if err != nil {
		return nil, fmt.Errorf("failed to parse refresh response: %w", err)
	}
	return tok, nil
}

// --- Dynamic Client Registration (RFC 7591) ---

type dcrRequest struct {
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

type dcrResponse struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret,omitempty"` //nolint:gosec // intentional: OAuth Dynamic Client Registration response field
}

func (s *OAuthService) registerClient(ctx context.Context, registrationEndpoint, callbackURL string) (*dcrResponse, error) {
	body := dcrRequest{
		ClientName:              "Memoh",
		RedirectURIs:            []string{callbackURL},
		GrantTypes:              []string{"authorization_code", "refresh_token"},
		ResponseTypes:           []string{"code"},
		TokenEndpointAuthMethod: "none",
	}
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, registrationEndpoint, strings.NewReader(string(bodyJSON)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req) //nolint:gosec // G704: URL is from OAuth server discovery metadata or operator config, not user input
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("DCR returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result dcrResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode DCR response: %w", err)
	}
	if result.ClientID == "" {
		return nil, errors.New("DCR response missing client_id")
	}
	return &result, nil
}

// --- PKCE helpers ---

func generateCodeVerifier() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func computeCodeChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

func generateState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func canonicalResourceURI(serverURL string) string {
	parsed, err := url.Parse(serverURL)
	if err != nil {
		return serverURL
	}
	result := strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host)
	if parsed.Path != "" && parsed.Path != "/" {
		result += strings.TrimRight(parsed.Path, "/")
	}
	return result
}

func extractWWWAuthParam(header, param string) string {
	lower := strings.ToLower(header)
	key := strings.ToLower(param) + "="
	idx := strings.Index(lower, key)
	if idx < 0 {
		return ""
	}
	rest := header[idx+len(key):]
	if len(rest) > 0 && rest[0] == '"' {
		end := strings.Index(rest[1:], "\"")
		if end >= 0 {
			return rest[1 : end+1]
		}
		return rest[1:]
	}
	end := strings.IndexAny(rest, " ,")
	if end >= 0 {
		return rest[:end]
	}
	return rest
}
