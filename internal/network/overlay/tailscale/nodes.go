package tailscale

import (
	"context"
	"strings"

	netctl "github.com/felinics/memoh/internal/network"
	"github.com/felinics/memoh/internal/network/overlay/internal/configutil"
)

func (d *nativeDriver) listNodes(ctx context.Context, botID string) ([]netctl.NodeOption, error) {
	req := netctl.AttachmentRequest{BotID: botID}
	localStatus, err := readLocalStatusWithPeers(ctx, d.socketPath(req), true)
	if err != nil {
		return nil, err
	}
	selectedValue := configutil.String(d.config.Config, "exit_node")
	items := make([]netctl.NodeOption, 0, len(localStatus.Peer))
	for peerKey, peer := range localStatus.Peer {
		if peer == nil || !peer.ExitNodeOption {
			continue
		}
		addresses := append([]string(nil), peer.TailscaleIPs...)
		value := configutil.FirstNonEmpty(append(addresses, strings.TrimSuffix(strings.TrimSpace(peer.DNSName), "."), strings.TrimSpace(peer.HostName), strings.TrimSpace(peer.ID), strings.TrimSpace(peerKey))...)
		displayName := configutil.FirstNonEmpty(strings.TrimSpace(peer.HostName), strings.TrimSuffix(strings.TrimSpace(peer.DNSName), "."), value)
		items = append(items, netctl.NodeOption{
			ID:          configutil.FirstNonEmpty(strings.TrimSpace(peer.ID), strings.TrimSpace(peerKey), value),
			Value:       value,
			DisplayName: displayName,
			Description: strings.TrimSuffix(strings.TrimSpace(peer.DNSName), "."),
			Online:      peer.Online,
			Addresses:   addresses,
			CanExitNode: peer.ExitNodeOption,
			Selected:    selectedValue != "" && selectedValue == value,
			Details: map[string]any{
				"dns_name": strings.TrimSpace(peer.DNSName),
				"hostname": strings.TrimSpace(peer.HostName),
				"os":       strings.TrimSpace(peer.OS),
				"active":   peer.Active,
			},
		})
	}
	return items, nil
}
