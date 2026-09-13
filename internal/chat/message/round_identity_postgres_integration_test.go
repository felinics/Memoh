package message

import (
	"context"
	"testing"

	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	"github.com/felinics/memoh/internal/runtimefence"
)

func TestPostgresPersistRoundReturnsSteerTurnIdentities(t *testing.T) {
	ctx := context.Background()
	pool := openRuntimeFencePostgresPool(t, ctx)
	botID, sessionID := createRuntimeFenceFixtures(t, ctx, pool)
	queries := dbsqlc.New(pool)
	svc := NewService(nil, postgresstore.NewQueriesWithPool(pool, queries))
	token := acquireRuntimeFenceToken(t, ctx, queries, botID, sessionID)
	owner := runtimefence.WithContext(ctx, runtimefence.Fence{BotID: botID.String(), SessionID: sessionID.String(), Token: token})
	roles := []string{"user", "assistant", "tool", "user", "assistant", "user", "assistant"}
	inputs := make([]PersistInput, len(roles))
	for i, role := range roles {
		inputs[i] = PersistInput{
			BotID: botID.String(), SessionID: sessionID.String(), Role: role,
			Content: []byte(`{"role":"` + role + `","content":"same text is intentional"}`),
		}
	}
	persisted, handled, err := svc.PersistRound(owner, inputs, RoundPersistenceOptions{})
	if err != nil || !handled || len(persisted) != len(inputs) {
		t.Fatalf("persist round: %d %v %v", len(persisted), handled, err)
	}
	read, err := svc.ListLatestUIBySession(ctx, sessionID.String(), 100)
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]Message, len(read))
	for _, msg := range read {
		byID[msg.ID] = msg
	}
	var turnID string
	seen := map[string]bool{}
	for i, msg := range persisted {
		durable, ok := byID[msg.ID]
		if !ok || msg.TurnID == "" || msg.TurnPosition == nil ||
			durable.TurnPosition == nil || msg.TurnID != durable.TurnID || *msg.TurnPosition != *durable.TurnPosition {
			t.Fatalf("write/read identity differs at %d: write=%+v read=%+v", i, msg, durable)
		}
		if msg.Role == "user" {
			if seen[msg.TurnID] {
				t.Fatalf("distinct steer reused turn %s", msg.TurnID)
			}
			seen[msg.TurnID] = true
			turnID = msg.TurnID
		}
		if msg.TurnID != turnID {
			t.Fatalf("reply %d not bound to preceding user", i)
		}
	}
}
