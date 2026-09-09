package acp

import (
	"context"
	"maps"
	"sync"
	"testing"

	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
	"github.com/felinics/memoh/internal/agent/sessionmode"
)

type durablePairStore struct {
	mu   sync.Mutex
	pair map[string]any
}

func (s *durablePairStore) Get(context.Context, string) (SessionDescriptor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return SessionDescriptor{
		BotID: "bot-1", SessionType: sessionmode.ACPAgent, IsACP: true,
		Metadata:        map[string]any{"acp_agent_id": acpprofile.AgentACPID, "project_path": "/data/project", "runtime_owner_account_id": "user-1"},
		RuntimeMetadata: maps.Clone(s.pair),
	}, nil
}

func (s *durablePairStore) SaveModelPreference(_ context.Context, _ string, model, effort string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pair = map[string]any{"acp_model_id": model, "acp_reasoning_effort": effort}
	return nil
}

func TestSessionPoolRestoresPersistedPromptPairAfterRuntimeClose(t *testing.T) {
	t.Setenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_MODELS", "1")
	t.Setenv("MEMOH_ACP_SESSION_POOL_FAKE_AGENT_REASONING", "1")
	pool := newFakeScriptPool(t)
	store := &durablePairStore{}
	pool.store = store
	input := PromptInput{
		BotID: "bot-1", SessionID: "session-1", AgentID: acpprofile.AgentACPID,
		ProjectPath: "/data/project", RuntimeOwnerAccountID: "user-1", Prompt: "persist selection", ModelID: "gpt-5.1-codex-high", ReasoningEffort: "low",
	}
	if _, err := pool.Prompt(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	saved, _ := store.Get(context.Background(), "session-1")
	if saved.RuntimeMetadata["acp_model_id"] != input.ModelID || saved.RuntimeMetadata["acp_reasoning_effort"] != "low" {
		t.Fatalf("saved pair = %v", saved.RuntimeMetadata)
	}
	old := pool.sessionHandle("session-1")
	if err := pool.CloseRuntime("bot-1", old.id); err != nil {
		t.Fatal(err)
	}
	input.ModelID, input.ReasoningEffort = "", ""
	if _, err := pool.Ensure(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	restored := pool.sessionHandle("session-1")
	if restored == old {
		t.Fatal("runtime was not recreated")
	}
	if got := restored.session.ModelState().CurrentModelID; got != "gpt-5.1-codex-high" {
		t.Fatalf("restored model = %q", got)
	}
	if got := restored.session.ReasoningState().CurrentEffort; got != "low" {
		t.Fatalf("restored effort = %q", got)
	}
}
