package tailscale

import (
	"context"
	"sort"
	"strings"

	netctl "github.com/felinics/memoh/internal/network"
	"github.com/felinics/memoh/internal/network/overlay/internal/sidecar"
)

func (d *nativeDriver) enrichStatus(ctx context.Context, req netctl.AttachmentRequest, status *netctl.OverlayStatus) {
	c := d.client(req)
	localStatus, err := c.status(ctx, false)
	if err != nil {
		status.Details = sidecar.MergeStatusDetails(status.Details, map[string]any{
			"sidecar_status": "status_api_unavailable",
		})
		status.State = netctl.OverlayStateStarting
		status.Message = "Tailscale client is running but status reporting is not ready yet."
		return
	}
	status.Details = sidecar.MergeStatusDetails(status.Details, map[string]any{
		"backend_state": localStatus.BackendState,
	})
	if localStatus.Self != nil {
		if len(localStatus.Self.TailscaleIPs) > 0 {
			status.NetworkIP = strings.TrimSpace(localStatus.Self.TailscaleIPs[0])
		}
		status.Details = sidecar.MergeStatusDetails(status.Details, map[string]any{
			"dns_name":      localStatus.Self.DNSName,
			"hostname":      localStatus.Self.HostName,
			"online":        localStatus.Self.Online,
			"tailscale_ips": localStatus.Self.TailscaleIPs,
		})
	}
	warnings, err := c.healthWarnings(ctx)
	if err != nil {
		// Older tailscaled or a busy bus: fall back to the plain messages for
		// display only, since they carry no severity to judge by.
		warnings = nil
		if len(localStatus.Health) > 0 {
			status.Details = sidecar.MergeStatusDetails(status.Details, map[string]any{"health": localStatus.Health})
		}
	}
	prefs, _ := c.prefs(ctx)
	state, message := overlayState(localStatus, hasIdentity(prefs), warnings)
	status.State = state
	status.Message = message
	if len(warnings) > 0 {
		status.Details = sidecar.MergeStatusDetails(status.Details, map[string]any{"health": warningTexts(warnings)})
	}
	if localStatus.AuthURL != "" {
		status.Details = sidecar.MergeStatusDetails(status.Details, map[string]any{"auth_url": localStatus.AuthURL})
	}
}

// overlayState derives the bot network state from tailscaled's backend state,
// whether the node has an identity, and health. Only warnings that affect connectivity, or are rated high,
// degrade a running node; the rest are informational.
func overlayState(st localStatus, identity bool, warnings map[string]healthWarning) (netctl.OverlayState, string) {
	switch st.BackendState {
	case "NoState", "":
		if identity {
			return netctl.OverlayStateStarting, "Connecting to the control server."
		}
		return netctl.OverlayStateNeedsLogin, "Open the authentication URL to complete login."
	case "NeedsLogin":
		return netctl.OverlayStateNeedsLogin, "Open the authentication URL to complete login."
	case "NeedsMachineAuth":
		return netctl.OverlayStateNeedsLogin, "Waiting for a tailnet admin to approve this device."
	case "Stopped":
		return netctl.OverlayStateStopped, "Tailscale is logged in but not running."
	case "Starting":
		return netctl.OverlayStateStarting, "Tailscale is connecting."
	case "Running":
	default:
		return netctl.OverlayStateUnknown, "Tailscale reported backend state " + st.BackendState + "."
	}
	if st.Self == nil || len(st.Self.TailscaleIPs) == 0 {
		return netctl.OverlayStateStarting, "Tailscale is running but has no address yet."
	}
	for _, w := range sortedWarnings(warnings) {
		if w.ImpactsConnectivity || w.Severity == "high" {
			return netctl.OverlayStateDegraded, warningText(w)
		}
	}
	return netctl.OverlayStateReady, ""
}

// sortedWarnings orders warnings by severity, then text, so that the
// message picked for a degraded state is stable between polls.
func sortedWarnings(warnings map[string]healthWarning) []healthWarning {
	rank := map[string]int{"high": 0, "medium": 1, "low": 2}
	out := make([]healthWarning, 0, len(warnings))
	for _, w := range warnings {
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool {
		ri, rj := rank[out[i].Severity], rank[out[j].Severity]
		if ri != rj {
			return ri < rj
		}
		return warningText(out[i]) < warningText(out[j])
	})
	return out
}

func warningTexts(warnings map[string]healthWarning) []string {
	sorted := sortedWarnings(warnings)
	texts := make([]string, 0, len(sorted))
	for _, w := range sorted {
		texts = append(texts, warningText(w))
	}
	return texts
}

func warningText(w healthWarning) string {
	if text := strings.TrimSpace(w.Text); text != "" {
		return text
	}
	return strings.TrimSpace(w.Title)
}
