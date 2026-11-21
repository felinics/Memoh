package netbird

import netctl "github.com/felinics/memoh/internal/network"

func schema() netctl.ConfigSchema {
	return netctl.ConfigSchema{
		Version: 1,
		Title:   "NetBird",
		Fields: []netctl.ConfigField{
			{Key: "setup_key", Type: netctl.FieldTypeSecret, Title: "Setup Key", Description: "Leave empty to use interactive SSO login.", Order: 1},
			{Key: "management_url", Type: netctl.FieldTypeString, Title: "Management URL", Description: "Leave empty for official NetBird Cloud.", Example: "https://netbird.example.com", Order: 2},
			{Key: "hostname", Type: netctl.FieldTypeString, Title: "Hostname", Example: "my-bot", Order: 3},
			{Key: "admin_url", Type: netctl.FieldTypeString, Title: "Admin URL", Collapsed: true, Description: "Dashboard URL of a self-hosted NetBird. Leave empty for NetBird Cloud.", Example: "https://netbird.example.com", Order: 10},
			{Key: "disable_dns", Type: netctl.FieldTypeBool, Title: "Disable DNS", Collapsed: true, Default: false, Order: 11},
			{Key: "userspace", Type: netctl.FieldTypeBool, Title: "Userspace Networking", Collapsed: true, Default: false, Order: 12},
		},
	}
}
