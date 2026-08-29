package registry

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// Declaring a wire dialect per model is what lets the code avoid sniffing model
// ids, but it moves a cost: a new controllable model added without one has to fail
// closed and sends no control. That is safe for upgrades, but it would quietly
// turn a newly catalogued picker's choices into a placebo.
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
		compatibilities, _ := m.Config["compatibilities"].([]any)
		hasReasoning := false
		for _, compatibility := range compatibilities {
			if compatibility == "reasoning" {
				hasReasoning = true
				break
			}
		}
		if !hasReasoning || mode == "none" || mode == "always" {
			continue
		}
		if mode == "" {
			t.Errorf("%s declares reasoning compatibility but no thinking_mode", m.ModelID)
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

// The active budget range does not answer whether the separate zero sentinel is
// accepted: Flash Lite starts active thinking at 512 but still accepts 0 to turn
// it off. Require the catalog to declare that capability instead of deriving it
// from the active floor.
func TestGoogleBudgetModelsDeclareOffSupport(t *testing.T) {
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
		offSupport, _ := m.Config["reasoning_off_support"].(string)
		if offSupport != "accepted" && offSupport != "rejected" {
			t.Errorf("%s must declare reasoning_off_support as accepted or rejected; got %q",
				m.ModelID, offSupport)
		}
	}
}

func TestMinimaxReasoningModelsDeclareAMode(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("../../conf/providers/minimax.yaml") //nolint:gosec // repo-local template
	if err != nil {
		t.Fatalf("read minimax.yaml: %v", err)
	}
	var doc struct {
		Models []struct {
			ModelID string         `yaml:"model_id"`
			Config  map[string]any `yaml:"config"`
		} `yaml:"models"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse minimax.yaml: %v", err)
	}

	foundM27 := false
	for _, m := range doc.Models {
		compatibilities, _ := m.Config["compatibilities"].([]any)
		hasReasoning := false
		for _, compatibility := range compatibilities {
			if compatibility == "reasoning" {
				hasReasoning = true
				break
			}
		}
		if m.ModelID == "MiniMax-M2.7" {
			foundM27 = true
			mode, _ := m.Config["thinking_mode"].(string)
			if !hasReasoning || mode != "always" {
				t.Errorf("%s is an always-on reasoning model; compatibilities = %v, thinking_mode = %q",
					m.ModelID, compatibilities, mode)
			}
		}
		if !hasReasoning {
			continue
		}
		mode, _ := m.Config["thinking_mode"].(string)
		if mode != "always" && mode != "toggle" {
			t.Errorf("%s declares reasoning compatibility but thinking_mode is %q", m.ModelID, mode)
		}
	}
	if !foundM27 {
		t.Fatal("MiniMax-M2.7 is missing from the provider catalog")
	}
}
