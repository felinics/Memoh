package channel

import (
	"testing"

	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/db"
)

func TestDecodeConfigMap(t *testing.T) {
	t.Parallel()

	cfg, err := DecodeConfigMap([]byte(`{"a":1}`))
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if cfg["a"] == nil {
		t.Fatalf("expected key in map")
	}
	cfg, err = DecodeConfigMap([]byte(`null`))
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if cfg == nil || len(cfg) != 0 {
		t.Fatalf("expected empty map")
	}
}

func TestReadString(t *testing.T) {
	t.Parallel()

	raw := map[string]any{
		"bot_token": 123,
	}
	got := ReadString(raw, "bot_token")
	if got != "123" {
		t.Fatalf("unexpected value: %s", got)
	}
}

func TestParseUUID(t *testing.T) {
	t.Parallel()

	id := uuid.NewString()
	if _, err := db.ParseUUID(id); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if _, err := db.ParseUUID("invalid"); err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestBindingCriteriaFromIdentity(t *testing.T) {
	t.Parallel()

	criteria := BindingCriteriaFromIdentity(Identity{
		SubjectID:  "u1",
		Attributes: map[string]string{"username": "alice"},
	})
	if criteria.SubjectID != "u1" {
		t.Fatalf("unexpected subject id: %s", criteria.SubjectID)
	}
	if criteria.Attribute("username") != "alice" {
		t.Fatalf("unexpected username: %s", criteria.Attribute("username"))
	}
}
