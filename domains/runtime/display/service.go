package display

import (
	"context"
	"errors"
	"time"
)

const (
	TransportWebRTC  = "webrtc"
	EncoderGStreamer = "gstreamer"
)

var (
	ErrManagerUnavailable = errors.New("manager not configured")
	ErrDisplayDisabled    = errors.New("display disabled")
	ErrDisplayUnavailable = errors.New("display server not reachable")
	ErrEncoderUnavailable = errors.New("gstreamer unavailable")
	ErrCodecUnsupported   = errors.New("no compatible video codec offered")
)

// Workspace is the consumer port display needs from the workspace manager.
type Workspace interface {
	BotDisplayEnabled(ctx context.Context, botID string) bool
	DisplaySocketPath(botID string) string
}

// Status reports workspace display availability for a bot.
type Status struct {
	Enabled           bool
	Available         bool
	Running           bool
	Transport         string
	Encoder           string
	EncoderAvailable  bool
	UnavailableReason string
}

// OfferRequest is a WebRTC SDP offer for a display session.
type OfferRequest struct {
	Type      string   `json:"type"`
	SDP       string   `json:"sdp"`
	SessionID string   `json:"session_id,omitempty"`
	NATIPs    []string `json:"-"`
}

// OfferResponse is the WebRTC SDP answer for a display session.
type OfferResponse struct {
	Type      string `json:"type"`
	SDP       string `json:"sdp"`
	SessionID string `json:"session_id"`
}

// SessionInfo describes an active display WebRTC peer session.
type SessionInfo struct {
	ID        string    `json:"id"`
	Codec     string    `json:"codec"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
}

// ControlInput is a pointer or key event delivered over RFB.
type ControlInput struct {
	Type       string
	X          int
	Y          int
	ButtonMask uint8
	Keysym     uint32
	Down       bool
}

// Service is the workspace display surface consumed by HTTP handlers and tools.
type Service interface {
	Status(ctx context.Context, botID string) Status
	Answer(ctx context.Context, botID string, req OfferRequest) (OfferResponse, error)
	ListSessions(botID string) []SessionInfo
	CloseSession(botID, sessionID string) bool
	Screenshot(ctx context.Context, botID string) ([]byte, string, error)
	ControlInput(ctx context.Context, botID string, event ControlInput) error
	ControlInputs(ctx context.Context, botID string, events []ControlInput) error
}
