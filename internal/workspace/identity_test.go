package workspace

import (
	"testing"
	"time"

	ctr "github.com/felinics/memoh/internal/container"
)

func TestBotIDFromContainerInfoPrefersCurrentLabel(t *testing.T) {
	t.Parallel()

	info := ctr.ContainerInfo{
		ID: "workspace-ignored",
		Labels: map[string]string{
			BotLabelKey: "bot-from-label",
		},
		UpdatedAt: time.Now(),
	}

	botID, ok := BotIDFromContainerInfo(info)
	if !ok {
		t.Fatal("expected bot ID to resolve")
	}
	if botID != "bot-from-label" {
		t.Fatalf("expected labeled bot ID, got %q", botID)
	}
}

func TestBotIDFromContainerInfoFallsBackToKnownPrefixes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		containerID string
		want        string
	}{
		{name: "workspace", containerID: "workspace-bot-123", want: "bot-123"},
		{name: "legacy", containerID: "mcp-bot-456", want: "bot-456"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := ctr.ContainerInfo{ID: tc.containerID}
			got, ok := BotIDFromContainerInfo(info)
			if !ok {
				t.Fatal("expected bot ID to resolve")
			}
			if got != tc.want {
				t.Fatalf("expected %q, got %q", tc.want, got)
			}
		})
	}
}
