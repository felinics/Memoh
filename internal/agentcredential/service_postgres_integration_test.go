//go:build integration

package agentcredential

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felinics/memoh/internal/config"
	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/dbtest"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	"github.com/felinics/memoh/internal/team"
)

var (
	credMigrationOnce sync.Once
	credMigrationErr  error
)

func TestAttachResolveDetachLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := openCredentialPostgres(t, ctx)
	svc := newCredentialService(t, pool)
	userID, botID, codexAgent, claudeAgent := createCredentialFixture(t, ctx, pool)

	// Attach an API key to the codex instance.
	first, err := svc.AttachToBotAgent(ctx, userID, botID, codexAgent, CreateRequest{
		Provider: ProviderOpenAI, AuthKind: AuthKindOpenAIAPIKey,
		Secret: map[string]string{"api_key": "sk-first"},
	})
	if err != nil {
		t.Fatalf("attach first: %v", err)
	}

	// Runtime resolution decrypts the same secret and reports the provider.
	resolved, err := svc.ResolveForBotAgent(ctx, botID, codexAgent)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.ID != first.ID || resolved.Secret["api_key"] != "sk-first" {
		t.Fatalf("resolve mismatch: %+v", resolved)
	}

	// Replacing revokes the orphaned first credential.
	second, err := svc.AttachToBotAgent(ctx, userID, botID, codexAgent, CreateRequest{
		Provider: ProviderOpenAI, AuthKind: AuthKindOpenAIAPIKey,
		Secret: map[string]string{"api_key": "sk-second"},
	})
	if err != nil {
		t.Fatalf("attach second: %v", err)
	}
	if second.ID == first.ID {
		t.Fatal("replacement reused the credential row")
	}
	assertRevoked(t, ctx, pool, first.ID, true)
	assertRevoked(t, ctx, pool, second.ID, false)

	// A wrong-profile secret cannot link: claude instance rejects an OpenAI key.
	if _, err := svc.AttachToBotAgent(ctx, userID, botID, claudeAgent, CreateRequest{
		Provider: ProviderOpenAI, AuthKind: AuthKindOpenAIAPIKey,
		Secret: map[string]string{"api_key": "sk-wrong"},
	}); !errors.Is(err, ErrIncompatible) {
		t.Fatalf("wrong-profile attach error = %v, want ErrIncompatible", err)
	}

	// Detach clears the pointer and revokes the orphan.
	if err := svc.DetachFromBotAgent(ctx, botID, codexAgent); err != nil {
		t.Fatalf("detach: %v", err)
	}
	assertRevoked(t, ctx, pool, second.ID, true)
	if _, err := svc.ResolveForBotAgent(ctx, botID, codexAgent); !errors.Is(err, ErrNotFound) {
		t.Fatalf("resolve after detach error = %v, want ErrNotFound", err)
	}
	// Detaching an unconnected agent reports not found.
	if err := svc.DetachFromBotAgent(ctx, botID, codexAgent); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second detach error = %v, want ErrNotFound", err)
	}
}

func TestUpdateSecretCASConflict(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := openCredentialPostgres(t, ctx)
	svc := newCredentialService(t, pool)
	userID, botID, codexAgent, _ := createCredentialFixture(t, ctx, pool)

	created, err := svc.AttachToBotAgent(ctx, userID, botID, codexAgent, CreateRequest{
		Provider: ProviderOpenAI, AuthKind: AuthKindOpenAIAPIKey,
		Secret: map[string]string{"api_key": "sk-rotate"},
	})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	updated, err := svc.UpdateSecretCAS(ctx, created.ID, created.CredentialVersion, map[string]string{"api_key": "sk-rotated"}, nil, nil)
	if err != nil {
		t.Fatalf("CAS update: %v", err)
	}
	if updated.CredentialVersion != created.CredentialVersion+1 {
		t.Fatalf("version = %d, want %d", updated.CredentialVersion, created.CredentialVersion+1)
	}
	// A stale writer loses.
	if _, err := svc.UpdateSecretCAS(ctx, created.ID, created.CredentialVersion, map[string]string{"api_key": "sk-stale"}, nil, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale CAS error = %v, want ErrNotFound", err)
	}
	resolved, err := svc.ResolveForBotAgent(ctx, botID, codexAgent)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.Secret["api_key"] != "sk-rotated" {
		t.Fatalf("secret = %q, want rotated value", resolved.Secret["api_key"])
	}
}

func newCredentialService(t *testing.T, pool *pgxpool.Pool) *Service {
	t.Helper()
	cfg := config.Config{}
	cfg.Auth.AgentCredentialsEncryptionKey = base64.StdEncoding.EncodeToString(make([]byte, 32))
	store := postgresstore.NewQueriesWithPool(pool, dbsqlc.New(pool))
	svc := NewService(store, cfg)
	if !svc.Configured() {
		t.Fatal("credential service should be configured")
	}
	return svc
}

func openCredentialPostgres(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set")
	}
	if os.Getenv("TEST_POSTGRES_BOOTSTRAP_SCHEMA") == "1" {
		credMigrationOnce.Do(func() { credMigrationErr = dbtest.MigratePostgresUp(dsn) })
		if credMigrationErr != nil {
			t.Fatalf("migrate PostgreSQL test database: %v", credMigrationErr)
		}
	}
	pool, err := dbpkg.OpenPostgresDSN(ctx, dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func createCredentialFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (userID, botID, codexAgentID, claudeAgentID string) {
	t.Helper()
	uid := uuid.New()
	bid := uuid.New()
	codex := uuid.New()
	claude := uuid.New()
	name := "agent-credential-it-" + uuid.NewString()
	if _, err := pool.Exec(ctx, `
		WITH created_user AS (
			INSERT INTO users (id, username, is_active, metadata)
			VALUES ($1, $2, true, '{}'::jsonb)
			RETURNING id
		), created_member AS (
			INSERT INTO team_members (team_id, user_id, role)
			SELECT $3, id, 'admin' FROM created_user
			RETURNING user_id
		), created_bot AS (
			INSERT INTO bots (id, team_id, owner_user_id, name, status, metadata)
			SELECT $4, $3, user_id, $2, 'ready', '{}'::jsonb
			FROM created_member
			RETURNING id
		), codex_agent AS (
			INSERT INTO bot_agents (id, team_id, bot_id, name, runtime, enabled, metadata)
			SELECT $5, $3, id, 'Codex', 'codex', true, '{"provider":"codex"}'::jsonb
			FROM created_bot
			RETURNING bot_id
		)
		INSERT INTO bot_agents (id, team_id, bot_id, name, runtime, enabled, metadata)
		SELECT $6, $3, bot_id, 'Claude Code', 'claude-code', true, '{"provider":"claude-code"}'::jsonb
		FROM codex_agent`,
		uid, name, team.DefaultTeamID, bid, codex, claude,
	); err != nil {
		t.Fatalf("create credential fixture: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM bots WHERE id = $1", bid)
		_, _ = pool.Exec(context.Background(), "DELETE FROM agent_credentials WHERE owner_user_id = $1", uid)
		_, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", uid)
	})
	return uid.String(), bid.String(), codex.String(), claude.String()
}

func assertRevoked(t *testing.T, ctx context.Context, pool *pgxpool.Pool, credentialID string, want bool) {
	t.Helper()
	var revoked bool
	if err := pool.QueryRow(ctx, "SELECT revoked_at IS NOT NULL FROM agent_credentials WHERE id = $1", credentialID).Scan(&revoked); err != nil {
		t.Fatalf("read revoked state: %v", err)
	}
	if revoked != want {
		t.Fatalf("credential %s revoked = %v, want %v", credentialID, revoked, want)
	}
}
