// Package toolexec runs the tool calls a model makes and assembles the step
// messages that carry them. It is the tool-execution surface Twilight removed
// from its SDK in felinics/twilight#53 (commit 391b059): the SDK stops at
// sdk.ToolDefinition, sdk.ToolCall, sdk.ToolArguments and sdk.ToolOutput, and
// how a call is found, approved, run and written back is the caller's own
// decision.
//
// The code is copied from twilight's sdk package at commit 44a22e5, the last
// revision before the removal, with the package name changed and the SDK types
// it still uses referenced through the sdk import. Approvals resolve
// sequentially in call order, approved tools run in parallel, and a call whose
// arguments are not a JSON document is answered to the model without running.
// One behaviour follows the executor Memoh ran before the copy rather than
// the copied revision: a deferred approval executes nothing in its batch (see
// ExecuteTools).
//
// Two fields deviate from the copy: ToolApprovalResult.Metadata and
// ToolApprovalRequestPart.Metadata are map[string]any where the SDK had
// map[string]string. Memoh's approval handler carries structured records
// there (the ask_user UI payload, the operation summary, permission options,
// the execution location) that its event stream and decision store consume
// as JSON values.
//
// adapter.go is Memoh's own: it converts between the typed argument/output
// values and the plain JSON values Memoh's events, decisions, history rows and
// map-based tool helpers carry.
package toolexec
