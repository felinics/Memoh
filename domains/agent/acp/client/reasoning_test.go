package client

import (
	"context"
	"testing"

	acp "github.com/coder/acp-go-sdk"
)

func TestReasoningStatePrefersThoughtLevelCategoryOverProfileFallback(t *testing.T) {
	t.Parallel()

	fallback := testSelectOption("effort", nil, "high", "low", "high")
	category := acp.SessionConfigOptionCategoryThoughtLevel
	standard := testSelectOption("thinking", &category, "medium", "low", "medium", "high")

	configID, state := reasoningStateFromACP([]acp.SessionConfigOption{fallback, standard}, "effort")
	if configID != "thinking" || !state.Supported || state.CurrentEffort != "medium" {
		t.Fatalf("reasoning state = %q %#v, want categorized option", configID, state)
	}
	if len(state.Available) != 3 || state.Available[2].ID != "high" {
		t.Fatalf("available efforts = %#v", state.Available)
	}
}

func TestReasoningStateUsesProfileFallbackWithoutGuessing(t *testing.T) {
	t.Parallel()

	effort := testSelectOption("effort", nil, "high", "low", "high")
	if configID, state := reasoningStateFromACP([]acp.SessionConfigOption{effort}, ""); configID != "" || state.Supported {
		t.Fatalf("state without mapping = %q %#v, want unsupported", configID, state)
	}
	configID, state := reasoningStateFromACP([]acp.SessionConfigOption{effort}, "effort")
	if configID != "effort" || !state.Supported || state.CurrentEffort != "high" {
		t.Fatalf("mapped state = %q %#v", configID, state)
	}
}

func TestReasoningStatePreservesGroupedOptionDisplayMetadata(t *testing.T) {
	t.Parallel()

	description := "Agent supplied description"
	groups := acp.SessionConfigSelectOptionsGrouped{{
		Group: "quality",
		Name:  "Quality",
		Options: []acp.SessionConfigSelectOption{{
			Value:       "custom-max",
			Name:        "Maximum (agent)",
			Description: &description,
		}},
	}}
	category := acp.SessionConfigOptionCategoryThoughtLevel
	_, state := reasoningStateFromACP([]acp.SessionConfigOption{{Select: &acp.SessionConfigOptionSelect{
		Id:           "thinking",
		Name:         "Thinking",
		Type:         "select",
		Category:     &category,
		CurrentValue: "custom-max",
		Options:      acp.SessionConfigSelectOptions{Grouped: &groups},
	}}}, "")
	if len(state.Available) != 1 || state.Available[0].ID != "custom-max" || state.Available[0].Name != "Maximum (agent)" || state.Available[0].Description != description {
		t.Fatalf("grouped reasoning options = %#v", state.Available)
	}
}

func TestSessionConfigOptionUpdateReplacesReasoningState(t *testing.T) {
	t.Parallel()

	category := acp.SessionConfigOptionCategoryThoughtLevel
	sess := &Session{
		sessionID: acp.SessionId("session-1"),
	}
	sess.replaceConfigOptions(sess.sessionID, []acp.SessionConfigOption{
		testSelectOption("thinking", &category, "low", "low", "high"),
	})
	callbacks := &clientCallbacks{}
	callbacks.setConfigOptionsHandler(sess.replaceConfigOptions)

	err := callbacks.SessionUpdate(context.Background(), acp.SessionNotification{
		SessionId: sess.sessionID,
		Update: acp.SessionUpdate{ConfigOptionUpdate: &acp.SessionConfigOptionUpdate{
			SessionUpdate: "config_option_update",
			ConfigOptions: []acp.SessionConfigOption{
				testSelectOption("thinking", &category, "max", "medium", "max"),
			},
		}},
	})
	if err != nil {
		t.Fatalf("SessionUpdate() error = %v", err)
	}
	state := sess.ReasoningState()
	if state.CurrentEffort != "max" || len(state.Available) != 2 || state.Available[0].ID != "medium" {
		t.Fatalf("ReasoningState() = %#v, want replacement config state", state)
	}
}

func testSelectOption(id string, category *acp.SessionConfigOptionCategory, current string, values ...string) acp.SessionConfigOption {
	options := make(acp.SessionConfigSelectOptionsUngrouped, 0, len(values))
	for _, value := range values {
		options = append(options, acp.SessionConfigSelectOption{
			Value: acp.SessionConfigValueId(value),
			Name:  value,
		})
	}
	return acp.SessionConfigOption{Select: &acp.SessionConfigOptionSelect{
		Id:           acp.SessionConfigId(id),
		Name:         id,
		Type:         "select",
		Category:     category,
		CurrentValue: acp.SessionConfigValueId(current),
		Options: acp.SessionConfigSelectOptions{
			Ungrouped: &options,
		},
	}}
}
