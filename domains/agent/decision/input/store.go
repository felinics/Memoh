package input

import (
	"context"
	"time"
)

// Record is the persistence representation consumed by the input service.
type Record struct {
	ID                           string
	BotID                        string
	SessionID                    string
	RouteID                      string
	ChannelIdentityID            string
	WorkspaceTargetID            string
	ToolCallID                   string
	ToolName                     string
	ShortID                      int
	Status                       string
	RuntimeFencingToken          *int64
	InputJSON                    []byte
	UIPayloadJSON                []byte
	InteractionJSON              []byte
	InteractionRevision          int
	ResultJSON                   []byte
	ProviderMetadata             []byte
	RequestedByChannelIdentityID string
	RespondedByChannelIdentityID string
	AssistantMessageID           string
	ToolResultMessageID          string
	PromptMessageID              string
	PromptExternalMessageID      string
	SourcePlatform               string
	ReplyTarget                  string
	ConversationType             string
	ExpiresAt                    *time.Time
	CreatedAt                    time.Time
	RespondedAt                  *time.Time
	CanceledAt                   *time.Time
	UpdatedAt                    time.Time
}

type CreateRecordInput struct {
	BotID                        string
	SessionID                    string
	RouteID                      string
	ChannelIdentityID            string
	WorkspaceTargetID            string
	ToolCallID                   string
	ToolName                     string
	RuntimeFencingToken          *int64
	InputJSON                    []byte
	UIPayloadJSON                []byte
	ProviderMetadata             []byte
	RequestedByChannelIdentityID string
	SourcePlatform               string
	ReplyTarget                  string
	ConversationType             string
	ExpiresAt                    *time.Time
}

type ResolveRecordInput struct {
	ID                  string
	RuntimeFencingToken *int64
}

type ResultRecordInput struct {
	ID                           string
	ResultJSON                   []byte
	RespondedByChannelIdentityID string
	RuntimeFencingToken          *int64
}

type SessionInput struct {
	BotID               string
	SessionID           string
	RuntimeFencingToken *int64
}

type CancelSessionInput struct {
	SessionInput
	ResultJSON []byte
}

type InteractionRecordInput struct {
	ID                  string
	InteractionJSON     []byte
	InteractionRevision int
}

type UpdatePromptInput struct {
	ID                      string
	PromptMessageID         string
	PromptExternalMessageID string
}

// Store is the minimal user-input persistence contract.
type Store interface {
	ChannelIdentityExists(context.Context, string) (bool, error)
	Create(context.Context, CreateRecordInput) (Record, error)
	Get(context.Context, string) (Record, error)
	GetRespondable(context.Context, ResolveRecordInput) (Record, error)
	GetBySessionToolCall(context.Context, string, string) (Record, error)
	GetPendingBySessionShortID(context.Context, string, string, int) (Record, error)
	GetPendingByReplyMessage(context.Context, string, string, string) (Record, error)
	GetLatestPendingBySession(context.Context, string, string) (Record, error)
	UpdateInteraction(context.Context, InteractionRecordInput) (Record, error)
	Submit(context.Context, ResultRecordInput) (Record, error)
	Cancel(context.Context, ResultRecordInput) (Record, error)
	CancelPendingBySession(context.Context, CancelSessionInput) ([]Record, error)
	Fail(context.Context, ResultRecordInput) (Record, error)
	UpdatePrompt(context.Context, UpdatePromptInput) (Record, error)
	UpdateAssistantMessage(context.Context, string, string) (Record, error)
	UpdateToolResultMessage(context.Context, string, string) (Record, error)
	ListPendingBySession(context.Context, string, string) ([]Record, error)
	ListBySession(context.Context, string, string) ([]Record, error)
	ListBySessionToolCalls(context.Context, string, string, []string) ([]Record, error)
}

// Transactor serializes request creation with the Bot -> Session decision lock.
type Transactor interface {
	InInputCreateTransaction(context.Context, string, string, func(Store) error) error
}

// FenceRunner validates the runtime fence and mutates input state atomically.
type FenceRunner interface {
	InInputFenceTransaction(context.Context, string, string, func(Store) error) error
}

type Persistence interface {
	Store
	Transactor
	FenceRunner
}
