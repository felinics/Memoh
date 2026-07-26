// Package persistence defines the heartbeat automation persistence ports and
// the records they exchange.
//
// It is separate from the heartbeat package so the Postgres adapter can depend
// on ports without the service depending on the adapter — that cycle is what
// forces a domain to grow a separate assembly seam.
package persistence

import (
	"context"
	"encoding/json"
	"time"
)

// BotReader loads API-owned bot fields needed by heartbeat automation.
type BotReader interface {
	ListEnabledBots(context.Context) ([]BotRecord, error)
	GetBot(context.Context, string) (BotRecord, error)
}

// Store is the persistence surface consumed by heartbeat orchestration.
type Store interface {
	ListEnabledBots(context.Context) ([]BotRecord, error)
	GetBot(context.Context, string) (BotRecord, error)
	CreateLog(context.Context, CreateLogCommand) (string, error)
	CompleteLog(context.Context, CompleteLogCommand) error
	CountLogsByBot(context.Context, string) (int64, error)
	ListLogsByBot(context.Context, LogPage) ([]LogRecord, error)
	DeleteLogsByBot(context.Context, string) error
}

type BotRecord struct {
	ID                string
	OwnerUserID       string
	Status            string
	HeartbeatEnabled  bool
	HeartbeatInterval int
}

type CreateLogCommand struct {
	BotID     string
	SessionID string
}

type CompleteLogCommand struct {
	ID           string
	Status       string
	ResultText   string
	ErrorMessage string
	Usage        json.RawMessage
	ModelID      string
}

type LogPage struct {
	BotID  string
	Limit  int32
	Offset int32
}

type LogRecord struct {
	ID           string
	BotID        string
	SessionID    string
	Status       string
	ResultText   string
	ErrorMessage string
	Usage        json.RawMessage
	StartedAt    time.Time
	CompletedAt  time.Time
}
