// Driver adapts the ACP session pool to the external runtime port, so ACP
// sits at the same level as the other out-of-process external agent runtimes
// (codex, claude-code) and the application keeps one turn orchestration for
// all of them. The pool keeps owning everything ACP-specific — warm process
// handles, runtime storage, native session restore — while this adapter
// owns only the contract translation.
package acp

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/external/acp/client"
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
// satisfies it.
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

// ResetBotAgent implements external.BotAgentResetter: an instance's warm
// processes were launched from its previous setup.
func (d *Driver) ResetBotAgent(botID, botAgentID string) {
	if strings.TrimSpace(botAgentID) == "" {
		return
	}
	if closer, ok := d.pool.(interface{ CloseBotAgentRuntimes(string, string) error }); ok {
		_ = closer.CloseBotAgentRuntimes(botID, botAgentID)
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
		BotAgentID:               input.BotAgentID,
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
		SupportsImageInput:     false,
		ToolOutputLimit:        input.ToolOutputLimit,
		ToolHTTPURL:            input.ToolHTTPURL,
		ContextURI:             firstNonEmpty(input.ContextURI, contextURI),
		ContextMarkdown:        input.ContextMarkdown,
		ContextBudgetMaxTokens: input.ContextBudgetMaxTokens,
		RuntimeOwnerAccountID:  input.RuntimeOwnerAccountID,
		ForceFreshRuntime:      input.ForceFreshRuntime,
		RequiredCommand:        input.Command,
		PermissionMode:         driverMetadataString(input.RuntimeMetadata, "permission_mode"),
		Sink:                   sink,
	})
	out := DriverPromptResult(result, agentID)
	// Discuss turns carry their whole context and never restore a session.
	if result.SessionID != "" && !input.ForceFreshRuntime {
		if stored, owned := storedNativeSession(input.RuntimeMetadata, input.ThreadID); !owned || stored != result.SessionID {
			out.RuntimeMetadata = nativeSessionMetadata(result.SessionID, input.ThreadID)
		}
	}
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

// DriverPromptResult maps a pool result onto the port result and adds round
// provenance. Exported so tests can hold the mapping fixed while exercising
// the unified flow.
func DriverPromptResult(result client.PromptResult, agentID string) external.PromptResult {
	out := external.PromptResult{
		Output:     result.Output,
		Text:       result.Text,
		Usage:      result.Usage,
		StopReason: result.StopReason,
	}
	if agentID != "" {
		out.RoundMetadata = map[string]any{metadataAgentIDKey: agentID}
	}
	return out
}

// PromptError is a pool failure that rejects the turn's configuration before
// the agent runs it: an unknown command, a model or reasoning choice the agent
// cannot apply, or a configuration update the agent refused. Err is the pool
// error, and the application chooses the public error from it. Like a public
// error, PromptError hides Err from errors.Is and errors.As; diagnostics reach
// it through Cause.
type PromptError struct {
	Err error
}

func (e *PromptError) Error() string { return e.Err.Error() }

// Cause returns the pool error.
func (e *PromptError) Cause() error { return e.Err }

// normalizePromptError marks the pool failures that reject the turn's
// configuration as a PromptError, and an error the agent answered with as a
// failure it reported in its own words. Any other error is returned
// unchanged: input-class failures (a stale command, an image the agent cannot
// read) stay package errors for the application to translate.
func normalizePromptError(err error) error {
	var commandNotFound *client.CommandNotFoundError
	switch {
	case errors.As(err, &commandNotFound),
		errors.Is(err, client.ErrModelSelectionUnsupported),
		errors.Is(err, client.ErrModelIDRequired),
		errors.Is(err, client.ErrModelUnavailable),
		errors.Is(err, client.ErrReasoningSelectionUnsupported),
		errors.Is(err, client.ErrReasoningEffortRequired),
		errors.Is(err, client.ErrReasoningEffortUnavailable),
		errors.Is(err, ErrRuntimeConfigUpdateFailed):
		return &PromptError{Err: err}
	}
	if _, text, ok := client.AgentError(err); ok {
		return external.Reported(external.FailureReported, text)
	}
	return err
}

func driverMetadataString(metadata map[string]any, key string) string {
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}
