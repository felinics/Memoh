package bots

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
)

func TestUpdateMergesACPSensitiveMetadataBeforePersisting(t *testing.T) {
	botUUID := mustParseUUID("00000000-0000-0000-0000-000000000002")
	ownerUUID := mustParseUUID("00000000-0000-0000-0000-000000000001")
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

	db := &fakeDBTX{
		queryRowFunc: func(_ context.Context, query string, args ...any) pgx.Row {
			switch {
			case strings.Contains(query, "SELECT id, owner_user_id") && strings.Contains(query, "FROM bots"):
				return makeGetBotRowWithMetadata(botUUID, ownerUUID, existingMetadata)
			case strings.Contains(query, "UPDATE bots") && strings.Contains(query, "metadata = $7"):
				payload, ok := args[6].([]byte)
				if !ok {
					t.Fatalf("metadata arg type = %T, want []byte", args[5])
				}
				persisted = append([]byte(nil), payload...)
				return makeUpdateBotProfileRowWithMetadata(botUUID, ownerUUID, payload)
			default:
				t.Fatalf("unexpected query: %s", query)
				return &fakeRow{scanFunc: func(_ ...any) error { return pgx.ErrNoRows }}
			}
		},
	}

	svc := NewService(nil, postgresstore.NewQueries(sqlc.New(db)))
	resp, err := svc.Update(context.Background(), botUUID.String(), UpdateBotRequest{
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
	setup := acpprofile.ParseAgentSetup(saved, acpprofile.AgentCodexID)
	if got := setup.Managed["api_key"]; got != "sk-existing-secret" {
		t.Fatalf("persisted api_key = %q, want existing secret preserved", got)
	}
	if got := setup.Managed["base_url"]; got != "https://new.example.test/v1" {
		t.Fatalf("persisted base_url = %q, want new non-sensitive value", got)
	}

	respSetup := acpprofile.ParseAgentSetup(resp.Metadata, acpprofile.AgentCodexID)
	if got := respSetup.Managed["api_key"]; got != "sk-existing-secret" {
		t.Fatalf("response api_key = %q, want service to return persisted metadata", got)
	}
}

func mustJSON(value map[string]any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

func makeGetBotRowWithMetadata(botID, ownerUserID pgtype.UUID, metadata []byte) *fakeRow {
	return &fakeRow{
		scanFunc: func(dest ...any) error {
			if len(dest) != 20 {
				return pgx.ErrNoRows
			}
			*dest[0].(*pgtype.UUID) = botID
			*dest[1].(*pgtype.UUID) = ownerUserID
			*dest[2].(*string) = "test-bot"
			*dest[3].(*pgtype.Text) = pgtype.Text{}
			*dest[4].(*pgtype.Text) = pgtype.Text{}
			*dest[5].(*pgtype.Text) = pgtype.Text{}
			*dest[6].(*bool) = true
			*dest[7].(*string) = BotStatusReady
			*dest[8].(*string) = "en"
			*dest[9].(*string) = "medium"
			*dest[10].(*pgtype.UUID) = pgtype.UUID{}
			*dest[11].(*pgtype.UUID) = pgtype.UUID{}
			*dest[12].(*pgtype.UUID) = pgtype.UUID{}
			*dest[13].(*bool) = false
			*dest[14].(*int32) = 100000
			*dest[15].(*pgtype.Int4) = pgtype.Int4{}
			*dest[16].(*pgtype.UUID) = pgtype.UUID{}
			*dest[17].(*[]byte) = append([]byte(nil), metadata...)
			*dest[18].(*pgtype.Timestamptz) = pgtype.Timestamptz{}
			*dest[19].(*pgtype.Timestamptz) = pgtype.Timestamptz{}
			return nil
		},
	}
}

func makeUpdateBotProfileRowWithMetadata(botID, ownerUserID pgtype.UUID, metadata []byte) *fakeRow {
	return &fakeRow{
		scanFunc: func(dest ...any) error {
			if len(dest) != 16 {
				return pgx.ErrNoRows
			}
			*dest[0].(*pgtype.UUID) = botID
			*dest[1].(*pgtype.UUID) = ownerUserID
			*dest[2].(*string) = "test-bot"
			*dest[3].(*pgtype.Text) = pgtype.Text{}
			*dest[4].(*pgtype.Text) = pgtype.Text{}
			*dest[5].(*pgtype.Text) = pgtype.Text{}
			*dest[6].(*bool) = true
			*dest[7].(*string) = BotStatusCreating
			*dest[8].(*string) = "en"
			*dest[9].(*string) = "medium"
			*dest[10].(*pgtype.UUID) = pgtype.UUID{}
			*dest[11].(*pgtype.UUID) = pgtype.UUID{}
			*dest[12].(*pgtype.UUID) = pgtype.UUID{}
			*dest[13].(*[]byte) = append([]byte(nil), metadata...)
			*dest[14].(*pgtype.Timestamptz) = pgtype.Timestamptz{}
			*dest[15].(*pgtype.Timestamptz) = pgtype.Timestamptz{}
			return nil
		},
	}
}
