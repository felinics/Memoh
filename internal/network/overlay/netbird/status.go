package netbird

import (
	"context"
	"strings"

	netctl "github.com/felinics/memoh/internal/network"
	"github.com/felinics/memoh/internal/network/overlay/internal/configutil"
	"github.com/felinics/memoh/internal/network/overlay/internal/sidecar"
	"github.com/felinics/memoh/internal/network/overlay/netbird/daemonpb"
)

func (d *nativeDriver) enrichStatus(ctx context.Context, req netctl.AttachmentRequest, status *netctl.OverlayStatus) {
	st, err := d.client(req).status(ctx)
	if err != nil {
		status.Details = sidecar.MergeStatusDetails(status.Details, map[string]any{
			"sidecar_status": "status_api_unavailable",
		})
		status.State = netctl.OverlayStateStarting
		status.Message = "NetBird client is running but status reporting is not ready yet."
		return
	}
	full := st.GetFullStatus()
	status.NetworkIP = peerAddress(full.GetLocalPeerState().GetIP())
	status.Details = sidecar.MergeStatusDetails(status.Details, map[string]any{
		"backend_state":        st.GetStatus(),
		"dns_name":             full.GetLocalPeerState().GetFqdn(),
		"management_connected": full.GetManagementState().GetConnected(),
		"signal_connected":     full.GetSignalState().GetConnected(),
		"peers":                len(full.GetPeers()),
		"daemon_version":       st.GetDaemonVersion(),
	})
	pending, hasPending := d.pendingSSO(req)
	if hasPending {
		status.Details = sidecar.MergeStatusDetails(status.Details, map[string]any{"auth_url": pending.URL})
	}
	status.State, status.Message = overlayState(st, hasPending, d.loginError(req))
}

// overlayState maps the daemon status to the bot network state. A connected
// peer is degraded when it lost the management or signal server, since it
// then misses peer and route updates.
func overlayState(st *daemonpb.StatusResponse, ssoPending bool, loginError string) (netctl.OverlayState, string) {
	full := st.GetFullStatus()
	switch st.GetStatus() {
	case daemonNeedsLogin, daemonSessionExpired:
		if loginError != "" {
			return netctl.OverlayStateUnauthorized, "The management server rejected the login: " + loginError
		}
		if ssoPending {
			return netctl.OverlayStateNeedsLogin, "Open the authentication URL to complete login."
		}
		return netctl.OverlayStateNeedsLogin, "NetBird needs to log in. Set a setup key or run Test Connection to start an SSO login."
	case daemonLoginFailed:
		return netctl.OverlayStateUnauthorized, "The management server rejected the login. Check the setup key."
	case daemonIdle:
		return netctl.OverlayStateStopped, "NetBird is logged in but not connected."
	case daemonConnecting:
		return netctl.OverlayStateStarting, "NetBird is connecting."
	case daemonConnected:
	default:
		return netctl.OverlayStateUnknown, "NetBird reported status " + st.GetStatus() + "."
	}
	if peerAddress(full.GetLocalPeerState().GetIP()) == "" {
		return netctl.OverlayStateStarting, "NetBird is connected but has no address yet."
	}
	if m := full.GetManagementState(); !m.GetConnected() {
		return netctl.OverlayStateDegraded, configutil.FirstNonEmpty(m.GetError(), "Lost connection to the management server.")
	}
	if s := full.GetSignalState(); !s.GetConnected() {
		return netctl.OverlayStateDegraded, configutil.FirstNonEmpty(s.GetError(), "Lost connection to the signal server.")
	}
	return netctl.OverlayStateReady, ""
}

// peerAddress strips the prefix length from the daemon's "100.x.y.z/16".
func peerAddress(cidr string) string {
	addr, _, _ := strings.Cut(cidr, "/")
	return addr
}
