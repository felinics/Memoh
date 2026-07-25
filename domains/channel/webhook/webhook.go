package webhook

import "context"

const (
	DefaultListenAddr = "127.0.0.1:18734"

	StatusDisabled = "disabled"
	StatusStarting = "starting"
	StatusReady    = "ready"
	StatusError    = "error"
	StatusStopped  = "stopped"
)

// Status is the stable webhook tunnel readiness DTO for API consumers.
type Status struct {
	Enabled       bool   `json:"enabled"`
	Mode          string `json:"mode"`
	Status        string `json:"status"`
	PublicBaseURL string `json:"public_base_url,omitempty"`
	Error         string `json:"error,omitempty"`
}

// Manager owns webhook tunnel lifecycle and public-base resolution.
type Manager interface {
	Start(context.Context) error
	Stop(context.Context) error
	Status() Status
	PublicBaseURL() string
}

// Service is the read-only status surface used by HTTP handlers.
type Service interface {
	Status() Status
}
