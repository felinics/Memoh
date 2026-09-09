package netbird

import netctl "github.com/felinics/memoh/internal/network"

func schema() netctl.ConfigSchema {
	return netctl.ConfigSchema{
		Version: 1,
		Title:   "NetBird",
		Fields: []netctl.ConfigField{
			{Key: "hostname", Type: netctl.FieldTypeString, Title: "Hostname", Order: 1},
			{Key: "setup_key", Type: netctl.FieldTypeSecret, Title: "Setup Key", Collapsed: true, Description: "Leave empty to use interactive SSO login.", Order: 10},
			{Key: "management_url", Type: netctl.FieldTypeString, Title: "Management URL", Collapsed: true, Description: "Leave empty for official NetBird Cloud.", Order: 11},
			{Key: "admin_url", Type: netctl.FieldTypeString, Title: "Admin URL", Collapsed: true, Order: 12},
			{Key: "disable_dns", Type: netctl.FieldTypeBool, Title: "Disable DNS", Collapsed: true, Default: false, Order: 12},
			{Key: "userspace", Type: netctl.FieldTypeBool, Title: "Userspace Networking", Collapsed: true, Default: false, Order: 13},
			{Key: "state_dir", Type: netctl.FieldTypeString, Title: "State Directory", Collapsed: true, Default: "/var/lib/netbird", Order: 14},
			{Key: "extra_args", Type: netctl.FieldTypeTextarea, Title: "Extra Args", Collapsed: true, Order: 15},
		},
	}
}
