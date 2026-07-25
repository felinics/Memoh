// Package local implements the CLI and Web channel adapters for local development.
package local

import "github.com/memohai/memoh/domains/channel/gateway"

const (
	// CLIType is the registered ChannelType for the CLI adapter.
	CLIType gateway.ChannelType = "cli"
	// WebType is the registered ChannelType for the Web adapter.
	WebType gateway.ChannelType = "web"
)
