// Package persistence defines the bot settings persistence ports, the records
// they exchange, and the tool-approval vocabulary those records carry.
//
// Tool approval lives here rather than with the service because the Postgres
// adapter normalizes stored config on read, so the normalization rules must
// sit on the port side of the boundary.
package persistence

import (
	"context"
	"errors"
)

var ErrNotFound = errors.New("settings not found")

// Record is the persistence-neutral settings state consumed by Service.
type Record struct {
	Language               string
	CommandUILanguage      string
	ReasoningEnabled       bool
	ReasoningEffort        string
	HeartbeatEnabled       bool
	HeartbeatInterval      int32
	CompactionEnabled      bool
	CompactionThreshold    int32
	CompactionRatio        int32
	Timezone               string
	ChatModelID            string
	ChatRuntime            string
	ChatACPAgentID         string
	ChatACPProjectPath     string
	ChatACPProjectMode     string
	HeartbeatModelID       string
	CompactionModelID      string
	ImageModelID           string
	SearchProviderID       string
	FetchProviderID        string
	MemoryProviderID       string
	TTSModelID             string
	TranscriptionModelID   string
	VideoModelID           string
	PersistFullToolResults bool
	ShowToolCallsInIM      bool
	ToolApprovalConfig     []byte
	DisplayEnabled         bool
	OverlayProvider        string
	OverlayEnabled         bool
	OverlayConfig          []byte
}

type BotRecord struct {
	OwnerUserID         string
	Metadata            []byte
	Language            string
	ReasoningEnabled    bool
	ReasoningEffort     string
	HeartbeatEnabled    bool
	HeartbeatInterval   int32
	CompactionEnabled   bool
	CompactionThreshold int32
	CompactionRatio     int32
}

type OverlayRecord struct {
	Enabled  bool
	Provider string
	Config   []byte
}

type UpsertInput struct {
	BotID                  string
	Timezone               *string
	Language               string
	CommandUILanguage      string
	ReasoningEnabled       bool
	ReasoningEffort        string
	HeartbeatEnabled       bool
	HeartbeatInterval      int32
	CompactionEnabled      bool
	CompactionThreshold    int32
	CompactionRatio        int32
	ChatModelID            string
	ChatRuntime            string
	ChatACPAgentID         string
	ChatACPProjectPath     string
	ChatACPProjectMode     string
	HeartbeatModelID       string
	CompactionModelID      string
	ImageModelID           string
	SearchProviderID       string
	FetchProviderIDSet     bool
	FetchProviderID        string
	MemoryProviderID       string
	TTSModelID             string
	TranscriptionModelID   string
	VideoModelID           string
	PersistFullToolResults bool
	ShowToolCallsInIM      bool
	ToolApprovalConfig     []byte
	DisplayEnabled         bool
	OverlayProvider        string
	OverlayEnabled         bool
	OverlayConfig          []byte
}

// Store owns Settings reads and writes against the bot aggregate.
type Store interface {
	Get(context.Context, string) (Record, error)
	GetBot(context.Context, string) (BotRecord, error)
	GetOverlay(context.Context, string) (OverlayRecord, error)
	Upsert(context.Context, UpsertInput) (Record, error)
	Delete(context.Context, string) error
}

// ModelReader resolves Model-owned references without leaking persistence types.
type ModelReader interface {
	ModelExists(context.Context, string) (bool, error)
	ListModelIDs(context.Context, string) ([]string, error)
}
