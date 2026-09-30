package chatgptplan

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

const (
	authBaseURL = "https://auth.openai.com"
	scopes      = "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"
)

// Tokens never leave the main process except over the authenticated import API.
// The renderer only receives Status. ID tokens are verified before persistence.
type Tokens struct {
	AccessToken       string          `json:"access_token"`  // #nosec G117 -- OAuth wire DTO; persistence is encrypted and responses never include it.
	RefreshToken      string          `json:"refresh_token"` // #nosec G117 -- OAuth wire DTO; persistence is encrypted and responses never include it.
	IDToken           string          `json:"id_token"`
	TokenType         string          `json:"token_type"`
	Scope             string          `json:"scope"`
	ExpiresIn         int64           `json:"expires_in"`
	EarliestRefreshAt json.RawMessage `json:"earliest_refresh_at,omitempty"`
}
type BeginRequest struct {
	RedirectURI string `json:"redirect_uri"`
}
type BeginResponse struct {
	AuthorizationURL string    `json:"authorization_url"`
	State            string    `json:"state"`
	CodeVerifier     string    `json:"code_verifier"`
	ClientID         string    `json:"client_id"`
	ExpiresAt        time.Time `json:"expires_at"`
}
type CompleteRequest struct {
	State    string `json:"state"`
	ClientID string `json:"client_id"`
	Tokens   Tokens `json:"tokens"`
}
type RegistrationRequest struct {
	State    string `json:"state"`
	ClientID string `json:"client_id"`
}
type Status struct {
	Configured    bool       `json:"configured"`
	UsageEnabled  bool       `json:"usage_enabled"`
	NeedsRecovery bool       `json:"needs_recovery"`
	Available     bool       `json:"available"`
	OwnerUserID   string     `json:"owner_user_id,omitempty"`
	ClientID      string     `json:"client_id,omitempty"`
	Email         string     `json:"email,omitempty"`
	Scope         string     `json:"scope,omitempty"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
}
type pending struct {
	State     string
	Nonce     string
	ExpiresAt time.Time
}
type session struct {
	ClientID  string
	Subject   string
	Email     string
	Scope     string
	ExpiresAt time.Time
	Tokens    Tokens
	Pending   *pending
}
type SessionService struct {
	queries dbstore.Queries
	aead    cipher.AEAD
	client  *http.Client
}

func NewSessionService(queries dbstore.Queries, cfg config.Config) *SessionService {
	s := &SessionService{queries: queries, client: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(cfg.Auth.AgentCredentialsEncryptionKey))
	if err != nil || len(key) != 32 {
		return s
	}
	block, err := aes.NewCipher(key)
	if err == nil {
		s.aead, _ = cipher.NewGCM(block)
	}
	return s
}
func (s *SessionService) Available() bool { return s != nil && s.aead != nil && s.queries != nil }
func (s *SessionService) Begin(ctx context.Context, providerID, ownerID string, req BeginRequest) (BeginResponse, error) {
	if !s.Available() {
		return BeginResponse{}, ErrEncryptionUnavailable
	}
	redirect, err := url.Parse(req.RedirectURI)
	if err != nil || redirect.Scheme != "http" || redirect.Hostname() != "127.0.0.1" || redirect.Port() == "" || redirect.Path != "/auth/callback" || redirect.RawQuery != "" || redirect.Fragment != "" || redirect.User != nil {
		return BeginResponse{}, ErrInvalidAuthorization
	}
	id, err := db.ParseUUID(providerID)
	if err != nil {
		return BeginResponse{}, ErrInvalidAuthorization
	}
	owner, err := db.ParseUUID(ownerID)
	if err != nil {
		return BeginResponse{}, ErrInvalidAuthorization
	}
	if err = s.queries.EnsureChatGPTProviderSession(ctx, dbsqlc.EnsureChatGPTProviderSessionParams{ProviderID: id, OwnerUserID: owner}); err != nil {
		return BeginResponse{}, err
	}
	host, err := s.queries.EnsureChatGPTRuntimeHost(ctx)
	if err != nil {
		return BeginResponse{}, err
	}
	state, err := randomString()
	if err != nil {
		return BeginResponse{}, err
	}
	nonce, err := randomString()
	if err != nil {
		return BeginResponse{}, err
	}
	verifier, err := randomString()
	if err != nil {
		return BeginResponse{}, err
	}
	challenge := sha256.Sum256([]byte(verifier))
	expires := time.Now().Add(10 * time.Minute)
	result := BeginResponse{State: state, CodeVerifier: verifier, ExpiresAt: expires}
	err = s.locked(ctx, id, func(q dbstore.Queries, row dbsqlc.ChatgptProviderSession, data *session) error {
		if row.OwnerUserID != owner {
			return ErrOwner
		}
		clientID := data.ClientID
		if clientID == "" {
			clientID = "dynamic_agent_client"
		}
		params := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {req.RedirectURI}, "scope": {scopes}, "resource": {APIBaseURL}, "state": {state}, "nonce": {nonce}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"}, "ext_agent_host_id": {"urn:uuid:" + host.String()}}
		if data.ClientID == "" {
			params.Set("agent_name_hint", "Memoh")
		} else if data.Tokens.IDToken != "" {
			params.Set("id_token_hint", data.Tokens.IDToken)
		}
		if data.Subject != "" && !usageEnabled(data.Scope) {
			params.Set("prompt", "consent")
		}
		data.Pending = &pending{State: state, Nonce: nonce, ExpiresAt: expires}
		result.ClientID = clientID
		result.AuthorizationURL = authBaseURL + "/api/accounts/authorize?" + params.Encode()
		return s.save(ctx, q, row, data)
	})
	return result, err
}

// RetainRegistration preserves an issued client before code exchange.
func (s *SessionService) RetainRegistration(ctx context.Context, providerID, ownerID string, req RegistrationRequest) error {
	id, err := db.ParseUUID(providerID)
	if err != nil {
		return ErrInvalidAuthorization
	}
	owner, err := db.ParseUUID(ownerID)
	if err != nil || !strings.HasPrefix(req.ClientID, "oaiapp_") {
		return ErrInvalidAuthorization
	}
	return s.locked(ctx, id, func(q dbstore.Queries, row dbsqlc.ChatgptProviderSession, data *session) error {
		if row.OwnerUserID != owner {
			return ErrOwner
		}
		if data.Pending == nil || time.Now().After(data.Pending.ExpiresAt) || data.Pending.State != req.State || (data.ClientID != "" && data.ClientID != req.ClientID) {
			return ErrInvalidAuthorization
		}
		data.ClientID = req.ClientID
		return s.save(ctx, q, row, data)
	})
}

func (s *SessionService) Complete(ctx context.Context, providerID, ownerID string, req CompleteRequest) (Status, error) {
	id, err := db.ParseUUID(providerID)
	if err != nil {
		return Status{}, ErrInvalidAuthorization
	}
	owner, err := db.ParseUUID(ownerID)
	if err != nil {
		return Status{}, ErrInvalidAuthorization
	}
	if !strings.HasPrefix(req.ClientID, "oaiapp_") {
		return Status{}, ErrInvalidAuthorization
	}
	err = s.locked(ctx, id, func(q dbstore.Queries, row dbsqlc.ChatgptProviderSession, data *session) error {
		if row.OwnerUserID != owner {
			return ErrOwner
		}
		if data.Pending == nil || time.Now().After(data.Pending.ExpiresAt) || data.Pending.State != req.State || (data.ClientID != "" && data.ClientID != req.ClientID) {
			return ErrInvalidAuthorization
		}
		if !validTokens(req.Tokens) {
			return ErrInvalidAuthorization
		}
		key, err := s.signingKeys(ctx)
		if err != nil {
			return err
		}
		identity, err := verifyIDToken(req.Tokens.IDToken, req.ClientID, data.Pending.Nonce, key)
		if err != nil {
			return err
		}
		if data.Subject != "" && data.Subject != identity.Subject {
			return ErrInvalidAuthorization
		}
		access, err := verifyAccessToken(req.Tokens.AccessToken, req.ClientID, identity.Subject, key)
		if err != nil {
			return err
		}
		req.Tokens.Scope = access.Scope
		data.ClientID = req.ClientID
		data.Subject = identity.Subject
		data.Email = identity.Email
		data.Scope = req.Tokens.Scope
		data.Tokens = req.Tokens
		data.ExpiresAt = access.ExpiresAt.Time
		data.Pending = nil
		return s.save(ctx, q, row, data)
	})
	if err != nil {
		return Status{}, err
	}
	return s.Status(ctx, providerID)
}

func (s *SessionService) Status(ctx context.Context, providerID string) (Status, error) {
	if !s.Available() {
		return Status{Available: false}, nil
	}
	id, err := db.ParseUUID(providerID)
	if err != nil {
		return Status{}, ErrInvalidAuthorization
	}
	row, err := s.queries.GetChatGPTProviderSession(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Status{Available: true}, nil
	}
	if err != nil {
		return Status{}, err
	}
	data, err := s.open(row)
	if err != nil {
		return Status{}, err
	}
	status := Status{Available: true, Configured: data.Tokens.RefreshToken != "", UsageEnabled: usageEnabled(data.Scope) && data.Tokens.RefreshToken != "", NeedsRecovery: data.Tokens.RefreshToken != "" && data.Tokens.AccessToken == "", OwnerUserID: row.OwnerUserID.String(), ClientID: data.ClientID, Email: data.Email, Scope: data.Scope}
	if status.Configured {
		status.ExpiresAt = &data.ExpiresAt
	}
	return status, nil
}

func (s *SessionService) AccessToken(ctx context.Context, providerID string) (string, error) {
	id, err := db.ParseUUID(providerID)
	if err != nil {
		return "", ErrNotConnected
	}
	var token string
	var terminal error
	err = s.locked(ctx, id, func(q dbstore.Queries, row dbsqlc.ChatgptProviderSession, data *session) error {
		if data.Tokens.RefreshToken == "" {
			return ErrNotConnected
		}
		if !usageEnabled(data.Scope) {
			return ErrPermission
		}
		if data.Tokens.AccessToken != "" && time.Until(data.ExpiresAt) > 2*time.Minute {
			token = data.Tokens.AccessToken
			return nil
		}
		form := url.Values{"grant_type": {"refresh_token"}, "client_id": {data.ClientID}, "refresh_token": {data.Tokens.RefreshToken}, "resource": {APIBaseURL}}
		var tokens Tokens
		if err := s.postForm(ctx, authBaseURL+"/api/accounts/oauth/token", form, &tokens); err != nil {
			if errors.Is(err, ErrRefreshInvalid) {
				data.Tokens = Tokens{}
				data.ExpiresAt = time.Time{}
				terminal = errors.Join(ErrNotConnected, err)
				return s.save(ctx, q, row, data)
			}
			return err
		}
		// Refresh may omit unmodified ID token/scope; a rotated refresh token is required.
		if tokens.IDToken == "" {
			tokens.IDToken = data.Tokens.IDToken
		}
		if tokens.Scope == "" {
			tokens.Scope = data.Scope
		}
		if !validTokens(tokens) {
			return ErrInvalidAuthorization
		}
		// The HTTPS token endpoint authenticated this issued client and refresh
		// token. Persist its replacement without a second network dependency after
		// rotation; transient JWKS failures must not strand the old refresh token.

		data.Tokens = tokens
		data.Scope = tokens.Scope
		data.ExpiresAt = time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second)
		if err := s.save(ctx, q, row, data); err != nil {
			return err
		}
		if !usageEnabled(data.Scope) {
			terminal = ErrPermission
			return nil
		}
		token = tokens.AccessToken
		return nil
	})
	if err == nil && terminal != nil {
		err = terminal
	}
	return token, err
}

type sessionTokenSource struct {
	sessions   *SessionService
	providerID string
}

func (s *SessionService) TokenSource(providerID string) TokenSource {
	return &sessionTokenSource{sessions: s, providerID: providerID}
}

func (s *sessionTokenSource) AccessToken(ctx context.Context) (string, error) {
	return s.sessions.AccessToken(ctx, s.providerID)
}

func (s *sessionTokenSource) RejectAccessToken(ctx context.Context, rejected string) error {
	id, err := db.ParseUUID(s.providerID)
	if err != nil {
		return ErrNotConnected
	}
	return s.sessions.locked(ctx, id, func(q dbstore.Queries, row dbsqlc.ChatgptProviderSession, data *session) error {
		// A delayed rejection must not invalidate a token refreshed or imported
		// by another request. Retain the renewable session for recovery.
		if rejected == "" || data.Tokens.AccessToken != rejected {
			return nil
		}
		data.Tokens.AccessToken = ""
		data.ExpiresAt = time.Time{}
		return s.sessions.save(ctx, q, row, data)
	})
}

func (s *SessionService) Revoke(ctx context.Context, providerID, ownerID string) error {
	id, err := db.ParseUUID(providerID)
	if err != nil {
		return ErrInvalidAuthorization
	}
	owner, err := db.ParseUUID(ownerID)
	if err != nil {
		return ErrInvalidAuthorization
	}
	return s.locked(ctx, id, func(q dbstore.Queries, row dbsqlc.ChatgptProviderSession, data *session) error {
		if row.OwnerUserID != owner {
			return ErrOwner
		}
		if data.Tokens.RefreshToken != "" {
			discovery, err := s.discover(ctx)
			if err != nil {
				return err
			}
			if !trustedAuthURL(discovery.RevocationEndpoint) {
				return ErrUpstream
			}
			if err := s.postForm(ctx, discovery.RevocationEndpoint, url.Values{"token": {data.Tokens.RefreshToken}, "token_type_hint": {"refresh_token"}, "client_id": {data.ClientID}}, nil); err != nil {
				return err
			}
		}
		data.Tokens = Tokens{}
		data.Pending = nil
		data.ExpiresAt = time.Time{}
		return s.save(ctx, q, row, data)
	})
}

func validTokens(t Tokens) bool {
	if t.AccessToken == "" || t.RefreshToken == "" || t.IDToken == "" || t.ExpiresIn <= 0 || t.ExpiresIn > 86400 || !strings.EqualFold(t.TokenType, "bearer") {
		return false
	}
	granted := map[string]bool{}
	for _, scope := range strings.Fields(t.Scope) {
		granted[scope] = true
	}
	return granted["openid"]
}

func usageEnabled(scope string) bool {
	granted := map[string]bool{}
	for _, s := range strings.Fields(scope) {
		granted[s] = true
	}
	return granted["resource.invoke"] && granted["chatgpt.tokens.use.direct"]
}

func randomString() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (s *SessionService) locked(ctx context.Context, id pgtype.UUID, fn func(dbstore.Queries, dbsqlc.ChatgptProviderSession, *session) error) error {
	if !s.Available() {
		return ErrEncryptionUnavailable
	}
	tx, ok := s.queries.(interface {
		InTx(context.Context, func(dbstore.Queries) error) error
		SupportsTransactions() bool
	})
	if !ok || !tx.SupportsTransactions() {
		return ErrEncryptionUnavailable
	}
	return tx.InTx(ctx, func(q dbstore.Queries) error {
		row, err := q.LockChatGPTProviderSession(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotConnected
		}
		if err != nil {
			return err
		}
		data, err := s.open(row)
		if err != nil {
			return err
		}
		return fn(q, row, &data)
	})
}

func (*SessionService) aad(row dbsqlc.ChatgptProviderSession) []byte {
	return []byte("memoh:chatgpt:v1:" + row.TeamID.String() + ":" + row.ProviderID.String() + ":" + row.OwnerUserID.String())
}

func (s *SessionService) open(row dbsqlc.ChatgptProviderSession) (session, error) {
	if len(row.EncryptedPayload) == 0 {
		return session{}, nil
	}
	if len(row.EncryptionNonce) != s.aead.NonceSize() {
		return session{}, ErrEncryptionUnavailable
	}
	plain, err := s.aead.Open(nil, row.EncryptionNonce, row.EncryptedPayload, s.aad(row))
	if err != nil {
		return session{}, ErrEncryptionUnavailable
	}
	var data session
	err = json.Unmarshal(plain, &data)
	return data, err
}

func (s *SessionService) save(ctx context.Context, q dbstore.Queries, row dbsqlc.ChatgptProviderSession, data *session) error {
	plain, err := json.Marshal(data)
	if err != nil {
		return err
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return err
	}
	encrypted := s.aead.Seal(nil, nonce, plain, s.aad(row))
	return q.SaveChatGPTProviderSession(ctx, dbsqlc.SaveChatGPTProviderSessionParams{ProviderID: row.ProviderID, EncryptedPayload: encrypted, EncryptionNonce: nonce})
}

func (s *SessionService) postForm(ctx context.Context, endpoint string, form url.Values, out any) error {
	if !trustedAuthURL(endpoint) {
		return ErrUpstream
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return ErrUpstream
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// #nosec G704 -- trustedAuthURL restricts endpoints to auth.openai.com; redirects are disabled.
	resp, err := s.client.Do(req)
	if err != nil {
		return ErrUpstream
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		failure := readUpstreamError(resp)
		switch failure.Code {
		case "invalid_grant", "invalid_refresh_token", "token_expired", "refresh_token_expired", "refresh_token_invalidated", "refresh_token_reused":
			failure.kind = ErrRefreshInvalid
		case "invalid_client":
			failure.kind = ErrInvalidAuthorization
		default:
			failure.kind = ErrUpstream
		}
		return failure
	}
	if out == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return err
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out) != nil {
		return ErrUpstream
	}
	return nil
}
