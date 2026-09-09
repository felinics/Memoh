package channel_test

import (
	"context"
	"errors"
	"testing"

	"github.com/felinics/memoh/internal/channel"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/team"
)

const testChannelType = channel.ChannelType("test-config")

// testConfigAdapter implements Adapter, ConfigNormalizer, TargetResolver, BindingMatcher for tests.
type testConfigAdapter struct{}

func (*testConfigAdapter) Type() channel.ChannelType { return testChannelType }
func (*testConfigAdapter) Descriptor() channel.Descriptor {
	return channel.Descriptor{
		Type:        testChannelType,
		DisplayName: "Test",
		Capabilities: channel.ChannelCapabilities{
			Text: true,
		},
		ConfigSchema: channel.ConfigSchema{
			Version: 1,
			Fields: map[string]channel.FieldSchema{
				"value": {Type: channel.FieldString, Required: true},
			},
		},
		UserConfigSchema: channel.ConfigSchema{
			Version: 1,
			Fields: map[string]channel.FieldSchema{
				"user": {Type: channel.FieldString, Required: true},
			},
		},
	}
}

func (*testConfigAdapter) NormalizeConfig(raw map[string]any) (map[string]any, error) {
	value := channel.ReadString(raw, "value")
	if value == "" {
		return nil, errors.New("value is required")
	}
	return map[string]any{"value": value}, nil
}

func (*testConfigAdapter) NormalizeUserConfig(raw map[string]any) (map[string]any, error) {
	value := channel.ReadString(raw, "user")
	if value == "" {
		return nil, errors.New("user is required")
	}
	return map[string]any{"user": value}, nil
}

func (*testConfigAdapter) NormalizeTarget(raw string) string { return raw }

func (*testConfigAdapter) ResolveTarget(raw map[string]any) (string, error) {
	value := channel.ReadString(raw, "target")
	if value == "" {
		return "", errors.New("target is required")
	}
	return "resolved:" + value, nil
}

func (*testConfigAdapter) MatchBinding(raw map[string]any, criteria channel.BindingCriteria) bool {
	value := channel.ReadString(raw, "user")
	return value != "" && value == criteria.SubjectID
}

func (*testConfigAdapter) BuildUserConfig(_ channel.Identity) map[string]any {
	return map[string]any{}
}

func newTestConfigRegistry() *channel.Registry {
	reg := channel.NewRegistry()
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
const configlessTestType = channel.ChannelType("test-configless")

type configlessTestAdapter struct{}

func (*configlessTestAdapter) Type() channel.ChannelType { return configlessTestType }
func (*configlessTestAdapter) Descriptor() channel.Descriptor {
	return channel.Descriptor{Type: configlessTestType, DisplayName: "Configless", Configless: true}
}

// stubQueries is a non-nil Queries whose methods all panic: the configless
// path must never touch the database.
type stubQueries struct{ dbstore.Queries }

// TestResolveEffectiveConfigConfiglessCarriesTeamID pins the synthetic
// config for configless channels (web/local) to the singleton team.
// turn.Service fails closed on an empty TeamID, so losing this field breaks
// every REST web message and web discuss turn.
func TestResolveEffectiveConfigConfiglessCarriesTeamID(t *testing.T) {
	t.Parallel()
	registry := channel.NewRegistry()
	if err := registry.Register(&configlessTestAdapter{}); err != nil {
		t.Fatalf("register adapter: %v", err)
	}
	store := channel.NewStore(stubQueries{}, registry)

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
