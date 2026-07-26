package bot

import (
	"context"
	"encoding/json"
	"testing"

	botpersistence "github.com/memohai/memoh/domains/api/bot/persistence"

	acpprofile "github.com/memohai/memoh/domains/agent/acp/profile"
)

func TestUpdateMergesACPSensitiveMetadataBeforePersisting(t *testing.T) {
	existingMetadata := mustJSON(map[string]any{
		acpprofile.MetadataKeyACP: map[string]any{
			"agents": map[string]any{
				acpprofile.AgentCodexID: map[string]any{
					"enabled": true,
					"managed": map[string]any{
						"api_key":  "sk-existing-secret",
						"base_url": "https://old.example.test/v1",
					},
				},
			},
		},
	})
	var persisted []byte
	store := &botStoreFake{
		getByID: func(context.Context, string) (botpersistence.Record, error) {
			row := baseRecord()
			row.Metadata = existingMetadata
			return row, nil
		},
		update: func(_ context.Context, input botpersistence.UpdateInput) (botpersistence.Record, error) {
			persisted = append([]byte(nil), input.Metadata...)
			row := baseRecord()
			row.Metadata = input.Metadata
			return row, nil
		},
	}
	svc := NewService(nil, store, nil, nil, nil)
	resp, err := svc.Update(t.Context(), testBotID, UpdateBotRequest{
		Metadata: map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentCodexID: map[string]any{
						"enabled": true,
						"managed": map[string]any{
							"api_key":  "sk-...cret",
							"base_url": "https://new.example.test/v1",
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	var saved map[string]any
	if err := json.Unmarshal(persisted, &saved); err != nil {
		t.Fatalf("decode persisted metadata: %v", err)
	}
	for name, metadata := range map[string]map[string]any{"saved": saved, "response": resp.Metadata} {
		setup := acpprofile.ParseAgentSetup(metadata, acpprofile.AgentCodexID)
		if setup.Managed["api_key"] != "sk-existing-secret" {
			t.Fatalf("%s api_key = %q", name, setup.Managed["api_key"])
		}
		if setup.Managed["base_url"] != "https://new.example.test/v1" {
			t.Fatalf("%s base_url = %q", name, setup.Managed["base_url"])
		}
	}
}

func mustJSON(value map[string]any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}
