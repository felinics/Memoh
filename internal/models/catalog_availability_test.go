package models

import (
	"encoding/json"
	"log/slog"
	"slices"
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

func TestProviderModelListUsesPersistedCatalogOrder(t *testing.T) {
	configJSON := func(order int) []byte {
		data, err := json.Marshal(ModelConfig{CatalogOrder: &order})
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	service := &Service{logger: slog.Default()}
	// Database order follows creation time; catalog order can change on refresh.
	rows := []sqlc.Model{
		{ModelID: "legacy-first"},
		{ModelID: "a", Config: configJSON(1)},
		{ModelID: "legacy-second"},
		{ModelID: "z", Config: configJSON(0)},
	}
	var ids []string
	for _, model := range service.convertToProviderModelList(rows) {
		ids = append(ids, model.ModelID)
	}
	if !slices.Equal(ids, []string{"z", "a", "legacy-first", "legacy-second"}) {
		t.Fatalf("provider catalog order = %v", ids)
	}
	if got := service.convertToGetResponseList(rows); got[0].ModelID != "legacy-first" || got[1].ModelID != "a" {
		t.Fatal("global model ordering changed")
	}
}
