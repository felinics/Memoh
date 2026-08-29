package channel_test

import (
	"testing"

	"github.com/felinics/memoh/internal/channel"
)

func TestResolveTargetFromUserConfig(t *testing.T) {
	t.Parallel()
	reg := newTestConfigRegistry()

	target, err := reg.ResolveTargetFromUserConfig(testChannelType, map[string]any{
		"target": "alice",
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if target != "resolved:alice" {
		t.Fatalf("unexpected target: %s", target)
	}
}

func TestResolveTargetFromUserConfigUnsupported(t *testing.T) {
	t.Parallel()
	reg := channel.NewRegistry()

	_, err := reg.ResolveTargetFromUserConfig("unknown", map[string]any{})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}
