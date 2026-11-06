package tailscale

import netctl "github.com/felinics/memoh/internal/network"

func schema() netctl.ConfigSchema {
	return netctl.ConfigSchema{
		Version: 1,
		Title:   "Tailscale",
		Fields: []netctl.ConfigField{
			{Key: "hostname", Type: netctl.FieldTypeString, Title: "Hostname", Order: 1},
			{Key: "auth_key", Type: netctl.FieldTypeSecret, Title: "Auth Key", Collapsed: true, Description: "Leave empty to use interactive browser login.", Order: 10},
			{Key: "control_url", Type: netctl.FieldTypeString, Title: "Control URL", Collapsed: true, Description: "Leave empty for official Tailscale. Set to your Headscale URL for self-hosted.", Order: 11},
			{Key: "accept_routes", Type: netctl.FieldTypeBool, Title: "Accept Routes", Collapsed: true, Default: false, Order: 12},
			{Key: "advertise_routes", Type: netctl.FieldTypeString, Title: "Advertise Routes", Collapsed: true, Description: "Comma-separated CIDRs to offer as subnet routes.", Order: 13},
			{Key: "userspace", Type: netctl.FieldTypeBool, Title: "Userspace Networking", Collapsed: true, Default: false, Order: 14},
			{Key: "socks5_enabled", Type: netctl.FieldTypeBool, Title: "Enable SOCKS5 Proxy", Collapsed: true, Default: false, Order: 15},
			{Key: "socks5_port", Type: netctl.FieldTypeNumber, Title: "SOCKS5 Port", Collapsed: true, Default: float64(1055), Order: 16},
			{Key: "http_proxy_enabled", Type: netctl.FieldTypeBool, Title: "Enable HTTP Proxy", Collapsed: true, Default: false, Order: 17},
			{Key: "http_proxy_port", Type: netctl.FieldTypeNumber, Title: "HTTP Proxy Port", Collapsed: true, Default: float64(1056), Order: 18},
		},
	}
}
