package channel

import "time"

type ConnectionStatus struct {
	ConfigID    string
	BotID       string
	ChannelType ChannelType
	Running     bool
	LastError   string
	UpdatedAt   time.Time
}

type TunnelMode uint8

const (
	TunnelModeUnspecified TunnelMode = iota
	TunnelModeDisabled
	TunnelModeConfigured
	TunnelModeManaged
)

type TunnelState uint8

const (
	TunnelStateUnspecified TunnelState = iota
	TunnelStateDisabled
	TunnelStateStarting
	TunnelStateReady
	TunnelStateError
)

type TunnelStatus struct {
	Enabled       bool
	Mode          TunnelMode
	Status        TunnelState
	PublicBaseURL string
	Error         string
}
