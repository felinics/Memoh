package toolexec

import (
	"context"
	"errors"
	"fmt"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/jsonschema-go/jsonschema"
)

// ToolExecuteFunc is the signature for a tool's execution handler.
// input is the parsed arguments from the LLM. The return value becomes the
// tool result output sent back to the model.
type ToolExecuteFunc func(ctx *ToolExecContext, input sdk.ToolArguments) (sdk.ToolOutput, error)

// ToolExecContext is passed to ToolExecuteFunc and carries the parent context,
// call metadata, and a mechanism for streaming progress updates.
type ToolExecContext struct {
	context.Context
	ToolCallID   string
	ToolName     string
	SendProgress func(content sdk.ToolOutput) // nil when not in streaming mode
}

type ToolApprovalDecision string

const (
	ToolApprovalDecisionApproved ToolApprovalDecision = "approved"
	ToolApprovalDecisionRejected ToolApprovalDecision = "rejected"
	ToolApprovalDecisionDeferred ToolApprovalDecision = "deferred"
)

type ToolApprovalResult struct {
	Decision   ToolApprovalDecision `json:"decision"`
	ApprovalID string               `json:"approvalId,omitempty"`
	Reason     string               `json:"reason,omitempty"`
	Metadata   map[string]any       `json:"metadata,omitempty"`
	// Input, when set, replaces the call's arguments for execution and in
	// the step record. Memoh's approval handler resolves the workspace
	// target the policy was evaluated against and pins it here, so the tool
	// runs where the policy looked and the persisted call names that target.
	// (Memoh addition; the SDK executor had no such field.)
	Input *sdk.ToolArguments `json:"input,omitempty"`
}

var ErrToolApprovalDeferred = errors.New("tool approval deferred")

type ToolApprovalDeferredError struct {
	Approval ToolApprovalResult
}

func (e *ToolApprovalDeferredError) Error() string {
	if e == nil {
		return ErrToolApprovalDeferred.Error()
	}
	if e.Approval.ApprovalID == "" {
		return ErrToolApprovalDeferred.Error()
	}
	return fmt.Sprintf("%s: %s", ErrToolApprovalDeferred, e.Approval.ApprovalID)
}

func (*ToolApprovalDeferredError) Is(target error) bool {
	return target == ErrToolApprovalDeferred
}

type Tool struct {
	Name            string             `json:"name"`
	Description     string             `json:"description,omitempty"`
	Parameters      *jsonschema.Schema `json:"parameters"`
	Execute         ToolExecuteFunc    `json:"-"`
	RequireApproval bool               `json:"-"`
	// CacheControl enables prompt caching for this tool's definition.
	// Only supported by Anthropic; other providers ignore this field.
	CacheControl *sdk.CacheControl `json:"-"`
}
