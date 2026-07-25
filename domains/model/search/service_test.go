package search

import (
	"errors"
	"fmt"
	"testing"
)

func TestMapSearchProviderWriteError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		err          error
		wantSentinel error
		wantWrapped  bool
	}{
		{
			name:         "provider type conflict",
			err:          ErrProviderTypeConflict,
			wantSentinel: ErrProviderTypeConflict,
		},
		{
			name:         "wrapped provider type conflict",
			err:          fmt.Errorf("wrapped: %w", ErrProviderTypeConflict),
			wantSentinel: ErrProviderTypeConflict,
		},
		{
			name:         "provider name conflict",
			err:          ErrProviderNameTaken,
			wantSentinel: ErrProviderNameTaken,
		},
		{
			name:        "infrastructure error",
			err:         errors.New("database unavailable"),
			wantWrapped: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := mapSearchProviderWriteError(tt.err, "write search provider")
			if tt.wantWrapped {
				if !errors.Is(got, tt.err) {
					t.Fatalf("error %v does not wrap %v", got, tt.err)
				}
				return
			}
			if !errors.Is(got, tt.wantSentinel) {
				t.Fatalf("error %v does not wrap sentinel %v", got, tt.wantSentinel)
			}
		})
	}
}

func TestServiceCRUDAndDefaults(t *testing.T) {
	t.Parallel()
	svc := NewMemoryService(nil)

	created, err := svc.Create(t.Context(), CreateRequest{
		Name: "Brave", Provider: ProviderBrave, Config: map[string]any{"api_key": "secret"},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.Enable {
		t.Fatal("Create() enable = true, want false")
	}
	if created.Config["api_key"] != "secret" {
		t.Fatalf("Create() config = %#v", created.Config)
	}

	got, err := svc.Get(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.ID != created.ID || got.Provider != string(ProviderBrave) {
		t.Fatalf("Get() = %+v", got)
	}

	raw, err := svc.GetRawByID(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("GetRawByID() error = %v", err)
	}
	if string(raw.Config) == "" || raw.Provider != string(ProviderBrave) {
		t.Fatalf("GetRawByID() = %+v", raw)
	}

	enable := true
	updated, err := svc.Update(t.Context(), created.ID, UpdateRequest{Enable: &enable})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if !updated.Enable {
		t.Fatal("Update() enable = false, want true")
	}

	listed, err := svc.List(t.Context(), string(ProviderBrave))
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("List() len = %d, want 1", len(listed))
	}

	if err := svc.Delete(t.Context(), created.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
}

func TestServiceEnsureDefaults(t *testing.T) {
	t.Parallel()
	svc := NewMemoryService(nil)
	if err := svc.EnsureDefaults(t.Context()); err != nil {
		t.Fatalf("EnsureDefaults() error = %v", err)
	}
	all, err := svc.List(t.Context(), "")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(all) != len(defaultProviders) {
		t.Fatalf("EnsureDefaults() len = %d, want %d", len(all), len(defaultProviders))
	}
	if err := svc.EnsureDefaults(t.Context()); err != nil {
		t.Fatalf("EnsureDefaults(second) error = %v", err)
	}
	again, err := svc.List(t.Context(), "")
	if err != nil {
		t.Fatalf("List(second) error = %v", err)
	}
	if len(again) != len(all) {
		t.Fatalf("EnsureDefaults created duplicates: before=%d after=%d", len(all), len(again))
	}
}

func TestServiceListMetaIncludesSecretFields(t *testing.T) {
	t.Parallel()
	svc := NewMemoryService(nil)
	meta := svc.ListMeta(t.Context())
	if len(meta) != len(defaultProviders) {
		t.Fatalf("ListMeta() len = %d, want %d", len(meta), len(defaultProviders))
	}
	var brave ProviderMeta
	for _, item := range meta {
		if item.Provider == string(ProviderBrave) {
			brave = item
		}
	}
	field, ok := brave.ConfigSchema.Fields["api_key"]
	if !ok || field.Type != "secret" {
		t.Fatalf("brave api_key schema = %#v", brave.ConfigSchema.Fields["api_key"])
	}
}
