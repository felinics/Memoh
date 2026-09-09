package tailscale

import (
	"context"
	"strings"

	netctl "github.com/felinics/memoh/internal/network"
	"github.com/felinics/memoh/internal/network/overlay/internal/sidecar"
)

func (d *nativeDriver) enrichStatus(ctx context.Context, req netctl.AttachmentRequest, status *netctl.OverlayStatus) {
	localStatus, err := readLocalStatus(ctx, d.socketPath(req))
	if err != nil {
		status.Details = sidecar.MergeStatusDetails(status.Details, map[string]any{
			"sidecar_status": "status_api_unavailable",
		})
		status.State = "starting"
		status.Message = "Tailscale client is running but status reporting is not ready yet."
		return
	}
	if localStatus.BackendState == "NeedsLogin" || localStatus.AuthURL != "" {
		status.State = "needs_login"
		status.Message = "Open the authentication URL to complete login."
		if localStatus.AuthURL != "" {
			status.Details = sidecar.MergeStatusDetails(status.Details, map[string]any{
				"auth_url":      localStatus.AuthURL,
				"backend_state": localStatus.BackendState,
			})
		}
		return
	}
	if localStatus.Self != nil {
		if len(localStatus.Self.TailscaleIPs) > 0 {
			status.NetworkIP = strings.TrimSpace(localStatus.Self.TailscaleIPs[0])
		}
		status.Details = sidecar.MergeStatusDetails(status.Details, map[string]any{
			"dns_name":      localStatus.Self.DNSName,
			"hostname":      localStatus.Self.HostName,
			"online":        localStatus.Self.Online,
			"mesh_ips":      localStatus.Self.TailscaleIPs,
			"tailscale_ips": localStatus.Self.TailscaleIPs,
		})
	}
	if localStatus.BackendState != "" {
		status.Details = sidecar.MergeStatusDetails(status.Details, map[string]any{
			"backend_state": localStatus.BackendState,
		})
		if localStatus.BackendState != "Running" && status.State == "ready" {
			status.State = strings.ToLower(localStatus.BackendState)
		}
	}
	if len(localStatus.Health) > 0 {
		status.Details = sidecar.MergeStatusDetails(status.Details, map[string]any{
			"health": localStatus.Health,
		})
		if status.State == "ready" {
			status.State = "degraded"
			status.Message = localStatus.Health[0]
		}
	}
	if status.NetworkIP == "" && status.State == "ready" {
		status.State = "starting"
		status.Message = "Tailscale client is starting and has not received a network IP yet."
	}
}
