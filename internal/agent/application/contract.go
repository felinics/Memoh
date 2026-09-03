package application

import (
	"encoding/json"

	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	sessionqueue "github.com/felinics/memoh/internal/agent/runtime/session/queue"
	"github.com/felinics/memoh/internal/agent/turn"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
)

// ChatRequest is the application-layer input used while orchestrating a chat
// turn. Transport callers should prefer turn.StartTurnCommand; the additional
// channel and function fields below are strictly in-process runtime state.
type ChatRequest struct {
	BotID    string `json:"-"`
	ChatID   string `json:"-"`
	ThreadID string `json:"-"`
	// RunID is the server-minted identity of this turn's run. Downstream state
	// that must agree on "which turn is this" — the compaction barrier, the ACP
	// session, interactive tool headers — keys on it.
	RunID string `json:"-"`
	// RunHandle is the server-owned execution capability for durable step and
	// queue commits. Transport callers cannot supply it.
	RunHandle sessionruntime.RunHandle `json:"-"`
	// TurnID and TurnPosition are the turn admission already allocated for this
	// run. They travel with the request so the persisted user turn lands under
	// the id the client was handed at run_accepted, instead of the history layer
	// minting a second one (SR-TURN-001). Empty means no admission decided the
	// turn — channel inbound and schedules — and history allocates it.
	TurnID                    string                `json:"-"`
	TurnPosition              *int64                `json:"-"`
	Token                     string                `json:"-"`
	UserID                    string                `json:"-"`
	SourceChannelIdentityID   string                `json:"-"`
	DisplayName               string                `json:"-"`
	RouteID                   string                `json:"-"`
	ChatToken                 string                `json:"-"`
	ExternalMessageID         string                `json:"-"`
	ReplyTarget               string                `json:"-"`
	ConversationType          string                `json:"-"`
	ConversationName          string                `json:"-"`
	SourceReplyToMessageID    string                `json:"-"`
	ReplySender               string                `json:"-"`
	ReplyPreview              string                `json:"-"`
	ReplyAttachments          []turn.Attachment     `json:"-"`
	MentionsBot               bool                  `json:"-"`
	RepliesToBot              bool                  `json:"-"`
	ForwardMessageID          string                `json:"-"`
	ForwardFromUserID         string                `json:"-"`
	ForwardFromConversationID string                `json:"-"`
	ForwardSender             string                `json:"-"`
	ForwardDate               int64                 `json:"-"`
	UserMessagePersisted      bool                  `json:"-"`
	PersistedUserMessageID    string                `json:"-"`
	ReusePersistedUserMessage bool                  `json:"-"`
	EventID                   string                `json:"-"`
	RawQuery                  string                `json:"-"`
	ModelQuery                string                `json:"-"`
	UserMessageKind           string                `json:"-"`
	UserVisibleText           string                `json:"-"`
	SkillActivation           *turn.SkillActivation `json:"-"`
	ToolHTTPURL               string                `json:"-"`
	SessionType               string                `json:"-"`
	RuntimeType               string                `json:"-"`
	SkipMemoryExtraction      bool                  `json:"-"`
	SkipHistoryTurn           bool                  `json:"-"`
	// TurnReplacement is set only for an admitted retry/edit run. Its step
	// output stays hidden until the queue coordinator reaches the true final
	// boundary and publishes this replacement in its own transaction.
	TurnReplacement     *messagepkg.TurnReplacement `json:"-"`
	SkipTitleGeneration bool                        `json:"-"`
	ForceFreshRuntime   bool                        `json:"-"`
	// AgentCommand is the exact agent-command selector the Web admission layer
	// matched against a live ACP runtime. The session pool re-validates it
	// against the final session at prompt time; it never crosses the turn
	// transport (Web admission runs in-process).
	AgentCommand                 string           `json:"-"`
	HistoryCutoffBeforeMessageID string           `json:"-"`
	RequiredHistoryMessageID     string           `json:"-"`
	WorkspaceTarget              *WorkspaceTarget `json:"-"`

	// OutboundAssetCollector returns asset refs accumulated during outbound
	// streaming. It is never serialized across the turn transport.
	OutboundAssetCollector func() []turn.OutboundAssetRef `json:"-"`

	// InjectCh receives user messages between tool rounds. Remote transports
	// use turn.RunHandle.Inject instead.
	InjectCh <-chan turn.InjectMessage `json:"-"`
	// QueueInjectCh is the execution-owned sender paired with InjectCh. Only the
	// durable step coordinator uses it after claiming a steer item.
	QueueInjectCh chan<- turn.InjectMessage `json:"-"`
	// QueueSteerClaim is a reclaimed coordinator-issued capability for a steer
	// item whose final handoff committed before the owner disappeared.
	QueueSteerClaim *sessionqueue.SteerClaimRef `json:"-"`
	// QueueFollowUpClaim is the coordinator-issued capability for a
	// continuation's source item. It is consumed by the first durable step.
	QueueFollowUpClaim *sessionqueue.FollowUpClaimRef `json:"-"`
	StepIndexOffset    int                            `json:"-"`
	// PublishRuntimeEvents is set for server-owned continuations, which do not
	// have a client runHandle pump to publish native events into the session
	// runtime projection.
	PublishRuntimeEvents bool `json:"-"`

	Query             string                       `json:"query"`
	Model             string                       `json:"model,omitempty"`
	Provider          string                       `json:"provider,omitempty"`
	ReasoningEffort   string                       `json:"reasoning_effort,omitempty"`
	WorkspaceTargetID string                       `json:"workspace_target_id,omitempty"`
	Channels          []string                     `json:"channels,omitempty"`
	CurrentChannel    string                       `json:"current_channel,omitempty"`
	Messages          []turn.ModelMessage          `json:"messages,omitempty"`
	Attachments       []turn.Attachment            `json:"attachments,omitempty"`
	RequestedSkills   []turn.RequestedSkillContext `json:"-"`
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
	Messages []turn.ModelMessage `json:"messages"`
	Model    string              `json:"model,omitempty"`
	Provider string              `json:"provider,omitempty"`
}

// StreamChunk is one raw event emitted by the application stream.
type StreamChunk = json.RawMessage

// The canonical turn value objects are re-exported inside application so the
// orchestration code can use them without maintaining a second DTO family.
type (
	ChatAttachment        = turn.Attachment
	ModelMessage          = turn.ModelMessage
	ContentPart           = turn.ContentPart
	ToolCall              = turn.ToolCall
	ToolCallFunction      = turn.ToolCallFunction
	RequestedSkillContext = turn.RequestedSkillContext
	SkillActivation       = turn.SkillActivation
	SkillActivationSkill  = turn.SkillActivationSkill
	OutboundAssetRef      = turn.OutboundAssetRef
	InjectMessage         = turn.InjectMessage
	AssistantOutput       = turn.AssistantOutput
)

const UserMessageKindSkillActivation = turn.UserMessageKindSkillActivation

func newTextContent(text string) json.RawMessage {
	return turn.NewTextContent(text)
}

func skillActivationModelQuery(activation *SkillActivation) string {
	return turn.SkillActivationModelQuery(activation)
}
