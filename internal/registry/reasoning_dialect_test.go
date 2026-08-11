package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Declaring a wire dialect per model is what lets the code avoid sniffing model
// ids, but it moves a cost: a new model added without one silently falls back to
// the provider's modern default. For Google that fallback is wrong half the time
// — a 2.5 model treated as 3.x gets thinkingLevel, which the API rejects.
//
// This test is the tripwire. It is scoped to providers whose generations actually
// disagree about the wire shape; elsewhere an empty dialect is correct and
// requiring one would be noise.
func TestGoogleReasoningModelsDeclareAWireDialect(t *testing.T) {
	t.Parallel()

	const dir = "../../conf/providers"
	raw, err := os.ReadFile(filepath.Join(dir, "google.yaml")) //nolint:gosec // repo-local template
	if err != nil {
		t.Fatalf("read google.yaml: %v", err)
	}

	var doc struct {
		Models []struct {
			ModelID string         `yaml:"model_id"`
			Config  map[string]any `yaml:"config"`
		} `yaml:"models"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse google.yaml: %v", err)
	}
	if len(doc.Models) == 0 {
		t.Fatal("no models parsed; the fixture path or schema changed")
	}

	for _, m := range doc.Models {
		mode, _ := m.Config["thinking_mode"].(string)
		if mode == "" || mode == "none" {
			continue
		}
		dialect, _ := m.Config["reasoning_dialect"].(string)
		if dialect == "" {
			t.Errorf("%s declares thinking_mode %q but no reasoning_dialect: "+
				"2.5 needs \"budget\", 3.x needs \"tier\", and guessing from the id is "+
				"what this field exists to avoid", m.ModelID, mode)
			continue
		}
		if dialect != "budget" && dialect != "tier" {
			t.Errorf("%s has unknown reasoning_dialect %q", m.ModelID, dialect)
		}
		// A budget dialect without bounds would scale tiers across a zero range and
		// collapse every tier onto the dynamic sentinel.
		if dialect == "budget" {
			if _, ok := m.Config["thinking_budget_max"]; !ok {
				t.Errorf("%s uses the budget dialect but declares no thinking_budget_max", m.ModelID)
			}
		}
	}
}

// Off-ability on the budget dialect is not a matter of taste: a zero budget is
// legal only where the model's floor is zero. Flash allows it, Pro does not, and
// the catalog has to say the same thing the wire will.
func TestGoogleBudgetModelsAdvertiseOffOnlyWhenTheFloorIsZero(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("../../conf/providers/google.yaml") //nolint:gosec // repo-local template
	if err != nil {
		t.Fatalf("read google.yaml: %v", err)
	}
	var doc struct {
		Models []struct {
			ModelID string         `yaml:"model_id"`
			Config  map[string]any `yaml:"config"`
		} `yaml:"models"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse google.yaml: %v", err)
	}

	for _, m := range doc.Models {
		if dialect, _ := m.Config["reasoning_dialect"].(string); dialect != "budget" {
			continue
		}
		minBudget, hasMin := m.Config["thinking_budget_min"].(int)
		efforts, _ := m.Config["reasoning_efforts"].([]any)
		advertisesOff := false
		for _, e := range efforts {
			if s, ok := e.(string); ok && strings.TrimSpace(s) == "disable" {
				advertisesOff = true
			}
		}

		switch {
		case hasMin && minBudget == 0 && !advertisesOff:
			t.Errorf("%s allows a zero budget but does not advertise off; the control "+
				"would be hidden from a model that supports it", m.ModelID)
		case hasMin && minBudget > 0 && advertisesOff:
			t.Errorf("%s advertises off but its floor is %d, so a zero budget is a 400; "+
				"the picker would offer a switch the wire rejects", m.ModelID, minBudget)
		}
	}
}
