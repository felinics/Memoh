package acp

// Driver adapts the ACP session pool to the external runtime port, so ACP
// sits at the same level as the other external agent runtimes and the
// application keeps one turn orchestration for all of them. The pool owns
// everything ACP-specific; this adapter owns only the contract translation.

import (
	"context"
	"encoding/base64"
	"errors"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/external/acp/client"
	"github.com/felinics/memoh/internal/runtimekind"
)

// RuntimeType is the thread runtime type this driver serves.
const RuntimeType = string(runtimekind.ACPAgent)

// contextURI names the Memoh context document resource embedded in ACP
// prompts.
const contextURI = "memoh://context/current-turn"

// Pool is the session pool surface the driver adapts. *SessionPool
// satisfies it.
type Pool interface {
	Prompt(ctx context.Context, input PromptInput) (client.PromptResult, error)
	Commands(context.Context, PromptInput) ([]client.AvailableCommandInfo, error)
	RuntimeStatus(input PromptInput) RuntimeStatus
	SetMode(context.Context, PromptInput, string) (RuntimeStatus, error)
}

// Driver implements external.Driver over the session pool.
type Driver struct {
	pool Pool
}

// NewDriver wraps the pool in the external runtime port.
func NewDriver(pool Pool) *Driver {
	return &Driver{pool: pool}
}

// RuntimeType implements external.Driver.
func (*Driver) RuntimeType() string { return RuntimeType }

// Caps implements external.Driver. A custom agent's command comes from its
// setup, and its modes and commands from the agent itself.
func (*Driver) Caps() external.Caps { return external.Caps{} }

// Prompt implements external.Driver: one pooled ACP turn.
func (d *Driver) Prompt(ctx context.Context, input external.PromptInput) (external.PromptResult, error) {
	result, err := d.pool.Prompt(ctx, TurnInput(input))
	out := DriverPromptResult(result)
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
		return out, promptFailure(err)
	}
	out.TurnCompleted = TurnCompleted(result)
	return out, nil
}

// TurnCompleted reports whether a prompt that returned finished its turn.
// An agent also ends a turn as cancelled on its own, for one when a
// permission it asked for is refused: that turn stopped, it did not finish.
func TurnCompleted(result client.PromptResult) bool {
	return result.StopReason != string(acpsdk.StopReasonCancelled)
}

// TurnInput is the ACP turn of an external runtime turn.
func TurnInput(input external.PromptInput) PromptInput {
	images := make([]client.PromptImage, 0, len(input.Images))
	for _, img := range input.Images {
		images = append(images, client.PromptImage{
			MimeType: img.MimeType,
			Data:     base64.StdEncoding.EncodeToString(img.Data),
		})
	}
	var sink client.EventSink
	if input.Sink != nil {
		sink = client.EventSinkFunc(input.Sink.EmitStreamEvent)
	}
	return PromptInput{
		BotID:                    input.BotID,
		BotAgentID:               input.BotAgentID,
		ChatID:                   input.ChatID,
		SessionID:                input.ThreadID,
		RunID:                    input.RunID,
		SessionType:              input.SessionMode,
		RouteID:                  input.RouteID,
		ProjectPath:              external.MetadataString(input.RuntimeMetadata, "project_path"),
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
		ContextURI:             external.FirstNonEmpty(input.ContextURI, contextURI),
		ContextMarkdown:        input.ContextMarkdown,
		ContextBudgetMaxTokens: input.ContextBudgetMaxTokens,
		RuntimeOwnerAccountID:  input.RuntimeOwnerAccountID,
		ForceFreshRuntime:      input.ForceFreshRuntime,
		RequiredCommand:        input.Command,
		PermissionMode:         external.MetadataString(input.RuntimeMetadata, "permission_mode"),
		RuntimeMetadata:        input.RuntimeMetadata,
		Sink:                   sink,
	}
}

// DriverPromptResult maps a pool result onto the port result. Exported so
// tests can hold the mapping fixed while exercising the unified flow.
func DriverPromptResult(result client.PromptResult) external.PromptResult {
	return external.PromptResult{
		Output:     result.Output,
		Text:       result.Text,
		Usage:      result.Usage,
		StopReason: result.StopReason,
	}
}

// promptFailure shapes a failed ACP turn. A model, reasoning effort or mode
// the agent does not offer is refused by Memoh's rule; what the agent said,
// and a launch command the workspace does not have, are shown as they were
// said. Every other error stays as it is: Memoh's own setup rules keep their
// codes, and a failure inside Memoh is never shown as the agent's.
func promptFailure(err error) error {
	if isPromptConfigSelectionError(err) {
		return external.Fail(external.FailureModeUnavailable, err)
	}
	var missing *client.CommandNotFoundError
	if errors.As(err, &missing) {
		return external.Upstream(err)
	}
	if _, text, ok := client.AgentError(err); ok {
		return external.Reported(external.FailureReported, text)
	}
	return err
}
