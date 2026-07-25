package fetch

import (
	"errors"
	"testing"
)

func TestServiceCRUDAndManagedNative(t *testing.T) {
	t.Parallel()
	svc := NewMemoryService(nil)

	if _, err := svc.Create(t.Context(), CreateRequest{Name: "Native", Provider: ProviderNative}); !errors.Is(err, ErrManagedNativeProvider) {
		t.Fatalf("Create(native) error = %v, want ErrManagedNativeProvider", err)
	}

	created, err := svc.Create(t.Context(), CreateRequest{
		Name: "Jina", Provider: ProviderJina, Config: map[string]any{"api_key": "secret"},
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
	if got.ID != created.ID || got.Provider != string(ProviderJina) {
		t.Fatalf("Get() = %+v", got)
	}

	raw, err := svc.GetRawByID(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("GetRawByID() error = %v", err)
	}
	if string(raw.Config) == "" || raw.Provider != string(ProviderJina) {
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

	listed, err := svc.List(t.Context(), string(ProviderJina))
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
	byProvider := map[string]GetResponse{}
	for _, item := range all {
		byProvider[item.Provider] = item
	}
	for _, name := range []ProviderName{ProviderNative, ProviderJina, ProviderCloudflareMarkdown} {
		item, ok := byProvider[string(name)]
		if !ok {
			t.Fatalf("EnsureDefaults missing provider %s", name)
		}
		if name == ProviderNative && !item.Enable {
			t.Fatal("native provider should be enabled")
		}
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
	if len(meta) != 3 {
		t.Fatalf("ListMeta() len = %d, want 3", len(meta))
	}
	var jina ProviderMeta
	for _, item := range meta {
		if item.Provider == string(ProviderJina) {
			jina = item
		}
	}
	field, ok := jina.ConfigSchema.Fields["api_key"]
	if !ok || field.Type != "secret" {
		t.Fatalf("jina api_key schema = %#v", jina.ConfigSchema.Fields["api_key"])
	}
}
