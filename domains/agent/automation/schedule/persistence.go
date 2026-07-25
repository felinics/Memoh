package schedule

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var ErrNotFound = errors.New("schedule not found")

// BotReader loads API-owned bot fields needed by schedule automation.
type BotReader interface {
	GetBot(context.Context, string) (BotRecord, error)
}

// Store is the persistence surface consumed by schedule orchestration.
type Store interface {
	ListEnabled(context.Context) ([]Record, error)
	Create(context.Context, CreateCommand) (Record, error)
	Get(context.Context, string) (Record, error)
	ListByBot(context.Context, string) ([]Record, error)
	Update(context.Context, UpdateCommand) (Record, error)
	Delete(context.Context, string) error
	IncrementCalls(context.Context, string) (Record, error)
	GetBot(context.Context, string) (BotRecord, error)
	CreateLog(context.Context, CreateLogCommand) (string, error)
	CompleteLog(context.Context, CompleteLogCommand) error
	CountLogsByBot(context.Context, string) (int64, error)
	ListLogsByBot(context.Context, LogPage) ([]LogRecord, error)
	CountLogsBySchedule(context.Context, string) (int64, error)
	ListLogsBySchedule(context.Context, LogPage) ([]LogRecord, error)
	DeleteLogsByBot(context.Context, string) error
}

type Record struct {
	ID           string
	Name         string
	Description  string
	Pattern      string
	MaxCalls     *int
	CurrentCalls int
	CreatedAt    time.Time
	UpdatedAt    time.Time
	Enabled      bool
	Command      string
	BotID        string
}

type CreateCommand struct {
	Name        string
	Description string
	Pattern     string
	MaxCalls    *int
	Enabled     bool
	Command     string
	BotID       string
}

type UpdateCommand struct {
	ID          string
	Name        string
	Description string
	Pattern     string
	MaxCalls    *int
	Enabled     bool
	Command     string
}

type BotRecord struct {
	OwnerUserID string
	Timezone    string
}

type CreateLogCommand struct {
	ScheduleID string
	BotID      string
	SessionID  string
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
	ID     string
	Limit  int32
	Offset int32
}

type LogRecord struct {
	ID           string
	ScheduleID   string
	BotID        string
	SessionID    string
	Status       string
	ResultText   string
	ErrorMessage string
	Usage        json.RawMessage
	StartedAt    time.Time
	CompletedAt  time.Time
}
