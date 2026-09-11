//go:build integration

package db_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"sync"
	"testing"

	"github.com/felinics/memoh/internal/agentcredential"
	"github.com/felinics/memoh/internal/config"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
)

func TestAgentAuthorizationClaimAndExpiry(t *testing.T) {
	ctx := context.Background()
	pool := teamScopedPool(t)
	seedAppBot(t, pool)
	const agentOne = "30000000-0000-4000-8000-000000000001"
	const agentTwo = "30000000-0000-4000-8000-000000000002"
	for _, id := range []string{agentOne, agentTwo} {
		if _, err := pool.Exec(ctx, `INSERT INTO bot_agents(id, bot_id, name, runtime, enabled, metadata) VALUES ($1,$2,$3,'claude-code',false,'{"auth":"api_key"}')`, id, appBotOneID, id); err != nil {
			t.Fatal(err)
		}
	}
	creds := agentcredential.NewService(postgresstore.NewQueriesWithPool(pool, dbsqlc.New(pool)), config.Config{Auth: config.AuthConfig{AgentCredentialsEncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))}})
	svc := agentcredential.NewAuthorizationService(creds, nil)
	create := func() agentcredential.Authorization {
		t.Helper()
		session, err := svc.Create(ctx, appUserID, agentcredential.AuthorizationRequest{Runtime: "claude-code", AuthKind: agentcredential.AuthKindAnthropicAPIKey, Secret: map[string]string{"api_key": "SECRET-test-key"}})
		if err != nil {
			t.Fatal(err)
		}
		return session
	}
	session := create()
	var ciphertext []byte
	if err := pool.QueryRow(ctx, `SELECT encrypted_payload FROM agent_authorizations WHERE id=$1`, session.ID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte("SECRET")) {
		t.Fatal("plaintext credential stored")
	}
	if _, err := svc.Get(ctx, "10000000-0000-4000-8000-000000000099", session.ID, false); !errors.Is(err, agentcredential.ErrAuthorizationExpired) {
		t.Fatalf("other owner read: %v", err)
	}
	// Competing claims serialize on the session row: exactly one target wins.
	var wg sync.WaitGroup
	results := make(chan string, 2)
	for _, id := range []string{agentOne, agentTwo} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_, err := svc.Claim(ctx, appUserID, session.ID, appBotOneID, id)
			if err == nil {
				results <- id
			} else if !errors.Is(err, agentcredential.ErrRevoked) {
				t.Errorf("claim: %v", err)
			}
		}(id)
	}
	wg.Wait()
	close(results)
	var winners []string
	for id := range results {
		winners = append(winners, id)
	}
	if len(winners) != 1 {
		t.Fatalf("claim winners = %v", winners)
	}
	first, err := svc.Claim(ctx, appUserID, session.ID, appBotOneID, winners[0])
	if err != nil {
		t.Fatal(err)
	}
	again, err := svc.Claim(ctx, appUserID, session.ID, appBotOneID, winners[0])
	if err != nil || first.ID != again.ID {
		t.Fatalf("retry replaced credential: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT encrypted_payload FROM agent_authorizations WHERE id=$1`, session.ID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if len(ciphertext) != 0 {
		t.Fatal("claimed session retains secret")
	}
	if err := svc.Cancel(ctx, appUserID, session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := creds.ResolveForBotAgent(ctx, appBotOneID, winners[0]); err != nil {
		t.Fatalf("cancel detached claimed credential: %v", err)
	}
	expired := create()
	if _, err := pool.Exec(ctx, `UPDATE agent_authorizations SET expires_at=now()-interval '1 second' WHERE id=$1`, expired.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Claim(ctx, appUserID, expired.ID, appBotOneID, agentOne); !errors.Is(err, agentcredential.ErrAuthorizationExpired) {
		t.Fatalf("expired claim: %v", err)
	}
	_ = create()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_authorizations WHERE id=$1`, expired.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("expired session not pruned: %d %v", count, err)
	}
}

func TestAgentAuthorizationMigrationRoundTrip(t *testing.T) {
	pool := teamScopedPool(t)
	ctx := context.Background()
	migrateTo(t, teamMigrationDSN(t), 149)
	var exists bool
	inspect := func(want bool) {
		t.Helper()
		if err := pool.QueryRow(ctx, `SELECT to_regclass('public.agent_authorizations') IS NOT NULL`).Scan(&exists); err != nil || exists != want {
			t.Fatalf("table exists=%v, want=%v, err=%v", exists, want, err)
		}
	}
	inspect(false)
	migrateTo(t, teamMigrationDSN(t), 150)
	inspect(true)
	migrateTo(t, teamMigrationDSN(t), 149)
	inspect(false)
	migrateTo(t, teamMigrationDSN(t), 150)
	inspect(true)
}
