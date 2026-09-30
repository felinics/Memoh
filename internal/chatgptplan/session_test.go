package chatgptplan

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/config"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

type sessionQueries struct {
	dbstore.Queries
	mu   sync.Mutex
	row  dbsqlc.ChatgptProviderSession
	host pgtype.UUID
}

func (*sessionQueries) SupportsTransactions() bool { return true }
func (q *sessionQueries) InTx(_ context.Context, f func(dbstore.Queries) error) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	before := q.row
	err := f(q)
	if err != nil {
		q.row = before
	}
	return err
}

func (q *sessionQueries) EnsureChatGPTRuntimeHost(context.Context) (pgtype.UUID, error) {
	return q.host, nil
}

func (*sessionQueries) EnsureChatGPTProviderSession(context.Context, dbsqlc.EnsureChatGPTProviderSessionParams) error {
	return nil
}

func (q *sessionQueries) GetChatGPTProviderSession(context.Context, pgtype.UUID) (dbsqlc.ChatgptProviderSession, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.row, nil
}

func (q *sessionQueries) LockChatGPTProviderSession(context.Context, pgtype.UUID) (dbsqlc.ChatgptProviderSession, error) {
	return q.row, nil
}

func (q *sessionQueries) SaveChatGPTProviderSession(_ context.Context, p dbsqlc.SaveChatGPTProviderSessionParams) error {
	q.row.EncryptedPayload = p.EncryptedPayload
	q.row.EncryptionNonce = p.EncryptionNonce
	return nil
}

func newSessionFixture(t *testing.T) (*SessionService, *sessionQueries, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	q := &sessionQueries{host: pgtype.UUID{Bytes: uuid.New(), Valid: true}, row: dbsqlc.ChatgptProviderSession{TeamID: pgtype.UUID{Bytes: uuid.New(), Valid: true}, ProviderID: pgtype.UUID{Bytes: uuid.New(), Valid: true}, OwnerUserID: pgtype.UUID{Bytes: uuid.New(), Valid: true}}}
	s := NewSessionService(q, config.Config{Auth: config.AuthConfig{AgentCredentialsEncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))}})
	s.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var data any
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			data = discovery{Issuer: authBaseURL, JWKSURI: authBaseURL + "/jwks", RevocationEndpoint: authBaseURL + "/revoke"}
		case "/jwks":
			data = map[string]any{"keys": []any{map[string]any{"kty": "RSA", "kid": "test-key", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}}
		default:
			t.Errorf("unexpected auth request %s", r.URL.Path)
		}
		b, _ := json.Marshal(data)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(b))}, nil
	})
	return s, q, key
}

func signedToken(t *testing.T, key *rsa.PrivateKey, claims jwt.Claims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = "test-key"
	raw, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestAuthorizationIdentityScopeAndReplay(t *testing.T) {
	s, q, key := newSessionFixture(t)
	requests := map[string]int{}
	transport := s.client.Transport
	s.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests[r.URL.Path]++
		return transport.RoundTrip(r)
	})
	ctx := context.Background()
	id := q.row.ProviderID.String()
	owner := q.row.OwnerUserID.String()
	begin, err := s.Begin(ctx, id, owner, BeginRequest{RedirectURI: "http://127.0.0.1:45001/auth/callback"})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(begin.AuthorizationURL)
	if u.Query().Get("client_id") != "dynamic_agent_client" || u.Query().Get("ext_agent_host_id") != "urn:uuid:"+q.host.String() {
		t.Fatal("wrong registration or runtime host")
	}
	claims := jwt.RegisteredClaims{Issuer: authBaseURL, Subject: "account", Audience: jwt.ClaimStrings{"oaiapp_test"}, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}
	idToken := signedToken(t, key, identityClaims{RegisteredClaims: claims, Nonce: u.Query().Get("nonce"), Email: "user@example.com"})
	access := claims
	access.Audience = jwt.ClaimStrings{APIBaseURL}
	// Caller claims full permission; the signed token is identity-only. Do not
	// enable inference or lose the registration after declined consent.
	accessToken := signedToken(t, key, accessClaims{RegisteredClaims: access, ClientID: "oaiapp_test", Scope: "openid profile offline_access"})
	req := CompleteRequest{State: begin.State, ClientID: "oaiapp_test", Tokens: Tokens{AccessToken: accessToken, RefreshToken: "refresh", IDToken: idToken, TokenType: "Bearer", Scope: scopes, ExpiresIn: 3600}}
	status, err := s.Complete(ctx, id, owner, req)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Configured || status.UsageEnabled || status.ClientID != "oaiapp_test" {
		t.Fatalf("incorrect consent status: %+v", status)
	}
	if requests["/.well-known/openid-configuration"] != 1 || requests["/jwks"] != 1 {
		t.Fatalf("authorization fetched signing keys more than once: %v", requests)
	}
	if bytes.Contains(q.row.EncryptedPayload, []byte("refresh")) || bytes.Contains(q.row.EncryptedPayload, []byte(idToken)) {
		t.Fatal("plaintext credential storage")
	}
	if _, err = s.AccessToken(ctx, id); !errors.Is(err, ErrPermission) {
		t.Fatalf("identity-only token inferred: %v", err)
	}
	if _, err = s.Complete(ctx, id, owner, req); !errors.Is(err, ErrInvalidAuthorization) {
		t.Fatalf("authorization replay accepted: %v", err)
	}
	second, err := s.Begin(ctx, id, owner, BeginRequest{RedirectURI: "http://127.0.0.1:45002/auth/callback"})
	if err != nil {
		t.Fatal(err)
	}
	uu, _ := url.Parse(second.AuthorizationURL)
	if second.State == begin.State || second.CodeVerifier == begin.CodeVerifier || uu.Query().Get("client_id") != "oaiapp_test" || uu.Query().Get("prompt") != "consent" {
		t.Fatal("reauthorization did not retain registration and renew proof")
	}
	altered := q.row
	altered.ProviderID = pgtype.UUID{Bytes: uuid.New(), Valid: true}
	if _, err = s.open(altered); err == nil {
		t.Fatal("ciphertext moved between providers")
	}
}

func TestIssuedRegistrationSurvivesFailedExchangeAndRestart(t *testing.T) {
	s, q, _ := newSessionFixture(t)
	ctx := context.Background()
	id, owner := q.row.ProviderID.String(), q.row.OwnerUserID.String()
	begin, err := s.Begin(ctx, id, owner, BeginRequest{RedirectURI: "http://127.0.0.1:45001/auth/callback"})
	if err != nil {
		t.Fatal(err)
	}
	req := RegistrationRequest{State: begin.State, ClientID: "oaiapp_registered"}
	if err := s.RetainRegistration(ctx, id, uuid.NewString(), req); !errors.Is(err, ErrOwner) {
		t.Fatalf("wrong owner accepted: %v", err)
	}
	wrongState := req
	wrongState.State = "wrong-state"
	if err := s.RetainRegistration(ctx, id, owner, wrongState); !errors.Is(err, ErrInvalidAuthorization) {
		t.Fatalf("wrong state accepted: %v", err)
	}
	if err := s.RetainRegistration(ctx, id, owner, req); err != nil {
		t.Fatal(err)
	}
	// Restart after code exchange fails.
	s = NewSessionService(q, config.Config{Auth: config.AuthConfig{AgentCredentialsEncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))}})
	status, err := s.Status(ctx, id)
	if err != nil || status.ClientID != req.ClientID || status.Configured || status.UsageEnabled || status.Email != "" {
		t.Fatalf("registration activated credentials or was lost: %+v, %v", status, err)
	}
	if _, err := s.AccessToken(ctx, id); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("unverified registration inferred: %v", err)
	}
	retry, err := s.Begin(ctx, id, owner, BeginRequest{RedirectURI: "http://127.0.0.1:45002/auth/callback"})
	if err != nil {
		t.Fatal(err)
	}
	url1, _ := url.Parse(begin.AuthorizationURL)
	url2, _ := url.Parse(retry.AuthorizationURL)
	if retry.ClientID != req.ClientID || retry.State == begin.State || retry.CodeVerifier == begin.CodeVerifier || url2.Query().Get("nonce") == url1.Query().Get("nonce") || url2.Query().Has("agent_name_hint") || url2.Query().Has("prompt") {
		t.Fatalf("retry lost registration or reused OAuth proof: %+v", retry)
	}
	if err := s.RetainRegistration(ctx, id, owner, req); !errors.Is(err, ErrInvalidAuthorization) {
		t.Fatalf("stale callback accepted: %v", err)
	}
	if err := s.RetainRegistration(ctx, id, owner, RegistrationRequest{State: retry.State, ClientID: "oaiapp_other"}); !errors.Is(err, ErrInvalidAuthorization) {
		t.Fatalf("different registration accepted: %v", err)
	}
}

func TestRefreshSerializedAndTerminalRecovery(t *testing.T) {
	s, q, _ := newSessionFixture(t)
	ctx := context.Background()
	id := q.row.ProviderID.String()
	initial := session{ClientID: "oaiapp_test", Subject: "account", Scope: scopes, ExpiresAt: time.Now().Add(-time.Minute), Tokens: Tokens{AccessToken: "old-access", RefreshToken: "old-refresh", IDToken: "id", TokenType: "Bearer", Scope: scopes, ExpiresIn: 3600}}
	if err := s.save(ctx, q, q.row, &initial); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	s.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		params, _ := url.ParseQuery(string(body))
		if params.Get("refresh_token") != "old-refresh" || params.Get("client_id") != "oaiapp_test" || params.Has("scope") {
			t.Error("wrong refresh request")
		}
		encoded, _ := json.Marshal(Tokens{AccessToken: "new-access", RefreshToken: "new-refresh", IDToken: "id", Scope: scopes, TokenType: "Bearer", ExpiresIn: 3600})
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(encoded))}, nil
	})
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			token, err := s.AccessToken(ctx, id)
			if err != nil || token != "new-access" {
				t.Errorf("refresh result=%q error=%v", token, err)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("rotation occurred %d times", calls.Load())
	}
	current, err := s.open(q.row)
	if err != nil || current.Tokens.RefreshToken != "new-refresh" {
		t.Fatal("replacement not persisted")
	}
	current.ExpiresAt = time.Now().Add(-time.Minute)
	if err = s.save(ctx, q, q.row, &current); err != nil {
		t.Fatal(err)
	}
	s.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader(`{"error":"invalid_grant"}`))}, nil
	})
	if _, err = s.AccessToken(ctx, id); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("terminal refresh result=%v", err)
	}
	cleared, err := s.open(q.row)
	if err != nil || cleared.ClientID != "oaiapp_test" || cleared.Tokens.RefreshToken != "" {
		t.Fatal("terminal failure lost registration or retained unusable token")
	}
}

func TestLocalRedirectAndOwnerBoundaries(t *testing.T) {
	s, q, _ := newSessionFixture(t)
	id := q.row.ProviderID.String()
	owner := q.row.OwnerUserID.String()
	for _, uri := range []string{"http://localhost:45001/auth/callback", "https://127.0.0.1:45001/auth/callback", "http://127.0.0.1:45001/other", "http://127.0.0.1:45001/auth/callback?code=bad"} {
		if _, err := s.Begin(context.Background(), id, owner, BeginRequest{RedirectURI: uri}); !errors.Is(err, ErrInvalidAuthorization) {
			t.Fatalf("accepted callback %s", uri)
		}
	}
	if _, err := s.Begin(context.Background(), id, uuid.NewString(), BeginRequest{RedirectURI: "http://127.0.0.1:45001/auth/callback"}); !errors.Is(err, ErrOwner) {
		t.Fatalf("owner boundary=%v", err)
	}
}
