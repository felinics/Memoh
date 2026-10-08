// Package step defines the record of one model step as the runtime keeps it.
//
// A step record separates two kinds of fact. Result is what the model call
// returned, held exactly as the provider seam produced it, so SDK additions to
// that value flow through without a copy step here. Everything beside it is
// what the runtime itself decided or observed: the tool results it executed for
// the step's calls, the output messages it assembled and committed, and a
// deferred approval that paused the step. That second half is what the SDK
// cannot know, and it is why a runtime owns this record rather than reading a
// multi-step result back out of the client layer.
//
// The record is the durable unit the application layer persists and the seed
// for the per-step events the agent core will project.
package step

import (
	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/toolexec"
)

// Record is the runtime's record of a single model step.
type Record struct {
	// Result is the outcome of the step's one model call, as returned by the
	// provider seam.
	Result sdk.ModelResult
	// ToolResults holds the results the loop executed for this step's
	// ToolCalls, joined to the originating calls by toolexec.ToolCallResults, so
	// each entry carries the call's Input beside the loop's Output. The SDK
	// cannot fill this in: it never runs tools. The result parts themselves
	// (with IsError and cache control) stay in Messages, which is this step's
	// lossless record.
	ToolResults []toolexec.ToolResult
	// Messages holds the messages this step produced (assistant + tool),
	// excluding any prior context from earlier steps.
	Messages []sdk.Message
	// Deferred is set when the step paused for tool approval instead of
	// completing; the tool calls stay unexecuted and the step carries no tool
	// messages.
	Deferred *toolexec.ToolApprovalResult
}
