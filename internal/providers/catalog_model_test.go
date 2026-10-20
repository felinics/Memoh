package providers

import (
	"encoding/json"
	"testing"

	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/reasoning"
)

// A catalog model sent in the version 1 shape upgrades by the provider's
// protocol once the import knows it, as a single model's request does.
func TestCatalogModelConfigForReadsTheProviderClientType(t *testing.T) {
	t.Parallel()

	var m CatalogModel
	if err := json.Unmarshal([]byte(`{"model_id":"m","config":{"compatibilities":["reasoning"],"thinking_mode":"toggle","reasoning_efforts":["enabled"]}}`), &m); err != nil {
		t.Fatal(err)
	}
	if m.Config.Reasoning == nil || m.Config.Reasoning.Off != "" {
		t.Fatalf("decoded without the provider: %+v", m.Config.Reasoning)
	}
	config, err := m.ConfigFor(string(models.ClientTypeOpenAICompletions))
	if err != nil {
		t.Fatal(err)
	}
	if config.Reasoning == nil || config.Reasoning.Off != reasoning.OffSupportRejected {
		t.Fatalf("reasoning = %+v, want off rejected on OpenAI", config.Reasoning)
	}

	var current CatalogModel
	if err := json.Unmarshal([]byte(`{"model_id":"m","config":{"schema_version":2,"tool_call":true}}`), &current); err != nil {
		t.Fatal(err)
	}
	if config, err := current.ConfigFor(string(models.ClientTypeOpenAICompletions)); err != nil || !config.ToolCall {
		t.Fatalf("current config = %+v, %v", config, err)
	}
}
