package gateway_test

import (
	"context"
	"errors"
	"testing"

	"github.com/memohai/memoh/domains/channel/gateway"
	team "github.com/memohai/memoh/domains/iam/team"
)

const testChannelType = gateway.ChannelType("test-config")

// testConfigAdapter implements Adapter, ConfigNormalizer, TargetResolver, BindingMatcher for tests.
type testConfigAdapter struct{}

func (*testConfigAdapter) Type() gateway.ChannelType { return testChannelType }
func (*testConfigAdapter) Descriptor() gateway.Descriptor {
	return gateway.Descriptor{
		Type:        testChannelType,
		DisplayName: "Test",
		Capabilities: gateway.ChannelCapabilities{
			Text: true,
		},
		ConfigSchema: gateway.ConfigSchema{
			Version: 1,
			Fields: map[string]gateway.FieldSchema{
				"value": {Type: gateway.FieldString, Required: true},
			},
		},
		UserConfigSchema: gateway.ConfigSchema{
			Version: 1,
			Fields: map[string]gateway.FieldSchema{
				"user": {Type: gateway.FieldString, Required: true},
			},
		},
	}
}

func (*testConfigAdapter) NormalizeConfig(raw map[string]any) (map[string]any, error) {
	value := gateway.ReadString(raw, "value")
	if value == "" {
		return nil, errors.New("value is required")
	}
	return map[string]any{"value": value}, nil
}

func (*testConfigAdapter) NormalizeUserConfig(raw map[string]any) (map[string]any, error) {
	value := gateway.ReadString(raw, "user")
	if value == "" {
		return nil, errors.New("user is required")
	}
	return map[string]any{"user": value}, nil
}

func (*testConfigAdapter) NormalizeTarget(raw string) string { return raw }

func (*testConfigAdapter) ResolveTarget(raw map[string]any) (string, error) {
	value := gateway.ReadString(raw, "target")
	if value == "" {
		return "", errors.New("target is required")
	}
	return "resolved:" + value, nil
}

func (*testConfigAdapter) MatchBinding(raw map[string]any, criteria gateway.BindingCriteria) bool {
	value := gateway.ReadString(raw, "user")
	return value != "" && value == criteria.SubjectID
}

func (*testConfigAdapter) BuildUserConfig(_ gateway.Identity) map[string]any {
	return map[string]any{}
}

func newTestConfigRegistry() *gateway.Registry {
	reg := gateway.NewRegistry()
	reg.MustRegister(&testConfigAdapter{})
	return reg
}

func TestParseChannelType(t *testing.T) {
	t.Parallel()
	reg := newTestConfigRegistry()

	got, err := reg.ParseChannelType(" test-config ")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got != testChannelType {
		t.Fatalf("unexpected channel type: %s", got)
	}
	if _, err := reg.ParseChannelType("unknown"); err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestNormalizeChannelConfig(t *testing.T) {
	t.Parallel()
	reg := newTestConfigRegistry()

	got, err := reg.NormalizeConfig(testChannelType, map[string]any{"value": "ok"})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got["value"] != "ok" {
		t.Fatalf("unexpected value: %#v", got["value"])
	}
}

func TestNormalizeChannelConfigRequiresValue(t *testing.T) {
	t.Parallel()
	reg := newTestConfigRegistry()

	_, err := reg.NormalizeConfig(testChannelType, map[string]any{})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestNormalizeChannelUserConfig(t *testing.T) {
	t.Parallel()
	reg := newTestConfigRegistry()

	got, err := reg.NormalizeUserConfig(testChannelType, map[string]any{"user": "alice"})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got["user"] != "alice" {
		t.Fatalf("unexpected user: %#v", got["user"])
	}
}

func TestNormalizeChannelUserConfigRequiresUser(t *testing.T) {
	t.Parallel()
	reg := newTestConfigRegistry()

	_, err := reg.NormalizeUserConfig(testChannelType, map[string]any{})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

// configlessTestType is a synthetic configless channel (like local/web).
const configlessTestType = gateway.ChannelType("test-configless")

type configlessTestAdapter struct{}

func (*configlessTestAdapter) Type() gateway.ChannelType { return configlessTestType }
func (*configlessTestAdapter) Descriptor() gateway.Descriptor {
	return gateway.Descriptor{Type: configlessTestType, DisplayName: "Configless", Configless: true}
}

// stubPersistence is non-nil and its promoted methods panic: the configless
// path must never touch the database.
type stubPersistence struct{ gateway.Persistence }

// TestResolveEffectiveConfigConfiglessCarriesTeamID pins the synthetic
// config for configless channels (web/local) to the singleton team.
// turn.Service fails closed on an empty TeamID, so losing this field breaks
// every REST web message and web discuss turn.
func TestResolveEffectiveConfigConfiglessCarriesTeamID(t *testing.T) {
	t.Parallel()
	registry := gateway.NewRegistry()
	if err := registry.Register(&configlessTestAdapter{}); err != nil {
		t.Fatalf("register adapter: %v", err)
	}
	store := gateway.NewStore(stubPersistence{}, registry)

	cfg, err := store.ResolveEffectiveConfig(context.Background(), "bot-1", configlessTestType)
	if err != nil {
		t.Fatalf("resolve effective config: %v", err)
	}
	if cfg.TeamID != team.DefaultTeamID {
		t.Fatalf("expected TeamID %q, got %q", team.DefaultTeamID, cfg.TeamID)
	}
	if cfg.BotID != "bot-1" {
		t.Fatalf("unexpected BotID %q", cfg.BotID)
	}
}
