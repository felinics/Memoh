package client

import (
	"context"
	"testing"

	acp "github.com/coder/acp-go-sdk"
)

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
	callbacks.setSession(sess)

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
