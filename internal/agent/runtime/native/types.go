package native

import (
	"context"
	"encoding/json"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/background"
	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/event"
	tools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/models"
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

type ContextStepSelectionInput struct {
	Scope               contextfrag.Scope
	InitialMessageCount int
	Messages            []sdk.Message
	BudgetMaxTokens     int
	// ProviderSystem, ProviderTools, and ProviderInputAllowanceTokens carry
	// the complete provider envelope into step reselection. A zero allowance
	// keeps the unlimited-window contract and disables serialized enforcement.
	ProviderSystem               string
	ProviderTools                []sdk.Tool
	ProviderInputAllowanceTokens int
	// RecentProtectTokens carries the run's recent-protection window override
	// so step reselection resolves the same window as the provider view. Nil
	// uses the view default; a pointer to zero disables the window.
	RecentProtectTokens *int
	// KeepRecentToolResults keeps the newest N complete tool cycles intact
	// and truncates older bulky tool results to a size summary; <= 0 disables
	// content truncation.
	KeepRecentToolResults int
	// MinMessages gates content truncation on total provider message count.
	MinMessages int
}

type ContextStepSelectionResult struct {
	Messages []sdk.Message
	// MessageSourceIndexes maps each returned message to its absolute index in
	// ContextStepSelectionInput.Messages. Synthetic messages use -1. A
	// normalized byte-identical unchanged output can inherit every input
	// origin. Otherwise a missing or invalid vector preserves only verified
	// protected-prefix origins and treats every suffix origin as unknown.
	// Custom reselectors that need dynamic carriers to become durable must
	// return a complete, exact source-index vector.
	MessageSourceIndexes      []int
	MessageSourceIndexesKnown bool
	Dropped                   int
	Truncated                 int
	DropReasons               map[string]int
	FatalError                error
}

type ContextStepReselector func(context.Context, ContextStepSelectionInput) ContextStepSelectionResult

// InjectMessage carries a user message to be injected into a running agent
// stream between tool rounds via the PrepareStep hook.
type InjectMessage struct {
	Text            string
	HeaderifiedText string
	// ImageParts carries inline images (data URL or public URL) to attach
	// alongside the injected text when the model supports vision input.
	ImageParts []sdk.ImagePart
}

// RunConfig holds everything needed for a single agent invocation.
type RunConfig struct {
	// RunID is the stable identity allocated by durable admission for this
	// invocation. Direct callers without admission receive one at the
	// application creation boundary before the native runtime starts.
	RunID                       string
	Model                       *sdk.Model
	CurrentModelUUID            string
	CurrentModelID              string
	CurrentModelProvider        string
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
	ContextToolExchangePolicy      *contextfrag.ToolExchangePolicy
	ContextBudgetMaxTokens         int
	ContextRecentProtectTokens     *int
	ContextHistoryTokenEstimates   []int
	ContextTrimmableMessages       int
	ContextCachePlan               contextfrag.CachePlan
	ContextMutations               *contextfrag.MutationLedger
	ContextDynamicMutators         []contextfrag.DynamicMutator
	ContextLifecycle               *contextfrag.LifecycleHolder
	ContextStepReselector          ContextStepReselector
	initialProviderMessageCount    int
	initialProviderPrefixSet       bool
	providerAttemptState           *providerAttemptState
	providerMessageProvenance      preparedMessageProvenance
	preparedStepMessages           *stepMessageCapture
	contextStepFailure             func(error)
	SessionType                    string
	LiveToolStream                 bool
	CanRequestUserInput            bool
	SupportsImageInput             bool
	SupportsFileInput              bool
	SupportsToolCall               bool
	InlineImages                   []sdk.ImagePart
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
	Retry             RetryConfig

	// PromptCacheTTL controls prompt caching for this run. Empty or
	// unrecognized values default to 5m. Use "1h" for the long-cache tier
	// or "off" to disable caching entirely. The TTL is honored only when
	// the resolved model's vendor implements prompt caching (currently
	// Anthropic Messages); for other vendors the value is ignored.
	PromptCacheTTL string

	// InjectCh receives user messages to inject between tool rounds.
	// When non-nil, a PrepareStep hook drains this channel and appends
	// user messages to the conversation before the next LLM call.
	InjectCh <-chan InjectMessage

	// InjectedRecorder is called during terminal delivery for each injected
	// message admitted by a provider attempt, recording the headerified text
	// and the number of SDK output messages that preceded the injection. Used
	// by the resolver to interleave injected messages in storeRound.
	InjectedRecorder func(headerifiedText string, insertAfter int)

	// OnProviderStreamEventObserved receives normalized provider parts before
	// Twilight buffers them or invokes the step commit barrier. Persistence uses
	// this production boundary to measure reasoning without putting timing on
	// the public event wire.
	OnProviderStreamEventObserved func(StreamEvent)

	// OnStepCommitted is a synchronous durability barrier. The callback sees
	// the complete step plus any user/read-media messages prepared immediately
	// before it, with persistence-only tool metadata already attached.
	OnStepCommitted func(ctx context.Context, stepIndex int, step *sdk.StepResult) error

	// OnStepInterrupted persists text/reasoning emitted by the current model
	// call when cancellation arrives before finish-step. Tool-call steps never
	// use this path.
	OnStepInterrupted func(ctx context.Context, stepIndex int, step *sdk.StepResult) error

	// BackgroundManager provides access to the background task system.
	// When non-nil, the agent loop refreshes running task summaries at step
	// boundaries while tools handle waiting and result inspection.
	BackgroundManager *background.Manager

	ToolApprovalHandler func(ctx context.Context, call sdk.ToolCall) (sdk.ToolApprovalResult, error)
}

// GenerateResult holds the result of a non-streaming agent invocation.
type GenerateResult struct {
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
