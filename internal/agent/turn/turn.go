// Package turn defines the application-level contract for starting and
// observing agent turns. It is the only agent surface Channel may depend
// on; it must not import Echo, fx, sqlc, conversation, or any channel package.
package turn

import (
	"context"
	"encoding/json"
	"errors"

	userinput "github.com/felinics/memoh/internal/agent/decision/input"
)

// ErrDuplicateTurn reports that a StartTurnCommand's (TeamID,
// IdempotencyKey) pair was already claimed by an earlier run. Callers
// handling platform webhook retries should treat it as successful
// delivery and drop the duplicate silently.
var ErrDuplicateTurn = errors.New("turn: duplicate idempotency key")

// ErrSessionBusy reports that the thread already has a run in flight, so this
// command was not started and nothing was persisted for it.
//
// It is retryable by construction, and that is the whole point: a thread runs
// one turn at a time, and the runtime holds nothing on a caller's behalf. The
// caller redelivers through the retry mechanism it already owns — a platform
// webhook retry, the next cron fire — and because a redelivery repeats the same
// IdempotencyKey, the retry is the same invocation rather than a second turn.
//
// It is declared here rather than reused from the runtime because this package
// is the only agent surface Channel may import, and it must not depend on the
// runtime that produces the condition.
var ErrSessionBusy = errors.New("turn: thread already has a run in flight")

// ErrTeamNotServed reports that the service instance does not serve the
// command's team. The in-process runtime binds its database pool to the
// single self-hosted team, so commands for any other team must fail
// closed instead of silently reading and writing the default team's data.
var ErrTeamNotServed = errors.New("turn: team not served by this instance")

// Mode selects the turn orchestration path.
type Mode string

const (
	ModeChat    Mode = "chat"
	ModeDiscuss Mode = "discuss"
)

// StartTurnCommand is a pure-data command. Field set mirrors exactly what
// the channel inbound processor supplies today; function- and channel-typed fields are
// intentionally excluded — injection goes through RunHandle.Inject and
// outbound assets through RunHandle.AddOutboundAssets.
type StartTurnCommand struct {
	SchemaVersion int
	TeamID        string // required; the service fails closed when empty
	Mode          Mode

	BotID                   string
	ChatID                  string
	ThreadID                string
	RouteID                 string
	Token                   string
	ChatToken               string
	UserID                  string
	SourceChannelIdentityID string
	DisplayName             string

	IdempotencyKey    string // derived from platform external message id
	ExternalMessageID string
	EventID           string

	Query           string
	ModelQuery      string
	UserMessageKind string
	UserVisibleText string
	Attachments     []Attachment

	ReplyTarget            string
	ConversationType       string
	ConversationName       string
	SourceReplyToMessageID string
	ReplySender            string
	ReplyPreview           string
	ReplyAttachments       []Attachment
	MentionsBot            bool
	RepliesToBot           bool

	ForwardMessageID          string
	ForwardFromUserID         string
	ForwardFromConversationID string
	ForwardSender             string
	ForwardDate               int64

	CurrentChannel string
	Channels       []string

	Model             string
	ReasoningEffort   string
	WorkspaceTargetID string

	SkillActivation      *SkillActivation
	RequestedSkills      []RequestedSkillContext
	SkipMemoryExtraction bool
	SkipTitleGeneration  bool
	UserMessagePersisted bool

	// Discuss-mode extras.
	SessionToken string //nolint:gosec // session credential material; crosses only the authenticated internal RPC in split deployments
	ToolHTTPURL  string

	// DiscussMessages is the composed conversation context for a discuss
	// turn (Mode == ModeDiscuss), already rendered by the caller's
	// projection. Image parts are injected into the last real user message
	// before the runtime starts streaming.
	DiscussMessages  []DiscussMessage
	DiscussImageRefs []DiscussImageRef
	// DiscussAddressed covers an explicit @-mention, a reply-to, or a direct
	// (1:1) conversation. Expensive external runtimes (ACP) use it as a
	// participation gate and skip the run when false. Mention/reply details
	// stay attached to their canonical timeline messages.
	DiscussAddressed bool
}

// DiscussMessage is one composed context message for a discuss turn.
type DiscussMessage struct {
	Role                 string          `json:"role"`
	Content              string          `json:"content"`
	RawContent           json.RawMessage `json:"raw_content,omitempty"`
	CompactionArtifactID string          `json:"compaction_artifact_id,omitempty"`
}

// DiscussImageRef references an image attachment to inline as vision input.
type DiscussImageRef struct {
	ContentHash string `json:"content_hash"`
	Mime        string `json:"mime,omitempty"`
}

// Synthetic discuss event kinds emitted by the runtime before (or instead
// of) the model event stream.
const (
	// DiscussEventRunResolved is always the first event of a discuss run;
	// its payload is DiscussRunResolvedPayload.
	DiscussEventRunResolved = "discuss_run_resolved"
	// DiscussEventSkipped signals the runtime declined to start (e.g. ACP
	// participation gate); the run ends after this event.
	DiscussEventSkipped = "discuss_skipped"
)

// DiscussRunResolvedPayload is the payload of DiscussEventRunResolved.
type DiscussRunResolvedPayload struct {
	RuntimeType string `json:"runtime_type"`
}

// ToolApprovalResponse resumes a thread's turn deferred on tool approval
// (RFC ResumeApprovalCommand).
type ToolApprovalResponse struct {
	ControlID              string
	BotID                  string
	ThreadID               string
	ActorChannelIdentityID string
	ActorUserID            string
	ApprovalID             string
	ExplicitID             string
	ReplyExternalMessageID string
	Decision               string
	// OptionID is the opaque agent-provided permission option selected by the
	// Web client. Empty retains the legacy binary approve/reject behavior.
	OptionID                   string
	Reason                     string
	ChatToken                  string
	SuppressActivePromptAttach bool
}

// UserInputResponse resumes a thread's turn deferred on ask_user
// (RFC ResumeUserInputCommand).
type UserInputResponse struct {
	ControlID                  string
	BotID                      string
	ThreadID                   string
	ActorChannelIdentityID     string
	ActorUserID                string
	UserInputID                string
	ExplicitID                 string
	ReplyExternalMessageID     string
	Answers                    []QuestionAnswer
	TextAnswer                 string
	Canceled                   bool
	Reason                     string
	ChatToken                  string
	SuppressActivePromptAttach bool
}

// Event is one element of a thread turn's event stream. Payload is the raw JSON
// chunk exactly as produced by the runtime; Kind is the parsed "type"
// field (best effort, empty when unparsable). Seq is monotonically
// increasing per run.
type Event struct {
	RunID    string
	TeamID   string
	ThreadID string
	Seq      int64
	Kind     string
	Payload  json.RawMessage
}

// RunHandle observes and steers one running turn. Events and Errs mirror
// the runtime's chunk/error channel pair; both close when the run ends.
type RunHandle interface {
	RunID() string
	Events() <-chan Event
	Errs() <-chan error
	Inject(ctx context.Context, msg InjectMessage) error
	AddOutboundAssets(refs []OutboundAssetRef)
	Cancel()
}

// Service starts and resumes turns. The application service implements this
// contract directly; cross-process transports expose the same contract. The
// eventCh parameters mirror the application's raw JSON stream for resumed
// turns and are not part of the serialized command shape.
type Service interface {
	StartTurn(ctx context.Context, cmd StartTurnCommand) (RunHandle, error)
	RespondToolApproval(ctx context.Context, input ToolApprovalResponse, eventCh chan<- json.RawMessage) error
	RespondUserInput(ctx context.Context, input UserInputResponse, eventCh chan<- json.RawMessage) error
	AdvancePlainTextUserInput(ctx context.Context, input userinput.AdvanceTextInput) (userinput.AdvanceTextResult, error)
}
