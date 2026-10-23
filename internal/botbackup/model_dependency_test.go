package botbackup

import (
	"encoding/json"
	"testing"

	modelpkg "github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/reasoning"
)

// A version 1 switch exported without the disable token upgrades by its
// provider's protocol. The model entry alone cannot say which, so the import
// reads it again with the client type the backup's provider list names.
func TestModelDependencyUpgradesWithItsProviderClientType(t *testing.T) {
	t.Parallel()

	var dep modelDependency
	if err := json.Unmarshal([]byte(`{"model_id":"m","provider_id":"p","type":"chat",`+
		`"config":{"compatibilities":["reasoning"],"thinking_mode":"toggle","reasoning_efforts":["enabled"]}}`), &dep); err != nil {
		t.Fatal(err)
	}
	if dep.Config.Reasoning == nil || dep.Config.Reasoning.Off != "" {
		t.Fatalf("decoded without the provider: %+v", dep.Config.Reasoning)
	}
	if err := dep.withClientType(string(modelpkg.ClientTypeOpenAICompletions)); err != nil {
		t.Fatal(err)
	}
	if dep.Config.Reasoning == nil || dep.Config.Reasoning.Off != reasoning.OffSupportRejected {
		t.Fatalf("reasoning = %+v, want off rejected on OpenAI", dep.Config.Reasoning)
	}

	unknown := dep
	unknown.Config = modelpkg.ModelConfig{ToolCall: true}
	if err := unknown.withClientType(""); err != nil || !unknown.Config.ToolCall {
		t.Fatalf("an unknown provider changed the config: %+v, %v", unknown.Config, err)
	}
}
