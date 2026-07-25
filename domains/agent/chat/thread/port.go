package thread

import "context"

// RuntimeFence carries the ownership token required by fenced mutations.
type RuntimeFence struct {
	BotID    string
	ThreadID string
	Token    int64
}

// CreateRecord is the normalized persistence input for a thread.
type CreateRecord struct {
	BotID            string
	RouteID          string
	ChannelType      string
	ConversationType string
	ConversationName string
	ReplyTarget      string
	Type             string
	SessionMode      string
	RuntimeType      string
	RuntimeMetadata  map[string]any
	Title            string
	Metadata         map[string]any
	ParentThreadID   string
	CreatedByUserID  string
}

type ListRecord struct {
	BotID           string
	CreatedByUserID string
	RouteID         string
	Types           []string
	ParentThreadID  string
	Cursor          Cursor
	Limit           int32
	UseCursor       bool
	UseParent       bool
}

type DescriptorRecord struct {
	ThreadID        string
	Type            string
	SessionMode     string
	RuntimeType     string
	RuntimeMetadata map[string]any
	Metadata        map[string]any
}

type ForkRecord struct {
	BotID           string
	ThreadID        string
	MessageID       string
	Title           string
	Metadata        map[string]any
	CreatedByUserID string
}

type SubagentConfigRecord struct {
	ThreadID     string
	ModelUUID    string
	ModelID      string
	ProviderName string
	Forked       bool
}

type ForkContextRecord struct {
	ParentThreadID string
	ThreadID       string
	Messages       []SubagentForkContextMessage
}

// Store is the Thread-owned persistence bundle.
type Store interface {
	InTransaction(context.Context, func(Store) error) error
	CreateThread(context.Context, CreateRecord) (Thread, error)
	GetThread(context.Context, string) (Thread, error)
	ListThreadsByBot(context.Context, ListRecord) ([]Thread, error)
	ListThreadsByUser(context.Context, ListRecord) ([]Thread, error)
	ListThreadsByRoute(context.Context, string) ([]Thread, error)
	ListSubagentThreads(context.Context, string) ([]Thread, error)
	ForkThread(context.Context, ForkRecord) (Thread, error)
	UpdateThreadDescriptor(context.Context, DescriptorRecord) (Thread, error)
	UpdateThreadTitle(context.Context, string, string, *RuntimeFence) (Thread, error)
	UpdateThreadMetadata(context.Context, string, map[string]any, *RuntimeFence) (Thread, error)
	SoftDeleteThread(context.Context, string) error
	TouchThread(context.Context, string) error
	CountThreadMessages(context.Context, string) (int64, error)
	CreateSubagentConfig(context.Context, SubagentConfigRecord) (SubagentConfig, error)
	CreateSubagentForkContext(context.Context, ForkContextRecord) (int64, error)
	GetSubagentConfig(context.Context, string) (SubagentConfig, error)
	ListSubagentForkContext(context.Context, string) ([]SubagentForkContextMessage, error)
}

// ACPPolicyReader provides the Bot metadata needed for ACP setup policy.
type ACPPolicyReader interface {
	GetBotMetadata(context.Context, string) (map[string]any, error)
}
