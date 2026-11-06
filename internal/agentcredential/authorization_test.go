package agentcredential

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/config"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
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

func TestAuthorizationRuntimeEnumMatchesAuthKinds(t *testing.T) {
	field, _ := reflect.TypeOf(AuthorizationRequest{}).FieldByName("Runtime")
	advertised := strings.Split(field.Tag.Get("enums"), ",")
	var known []string
	for _, spec := range authKinds {
		if runtime := string(spec.runtime); !slices.Contains(known, runtime) {
			known = append(known, runtime)
		}
	}
	slices.Sort(advertised)
	slices.Sort(known)
	if !slices.Equal(advertised, known) {
		t.Fatalf("AuthorizationRequest.Runtime enums = %v, authKinds runtimes = %v", advertised, known)
	}
}

func TestAPIKeyAuthorizationStagesTheNormalizedRuntime(t *testing.T) {
	q := &authorizationQueries{}
	creds := NewService(q, config.Config{Auth: config.AuthConfig{AgentCredentialsEncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))}})
	svc := &AuthorizationService{credentials: creds}

	session, err := svc.Create(context.Background(), uuid.NewString(), AuthorizationRequest{Runtime: " Claude-Code ", AuthKind: AuthKindAnthropicAPIKey, Secret: map[string]string{"api_key": "SECRET"}})
	if err != nil || session.Status != "ready" || q.row.Runtime != "claude-code" {
		t.Fatalf("Claude Code API key staging = %+v, stored runtime %q, err %v", session, q.row.Runtime, err)
	}
}

// ChatGPT, Claude and Grok accounts sign in on the Agent itself; nothing stages them beforehand.
func TestAgentSignedInAccountsCannotBeStaged(t *testing.T) {
	q := &authorizationQueries{}
	creds := NewService(q, config.Config{Auth: config.AuthConfig{AgentCredentialsEncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))}})
	svc := &AuthorizationService{credentials: creds}
	for _, req := range []AuthorizationRequest{
		{Runtime: "grok-build", AuthKind: "xai_grok_oauth"},
		{Runtime: "grok-build", AuthKind: "xai_grok_oauth", Secret: map[string]string{"auth_json": "{}"}},
		{Runtime: "codex", AuthKind: "openai_codex_oauth"},
		{Runtime: "claude-code", AuthKind: "claude_code_oauth"},
		{Runtime: "claude-code", AuthKind: "claude_code_oauth", Secret: map[string]string{"oauth_token": "token"}},
	} {
		if _, err := svc.Create(context.Background(), uuid.NewString(), req); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("Create(%+v) error = %v, want ErrInvalidRequest", req, err)
		}
	}
}
