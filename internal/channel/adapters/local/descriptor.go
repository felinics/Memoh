// Package local implements the CLI and Web channel adapters for local development.
package local

import "github.com/felinics/memoh/internal/channel"

const (
	// CLIType is the registered ChannelType for the CLI adapter.
	CLIType channel.ChannelType = "cli"
	// WebType is the registered ChannelType for the Web adapter.
	WebType channel.ChannelType = "web"
)
