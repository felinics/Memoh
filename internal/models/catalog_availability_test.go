package models

import (
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

func TestConvertToEnabledGetResponseListFiltersUnavailableCatalogModels(t *testing.T) {
	t.Parallel()

	available := true
	unavailable := false
	configJSON := func(value *bool) []byte {
		data, err := json.Marshal(ModelConfig{CatalogAvailable: value})
		if err != nil {
			t.Fatalf("marshal model config: %v", err)
		}
		return data
	}

	service := &Service{logger: slog.Default()}
	models := service.convertToEnabledGetResponseList([]sqlc.Model{
		{ModelID: "legacy-model", Config: configJSON(nil)},
		{ModelID: "available-model", Config: configJSON(&available)},
		{ModelID: "unavailable-model", Config: configJSON(&unavailable)},
	})

	if len(models) != 2 {
		t.Fatalf("models = %d, want 2", len(models))
	}
	if models[0].ModelID != "legacy-model" || models[1].ModelID != "available-model" {
		t.Fatalf("unexpected enabled models: %#v", models)
	}
}
