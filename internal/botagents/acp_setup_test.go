package botagents

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

const (
	hermesAgentID = "30000000-0000-4000-8000-000000000003"
	grokAgentID   = "40000000-0000-4000-8000-000000000004"
)

// acpInstanceQueries holds a bot's ACP instances in creation order.
type acpInstanceQueries struct {
	dbstore.Queries
	rows []sqlc.BotAgent
}

func (q *acpInstanceQueries) GetBotAgentByID(_ context.Context, params sqlc.GetBotAgentByIDParams) (sqlc.BotAgent, error) {
	for _, row := range q.rows {
		if row.ID == params.ID {
			return row, nil
		}
	}
	return sqlc.BotAgent{}, pgx.ErrNoRows
}

func (q *acpInstanceQueries) FindActiveBotAgentByRuntimeProvider(context.Context, sqlc.FindActiveBotAgentByRuntimeProviderParams) (sqlc.BotAgent, error) {
	if len(q.rows) == 0 {
		return sqlc.BotAgent{}, pgx.ErrNoRows
	}
	return q.rows[0], nil
}

func acpInstanceRow(t *testing.T, id string, metadata map[string]any) sqlc.BotAgent {
	t.Helper()
	payload, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("marshal instance metadata: %v", err)
	}
	return sqlc.BotAgent{
		ID:       testUUID(id),
		BotID:    testUUID(testBotID),
		Name:     id,
		Runtime:  RuntimeACP,
		Enabled:  true,
		Metadata: payload,
	}
}

func legacyBotMetadata(command string) map[string]any {
	return map[string]any{"acp": map[string]any{"agents": map[string]any{"acp": map[string]any{
		"enabled":    true,
		"setup_mode": "api_key",
		"managed":    map[string]any{"command": command},
	}}}}
}

func TestResolveACPSetupKeepsInstancesApart(t *testing.T) {
	queries := &acpInstanceQueries{}
	queries.rows = []sqlc.BotAgent{
		acpInstanceRow(t, hermesAgentID, map[string]any{"provider": "acp", "managed": map[string]any{"command": "hermes-acp"}}),
		acpInstanceRow(t, grokAgentID, map[string]any{"provider": "acp", "managed": map[string]any{"command": "grok-acp"}}),
	}
	service := NewService(slog.Default(), queries)
	// The shared slot still holds whatever was saved last before instances
	// owned their setup; neither instance may read it.
	botMetadata := legacyBotMetadata("stale-shared-command")

	for agentID, want := range map[string]string{hermesAgentID: "hermes-acp", grokAgentID: "grok-acp"} {
		setup, err := service.ResolveACPSetup(context.Background(), testBotID, agentID, "acp", botMetadata)
		if err != nil {
			t.Fatalf("ResolveACPSetup(%s) error = %v", agentID, err)
		}
		if !setup.Enabled || setup.Managed["command"] != want {
			t.Fatalf("ResolveACPSetup(%s) = %#v, want enabled command %q", agentID, setup, want)
		}
	}
}

func TestResolveACPSetupFallsBackOnlyForInstancesWithoutTheirOwn(t *testing.T) {
	queries := &acpInstanceQueries{}
	queries.rows = []sqlc.BotAgent{
		acpInstanceRow(t, hermesAgentID, map[string]any{"provider": "acp"}),
		acpInstanceRow(t, grokAgentID, map[string]any{"provider": "acp", "managed": map[string]any{}}),
	}
	service := NewService(slog.Default(), queries)
	botMetadata := legacyBotMetadata("hermes-acp")

	legacy, err := service.ResolveACPSetup(context.Background(), testBotID, hermesAgentID, "acp", botMetadata)
	if err != nil {
		t.Fatalf("ResolveACPSetup(legacy instance) error = %v", err)
	}
	if legacy.Managed["command"] != "hermes-acp" {
		t.Fatalf("legacy instance setup = %#v, want the bot's shared command", legacy)
	}

	fresh, err := service.ResolveACPSetup(context.Background(), testBotID, grokAgentID, "acp", botMetadata)
	if err != nil {
		t.Fatalf("ResolveACPSetup(new instance) error = %v", err)
	}
	if fresh.Managed["command"] != "" {
		t.Fatalf("new instance setup = %#v, must not inherit the shared command", fresh)
	}
}

func TestResolveACPSetupForProviderOnlySessions(t *testing.T) {
	botMetadata := legacyBotMetadata("legacy-command")

	queries := &acpInstanceQueries{}
	service := NewService(slog.Default(), queries)
	setup, err := service.ResolveACPSetup(context.Background(), testBotID, "", "acp", botMetadata)
	if err != nil {
		t.Fatalf("ResolveACPSetup(no instance) error = %v", err)
	}
	if setup.Managed["command"] != "legacy-command" {
		t.Fatalf("setup without instances = %#v, want the bot's legacy slot", setup)
	}

	queries.rows = []sqlc.BotAgent{
		acpInstanceRow(t, hermesAgentID, map[string]any{"provider": "acp", "managed": map[string]any{"command": "hermes-acp"}}),
		acpInstanceRow(t, grokAgentID, map[string]any{"provider": "acp", "managed": map[string]any{"command": "grok-acp"}}),
	}
	setup, err = service.ResolveACPSetup(context.Background(), testBotID, "", "acp", botMetadata)
	if err != nil {
		t.Fatalf("ResolveACPSetup(provider only) error = %v", err)
	}
	if setup.Managed["command"] != "hermes-acp" {
		t.Fatalf("provider-only setup = %#v, want the oldest active instance", setup)
	}
}

func TestCreateGivesNewACPInstanceItsOwnSetup(t *testing.T) {
	queries := &fakeQueries{createRow: testRow(true)}
	service := NewService(slog.Default(), queries)

	if _, err := service.Create(context.Background(), testBotID, CreateRequest{
		Name:     "Grok",
		Runtime:  RuntimeACP,
		Metadata: map[string]any{"provider": "acp"},
	}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	var stored map[string]any
	if err := json.Unmarshal(queries.createParams.Metadata, &stored); err != nil {
		t.Fatalf("decode stored metadata: %v", err)
	}
	if _, owns := stored["managed"].(map[string]any); !owns {
		t.Fatalf("stored metadata = %#v, want its own managed fields", stored)
	}
}

func TestUpdateKeepsAnInstanceItsOwnSetup(t *testing.T) {
	storedManaged := func(t *testing.T, current sqlc.BotAgent, sent map[string]any) (any, bool) {
		t.Helper()
		queries := &fakeQueries{getRow: current, updateRow: current, transactions: true}
		if _, err := NewService(slog.Default(), queries).Update(
			context.Background(), testBotID, testAgentID, UpdateRequest{Metadata: sent},
		); err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		var stored map[string]any
		if err := json.Unmarshal(queries.updateParams.Metadata, &stored); err != nil {
			t.Fatalf("decode stored metadata: %v", err)
		}
		managed, owns := stored["managed"]
		return managed, owns
	}

	owned := acpInstanceRow(t, testAgentID, map[string]any{"provider": "acp", "managed": map[string]any{"command": "grok-acp"}})
	managed, owns := storedManaged(t, owned, map[string]any{"provider": "acp"})
	if !owns || managed.(map[string]any)["command"] != "grok-acp" {
		t.Fatalf("stored managed = %#v, want the instance to keep its own setup", managed)
	}

	managed, _ = storedManaged(t, owned, map[string]any{"provider": "acp", "managed": map[string]any{}})
	if len(managed.(map[string]any)) != 0 {
		t.Fatalf("stored managed = %#v, want the explicitly cleared setup", managed)
	}

	// Nothing to carry over: a legacy instance keeps reading the bot's slot
	// until it saves a setup of its own.
	legacy := acpInstanceRow(t, testAgentID, map[string]any{"provider": "acp"})
	if _, owns := storedManaged(t, legacy, map[string]any{"provider": "acp"}); owns {
		t.Fatal("a legacy instance must not gain an empty setup from an unrelated update")
	}
}

func TestWithACPSetupShowsLegacyInstanceItsCurrentSetup(t *testing.T) {
	botMetadata := legacyBotMetadata("hermes-acp")
	legacy := BotAgent{Runtime: RuntimeACP, Metadata: map[string]any{"provider": "acp"}}

	shown := WithACPSetup(legacy, botMetadata)
	managed, _ := shown.Metadata["managed"].(map[string]any)
	if managed["command"] != "hermes-acp" {
		t.Fatalf("shown metadata = %#v, want the setup it launches with", shown.Metadata)
	}
	if _, mutated := legacy.Metadata["managed"]; mutated {
		t.Fatal("WithACPSetup must not write the fallback into the stored metadata")
	}

	owned := BotAgent{Runtime: RuntimeACP, Metadata: map[string]any{"provider": "acp", "managed": map[string]any{"command": "grok-acp"}}}
	if got := WithACPSetup(owned, botMetadata).Metadata["managed"].(map[string]any)["command"]; got != "grok-acp" {
		t.Fatalf("owned instance command = %v, want its own", got)
	}
}
