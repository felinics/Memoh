package mcp

// End-to-end OAuth journey tests against a fake provider modeled on Linear:
// one httptest server plays BOTH the MCP resource server (401 challenge, then
// token-gated) and the OAuth authorization server (PRM, ASM, DCR, authorize,
// token with REAL PKCE verification, single-use codes, refresh rotation).
// The fake enforces every check a real provider enforces, so the journey
// fails if Memoh's client cuts any spec corner (missing resource parameter,
// wrong PKCE, redirect_uri drift) — not just if its own bookkeeping breaks.
//
// Covered regressions (all shipped bugs):
//   - token exchange must survive the browser aborting the callback request
//     (the single-use auth code used to burn with "context canceled")
//   - authorization codes are single-use; replay must fail
//   - refresh must rotate tokens and invalidate the old pair

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// ---------- fake persistence ----------

// fakeOAuthQueries is an in-memory Queries for the OAuth token table plus the
// connection rows Update reads/writes. Anything else panics via the nil embed.
type fakeOAuthQueries struct {
	dbstore.Queries
	mu          sync.Mutex
	byConn      map[pgtype.UUID]sqlc.McpOauthToken
	authTypes   map[pgtype.UUID]string
	connections map[pgtype.UUID]sqlc.McpConnection
}

func newFakeOAuthQueries() *fakeOAuthQueries {
	return &fakeOAuthQueries{
		byConn:      map[pgtype.UUID]sqlc.McpOauthToken{},
		authTypes:   map[pgtype.UUID]string{},
		connections: map[pgtype.UUID]sqlc.McpConnection{},
	}
}

func (f *fakeOAuthQueries) GetMCPConnectionByID(_ context.Context, arg sqlc.GetMCPConnectionByIDParams) (sqlc.McpConnection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.connections[arg.ID]
	if !ok || row.BotID != arg.BotID {
		return sqlc.McpConnection{}, errors.New("no rows in result set")
	}
	return row, nil
}

func (f *fakeOAuthQueries) UpdateMCPConnection(_ context.Context, arg sqlc.UpdateMCPConnectionParams) (sqlc.McpConnection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.connections[arg.ID]
	if !ok {
		return sqlc.McpConnection{}, errors.New("no rows in result set")
	}
	row.Name = arg.Name
	row.Type = arg.Type
	row.Config = arg.Config
	row.IsActive = arg.IsActive
	row.AuthType = arg.AuthType
	f.connections[arg.ID] = row
	return row, nil
}

func (f *fakeOAuthQueries) UpsertMCPOAuthDiscovery(_ context.Context, arg sqlc.UpsertMCPOAuthDiscoveryParams) (sqlc.McpOauthToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := f.byConn[arg.ConnectionID]
	row.ID = arg.ConnectionID
	row.ConnectionID = arg.ConnectionID
	row.ResourceMetadataUrl = arg.ResourceMetadataUrl
	row.AuthorizationServerUrl = arg.AuthorizationServerUrl
	row.AuthorizationEndpoint = arg.AuthorizationEndpoint
	row.TokenEndpoint = arg.TokenEndpoint
	row.RegistrationEndpoint = arg.RegistrationEndpoint
	row.ScopesSupported = arg.ScopesSupported
	row.ResourceUri = arg.ResourceUri
	f.byConn[arg.ConnectionID] = row
	return row, nil
}

func (f *fakeOAuthQueries) GetMCPOAuthToken(_ context.Context, connectionID pgtype.UUID) (sqlc.McpOauthToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.byConn[connectionID]
	if !ok {
		return sqlc.McpOauthToken{}, errors.New("no rows in result set")
	}
	return row, nil
}

func (f *fakeOAuthQueries) GetMCPOAuthTokenByState(_ context.Context, stateParam string) (sqlc.McpOauthToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, row := range f.byConn {
		if row.StateParam == stateParam && stateParam != "" {
			return row, nil
		}
	}
	return sqlc.McpOauthToken{}, errors.New("no rows in result set")
}

func (f *fakeOAuthQueries) UpdateMCPOAuthPKCEState(_ context.Context, arg sqlc.UpdateMCPOAuthPKCEStateParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := f.byConn[arg.ConnectionID]
	row.PkceCodeVerifier = arg.PkceCodeVerifier
	row.StateParam = arg.StateParam
	row.ClientID = arg.ClientID
	row.RedirectUri = arg.RedirectUri
	f.byConn[arg.ConnectionID] = row
	return nil
}

func (f *fakeOAuthQueries) UpdateMCPOAuthClientSecret(_ context.Context, arg sqlc.UpdateMCPOAuthClientSecretParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := f.byConn[arg.ConnectionID]
	row.ClientSecret = arg.ClientSecret
	f.byConn[arg.ConnectionID] = row
	return nil
}

func (f *fakeOAuthQueries) UpdateMCPOAuthTokens(_ context.Context, arg sqlc.UpdateMCPOAuthTokensParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := f.byConn[arg.ConnectionID]
	row.AccessToken = arg.AccessToken
	row.RefreshToken = arg.RefreshToken
	row.TokenType = arg.TokenType
	row.ExpiresAt = arg.ExpiresAt
	row.Scope = arg.Scope
	f.byConn[arg.ConnectionID] = row
	return nil
}

func (f *fakeOAuthQueries) UpdateMCPConnectionAuthType(_ context.Context, arg sqlc.UpdateMCPConnectionAuthTypeParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.authTypes[arg.ID] = arg.AuthType
	return nil
}

func (f *fakeOAuthQueries) ClearMCPOAuthTokens(_ context.Context, connectionID pgtype.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := f.byConn[connectionID]
	row.AccessToken = ""
	row.RefreshToken = ""
	row.ExpiresAt = pgtype.Timestamptz{}
	f.byConn[connectionID] = row
	return nil
}

// test-only accessors (no production caller reads through these).

func (f *fakeOAuthQueries) setExpiry(connectionID pgtype.UUID, at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := f.byConn[connectionID]
	row.ExpiresAt = pgtype.Timestamptz{Time: at, Valid: true}
	f.byConn[connectionID] = row
}

func (f *fakeOAuthQueries) setVerifier(connectionID pgtype.UUID, verifier string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := f.byConn[connectionID]
	row.PkceCodeVerifier = verifier
	f.byConn[connectionID] = row
}

func (f *fakeOAuthQueries) authType(connectionID pgtype.UUID) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.authTypes[connectionID]
}

// ---------- fake provider (Linear's shape) ----------

type fakeAuthCode struct {
	challenge   string
	redirectURI string
	resource    string
	used        bool
}

// fakeLinearServer serves the MCP endpoint AND the authorization server from
// one origin, exactly like Linear. Every validation a real provider performs
// is performed here; violations fail the test at the point of the offense.
type fakeLinearServer struct {
	t   *testing.T
	srv *httptest.Server

	mu               sync.Mutex
	tokenDelay       time.Duration // simulates a slow token endpoint (abort test)
	registrations    []string      // redirect_uris registered via DCR, in order
	codes            map[string]*fakeAuthCode
	accessTokens     map[string]bool
	refreshTokens    map[string]bool
	tokenGrantCount  int
	lastTokenForm    url.Values
	lastAuthorizeURL url.Values
}

func newFakeLinearServer(t *testing.T) *fakeLinearServer {
	t.Helper()
	f := &fakeLinearServer{
		t:             t,
		codes:         map[string]*fakeAuthCode{},
		accessTokens:  map[string]bool{},
		refreshTokens: map[string]bool{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /mcp", f.handleMCP)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", f.handlePRM)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", f.handleASM)
	mux.HandleFunc("POST /register", f.handleRegister)
	mux.HandleFunc("GET /authorize", f.handleAuthorize)
	mux.HandleFunc("POST /token", f.handleToken)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeLinearServer) mcpURL() string { return f.srv.URL + "/mcp" }

// fakeLinearScope is the scope set the fake provider advertises and expects.
const fakeLinearScope = "mcp:read mcp:write"

// handleMCP is the resource server: 401 + WWW-Authenticate without a known
// token, 200 with one. This is the door the whole discovery flow starts at.
func (f *fakeLinearServer) handleMCP(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		f.mu.Lock()
		ok := f.accessTokens[strings.TrimPrefix(auth, "Bearer ")]
		f.mu.Unlock()
		if ok {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"oauth-probe","result":{"protocolVersion":"2025-06-18","capabilities":{},"serverInfo":{"name":"fake-linear","version":"0"}}}`))
			return
		}
	}
	w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="OAuth", resource_metadata="%s/.well-known/oauth-protected-resource/mcp", scope="%s"`, f.srv.URL, fakeLinearScope))
	w.WriteHeader(http.StatusUnauthorized)
}

func (f *fakeLinearServer) handlePRM(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"resource":                 f.mcpURL(),
		"authorization_servers":    []string{f.srv.URL},
		"scopes_supported":         []string{"mcp:read", "mcp:write"},
		"bearer_methods_supported": []string{"header"},
	})
}

func (f *fakeLinearServer) handleASM(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"issuer":                 f.srv.URL,
		"authorization_endpoint": f.srv.URL + "/authorize",
		"token_endpoint":         f.srv.URL + "/token",
		"registration_endpoint":  f.srv.URL + "/register",
	})
}

func (f *fakeLinearServer) handleRegister(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ClientName   string   `json:"client_name"`
		RedirectURIs []string `json:"redirect_uris"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.RedirectURIs) == 0 {
		http.Error(w, `{"error":"invalid_client_metadata"}`, http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.registrations = append(f.registrations, body.RedirectURIs[0])
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"client_id": "fake-linear-client"})
}

// handleAuthorize plays the consent page: it enforces registered redirect_uri
// (exact match), PKCE presence, and the RFC 8707 resource parameter — the one
// most MCP clients forget — then redirects with a single-use code.
func (f *fakeLinearServer) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f.mu.Lock()
	f.lastAuthorizeURL = q
	registered := false
	for _, ru := range f.registrations {
		if ru == q.Get("redirect_uri") {
			registered = true
		}
	}
	f.mu.Unlock()

	switch {
	case q.Get("response_type") != "code":
		f.t.Errorf("authorize: response_type = %q, want code", q.Get("response_type"))
	case q.Get("client_id") != "fake-linear-client":
		f.t.Errorf("authorize: client_id = %q, want the DCR-issued one", q.Get("client_id"))
	case !registered:
		f.t.Errorf("authorize: redirect_uri %q was never registered via DCR", q.Get("redirect_uri"))
	case q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256":
		f.t.Errorf("authorize: PKCE missing or not S256")
	case q.Get("resource") != f.mcpURL():
		f.t.Errorf("authorize: resource = %q, want canonical %q (RFC 8707)", q.Get("resource"), f.mcpURL())
	case q.Get("state") == "":
		f.t.Errorf("authorize: state missing")
	}

	code := "lin-code-" + q.Get("state")
	f.mu.Lock()
	f.codes[code] = &fakeAuthCode{
		challenge:   q.Get("code_challenge"),
		redirectURI: q.Get("redirect_uri"),
		resource:    q.Get("resource"),
	}
	f.mu.Unlock()

	loc, _ := url.Parse(q.Get("redirect_uri"))
	locQ := loc.Query()
	locQ.Set("code", code)
	locQ.Set("state", q.Get("state"))
	loc.RawQuery = locQ.Encode()
	w.Header().Set("Location", loc.String())
	w.WriteHeader(http.StatusFound)
}

// handleToken verifies the exchange the way a real provider does: code exists
// and is single-use, redirect_uri matches, PKCE verifier hashes to the stored
// challenge, resource matches. Refresh grants rotate the refresh token.
func (f *fakeLinearServer) handleToken(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	delay := f.tokenDelay
	f.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	if err := r.ParseForm(); err != nil { //nolint:gosec // G120: body is bounded by MaxBytesReader directly above
		http.Error(w, `{"error":"invalid_request"}`, http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.tokenGrantCount++
	f.lastTokenForm = r.Form
	f.mu.Unlock()

	writeErr := func(code, desc string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": desc})
	}

	switch r.Form.Get("grant_type") {
	case "authorization_code":
		code := r.Form.Get("code")
		f.mu.Lock()
		ac, ok := f.codes[code]
		if ok && ac.used {
			ok = false
		}
		if ok {
			ac.used = true
		}
		f.mu.Unlock()
		if !ok {
			writeErr("invalid_grant", "code unknown or already used")
			return
		}
		if r.Form.Get("redirect_uri") != ac.redirectURI {
			writeErr("invalid_grant", "redirect_uri mismatch")
			return
		}
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != ac.challenge {
			writeErr("invalid_grant", "PKCE verifier mismatch")
			return
		}
		if r.Form.Get("resource") != ac.resource {
			writeErr("invalid_grant", "resource mismatch")
			return
		}
		access := "lin-access-" + code
		refresh := "lin-refresh-" + code
		f.mu.Lock()
		f.accessTokens[access] = true
		f.refreshTokens[refresh] = true
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  access,
			"refresh_token": refresh,
			"token_type":    "Bearer",
			"expires_in":    3600,
			"scope":         fakeLinearScope,
		})

	case "refresh_token":
		old := r.Form.Get("refresh_token")
		f.mu.Lock()
		ok := f.refreshTokens[old]
		if ok {
			// rotate: the old refresh token dies with this grant, and the
			// access token issued alongside it is invalidated too
			delete(f.refreshTokens, old)
			delete(f.accessTokens, "lin-access-"+strings.TrimPrefix(old, "lin-refresh-"))
			delete(f.accessTokens, "lin-access-rotated-"+strings.TrimPrefix(old, "lin-refresh-rotated-"))
		}
		f.mu.Unlock()
		if !ok {
			writeErr("invalid_grant", "refresh token unknown or rotated")
			return
		}
		access := "lin-access-rotated-" + old
		refresh := "lin-refresh-rotated-" + old
		f.mu.Lock()
		f.accessTokens[access] = true
		f.refreshTokens[refresh] = true
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  access,
			"refresh_token": refresh,
			"token_type":    "Bearer",
			"expires_in":    3600,
			"scope":         fakeLinearScope,
		})

	default:
		writeErr("unsupported_grant_type", r.Form.Get("grant_type"))
	}
}

// ---------- the journey ----------

const (
	oauthJourneyConnID   = "00000000-0000-0000-0000-000000000001"
	oauthJourneyCallback = "http://localhost:18082/api/oauth/mcp/callback"
)

// authorizeInBrowser performs the consent-page GET without following the
// redirect back to the callback — the browser half of the journey.
func authorizeInBrowser(t *testing.T, ctx context.Context, authorizationURL string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, authorizationURL, nil)
	if err != nil {
		t.Fatalf("build authorize request: %v", err)
	}
	browser := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := browser.Do(req) //nolint:gosec // G704: test-only URL from the local httptest server
	if err != nil {
		t.Fatalf("authorize request: %v", err)
	}
	return resp
}

// completeLinearJourney runs the full authorization dance exactly as a user
// would drive it from the UI: discover → save → authorize → (browser) consent
// → callback. It returns the service, the fake store, the fake provider, and
// the connection UUID, so each test can assert its own slice of the aftermath.
func completeLinearJourney(t *testing.T) (*OAuthService, *fakeOAuthQueries, *fakeLinearServer, pgtype.UUID) {
	t.Helper()
	provider := newFakeLinearServer(t)
	queries := newFakeOAuthQueries()
	svc := NewOAuthService(nil, queries, oauthJourneyCallback)
	ctx := context.Background()

	// 1. Discovery: 401 challenge → PRM → ASM (all served by the fake).
	discovery, err := svc.Discover(ctx, provider.mcpURL())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if discovery.AuthorizationEndpoint != provider.srv.URL+"/authorize" ||
		discovery.TokenEndpoint != provider.srv.URL+"/token" ||
		discovery.RegistrationEndpoint != provider.srv.URL+"/register" {
		t.Fatalf("discovery endpoints wrong: %+v", discovery)
	}
	if discovery.ResourceURI != provider.mcpURL() {
		t.Fatalf("resource URI = %q, want canonical %q", discovery.ResourceURI, provider.mcpURL())
	}

	connUUID := mustParseUUID(oauthJourneyConnID)
	if err := svc.SaveDiscovery(ctx, oauthJourneyConnID, discovery); err != nil {
		t.Fatalf("SaveDiscovery: %v", err)
	}

	// 2. StartAuthorization: no client_id → DCR must kick in, exactly once.
	auth, err := svc.StartAuthorization(ctx, oauthJourneyConnID, "", "", oauthJourneyCallback)
	if err != nil {
		t.Fatalf("StartAuthorization: %v", err)
	}
	authURL, err := url.Parse(auth.AuthorizationURL)
	if err != nil {
		t.Fatalf("parse authorization URL: %v", err)
	}
	q := authURL.Query()
	for key, want := range map[string]string{
		"response_type":         "code",
		"client_id":             "fake-linear-client",
		"redirect_uri":          oauthJourneyCallback,
		"code_challenge_method": "S256",
		"resource":              provider.mcpURL(),
		"scope":                 fakeLinearScope,
	} {
		if q.Get(key) != want {
			t.Fatalf("authorization URL %s = %q, want %q", key, q.Get(key), want)
		}
	}
	if q.Get("code_challenge") == "" || q.Get("state") == "" {
		t.Fatalf("authorization URL missing PKCE challenge or state: %s", auth.AuthorizationURL)
	}

	// 3. The "browser": follow the authorization URL against the consent
	// endpoint; the fake provider 302s back to the callback with code+state.
	resp := authorizeInBrowser(t, ctx, auth.AuthorizationURL)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("authorize status = %d, want 302", resp.StatusCode)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parse redirect location: %v", err)
	}
	code, state := loc.Query().Get("code"), loc.Query().Get("state")
	if code == "" || state == "" || !strings.HasPrefix(loc.String(), oauthJourneyCallback) {
		t.Fatalf("redirect location wrong: %s", loc)
	}

	// 4. The callback: code → tokens, stored.
	if _, err := svc.HandleCallback(ctx, state, code); err != nil {
		t.Fatalf("HandleCallback: %v", err)
	}
	return svc, queries, provider, connUUID
}

func mustParseUUID(s string) pgtype.UUID {
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		panic(err)
	}
	return u
}

func TestMCPOAuthJourneyEndToEnd(t *testing.T) {
	svc, queries, provider, connUUID := completeLinearJourney(t)
	ctx := context.Background()

	status, err := svc.GetStatus(ctx, oauthJourneyConnID)
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if !status.Configured || !status.HasToken || status.Expired {
		t.Fatalf("status after journey = %+v, want configured+tokened+valid", status)
	}
	if got := queries.authType(connUUID); got != "oauth" {
		t.Fatalf("connection auth_type = %q, want oauth", got)
	}

	// The stored access token must open the resource server's door.
	access, err := svc.GetValidToken(ctx, oauthJourneyConnID)
	if err != nil {
		t.Fatalf("GetValidToken: %v", err)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, provider.mcpURL(), strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+access)
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // G704: test-only URL from the local httptest server
	if err != nil {
		t.Fatalf("call with token: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resource server answered %d to the issued token", resp.StatusCode)
	}

	// The token request carried the resource parameter (RFC 8707) — checked
	// server-side too, but assert the client-visible form for the record.
	if got := provider.lastTokenForm.Get("resource"); got != provider.mcpURL() {
		t.Fatalf("token request resource = %q, want %q", got, provider.mcpURL())
	}
}

func TestMCPOAuthCodeIsSingleUse(t *testing.T) {
	svc, _, provider, _ := completeLinearJourney(t)

	// Replay the exact callback. The provider has already burned the code, so
	// the exchange must fail — this is the guardrail that makes the abort
	// regression test below meaningful.
	provider.mu.Lock()
	var code string
	for c, ac := range provider.codes {
		if ac.used {
			code = c
		}
	}
	provider.mu.Unlock()
	if code == "" {
		t.Fatal("journey did not consume any authorization code")
	}
	state := strings.TrimPrefix(code, "lin-code-")
	if _, err := svc.HandleCallback(context.Background(), state, code); err == nil {
		t.Fatal("replayed callback succeeded; authorization codes must be single-use")
	}
}

// The 2026-07 regression: a user closing/re-navigating the popup mid-exchange
// canceled the request context, killing the exchange and burning the
// single-use code. The exchange now runs on a detached context.
func TestMCPOAuthCallbackSurvivesClientAbort(t *testing.T) {
	provider := newFakeLinearServer(t)
	queries := newFakeOAuthQueries()
	svc := NewOAuthService(nil, queries, oauthJourneyCallback)
	ctx := context.Background()

	discovery, err := svc.Discover(ctx, provider.mcpURL())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if err := svc.SaveDiscovery(ctx, oauthJourneyConnID, discovery); err != nil {
		t.Fatalf("SaveDiscovery: %v", err)
	}
	auth, err := svc.StartAuthorization(ctx, oauthJourneyConnID, "", "", oauthJourneyCallback)
	if err != nil {
		t.Fatalf("StartAuthorization: %v", err)
	}
	resp := authorizeInBrowser(t, ctx, auth.AuthorizationURL)
	_ = resp.Body.Close()
	loc, _ := url.Parse(resp.Header.Get("Location"))
	code, state := loc.Query().Get("code"), loc.Query().Get("state")

	// Slow token endpoint + a browser that gives up mid-exchange.
	provider.mu.Lock()
	provider.tokenDelay = 300 * time.Millisecond
	provider.mu.Unlock()
	abortCtx, cancel := context.WithCancel(ctx)
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	if _, err := svc.HandleCallback(abortCtx, state, code); err != nil {
		t.Fatalf("HandleCallback with aborted caller: %v", err)
	}
	status, err := svc.GetStatus(ctx, oauthJourneyConnID)
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if !status.HasToken {
		t.Fatal("client abort burned the exchange: no token stored")
	}
}

// A tampered PKCE verifier must fail the exchange at the provider — the
// end-to-end proof that challenge and verifier travel correctly.
func TestMCPOAuthRejectsTamperedPKCE(t *testing.T) {
	provider := newFakeLinearServer(t)
	queries := newFakeOAuthQueries()
	svc := NewOAuthService(nil, queries, oauthJourneyCallback)
	ctx := context.Background()

	discovery, err := svc.Discover(ctx, provider.mcpURL())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if err := svc.SaveDiscovery(ctx, oauthJourneyConnID, discovery); err != nil {
		t.Fatalf("SaveDiscovery: %v", err)
	}
	auth, err := svc.StartAuthorization(ctx, oauthJourneyConnID, "", "", oauthJourneyCallback)
	if err != nil {
		t.Fatalf("StartAuthorization: %v", err)
	}
	resp := authorizeInBrowser(t, ctx, auth.AuthorizationURL)
	_ = resp.Body.Close()
	loc, _ := url.Parse(resp.Header.Get("Location"))

	queries.setVerifier(mustParseUUID(oauthJourneyConnID), "not-the-real-verifier")
	if _, err := svc.HandleCallback(ctx, loc.Query().Get("state"), loc.Query().Get("code")); err == nil {
		t.Fatal("exchange succeeded with a tampered PKCE verifier")
	}
}

// Expiry triggers a refresh; the provider rotates the refresh token, so the
// stored pair must change and the old access token must stop working.
func TestMCPOAuthRefreshRotatesTokens(t *testing.T) {
	svc, queries, provider, connUUID := completeLinearJourney(t)
	ctx := context.Background()

	oldAccess, err := svc.GetValidToken(ctx, oauthJourneyConnID)
	if err != nil {
		t.Fatalf("GetValidToken: %v", err)
	}
	queries.setExpiry(connUUID, time.Now().Add(-time.Minute))

	newAccess, err := svc.GetValidToken(ctx, oauthJourneyConnID)
	if err != nil {
		t.Fatalf("GetValidToken after expiry: %v", err)
	}
	if newAccess == oldAccess {
		t.Fatal("refresh returned the same access token")
	}

	call := func(token string) int {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, provider.mcpURL(), strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req) //nolint:gosec // G704: test-only URL from the local httptest server
		if err != nil {
			t.Fatalf("resource call: %v", err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if got := call(newAccess); got != http.StatusOK {
		t.Fatalf("new access token got %d from the resource server", got)
	}
	if got := call(oldAccess); got != http.StatusUnauthorized {
		t.Fatalf("old access token got %d, want 401 after rotation", got)
	}

	// A second expiry must refresh with the ROTATED refresh token — if the
	// store kept the old one, the provider rejects the grant.
	queries.setExpiry(connUUID, time.Now().Add(-time.Minute))
	if _, err := svc.GetValidToken(ctx, oauthJourneyConnID); err != nil {
		t.Fatalf("second refresh (rotated token): %v", err)
	}
}

// Re-authorizing an already-registered connection must reuse the stored
// client_id — DCR runs once per connection, not once per flow.
func TestMCPOAuthDoesNotReregister(t *testing.T) {
	svc, _, provider, _ := completeLinearJourney(t)

	if _, err := svc.StartAuthorization(context.Background(), oauthJourneyConnID, "", "", oauthJourneyCallback); err != nil {
		t.Fatalf("second StartAuthorization: %v", err)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.registrations) != 1 {
		t.Fatalf("DCR ran %d times for one connection, want 1", len(provider.registrations))
	}
}
