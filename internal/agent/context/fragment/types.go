// Package contextfrag defines the typed intermediate representation used to
// describe context before it is rendered into provider-specific SDK inputs.
package contextfrag

import (
	"errors"
	"strings"

	sdk "github.com/felinics/twilight/sdk"
)

// Kind identifies the semantic source and intent of a context fragment.
type Kind string

const (
	KindSystemPrompt         Kind = "system_prompt"
	KindSystemPolicy         Kind = "system_policy"
	KindBotIdentity          Kind = "bot_identity"
	KindWorkspaceInstruction Kind = "workspace_instruction"
	KindPlatformIdentity     Kind = "platform_identity"
	KindToolUsage            Kind = "tool_usage"
	KindConversationEvent    Kind = "conversation_event"
	KindCurrentUserMessage   Kind = "current_user_message"
	KindAttachmentRef        Kind = "attachment_ref"
	KindNativeImage          Kind = "native_image"
	KindSkillsCatalog        Kind = "skills_catalog"
	KindHookContext          Kind = "hook_context"
	KindInjectedMessage      Kind = "injected_message"
	KindBackgroundSummary    Kind = "background_summary"
	KindRuntimeContext       Kind = "runtime_context"

	// Reserved for the memory/compaction rewrites. Phase 1 keeps their existing
	// resolver paths intact while making room for future collectors.
	KindMemoryRecall        Kind = "memory_recall"
	KindConversationSummary Kind = "conversation_summary"
)

// BackgroundSummaryMessagePrefix marks the per-step user message that carries
// KindBackgroundSummary content. The agent rebuilds that message between steps
// (remove by prefix, append the fresh summary) so running-task status never
// rewrites the cached system prefix, and step reselection recognizes it as a
// status notice rather than a conversation turn.
const BackgroundSummaryMessagePrefix = "[Background tasks]\n"

// IsBackgroundSummaryCarrier reports whether msg is the per-step background
// summary carrier: a user message holding exactly one unadorned text part that
// starts with BackgroundSummaryMessagePrefix. The agent's between-step removal
// and step reselection share this single contract so a message one side would
// remove is never content the other side protects.
func IsBackgroundSummaryCarrier(msg sdk.Message) bool {
	if msg.Role != sdk.MessageRoleUser || len(msg.Content) != 1 {
		return false
	}
	part, ok := msg.Content[0].(sdk.TextPart)
	return ok &&
		part.CacheControl == nil &&
		part.ProviderMetadata == nil &&
		strings.HasPrefix(part.Text, BackgroundSummaryMessagePrefix)
}

// WorkspaceInstructionAnchor is the heading that marks where the workspace
// instruction section begins in a flattened system prompt string; it must
// stay byte-identical to the heading system_common.md renders. Reverse-parse
// paths (the discuss pipeline and the legacy-fields fallback) search for it
// to splice tool usage before it or split it into its own fragment.
const WorkspaceInstructionAnchor = "\n## Workspace instruction files"

// v1 keeps all context schema versions in lockstep; future migrations can
// split this into per-schema supported ranges without changing manifest shape.
const CurrentSchemaVersion = 1

const (
	SchemaContextManifest = "context_manifest"
	SchemaContextFrag     = "context_frag"
	SchemaContextRef      = "context_ref"
	SchemaContextEdit     = "context_edit"
	SchemaSummaryCoverage = "summary_coverage"
	SchemaRenderPolicy    = "render_policy"
)

const (
	HashAlgoSHA256             = "sha256"
	HashScopeCanonicalFragment = "canonical_fragment"
	HashScopeSourcePayload     = "source_payload"
)

type SchemaVersion struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
}

type ContentRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

type ContextRef struct {
	Namespace   string        `json:"namespace"`
	ID          string        `json:"id"`
	Version     int           `json:"version,omitempty"`
	Range       *ContentRange `json:"range,omitempty"`
	HashAlgo    string        `json:"hash_algo,omitempty"`
	ContentHash string        `json:"content_hash,omitempty"`
	HashScope   string        `json:"hash_scope,omitempty"`
	Schema      string        `json:"schema"`
	Durability  RefDurability `json:"durability,omitempty"`
}

type FragmentHash struct {
	Algo  string `json:"algo"`
	Scope string `json:"scope"`
	Value string `json:"value"`
}

type RefDurability string

const (
	RefDurable   RefDurability = "durable"
	RefSynthetic RefDurability = "synthetic"
	RefDebug     RefDurability = "debug"
)

// Slot describes where a fragment is rendered in the LLM input layout.
type Slot string

const (
	SlotSystem                    Slot = "system"
	SlotBeforeHistory             Slot = "before_history"
	SlotHistory                   Slot = "history"
	SlotAfterHistoryBeforeCurrent Slot = "after_history_before_current"
	SlotCurrentUser               Slot = "current_user"
	SlotAfterCurrent              Slot = "after_current"
)

// CacheClass marks whether a fragment is expected to be prompt-cache friendly.
type CacheClass string

const (
	CacheStable  CacheClass = "stable"
	CacheDynamic CacheClass = "dynamic"
	CacheNever   CacheClass = "never"
)

// TrustLevel records whether a fragment comes from Memoh-controlled state or
// untrusted external conversation content.
type TrustLevel string

const (
	TrustSystem    TrustLevel = "system"
	TrustWorkspace TrustLevel = "workspace"
	TrustUser      TrustLevel = "user"
	TrustExternal  TrustLevel = "external"
)

// OverflowAction is the policy to use when a fragment exceeds budget.
type OverflowAction string

const (
	OverflowKeep      OverflowAction = "keep"
	OverflowTrim      OverflowAction = "trim"
	OverflowSummarize OverflowAction = "summarize"
	OverflowDrop      OverflowAction = "drop"
)

// RetentionTier groups fragments by how strongly a policy pass must retain
// them. The zero value leaves the policy unspecified.
type RetentionTier string

const (
	RetentionUnspecified RetentionTier = ""
	RetentionRequired    RetentionTier = "required"
	RetentionPreferred   RetentionTier = "preferred"
	RetentionOptional    RetentionTier = "optional"
)

var (
	ErrProtectedContextOverflow = errors.New("protected context exceeds its budget")
	ErrBudgetUnsatisfied        = errors.New("context budget reserves exceed the available window")
)

// DropPriority orders fragments within one retention tier. Higher values drop
// before lower values, so lower values survive longer under policy pressure.
type DropPriority int

func (p DropPriority) DropsBefore(other DropPriority) bool {
	return p > other
}

// BudgetPolicy captures the budget behavior for a fragment: the selector
// enforces MaxTokens/MaxChars via Trim or Drop, Summarize is not implemented
// (deferred to compaction), and Keep marks the fragment as must-keep.
type BudgetPolicy struct {
	MaxTokens int            `json:"max_tokens,omitempty"`
	MaxChars  int            `json:"max_chars,omitempty"`
	Overflow  OverflowAction `json:"overflow,omitempty"`
}

// RenderFormat describes how a fragment should be rendered.
type RenderFormat string

const (
	RenderPlainText  RenderFormat = "plain_text"
	RenderMarkdown   RenderFormat = "markdown"
	RenderSDKMessage RenderFormat = "sdk_message"
	RenderNativePart RenderFormat = "native_part"
)

// RenderPolicy stores rendering hints. Anchor is used for sections such as
// tool usage that must land before a known heading.
type RenderPolicy struct {
	Format      RenderFormat `json:"format,omitempty"`
	Anchor      string       `json:"anchor,omitempty"`
	GroupID     string       `json:"group_id,omitempty"`
	GroupJoiner string       `json:"group_joiner,omitempty"`
}

func RenderText(text string, policy RenderPolicy) string {
	if policy.GroupID != "" {
		return strings.Trim(text, " \t\r")
	}
	return strings.TrimSpace(text)
}

func RenderSeparator(previous, current RenderPolicy) string {
	if previous.GroupID == "" || previous.GroupID != current.GroupID {
		return "\n\n"
	}
	if current.GroupJoiner != "" {
		return current.GroupJoiner
	}
	if previous.GroupJoiner != "" {
		return previous.GroupJoiner
	}
	return "\n\n"
}

// Provenance identifies where a fragment came from.
type Provenance struct {
	Source    string `json:"source,omitempty"`
	SourceID  string `json:"source_id,omitempty"`
	Collector string `json:"collector,omitempty"`
	Index     int    `json:"index,omitempty"`
}

// AttentionReason explains why an IM/group-chat event deserves attention.
type AttentionReason string

const (
	AttentionDirect   AttentionReason = "direct"
	AttentionMention  AttentionReason = "mention"
	AttentionReply    AttentionReason = "reply"
	AttentionCommand  AttentionReason = "command"
	AttentionSchedule AttentionReason = "schedule"
	AttentionPassive  AttentionReason = "passive"
)

// Scope preserves IM/group-chat topology separately from rendered text.
type Scope struct {
	BotID                     string            `json:"bot_id,omitempty"`
	ChatID                    string            `json:"chat_id,omitempty"`
	SessionID                 string            `json:"session_id,omitempty"`
	ChannelIdentityID         string            `json:"channel_identity_id,omitempty"`
	DisplayName               string            `json:"display_name,omitempty"`
	Platform                  string            `json:"platform,omitempty"`
	ConversationType          string            `json:"conversation_type,omitempty"`
	ConversationName          string            `json:"conversation_name,omitempty"`
	ReplyTarget               string            `json:"reply_target,omitempty"`
	CurrentMessageID          string            `json:"current_message_id,omitempty"`
	EventID                   string            `json:"event_id,omitempty"`
	ReplyToMessageID          string            `json:"reply_to_message_id,omitempty"`
	ReplySender               string            `json:"reply_sender,omitempty"`
	MentionsBot               bool              `json:"mentions_bot,omitempty"`
	RepliesToBot              bool              `json:"replies_to_bot,omitempty"`
	ForwardMessageID          string            `json:"forward_message_id,omitempty"`
	ForwardFromUserID         string            `json:"forward_from_user_id,omitempty"`
	ForwardFromConversationID string            `json:"forward_from_conversation_id,omitempty"`
	Attention                 []AttentionReason `json:"attention,omitempty"`
	Metadata                  map[string]string `json:"metadata,omitempty"`
}

// PartType identifies the payload shape inside a ContextFrag.
type PartType string

const (
	PartText       PartType = "text"
	PartSDKMessage PartType = "sdk_message"
	PartImage      PartType = "image"
)

// ImageRef records image metadata without embedding the image payload in the
// manifest. The actual SDK image part is retained in SDKImage for rendering.
type ImageRef struct {
	MediaType string `json:"media_type,omitempty"`
	Source    string `json:"source,omitempty"`
}

// Part is one payload item in a context fragment.
type Part struct {
	Type       PartType       `json:"type"`
	Text       string         `json:"text,omitempty"`
	Image      ImageRef       `json:"image,omitempty"`
	Message    *sdk.Message   `json:"message,omitempty"`
	ImagePart  *sdk.ImagePart `json:"image_part,omitempty"`
	SDKMessage *sdk.Message   `json:"-"`
	SDKImage   *sdk.ImagePart `json:"-"`
}

// ContextFrag is the typed context fragment abstraction.
type ContextFrag struct {
	ID                 string          `json:"id"`
	Ref                ContextRef      `json:"ref,omitempty"`
	Kind               Kind            `json:"kind"`
	Role               sdk.MessageRole `json:"role,omitempty"`
	Slot               Slot            `json:"slot"`
	Priority           int             `json:"priority,omitempty"`
	RetentionTier      RetentionTier   `json:"retention_tier,omitempty"`
	DropPriority       DropPriority    `json:"drop_priority,omitempty"`
	RequiredCapability string          `json:"required_capability,omitempty"`
	CacheClass         CacheClass      `json:"cache_class,omitempty"`
	Trust              TrustLevel      `json:"trust,omitempty"`
	Scope              Scope           `json:"scope,omitempty"`
	Budget             BudgetPolicy    `json:"budget,omitempty"`
	Render             RenderPolicy    `json:"render,omitempty"`
	Provenance         Provenance      `json:"provenance,omitempty"`
	TokenEstimate      int             `json:"token_estimate,omitempty"`
	// ConflictKey groups fragments that are alternatives of one another: the
	// selector keeps only the highest-precedence member (closest scope, then
	// trust, then latest collected) and drops the rest.
	ConflictKey string           `json:"conflict_key,omitempty"`
	Coverage    *SummaryCoverage `json:"coverage,omitempty"`
	Parts       []Part           `json:"parts,omitempty"`
}

// AssembledContext is the compiled view produced from fragments.
type AssembledContext struct {
	Frags        []ContextFrag   `json:"frags,omitempty"`
	System       string          `json:"system,omitempty"`
	Messages     []sdk.Message   `json:"-"`
	Query        string          `json:"query,omitempty"`
	InlineImages []sdk.ImagePart `json:"-"`
	Manifest     Manifest        `json:"manifest"`
}

// Manifest is a content-light accounting view for debugging and review.
type Manifest struct {
	SchemaVersions     []SchemaVersion     `json:"schema_versions,omitempty"`
	View               ManifestView        `json:"view,omitempty"`
	DynamicMutators    []DynamicMutator    `json:"dynamic_mutators,omitempty"`
	SlotPolicies       []SlotRenderPolicy  `json:"slot_policies,omitempty"`
	RenderedOutputs    []RenderedOutputRef `json:"rendered_outputs,omitempty"`
	EditTrace          []ContextEditTrace  `json:"edit_trace,omitempty"`
	CoverageTrace      []SummaryCoverage   `json:"coverage_trace,omitempty"`
	ContinuityGroups   []ContinuityGroup   `json:"continuity_groups,omitempty"`
	ValidationWarnings []ValidationWarning `json:"validation_warnings,omitempty"`
	Counts             ManifestCounts      `json:"counts"`
	Breakdown          []KindBreakdown     `json:"breakdown,omitempty"`
	TrustBreakdown     []TrustBreakdown    `json:"trust_breakdown,omitempty"`
	ToolDefs           []ToolDefAccounting `json:"tool_defs,omitempty"`
	Items              []ManifestItem      `json:"items,omitempty"`
	SelectionDecisions []SelectionDecision `json:"selection_decisions,omitempty"`
	Selection          *SelectionTrace     `json:"selection,omitempty"`
	BudgetPlan         *ContextBudgetPlan  `json:"budget_plan,omitempty"`
	CachePlan          *CachePlan          `json:"cache_plan,omitempty"`
	Mutations          *MutationLedger     `json:"mutations,omitempty"`
}

// ManifestView names the exact view represented by a manifest.
type ManifestView string

const (
	ViewRunConfigPreProvider ManifestView = "run_config_pre_provider"
	ViewExternalAgentPrompt  ManifestView = "external_agent_prompt"
)

// DynamicMutator names a later runtime transform that can change provider params
// after the RunConfig-level context frag view has been compiled.
type DynamicMutator string

const (
	DynamicMutatorPromptCache         DynamicMutator = "prompt_cache"
	DynamicMutatorInjectCh            DynamicMutator = "inject_ch"
	DynamicMutatorReadMedia           DynamicMutator = "read_media"
	DynamicMutatorBeforeModelCallHook DynamicMutator = "before_model_call_hook"
	DynamicMutatorBackgroundSummary   DynamicMutator = "background_summary"
)

// ManifestCounts summarizes fragment composition.
type ManifestCounts struct {
	Fragments     int `json:"fragments"`
	Messages      int `json:"messages"`
	Images        int `json:"images"`
	TextBytes     int `json:"text_bytes"`
	TokenEstimate int `json:"token_estimate"`
}

// KindBreakdown aggregates manifest items of one Kind so consumers can show
// where the context window went without walking every item.
type KindBreakdown struct {
	Kind          Kind `json:"kind"`
	Fragments     int  `json:"fragments"`
	TokenEstimate int  `json:"token_estimate"`
	TextBytes     int  `json:"text_bytes,omitempty"`
	Images        int  `json:"images,omitempty"`
}

// TrustBreakdown aggregates manifest items of one TrustLevel so consumers
// can measure how much of the context window is attacker-influenceable
// (external) versus Memoh-controlled, per turn.
type TrustBreakdown struct {
	Trust         TrustLevel `json:"trust"`
	Fragments     int        `json:"fragments"`
	TokenEstimate int        `json:"token_estimate"`
	TextBytes     int        `json:"text_bytes,omitempty"`
	Images        int        `json:"images,omitempty"`
}

// ToolDefAccounting records the serialized size of one tool definition sent
// to the provider. Tool schemas never render as fragments, so without this
// entry the manifest understates the real prompt by the whole tool roster.
type ToolDefAccounting struct {
	Provider      string `json:"provider"`
	Name          string `json:"name"`
	Bytes         int    `json:"bytes"`
	TokenEstimate int    `json:"token_estimate"`
}

// ContextBudgetPlan records the numeric input-envelope allocation used for one
// provider-bound turn. Raw prompt content never enters this accounting view.
type ContextBudgetPlan struct {
	Estimator                    string `json:"estimator"`
	EstimatorSafetyFactorPercent int    `json:"estimator_safety_factor_percent"`
	Window                       int    `json:"window"`
	OutputReserve                int    `json:"output_reserve"`
	OutputReserveResolution      string `json:"output_reserve_resolution,omitempty"`
	ToolDefsCost                 int    `json:"tool_defs_cost"`
	CurrentRequestCost           int    `json:"current_request_cost"`
	SystemBudget                 int    `json:"system_budget"`
	ActualSystemCost             int    `json:"actual_system_cost"`
	HistoryBudget                int    `json:"history_budget"`
}

type SelectionTrace struct {
	Selected    int            `json:"selected"`
	Dropped     int            `json:"dropped"`
	DropReasons map[string]int `json:"drop_reasons,omitempty"`
}

type SelectionDecisionKind string

const (
	DecisionSelected SelectionDecisionKind = "selected"
	DecisionTrimmed  SelectionDecisionKind = "trimmed"
	DecisionDropped  SelectionDecisionKind = "dropped"
)

// SelectionDecision is the content-light per-fragment audit trail for
// selection. It identifies sources and costs without retaining prompt text.
type SelectionDecision struct {
	ID            string                `json:"id"`
	Ref           ContextRef            `json:"ref,omitempty"`
	Slot          Slot                  `json:"slot"`
	Source        string                `json:"source,omitempty"`
	SourceID      string                `json:"source_id,omitempty"`
	Decision      SelectionDecisionKind `json:"decision"`
	Reason        string                `json:"reason,omitempty"`
	TokenEstimate int                   `json:"token_estimate,omitempty"`
	TextBytes     int                   `json:"text_bytes,omitempty"`
	ImageCount    int                   `json:"image_count,omitempty"`
	CacheClass    CacheClass            `json:"cache_class,omitempty"`
	RetentionTier RetentionTier         `json:"retention_tier,omitempty"`
}

// ManifestItem is one non-sensitive fragment entry.
type ManifestItem struct {
	ID                 string          `json:"id"`
	Ref                ContextRef      `json:"ref,omitempty"`
	Kind               Kind            `json:"kind"`
	Slot               Slot            `json:"slot"`
	Role               sdk.MessageRole `json:"role,omitempty"`
	Priority           int             `json:"priority,omitempty"`
	RetentionTier      RetentionTier   `json:"retention_tier,omitempty"`
	DropPriority       DropPriority    `json:"drop_priority,omitempty"`
	RequiredCapability string          `json:"required_capability,omitempty"`
	CacheClass         CacheClass      `json:"cache_class,omitempty"`
	Trust              TrustLevel      `json:"trust,omitempty"`
	Source             string          `json:"source,omitempty"`
	SourceID           string          `json:"source_id,omitempty"`
	Collector          string          `json:"collector,omitempty"`
	ConflictKey        string          `json:"conflict_key,omitempty"`
	Render             RenderPolicy    `json:"render,omitempty"`
	PartTypes          []PartType      `json:"part_types,omitempty"`
	TextBytes          int             `json:"text_bytes,omitempty"`
	ImageCount         int             `json:"image_count,omitempty"`
	TokenEstimate      int             `json:"token_estimate,omitempty"`
	Scope              Scope           `json:"scope,omitempty"`
}

type SlotRenderPolicy struct {
	Slot          Slot   `json:"slot"`
	Order         string `json:"order,omitempty"`
	DedupeBy      string `json:"dedupe_by,omitempty"`
	CoverageAware bool   `json:"coverage_aware,omitempty"`
	Target        string `json:"target"`
}

type RenderedOutputRef struct {
	Target string       `json:"target"`
	Slot   Slot         `json:"slot,omitempty"`
	Refs   []ContextRef `json:"refs,omitempty"`
}

type ContextEditOp string

const (
	EditAppend   ContextEditOp = "append"
	EditReplace  ContextEditOp = "replace"
	EditRemove   ContextEditOp = "remove"
	EditCover    ContextEditOp = "cover"
	EditAnnotate ContextEditOp = "annotate"
)

type ContextEdit struct {
	EditID        string            `json:"edit_id"`
	Slot          Slot              `json:"slot"`
	Op            ContextEditOp     `json:"op"`
	Refs          []ContextRef      `json:"refs,omitempty"`
	Payload       []ContextFrag     `json:"payload,omitempty"`
	Preconditions EditPreconditions `json:"preconditions,omitempty"`
	Schema        SchemaVersion     `json:"schema"`
}

type EditPreconditions struct {
	ExpectedRevision string            `json:"expected_revision,omitempty"`
	MaxSequence      int64             `json:"max_sequence,omitempty"`
	ExpectedHashes   map[string]string `json:"expected_hashes,omitempty"`
}

type ContextEditTrace struct {
	EditID string        `json:"edit_id,omitempty"`
	Op     ContextEditOp `json:"op,omitempty"`
	Slot   Slot          `json:"slot,omitempty"`
	Refs   []ContextRef  `json:"refs,omitempty"`
}

type SummaryCoverage struct {
	CoverageID  string       `json:"coverage_id"`
	SummaryRef  ContextRef   `json:"summary_ref"`
	CoveredRefs []ContextRef `json:"covered_refs,omitempty"`
	// TraceFragIDs is debug-only and must not be used as durable coverage identity.
	TraceFragIDs []string      `json:"trace_frag_ids,omitempty"`
	Schema       SchemaVersion `json:"schema"`
}

type ContinuityGroup struct {
	ID               string       `json:"id"`
	Kind             string       `json:"kind"`
	Provider         string       `json:"provider,omitempty"`
	ModelFamily      string       `json:"model_family,omitempty"`
	Refs             []ContextRef `json:"refs,omitempty"`
	MustKeepTogether bool         `json:"must_keep_together,omitempty"`
	MustKeepRaw      bool         `json:"must_keep_raw,omitempty"`
	MustKeepOrder    bool         `json:"must_keep_order,omitempty"`
	MustBeComplete   bool         `json:"must_be_complete,omitempty"`
}

type ValidationWarning struct {
	Code    string     `json:"code"`
	Message string     `json:"message,omitempty"`
	Ref     ContextRef `json:"ref,omitempty"`
}

type ConflictKind string

const (
	ConflictMissingRef          ConflictKind = "missing_ref"
	ConflictContentHashMismatch ConflictKind = "content_hash_mismatch"
	ConflictInvalidSchema       ConflictKind = "invalid_schema"
)

type ContextConflict struct {
	Kind     ConflictKind `json:"kind"`
	Key      string       `json:"key,omitempty"`
	Expected string       `json:"expected,omitempty"`
	Actual   string       `json:"actual,omitempty"`
}

// ToolExchangePolicy asks the selector to strip bulky tool interactions from
// history: tool results and non-conversational tool calls are removed while
// ask_user exchanges survive because the question and answer are part of the
// visible conversation. MinMessages gates the policy: it applies only when
// the history holds more message fragments than the threshold (zero applies
// it unconditionally).
type ToolExchangePolicy struct {
	MinMessages int
}

const defaultToolExchangeMinMessages = 10

// DefaultToolExchangePolicy returns the package's shared default
// tool-exchange stripping policy. Returns a fresh pointer on every call so
// callers never share (and risk mutating) the same underlying struct.
func DefaultToolExchangePolicy() *ToolExchangePolicy {
	return &ToolExchangePolicy{MinMessages: defaultToolExchangeMinMessages}
}
