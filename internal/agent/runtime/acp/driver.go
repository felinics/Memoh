// Driver adapts the ACP session pool to the external runtime port, so ACP
// sits at the same level as the other out-of-process external agent runtimes
// (codex, claude-code) and the application keeps one turn orchestration for
// all of them. The pool keeps owning everything ACP-specific — warm process
// handles, runtime storage, checkpoint capture, fencing — while this adapter
// owns only the contract translation.
package acp

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"

	agentfeedback "github.com/felinics/memoh/internal/agent/decision/feedback"
	"github.com/felinics/memoh/internal/agent/runtime/acp/client"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/runtimekind"
)

// RuntimeType is the thread runtime type this driver serves.
const RuntimeType = string(runtimekind.ACPAgent)

// contextURI names the Memoh context document resource embedded in ACP
// prompts.
const contextURI = "memoh://context/current-turn"

// metadataAgentIDKey is the runtime-metadata key naming the bound ACP agent.
const metadataAgentIDKey = "acp_agent_id"

// Prompter is the minimal pool surface the driver adapts. *SessionPool
// satisfies it; pools that also implement CloseSession(string) error get the
// round-rollback repair.
type Prompter interface {
	Prompt(ctx context.Context, input PromptInput) (client.PromptResult, error)
}

// Driver implements external.Driver over the session pool.
type Driver struct {
	pool Prompter
}

// NewDriver wraps the pool in the external runtime port.
func NewDriver(pool Prompter) *Driver {
	return &Driver{pool: pool}
}

// RuntimeType implements external.Driver.
func (*Driver) RuntimeType() string { return RuntimeType }

// OnRoundRolledBack implements external.RoundRollbackHandler: a definite
// round rollback leaves the warm process ahead of canonical history, so the
// runtime is discarded and the next prompt restarts from the durable head.
// This is safe exactly here: the failed run still holds the session's single
// active slot, so no newer turn can own the session yet.
func (d *Driver) OnRoundRolledBack(_ context.Context, _, threadID string) {
	if closer, ok := d.pool.(interface{ CloseSession(string) error }); ok {
		_ = closer.CloseSession(threadID)
	}
}

// Prompt implements external.Driver: one pooled ACP turn.
func (d *Driver) Prompt(ctx context.Context, input external.PromptInput) (external.PromptResult, error) {
	agentID := driverMetadataString(input.RuntimeMetadata, metadataAgentIDKey)
	images := make([]client.PromptImage, 0, len(input.Images))
	for _, img := range input.Images {
		images = append(images, client.PromptImage{
			MimeType: img.MimeType,
			Data:     base64.StdEncoding.EncodeToString(img.Data),
		})
	}
	sink := client.EventSinkFunc(input.Sink.EmitStreamEvent)
	result, err := d.pool.Prompt(ctx, PromptInput{
		BotID:                    input.BotID,
		ChatID:                   input.ChatID,
		SessionID:                input.ThreadID,
		RunID:                    input.RunID,
		SessionType:              input.SessionMode,
		RouteID:                  input.RouteID,
		AgentID:                  agentID,
		ProjectPath:              driverMetadataString(input.RuntimeMetadata, "project_path"),
		ModelID:                  input.ModelID,
		ReasoningEffort:          input.ReasoningEffort,
		Prompt:                   input.Prompt,
		Images:                   images,
		AttachmentReferences:     input.AttachmentReferences,
		CanFallbackImagesToFiles: input.CanFallbackImagesToFiles,
		ChannelIdentityID:        input.ChannelIdentityID,
		SessionToken:             input.SessionToken,
		CurrentPlatform:          input.CurrentPlatform,
		ReplyTarget:              input.ReplyTarget,
		ConversationType:         input.ConversationType,
		CanRequestUserInput:      input.CanRequestUserInput,
		// Initial user images travel as ACP image blocks above; this flag only
		// governs image bytes returned later by the read-media MCP tool.
		SupportsImageInput:        false,
		ToolOutputLimit:           input.ToolOutputLimit,
		ToolHTTPURL:               input.ToolHTTPURL,
		ContextURI:                firstNonEmpty(input.ContextURI, contextURI),
		ContextMarkdown:           input.ContextMarkdown,
		ContextBudgetMaxTokens:    input.ContextBudgetMaxTokens,
		ContextToolExchangePolicy: input.ContextToolExchangePolicy,
		RuntimeOwnerAccountID:     input.RuntimeOwnerAccountID,
		ForceFreshRuntime:         input.ForceFreshRuntime,
		RequiredCommand:           input.Command,
		Sink:                      sink,
	})
	out := DriverPromptResult(result, agentID)
	if err != nil {
		if ctx.Err() != nil {
			// The port's interrupt contract: a canceled turn returns its
			// partial transcript without an error.
			return out, nil
		}
		return out, normalizePromptError(err)
	}
	out.TurnCompleted = true
	return out, nil
}

// DriverPromptResult maps a pool result onto the port result: transcript
// fallback from events, checkpoint outcome, and round provenance. Exported
// so tests can hold the mapping fixed while exercising the unified flow.
func DriverPromptResult(result client.PromptResult, agentID string) external.PromptResult {
	if len(result.Output) == 0 {
		result.Output = external.TranscriptFromEvents(result.Events, result.Text)
	}
	out := external.PromptResult{
		Output:     result.Output,
		Text:       result.Text,
		Usage:      result.Usage,
		StopReason: result.StopReason,
		// Every ACP turn participates in publication with a reset head: no
		// runtime snapshots are captured, but pool fencing still compares warm
		// handles against the canonical head watermark.
		Checkpoint: external.CheckpointDeclined,
	}
	if agentID != "" {
		out.RoundMetadata = map[string]any{metadataAgentIDKey: agentID}
	}
	return out
}

// normalizePromptError translates pool errors into the port's stable error
// shapes: user-facing feedback for input-class failures, apperror codes for
// configuration-class failures, and the raw error otherwise.
func normalizePromptError(err error) error {
	switch {
	case errors.Is(err, ErrAgentCommandUnavailable):
		// The runtime that admission matched was replaced (or updated its
		// command set) before the prompt; the turn fails closed exactly like
		// admission would have.
		return agentfeedback.New(
			agentfeedback.CodeAgentCommandStale,
			"agent_command_stale",
			http.StatusConflict,
			"chat.externalAgent.agentCommandStale",
			"The agent no longer offers this command. Reopen the command picker and try again.",
			nil,
		)
	case errors.Is(err, client.ErrImagePromptUnsupported):
		return agentfeedback.New(
			agentfeedback.CodeImageInputUnsupported,
			"image_input_unsupported",
			http.StatusBadRequest,
			"chat.externalAgent.imageInputUnsupported",
			"This external agent cannot read the attached image.",
			nil,
		)
	case errors.Is(err, client.ErrInvalidPromptImage):
		return agentfeedback.New(
			agentfeedback.CodeAttachmentInvalid,
			"invalid_image_data",
			http.StatusBadRequest,
			"chat.externalAgent.attachmentInvalid",
			"The attachment is invalid. Please attach it again.",
			nil,
		)
	case errors.Is(err, client.ErrModelSelectionUnsupported):
		return apperror.New(apperror.CodeACPModelSelectionUnsupported, nil)
	case errors.Is(err, client.ErrModelIDRequired):
		return apperror.New(apperror.CodeACPModelIDRequired, nil)
	case errors.Is(err, client.ErrModelUnavailable):
		return apperror.New(apperror.CodeACPModelUnavailable, nil)
	case errors.Is(err, client.ErrReasoningSelectionUnsupported):
		return apperror.New(apperror.CodeACPReasoningUnsupported, nil)
	case errors.Is(err, client.ErrReasoningEffortRequired):
		return apperror.New(apperror.CodeACPReasoningEffortRequired, nil)
	case errors.Is(err, client.ErrReasoningEffortUnavailable):
		return apperror.New(apperror.CodeACPReasoningUnavailable, nil)
	case errors.Is(err, ErrRuntimeConfigUpdateFailed):
		return apperror.Wrap(apperror.CodeACPConfigUpdateFailed, err, nil)
	default:
		return err
	}
}

func driverMetadataString(metadata map[string]any, key string) string {
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}
