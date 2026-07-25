// Package backup defines Channel-owned observation backup application contracts.
package backup

import (
	"context"
	"encoding/json"
	"time"
)

type ExportReader interface {
	Export(context.Context, string) (Snapshot, error)
}

// ImportWriter consumes mappings produced by earlier owner phases. Import and
// Compensate each own a transaction; deterministic event IDs and owner-scoped
// upserts make both methods safe to retry with the same request or receipt.
type ImportWriter interface {
	Import(context.Context, ImportRequest) (ImportResult, error)
	Compensate(context.Context, ImportReceipt) error
}

type Snapshot struct {
	DiscussCursors      []DiscussCursor
	SessionEvents       []SessionEvent
	RouteActiveSessions []RouteActiveSession
}

type DiscussCursor struct {
	SessionID      string    `json:"session_id"`
	ScopeKey       string    `json:"scope_key"`
	RouteID        *string   `json:"route_id"`
	Source         string    `json:"source"`
	ConsumedCursor int64     `json:"consumed_cursor"`
	UpdatedAt      time.Time `json:"updated_at"`
	TeamID         string    `json:"team_id"`
}

type SessionEvent struct {
	ID                      string          `json:"id"`
	BotID                   string          `json:"bot_id"`
	SessionID               string          `json:"session_id"`
	EventKind               string          `json:"event_kind"`
	EventData               json.RawMessage `json:"event_data"`
	ExternalMessageID       *string         `json:"external_message_id"`
	SenderChannelIdentityID *string         `json:"sender_channel_identity_id"`
	ReceivedAtMS            int64           `json:"received_at_ms"`
	CreatedAt               time.Time       `json:"created_at"`
	TeamID                  string          `json:"team_id"`
}

type RouteActiveSession struct {
	RouteID   string
	SessionID string
}

type ImportRequest struct {
	BotID              string
	Replace            bool
	DiscussCursors     []DiscussCursor
	SessionEvents      []SessionEvent
	RouteSessions      []RouteActiveSession
	SessionIDs         map[string]string
	RouteIDs           map[string]string
	ChannelIdentityIDs map[string]string
}

type ImportResult struct {
	EventIDs map[string]string
	Receipt  ImportReceipt
}

type CursorKey struct {
	SessionID string
	ScopeKey  string
}

type RouteBinding struct {
	RouteID   string
	SessionID string
}

type ImportReceipt struct {
	BotID         string
	EventIDs      []string
	Cursors       []CursorKey
	RouteBindings []RouteBinding
	Replace       bool
}
