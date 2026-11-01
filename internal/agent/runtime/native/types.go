package native

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/background"
	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/step"
	tools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/agent/toolexec"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/models/modelretry"
)

// SessionContext carries request-scoped identity and routing information.
type SessionContext struct {
	BotID               string
	ChatID              string
	SessionID           string
	UserID              string
	ChannelIdentityID   string
	CurrentPlatform     string
	ReplyTarget         string
	ConversationType    string
	Timezone            string
	TimezoneLocation    *time.Location
	SessionToken        string //nolint:gosec // carries session credential material at runtime
	WorkspaceTargetID   string
	WorkspaceTargetKind string
	WorkspaceTargetName string
	// WorkdirPath is the session's immutable working directory, resolved
	// once per run from the session's workdir binding.
	WorkdirPath string
	IsSubagent  bool
}

// BotInfo is service-owned bot metadata injected into the system prompt.
type BotInfo struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	Timezone    string `json:"timezone,omitempty"`
}

// SkillEntry represents a skill loaded from the bot container.
type SkillEntry struct {
	Name        string
	Description string
	Content     string
	Path        string
	Metadata    map[string]any
}

// Schedule represents a scheduled task definition.
type Schedule struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Pattern     string `json:"pattern"`
	MaxCalls    *int   `json:"maxCalls,omitempty"`
	Command     string `json:"command"`
}

// LoopDetectionConfig controls loop detection behavior.
type LoopDetectionConfig struct {
	Enabled bool
}

// StepDirective is returned by OnStepCommitted after a durable step lands.
// NextInputs become the next model request; a would-be-final step continues
// instead of ending the loop when this slice is non-empty.
type StepDirective struct {
	NextInputs []DirectiveInput
	// Provenance is, for each message the step persisted, in order, the
	// stored message it became. A run that compacts in place matches later
	// requests against it.
	Provenance []MessageProvenance
}

// DirectiveInput is one user input admitted at a step commit boundary.
// ID is caller-assigned identity (a queue item ID) and Text is the model input.
type DirectiveInput struct {
	ID   string
	Text string
}

// InjectMessage carries a user message to be injected into a running agent
// stream between tool rounds at a step boundary.
type InjectMessage struct {
	Text            string
	HeaderifiedText string
	// ImageParts carries inline images (data URL or public URL) to attach
	// alongside the injected text when the model supports vision input.
	ImageParts []sdk.ImagePart
}

// RunConfig holds everything needed for a single agent invocation.
type RunConfig struct {
	capabilityChanges      *atomic.Bool
	capabilityRefreshCount int
	// RunID is the stable identity allocated by durable admission for this
	// invocation. Direct callers without admission receive one at the
	// application creation boundary before the native runtime starts.
	RunID                       string
	Model                       *sdk.Model
	CurrentModelUUID            string
	CurrentModelID              string
	CurrentModelProvider        string
	CurrentModelProviderID      string
	ForkContext                 *tools.MessageSnapshot
	ForkContextSourceMessageIDs []string
	// ReasoningConfig is the resolved thinking decision, carried whole. It was
	// once five flat fields, which is how the subagent spawn path came to carry
	// one of them and silently drop the rest.
	ReasoningConfig *models.ReasoningConfig
	// ReasoningStoredEffort and ReasoningRequestedEffort retain the two inputs
	// behind ReasoningConfig. Tools that select a different model (notably
	// spawn_agent) must resolve those inputs against that model instead of copying
	// a decision made for the parent model.
	ReasoningStoredEffort          string
	ReasoningRequestedEffort       string
	ChatCompletionsCompat          string
	Messages                       []sdk.Message
	Query                          string
	System                         string
	ContextFrags                   []contextfrag.ContextFrag
	ContextSourceFrags             []contextfrag.ContextFrag
	ContextSourceWarnings          []contextfrag.ValidationWarning
	ContextManifest                contextfrag.Manifest
	ContextScope                   contextfrag.Scope
	ContextCurrentUserMessageIndex *int
	ContextMemoryMessageIndex      *int
	ContextQueryMaterialized       bool
	ContextToolUsage               string
	ContextToolUsageFrags          []contextfrag.ContextFrag
	ContextHookText                string
	ContextToolDefs                []contextfrag.ToolDefAccounting
	ContextToolDefsResolved        bool
	ContextBudgetMaxTokens         int
	// ContextDensity is how many tokens the provider counted for each token
	// the estimate priced in the session's latest step on this model, which
	// an armed run calibrates its estimate by until its own first step
	// reports; zero or below 1 calibrates nothing.
	ContextDensity float64
	// ModelMaxOutputTokens is the model's configured output cap; zero means unknown.
	ModelMaxOutputTokens         int
	RecoverContextBudget         func(context.Context, RunConfig) (RunConfig, bool, error)
	ContextRecentProtectTokens   *int
	ContextHistoryTokenEstimates []int
	ContextTrimmableMessages     int
	ContextCachePlan             contextfrag.CachePlan
	ContextMutations             *contextfrag.MutationLedger
	ContextDynamicMutators       []contextfrag.DynamicMutator
	ContextLifecycle             *contextfrag.LifecycleHolder
	initialProviderMessageCount  int
	initialProviderPrefixSet     bool
	providerAttemptState         *providerAttemptState
	retryDynamicRefs             []dynamicSourceRef
	dynamicInputs                *loopDynamicInputs
	contextStepFailure           func(error)
	SessionType                  string
	LiveToolStream               bool
	CanRequestUserInput          bool
	SupportsImageInput           bool
	SupportsFileInput            bool
	SupportsToolCall             bool
	InlineImages                 []sdk.ImagePart
	// InlineAttachments carries non-image native attachment parts (documents
	// as sdk.FilePart, small text files as wrapped sdk.TextPart) appended to
	// the current user message. Images stay in InlineImages, which also feeds
	// the context-frag view; these parts are materialized by prepareRunConfig
	// only.
	InlineAttachments []sdk.MessagePart
	Identity          SessionContext
	Bot               BotInfo
	Skills            []SkillEntry
	LoopDetection     LoopDetectionConfig
	Retry             modelretry.Config
	// StepIndexOffset lets an application-owned continuation of the same run
	// keep durable step indexes monotonic when the step loop is restarted
	// after a parked decision.
	StepIndexOffset int
	// SteerWake announces queue changes; PendingSteer rechecks the authoritative
	// queue. OnSteer checkpoints a stopped model attempt and claims its next input.
	// These callbacks retain the run context, unlike the cancelled invocation.
	SteerWake    <-chan struct{}
	PendingSteer func(context.Context) (bool, error)
	OnSteer      func(context.Context, int, *step.Record) (StepDirective, error)

	// PromptCacheTTL controls prompt caching for this run. Empty or
	// unrecognized values default to 5m. Use "1h" for the long-cache tier
	// or "off" to disable caching entirely. The TTL is honored only when
	// the resolved model's vendor implements prompt caching (currently
	// Anthropic Messages); for other vendors the value is ignored.
	PromptCacheTTL string

	// InjectCh receives user messages to inject between tool rounds.
	// When non-nil, the step loop drains this channel at each step boundary
	// and appends user messages to the conversation before the next LLM call.
	InjectCh <-chan InjectMessage

	// InjectedRecorder is called during terminal delivery for each user
	// message the loop appended at a step boundary and a provider attempt
	// admitted: live injections and steer directive inputs alike. It records
	// the headerified text and the number of output messages that preceded
	// the message, so the resolver can interleave them in storeRound.
	InjectedRecorder func(headerifiedText string, insertAfter int)

	// OnProviderStreamEventObserved receives normalized provider parts before
	// Twilight buffers them or invokes the step commit barrier. Persistence uses
	// this production boundary to measure reasoning without putting timing on
	// the public event wire.
	OnProviderStreamEventObserved func(StreamEvent)

	// OnStepCommitted is a synchronous durability barrier. The callback sees
	// the complete step plus any user/read-media messages prepared immediately
	// before it, with persistence-only tool metadata already attached.
	// NextInputs on the returned directive are appended as the next model
	// input; a would-be-final step with NextInputs continues the same loop.
	OnStepCommitted func(ctx context.Context, stepIndex int, step *step.Record) (StepDirective, error)

	// OnStepInterrupted persists text/reasoning emitted by the current model
	// call when cancellation arrives before finish-step. Tool-call steps never
	// use this path. It retains the original run cancellation cause so the
	// persistence adapter can reject ownership loss before detaching for IO.
	OnStepInterrupted func(ctx context.Context, stepIndex int, record *step.Record) error

	// SwitchContext arms the run to compact in place: when the request before
	// a step, the first one included, does not fit, it runs one compaction
	// event and returns the event's description for the run's audit.
	// ContextProvenance is what the messages the run starts with render; step
	// commits return the rest.
	SwitchContext     func(context.Context, ContextSwitch) (string, error)
	ContextProvenance []MessageProvenance
	contextSwitch     *contextSwitchState
	// ModelIdleWindow is the silence the run's watchdog allows a model
	// request. The watchdog does not watch a compaction event, so each fork
	// request of one ends after as long a silence; zero is five minutes.
	ModelIdleWindow time.Duration

	// BackgroundManager provides access to the background task system.
	// When non-nil, the agent loop refreshes running task summaries at step
	// boundaries while tools handle waiting and result inspection.
	BackgroundManager *background.Manager

	ToolApprovalHandler func(ctx context.Context, call sdk.ToolCall) (toolexec.ToolApprovalResult, error)
}

// GenerateResult holds the result of a non-streaming agent invocation.
type GenerateResult struct {
	InternalFeedbackIndexes []int

	Messages    []sdk.Message
	Text        string
	Attachments []FileAttachment
	Reactions   []ReactionItem
	Speeches    []SpeechItem
	Usage       *sdk.Usage
}

// FileAttachment, ReactionItem and SpeechItem live in the event leaf package
// (they ride on StreamEvent); aliased here for source compatibility.
type (
	FileAttachment = event.FileAttachment
	ReactionItem   = event.ReactionItem
	SpeechItem     = event.SpeechItem
)

// SystemFile is a file loaded from the bot container for prompt generation.
type SystemFile struct {
	Filename string
	Content  string
}

func mustMarshal(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return data
}
