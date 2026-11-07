package network

import (
	"context"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

type containerQueries struct {
	dbstore.Queries
}

func (containerQueries) GetContainerByBotID(context.Context, pgtype.UUID) (sqlc.Container, error) {
	return sqlc.Container{ContainerID: "workspace-bot"}, nil
}

type recordingController struct {
	detached []string
}

func (*recordingController) EnsureAttached(_ context.Context, req AttachmentRequest) (AttachmentStatus, error) {
	return AttachmentStatus{Overlay: OverlayStatus{Provider: req.Overlay.Provider}}, nil
}

func (c *recordingController) Detach(_ context.Context, req AttachmentRequest) error {
	c.detached = append(c.detached, req.Overlay.Provider)
	return nil
}

func (*recordingController) Status(_ context.Context, req AttachmentRequest) (AttachmentStatus, error) {
	return AttachmentStatus{Overlay: OverlayStatus{Provider: req.Overlay.Provider}}, nil
}

func TestReconcileBotKeepsSidecarWithinSameProvider(t *testing.T) {
	const botID = "00000000-0000-0000-0000-000000000001"
	cases := []struct {
		name         string
		previous     BotOverlayConfig
		next         BotOverlayConfig
		wantDetached []string
	}{
		{
			name:     "config change",
			previous: BotOverlayConfig{Enabled: true, Provider: "tailscale", Config: map[string]any{"accept_routes": false}},
			next:     BotOverlayConfig{Enabled: true, Provider: "tailscale", Config: map[string]any{"accept_routes": true}},
		},
		{
			name:         "provider change",
			previous:     BotOverlayConfig{Enabled: true, Provider: "tailscale"},
			next:         BotOverlayConfig{Enabled: true, Provider: "netbird"},
			wantDetached: []string{"tailscale"},
		},
		{
			name:         "disabled",
			previous:     BotOverlayConfig{Enabled: true, Provider: "tailscale"},
			next:         BotOverlayConfig{Enabled: false, Provider: "tailscale"},
			wantDetached: []string{"tailscale"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := &recordingController{}
			svc := NewService(slog.New(slog.DiscardHandler), containerQueries{}, NewRegistry(), nil, "containerd", "", "", "")
			svc.SetController(ctrl)
			if err := svc.ReconcileBot(context.Background(), botID, tc.previous, tc.next); err != nil {
				t.Fatalf("ReconcileBot: %v", err)
			}
			if len(ctrl.detached) != len(tc.wantDetached) {
				t.Fatalf("detached %v, want %v", ctrl.detached, tc.wantDetached)
			}
		})
	}
}
