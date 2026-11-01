package acp

import (
	"context"
	"testing"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	acpprofile "github.com/felinics/memoh/internal/agent/runtime/external/acp/profile"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

type setupByInstance map[string]acpprofile.AgentSetup

func (s setupByInstance) ResolveACPSetup(_ context.Context, _, botAgentID, _ string, _ map[string]any) (acpprofile.AgentSetup, error) {
	return s[botAgentID], nil
}

type infoRunner struct{ external.Workspaces }

func (infoRunner) WorkspaceInfo(context.Context, string) (bridge.WorkspaceInfo, error) {
	return bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendContainer, DefaultWorkDir: "/data"}, nil
}

func instanceSetup(command string) acpprofile.AgentSetup {
	return acpprofile.AgentSetup{
		AgentID: acpprofile.AgentACPID, Enabled: true, Mode: "api_key",
		Managed: map[string]string{"command": command, "api_key": "sk-1"},
	}
}

// Every generic instance shares the "acp" profile id, so only the instance
// id tells a Hermes process from a Grok one, and an edit to an instance's
// setup must replace its processes.
func TestEachInstanceStartsFromItsOwnSetup(t *testing.T) {
	setups := setupByInstance{"agent-hermes": instanceSetup("hermes"), "agent-grok": instanceSetup("grok")}
	pool := newSessionPool(nil, infoRunner{}, fakeBotGetter{bot: bots.Bot{ID: "bot-1"}}, newTestSupervisor(t), Deps{Setups: setups})
	launchFor := func(botAgentID string) Launch {
		t.Helper()
		l, err := pool.Launch(context.Background(), PromptInput{
			BotID: "bot-1", BotAgentID: botAgentID, AgentID: acpprofile.AgentACPID, RuntimeOwnerAccountID: "user-1",
		})
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	hermes, grok := launchFor("agent-hermes"), launchFor("agent-grok")
	if hermes.Key == grok.Key || hermes.Fingerprint == grok.Fingerprint {
		t.Fatalf("instances share a process: %+v / %+v", hermes.Key, grok.Key)
	}
	setups["agent-hermes"] = instanceSetup("hermes-v2")
	if launchFor("agent-hermes").Fingerprint == hermes.Fingerprint {
		t.Fatal("an edited setup keeps the old process")
	}
}
