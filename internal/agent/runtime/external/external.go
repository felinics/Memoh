// Package external defines the neutral port between the agent application
// layer and out-of-process runtimes: Codex, Claude Code, and ACP agents.
//
// A Driver owns one runtime type end to end: process lifecycle inside the bot
// workspace, the native wire protocol, and the translation of runtime events
// into the shared event.StreamEvent vocabulary. The application layer stays
// runtime-agnostic: it resolves a Driver from the session's runtime type,
// hands it a PromptInput, and persists the PromptResult — the same shape for
// every driver. New runtimes must implement Driver instead of growing new
// special cases in the application layer.
package external

import (
	"context"
	"errors"
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	contextlimit "github.com/felinics/memoh/internal/agent/context/limit"
	"github.com/felinics/memoh/internal/agent/event"
)

// Driver runs turns for one external agent runtime type.
type Driver interface {
	// RuntimeType is the thread runtime type this driver serves (e.g. "codex").
	RuntimeType() string
	// Prompt runs one turn. Stream events flow through input.Sink while the
	// turn runs; the returned result carries the transcript for persistence.
	// A context cancellation is an interrupt: the driver must stop the turn
	// and still return the partial transcript it has.
	Prompt(ctx context.Context, input PromptInput) (PromptResult, error)
}

type BotResetter interface {
	ResetBot(botID string)
}

type BotAgentResetter interface {
	ResetBotAgent(botID, botAgentID string)
}

type BotAgentAuthPurger interface {
	PurgeBotAgentAuth(ctx context.Context, botID, botAgentID string) error
}

type ModelCatalogProvider interface {
	ModelCatalog(ctx context.Context, botID, botAgentID string) (ModelCatalog, error)
}

var ErrModelCatalogUnavailable = errors.New("external agent model catalog unavailable")

type Drivers []Driver

func (drivers Drivers) ResetBot(botID string) {
	for _, driver := range drivers {
		if resetter, ok := driver.(BotResetter); ok {
			resetter.ResetBot(botID)
		}
	}
}

func (drivers Drivers) ResetBotAgent(runtimeType, botID, botAgentID string) {
	for _, driver := range drivers {
		if driver == nil || driver.RuntimeType() != strings.TrimSpace(runtimeType) {
			continue
		}
		if resetter, ok := driver.(BotAgentResetter); ok {
			resetter.ResetBotAgent(botID, botAgentID)
		}
		return
	}
}

func (drivers Drivers) PurgeBotAgentAuth(ctx context.Context, runtimeType, botID, botAgentID string) error {
	for _, driver := range drivers {
		if driver == nil || driver.RuntimeType() != strings.TrimSpace(runtimeType) {
			continue
		}
		if purger, ok := driver.(BotAgentAuthPurger); ok {
			return purger.PurgeBotAgentAuth(ctx, botID, botAgentID)
		}
		return nil
	}
	return nil
}

func (drivers Drivers) ModelCatalog(ctx context.Context, runtimeType, botID, botAgentID string) (ModelCatalog, error) {
	runtimeType = strings.TrimSpace(runtimeType)
	for _, driver := range drivers {
		if driver == nil || driver.RuntimeType() != runtimeType {
			continue
		}
		provider, ok := driver.(ModelCatalogProvider)
		if !ok {
			return ModelCatalog{}, ErrModelCatalogUnavailable
		}
		return provider.ModelCatalog(ctx, botID, botAgentID)
	}
	return ModelCatalog{}, ErrModelCatalogUnavailable
}

// CheckpointOutcome reports what a driver did about its native session
// checkpoint during the turn. Drivers that checkpoint stage the snapshot
// themselves at turn end (the run is still active and the persistence fence
// still holds); the application then publishes the matching head in the same
// transaction as the round's messages.
type CheckpointOutcome int

const (
	// CheckpointNone: the runtime does not checkpoint (or has no store); no
	// publication head is written for the round.
	CheckpointNone CheckpointOutcome = iota
	// CheckpointStaged: the turn's native state is durably staged under the
	// run; the round publishes a resumable checkpoint head.
	CheckpointStaged
	// CheckpointDeclined: the runtime checkpoints but this turn could not
	// stage (nothing to snapshot, or the capture diverged); the round
	// publishes an explicit reset head.
	CheckpointDeclined
)

// RoundRollbackHandler is implemented by drivers that must repair runtime
// state when a completed turn's round definitively rolled back (the runtime
// remembers a turn the visible history lost). Drivers whose durable state is
// authoritative on their own side simply omit it and the divergence is
// logged.
type RoundRollbackHandler interface {
	OnRoundRolledBack(ctx context.Context, botID, threadID string)
}

// ThreadForker is implemented by runtimes with native conversation forking.
// ForkThread derives a new runtime-side conversation from the session
// described by runtimeMetadata, cut after lastTurnID when non-empty (empty
// forks at the runtime head). It returns the runtime-metadata delta that
// identifies the fork — only the driver-owned session keys — which the
// caller overlays on the source session's runtime metadata.
type ThreadForker interface {
	ForkThread(ctx context.Context, botID, botAgentID string, runtimeMetadata map[string]any, lastTurnID string) (map[string]any, error)
}

// ModelCatalog is the runtime-owned model picker contract.
type ModelCatalog struct {
	Models                    []ModelOption `json:"models"`
	ConfiguredModelID         string        `json:"configured_model_id,omitempty"`
	ConfiguredReasoningEffort string        `json:"configured_reasoning_effort,omitempty"`
}

// ModelOption is one runtime model and its supported reasoning options.
type ModelOption struct {
	ID                     string                  `json:"id"`
	Name                   string                  `json:"name"`
	Description            string                  `json:"description,omitempty"`
	Default                bool                    `json:"default,omitempty"`
	DefaultReasoningEffort string                  `json:"default_reasoning_effort,omitempty"`
	ReasoningEfforts       []ReasoningEffortOption `json:"reasoning_efforts"`
}

// ReasoningEffortOption is one runtime-defined reasoning level.
type ReasoningEffortOption struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// PromptInput is one turn's worth of work for a Driver.
type PromptInput struct {
	BotID      string
	BotAgentID string
	ChatID     string
	ThreadID   string
	RunID      string
	RouteID    string

	// Prompt is the user's message text.
	Prompt string
	// ContextMarkdown is the assembled Memoh context document for this turn.
	ContextMarkdown string
	// Images are inline user images, raw bytes with MIME types.
	Images []Image

	// ModelID and ReasoningEffort are per-turn overrides; empty means the
	// runtime's configured default.
	ModelID         string
	ReasoningEffort string

	// SessionMode is the Memoh session mode driving this turn (chat,
	// schedule, ...); tool-gateway policy keys on it.
	SessionMode string

	// CurrentPlatform, ReplyTarget, and ConversationType describe the surface
	// the turn is answering; they flow into the trusted tool identity.
	CurrentPlatform  string
	ReplyTarget      string
	ConversationType string

	// Command is an exact agent-command selector matched at admission; a
	// runtime that advertises commands must re-validate it at dispatch.
	// Runtimes without a command vocabulary ignore it.
	Command string

	// ForceFreshRuntime asks the driver to abandon any resumable runtime
	// session and start this turn on a fresh one.
	ForceFreshRuntime bool

	// AttachmentReferences names non-image attachments already staged for the
	// runtime (paths or references it may read through its tools).
	AttachmentReferences []string
	// CanFallbackImagesToFiles permits a runtime without image input to
	// receive the images as workspace files instead of failing the turn.
	CanFallbackImagesToFiles bool
	// ToolOutputLimit bounds tool output the runtime feeds back into its
	// context through Memoh-owned tool surfaces.
	ToolOutputLimit contextlimit.ToolOutputLimit

	// RuntimeMetadata is the session's runtime metadata map. Drivers read and
	// persist their own keys through it (e.g. the codex thread id).
	RuntimeMetadata map[string]any

	// ContextURI names the Memoh-owned context document. The budget and tool
	// exchange policy flow into runtime-hosted Memoh tools.
	ContextURI                string
	ContextBudgetMaxTokens    int
	ContextToolExchangePolicy *contextfrag.ToolExchangePolicy

	// RuntimeOwnerAccountID is the account whose workspace authority the
	// runtime executes under.
	RuntimeOwnerAccountID string
	// ChannelIdentityID identifies the acting user for tool attribution.
	ChannelIdentityID string
	// SessionToken authenticates runtime-side calls back into Memoh.
	SessionToken string //nolint:gosec // session credential material, not a hardcoded secret
	// ToolHTTPURL is the Memoh tool gateway (HTTP MCP) base URL, empty when
	// the gateway is unavailable.
	ToolHTTPURL string

	// CanRequestUserInput reports whether the surface driving this turn can
	// deliver interactive decisions such as approvals and ask_user.
	CanRequestUserInput bool

	// Sink receives stream events as the turn runs. Never nil.
	Sink EventSink
}

// Image is an inline prompt image.
type Image struct {
	MimeType string
	// Data is the raw image bytes (not base64, not a data URL).
	Data []byte
}

// EventSink receives stream events during a turn.
type EventSink interface {
	EmitStreamEvent(event.StreamEvent)
}

// EventSinkFunc adapts a function to EventSink.
type EventSinkFunc func(event.StreamEvent)

func (f EventSinkFunc) EmitStreamEvent(ev event.StreamEvent) { f(ev) }

// PromptResult is the durable outcome of one turn.
type PromptResult struct {
	// Output is the transcript to persist, in provider-message form.
	Output []sdk.Message
	// Text is the final assistant message text (for surfaces that report a
	// plain-text result, e.g. schedule runs).
	Text string
	// Usage is the turn's token usage, when the runtime reported it.
	Usage *sdk.Usage
	// StopReason is the runtime's terminal stop reason, normalized to the
	// event vocabulary where possible.
	StopReason string
	// AgentTurnID is the runtime's own identifier for this turn (e.g. the
	// codex turn id); it anchors turn-level operations such as forking.
	AgentTurnID string
	// TurnCompleted reports whether the runtime finished the turn (as opposed
	// to an interrupt or failure part-way).
	TurnCompleted bool
	// Checkpoint reports the turn's native-state checkpoint outcome; the
	// application publishes the matching head with the round.
	Checkpoint CheckpointOutcome
	// RoundMetadata carries driver-owned keys merged into the round's
	// assistant-message metadata (provenance such as the ACP agent id).
	RoundMetadata map[string]any
	// RuntimeMetadata carries driver-owned metadata updates to merge back
	// into the session's runtime metadata (e.g. a newly created thread id).
	// Nil means no changes.
	RuntimeMetadata map[string]any
}
