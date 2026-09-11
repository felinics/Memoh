package agentcredential

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/config"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/providers"
)

type authorizationQueries struct {
	dbstore.Queries
	mu  sync.Mutex
	row dbsqlc.AgentAuthorization
}

func (q *authorizationQueries) InTx(_ context.Context, fn func(dbstore.Queries) error) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	return fn(q)
}
func (*authorizationQueries) LockAgentAuthorizationOwner(context.Context, string) error { return nil }
func (*authorizationQueries) PruneAgentAuthorizations(context.Context) error            { return nil }
func (*authorizationQueries) CountAgentAuthorizations(context.Context, pgtype.UUID) (int64, error) {
	return 0, nil
}

func (q *authorizationQueries) CreateAgentAuthorization(_ context.Context, p dbsqlc.CreateAgentAuthorizationParams) (dbsqlc.AgentAuthorization, error) {
	q.row = dbsqlc.AgentAuthorization{ID: pgtype.UUID{Bytes: uuid.New(), Valid: true}, OwnerUserID: p.OwnerUserID, Runtime: p.Runtime, AuthKind: p.AuthKind, Status: p.Status, EncryptedPayload: p.EncryptedPayload, EncryptionNonce: p.EncryptionNonce, ExpiresAt: p.ExpiresAt, PollAfter: p.PollAfter}
	return q.row, nil
}

func (q *authorizationQueries) GetAgentAuthorization(_ context.Context, p dbsqlc.GetAgentAuthorizationParams) (dbsqlc.AgentAuthorization, error) {
	if q.row.ID != p.ID || q.row.OwnerUserID != p.OwnerUserID {
		return dbsqlc.AgentAuthorization{}, pgx.ErrNoRows
	}
	return q.row, nil
}

func (q *authorizationQueries) UpdateAgentAuthorization(_ context.Context, p dbsqlc.UpdateAgentAuthorizationParams) (dbsqlc.AgentAuthorization, error) {
	q.row.Status = p.Status
	q.row.EncryptedPayload = p.EncryptedPayload
	q.row.EncryptionNonce = p.EncryptionNonce
	q.row.PollAfter = p.PollAfter
	q.row.ExpiresAt = p.ExpiresAt
	return q.row, nil
}

type testCodexAuthorizer struct {
	polls, exchanges int
	pending          bool
	claudeExchange   func(context.Context, string, string, string) (string, error)
}

func (*testCodexAuthorizer) StartClaudeCodeAuthorization() (providers.ClaudeCodeAuthorization, error) {
	return (&providers.Service{}).StartClaudeCodeAuthorization()
}

func (f *testCodexAuthorizer) ExchangeClaudeCodeAuthorization(ctx context.Context, code, state, verifier string) (string, error) {
	return f.claudeExchange(ctx, code, state, verifier)
}

func (*testCodexAuthorizer) StartOpenAICodexACPDeviceAuthorization(context.Context) (providers.OpenAICodexACPDeviceAuthorization, error) {
	return providers.OpenAICodexACPDeviceAuthorization{DeviceAuthID: "PRIVATE-DEVICE-ID", UserCode: "123456", VerificationURL: "https://auth.openai.com/codex/device", IntervalSeconds: 5}, nil
}

func TestClaudeAuthorizationExchangeOwnershipRetryAndRedaction(t *testing.T) {
	q := &authorizationQueries{}
	creds := NewService(q, config.Config{Auth: config.AuthConfig{AgentCredentialsEncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))}})
	calls := 0
	oauth := &testCodexAuthorizer{claudeExchange: func(_ context.Context, code, state, verifier string) (string, error) {
		calls++
		if state == "" || verifier == "" {
			t.Error("PKCE material was not preserved")
		}
		if code == "bad" {
			return "", providers.ErrClaudeCodeAuthorizationCodeInvalid
		}
		return "SECRET-claude-token", nil
	}}
	svc := &AuthorizationService{credentials: creds, oauth: oauth}
	owner := uuid.NewString()
	ctx := context.Background()
	session, err := svc.Create(ctx, owner, AuthorizationRequest{Runtime: "claude-code", AuthKind: AuthKindClaudeCodeOAuth})
	if err != nil || session.Status != "pending" || session.AuthorizationURL == "" {
		t.Fatalf("browser authorization did not start: %v", err)
	}
	secret, err := creds.decrypt(q.row.EncryptedPayload, q.row.EncryptionNonce, 1)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(session)
	if bytes.Contains(raw, []byte(secret["code_verifier"])) || bytes.Contains(q.row.EncryptedPayload, []byte(secret["code_verifier"])) {
		t.Fatal("PKCE verifier leaked")
	}
	q.row.PollAfter = timestamptz(timePtr(time.Now().Add(-time.Second)))
	if _, err := svc.Get(ctx, owner, session.ID, true); err != nil || oauth.polls != 0 {
		t.Fatal("Claude authorization used Codex polling")
	}
	if _, err := svc.Exchange(ctx, uuid.NewString(), session.ID, "code"); !errors.Is(err, ErrAuthorizationExpired) || calls != 0 {
		t.Fatal("another user could exchange the session")
	}
	if _, err := svc.Exchange(ctx, owner, session.ID, "bad"); !errors.Is(err, ErrAuthorizationCodeInvalid) || q.row.Status != "pending" {
		t.Fatal("invalid code did not leave a retryable session")
	}
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			result, err := svc.Exchange(ctx, owner, session.ID, "code")
			if err != nil || result.Status != "ready" {
				t.Errorf("exchange: %v", err)
			}
			raw, _ := json.Marshal(result)
			if bytes.Contains(raw, []byte("SECRET")) || result.AuthorizationURL != "" {
				t.Error("completed session leaked authorization material")
			}
		})
	}
	wg.Wait()
	if calls != 2 {
		t.Fatalf("one-time code exchanged more than once: %d", calls)
	}
	secret, err = creds.decrypt(q.row.EncryptedPayload, q.row.EncryptionNonce, 1)
	if err != nil || secret["oauth_token"] != "SECRET-claude-token" || len(secret) != 1 {
		t.Fatal("token not encrypted or PKCE material not erased")
	}
}

func (f *testCodexAuthorizer) PollOpenAICodexACPDeviceAuthorization(context.Context, string, string) (providers.OpenAICodexACPDevicePollResult, error) {
	f.polls++
	return providers.OpenAICodexACPDevicePollResult{Pending: f.pending, AuthorizationCode: "CODE", CodeVerifier: "VERIFIER"}, nil
}

func (f *testCodexAuthorizer) ExchangeOpenAICodexACPDeviceCode(context.Context, string, string) (providers.OpenAICodexOAuthCredentials, error) {
	f.exchanges++
	return providers.OpenAICodexOAuthCredentials{AccessToken: "SECRET-access", RefreshToken: "SECRET-refresh", IDToken: "SECRET-id", AccountID: "account"}, nil
}

func TestAuthorizationPollSerializesAndRedacts(t *testing.T) {
	q := &authorizationQueries{}
	creds := NewService(q, config.Config{Auth: config.AuthConfig{AgentCredentialsEncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))}})
	oauth := &testCodexAuthorizer{pending: true}
	svc := &AuthorizationService{credentials: creds, oauth: oauth}
	owner := uuid.NewString()
	ctx := context.Background()
	session, err := svc.Create(ctx, owner, AuthorizationRequest{Runtime: "codex", AuthKind: AuthKindOpenAICodexOAuth})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(session)
	if bytes.Contains(raw, []byte("PRIVATE")) {
		t.Fatal("provider device ID leaked")
	}
	if bytes.Contains(q.row.EncryptedPayload, []byte("PRIVATE")) {
		t.Fatal("device session is not encrypted")
	}
	if _, err := svc.Get(ctx, owner, session.ID, true); err != nil {
		t.Fatal(err)
	}
	if oauth.polls != 0 {
		t.Fatal("ignored provider poll interval")
	}
	q.row.PollAfter = timestamptz(timePtr(time.Now().Add(-time.Second)))
	pending, err := svc.Get(ctx, owner, session.ID, true)
	if err != nil || pending.Status != "pending" {
		t.Fatalf("pending: %v", err)
	}
	oauth.pending = false
	q.row.PollAfter = timestamptz(timePtr(time.Now().Add(-time.Second)))
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := svc.Get(ctx, owner, session.ID, true)
			if err != nil || got.Status != "ready" {
				t.Errorf("poll: %v", err)
			}
			raw, _ := json.Marshal(got)
			if bytes.Contains(raw, []byte("SECRET")) || got.UserCode != "" {
				t.Error("completed session leaked login material")
			}
		}()
	}
	wg.Wait()
	if oauth.exchanges != 1 {
		t.Fatalf("authorization code exchanged %d times", oauth.exchanges)
	}
	secret, err := creds.decrypt(q.row.EncryptedPayload, q.row.EncryptionNonce, 1)
	if err != nil || secret["refresh_token"] != "SECRET-refresh" {
		t.Fatal("refresh token not retained encrypted")
	}
}
