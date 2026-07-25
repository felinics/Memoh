package application

import (
	"encoding/json"

	agentdomain "github.com/memohai/memoh/domains/agent"
)

// ChatRequest is the application-layer input used while orchestrating a chat
// turn. Transport callers should prefer agentdomain.StartTurnCommand; the additional
// channel and function fields below are strictly in-process runtime state.
type ChatRequest struct {
	BotID                        string                       `json:"-"`
	ChatID                       string                       `json:"-"`
	ThreadID                     string                       `json:"-"`
	StreamID                     string                       `json:"-"`
	Token                        string                       `json:"-"`
	UserID                       string                       `json:"-"`
	SourceChannelIdentityID      string                       `json:"-"`
	DisplayName                  string                       `json:"-"`
	AvatarURL                    string                       `json:"-"`
	RouteID                      string                       `json:"-"`
	ChatToken                    string                       `json:"-"`
	ExternalMessageID            string                       `json:"-"`
	ReplyTarget                  string                       `json:"-"`
	ConversationType             string                       `json:"-"`
	ConversationName             string                       `json:"-"`
	SourceReplyToMessageID       string                       `json:"-"`
	ReplySender                  string                       `json:"-"`
	ReplyPreview                 string                       `json:"-"`
	ReplyAttachments             []agentdomain.Attachment     `json:"-"`
	MentionsBot                  bool                         `json:"-"`
	RepliesToBot                 bool                         `json:"-"`
	ForwardMessageID             string                       `json:"-"`
	ForwardFromUserID            string                       `json:"-"`
	ForwardFromConversationID    string                       `json:"-"`
	ForwardSender                string                       `json:"-"`
	ForwardDate                  int64                        `json:"-"`
	UserMessagePersisted         bool                         `json:"-"`
	PersistedUserMessageID       string                       `json:"-"`
	ReusePersistedUserMessage    bool                         `json:"-"`
	EventID                      string                       `json:"-"`
	RawQuery                     string                       `json:"-"`
	ModelQuery                   string                       `json:"-"`
	UserMessageKind              string                       `json:"-"`
	UserVisibleText              string                       `json:"-"`
	SkillActivation              *agentdomain.SkillActivation `json:"-"`
	ToolHTTPURL                  string                       `json:"-"`
	SessionType                  string                       `json:"-"`
	RuntimeType                  string                       `json:"-"`
	SkipMemoryExtraction         bool                         `json:"-"`
	SkipHistoryTurn              bool                         `json:"-"`
	SkipTitleGeneration          bool                         `json:"-"`
	ForceFreshRuntime            bool                         `json:"-"`
	HistoryCutoffBeforeMessageID string                       `json:"-"`
	RequiredHistoryMessageID     string                       `json:"-"`
	WorkspaceTarget              *WorkspaceTarget             `json:"-"`

	// OutboundAssetCollector returns asset refs accumulated during outbound
	// streaming. It is never serialized across the turn transport.
	OutboundAssetCollector func() []agentdomain.OutboundAssetRef `json:"-"`

	// InjectCh receives user messages between tool rounds. Remote transports
	// use agentdomain.RunHandle.Inject instead.
	InjectCh <-chan agentdomain.InjectMessage `json:"-"`

	Query             string                              `json:"query"`
	Model             string                              `json:"model,omitempty"`
	Provider          string                              `json:"provider,omitempty"`
	ReasoningEffort   string                              `json:"reasoning_effort,omitempty"`
	WorkspaceTargetID string                              `json:"workspace_target_id,omitempty"`
	Channels          []string                            `json:"channels,omitempty"`
	CurrentChannel    string                              `json:"current_channel,omitempty"`
	Messages          []agentdomain.ModelMessage          `json:"messages,omitempty"`
	Attachments       []agentdomain.Attachment            `json:"attachments,omitempty"`
	RequestedSkills   []agentdomain.RequestedSkillContext `json:"-"`
}

// WorkspaceTarget is the immutable execution-location snapshot resolved for
// one application request.
type WorkspaceTarget struct {
	TargetID string `json:"target_id"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
}

// InjectedMessageRecord records where an injected message belongs in the
// persisted model-message sequence.
type InjectedMessageRecord struct {
	HeaderifiedText string
	InsertAfter     int
}

// ChatResponse is the output of a non-streaming application call.
type ChatResponse struct {
	Messages []agentdomain.ModelMessage `json:"messages"`
	Model    string                     `json:"model,omitempty"`
	Provider string                     `json:"provider,omitempty"`
}

// StreamChunk is one raw event emitted by the application stream.
type StreamChunk = json.RawMessage
