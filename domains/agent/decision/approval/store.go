package approval

import (
	"context"
	"time"
)

// Record is the persistence representation consumed by the approval service.
// Database-specific nullable and generated types stay behind Store.
type Record struct {
	ID                           string
	BotID                        string
	SessionID                    string
	RouteID                      string
	ChannelIdentityID            string
	WorkspaceTargetID            string
	ToolCallID                   string
	ToolName                     string
	Operation                    string
	ToolInput                    []byte
	ShortID                      int
	Status                       string
	RuntimeFencingToken          *int64
	DecisionReason               string
	RequestedByChannelIdentityID string
	DecidedByChannelIdentityID   string
	RequestedMessageID           string
	PromptMessageID              string
	PromptExternalMessageID      string
	SourcePlatform               string
	ReplyTarget                  string
	ConversationType             string
	CreatedAt                    time.Time
	DecidedAt                    *time.Time
}

type CreateRecordInput struct {
	BotID                        string
	SessionID                    string
	RouteID                      string
	ChannelIdentityID            string
	WorkspaceTargetID            string
	ToolCallID                   string
	ToolName                     string
	Operation                    string
	ToolInput                    []byte
	RuntimeFencingToken          *int64
	RequestedByChannelIdentityID string
	RequestedMessageID           string
	SourcePlatform               string
	ReplyTarget                  string
	ConversationType             string
}

type DecisionRecordInput struct {
	ID                         string
	Reason                     string
	DecidedByChannelIdentityID string
	RuntimeFencingToken        *int64
}

type SessionInput struct {
	BotID               string
	SessionID           string
	RuntimeFencingToken *int64
}

type CancelSessionInput struct {
	SessionInput
	Reason string
}

type UpdatePromptInput struct {
	ID                      string
	PromptMessageID         string
	PromptExternalMessageID string
}

// Store is the minimal approval persistence contract.
type Store interface {
	ChannelIdentityExists(context.Context, string) (bool, error)
	Create(context.Context, CreateRecordInput) (Record, error)
	Get(context.Context, string) (Record, error)
	GetPendingBySessionShortID(context.Context, string, string, int) (Record, error)
	GetPendingByReplyMessage(context.Context, string, string, string) (Record, error)
	GetLatestPendingBySession(context.Context, string, string) (Record, error)
	Approve(context.Context, DecisionRecordInput) (Record, error)
	Reject(context.Context, DecisionRecordInput) (Record, error)
	CancelPendingBySession(context.Context, CancelSessionInput) ([]Record, error)
	UpdatePrompt(context.Context, UpdatePromptInput) (Record, error)
	ListPendingBySession(context.Context, string, string) ([]Record, error)
	ListBySession(context.Context, string, string) ([]Record, error)
	ListBySessionToolCalls(context.Context, string, string, []string) ([]Record, error)
}

// Transactor serializes request creation with the Bot -> Session decision lock.
type Transactor interface {
	InApprovalCreateTransaction(context.Context, string, string, func(Store) error) error
}

// FenceRunner validates the runtime fence and mutates approval state atomically.
type FenceRunner interface {
	InApprovalFenceTransaction(context.Context, string, string, func(Store) error) error
}

type Persistence interface {
	Store
	Transactor
	FenceRunner
}
