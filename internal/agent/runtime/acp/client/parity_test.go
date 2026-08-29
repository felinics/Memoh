package client

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	acp "github.com/coder/acp-go-sdk"
	sdk "github.com/felinics/twilight/sdk"

	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	"github.com/felinics/memoh/internal/agent/event"
	tools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/mcp"
)

// parityApproval implements both tools.NativeToolApprovalService and
// client.ToolApprovalService. Its policy mirrors the SHAPE of the real
// policy (toolapproval/policy.go): only write/edit/exec are ever gated,
// everything else bypasses. The real policy's internals are unit-tested in
// toolapproval; parity only needs both runtimes to face identical verdicts.
type parityApproval struct {
	mu       sync.Mutex
	created  []toolapproval.CreatePendingInput
	rejects  []string
	decision toolapproval.Request
	waiters  int
}

func (*parityApproval) EvaluatePolicy(_ context.Context, input toolapproval.CreatePendingInput) (toolapproval.Evaluation, error) {
	switch strings.ToLower(strings.TrimSpace(input.ToolName)) {
	case "write", "edit", "exec":
		return toolapproval.Evaluation{Decision: toolapproval.DecisionNeedsApproval}, nil
	default:
		return toolapproval.Evaluation{Decision: toolapproval.DecisionBypass}, nil
	}
}

func (f *parityApproval) CreatePending(_ context.Context, input toolapproval.CreatePendingInput) (toolapproval.Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, input)
	toolInput, _ := input.ToolInput.(map[string]any)
	return toolapproval.Request{
		ID:                fmt.Sprintf("approval-%d", len(f.created)),
		ShortID:           len(f.created),
		BotID:             input.BotID,
		SessionID:         input.SessionID,
		ChannelIdentityID: input.ChannelIdentityID,
		ToolCallID:        input.ToolCallID,
		ToolName:          input.ToolName,
		ToolInput:         toolInput,
		Status:            toolapproval.StatusPending,
	}, nil
}

func (f *parityApproval) Reject(_ context.Context, approvalID, _, reason string) (toolapproval.Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rejects = append(f.rejects, reason)
	return toolapproval.Request{ID: approvalID, Status: toolapproval.StatusRejected, DecisionReason: reason}, nil
}

func (f *parityApproval) Get(context.Context, string) (toolapproval.Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.decision, nil
}

func (f *parityApproval) WaitForDecision(context.Context, string) (toolapproval.Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.decision, nil
}

func (f *parityApproval) RegisterWaiter(string) func() {
	f.mu.Lock()
	f.waiters++
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		f.waiters--
		f.mu.Unlock()
	}
}

func (f *parityApproval) createdInputs() []toolapproval.CreatePendingInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]toolapproval.CreatePendingInput(nil), f.created...)
}

type parityToolProvider struct{ tool sdk.Tool }

func (p parityToolProvider) Tools(context.Context, tools.SessionContext) ([]sdk.Tool, error) {
	return []sdk.Tool{p.tool}, nil
}

type parityToolEvents struct {
	mu      sync.Mutex
	events  []mcp.ToolStreamEvent
	deliver bool
}

func (p *parityToolEvents) AppendToolEvent(_ mcp.ToolSessionContext, event mcp.ToolStreamEvent) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, event)
	return p.deliver
}

func (p *parityToolEvents) snapshot() []mcp.ToolStreamEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]mcp.ToolStreamEvent(nil), p.events...)
}

func parityNativeSession(runID string) mcp.ToolSessionContext {
	return mcp.ToolSessionContext{
		BotID:             "bot-1",
		SessionID:         "session-1",
		RunID:             runID,
		ChannelIdentityID: "channel-1",
		ToolCallID:        "call-1",
	}
}

func parityCallbacks(approval ToolApprovalService, runID string, nativeTools ...string) (*clientCallbacks, *eventCollector) {
	callbacks := &clientCallbacks{
		root:     "/data",
		cwd:      "/data",
		approval: approval,
		baseSession: ToolSessionContext{
			BotID:             "bot-1",
			SessionID:         "session-1",
			RunID:             runID,
			ChannelIdentityID: "channel-1",
		},
		events: &toolEventEmitter{},
	}
	if len(nativeTools) > 0 {
		callbacks.toolGateway = testACPToolGateway(nativeTools...)
	}
	collector := newEventCollector()
	callbacks.setPromptState(collector, nil, callbacks.baseSession)
	return callbacks, collector
}

func parityNativeSource(approval tools.NativeToolApprovalService, toolEvents tools.NativeToolEventSink, toolName string, executed *bool) *tools.NativeToolSource {
	if toolEvents == nil {
		toolEvents = &parityToolEvents{deliver: true}
	}
	return tools.NewNativeToolSource(nil, []tools.ToolProvider{parityToolProvider{tool: sdk.Tool{
		Name:       toolName,
		Parameters: map[string]any{"type": "object"},
		Execute: func(_ *sdk.ToolExecContext, _ any) (any, error) {
			if executed != nil {
				*executed = true
			}
			return "done", nil
		},
	}}}, tools.NativeToolSourceOptions{
		AllowAll:   true,
		Approval:   approval,
		ToolEvents: toolEvents,
	})
}

func mcpErrorText(t *testing.T, result map[string]any) string {
	t.Helper()
	if isErr, _ := result["isError"].(bool); !isErr {
		t.Fatalf("result = %#v, want MCP error result", result)
	}
	content, _ := result["content"].([]map[string]any)
	if len(content) == 0 {
		t.Fatalf("result content missing: %#v", result)
	}
	text, _ := content[0]["text"].(string)
	return text
}

// TestParityUngatedToolNeedsNoApprovalOnEitherRuntime: a tool the policy does
// not gate (ask_user, schedules, every Memoh built-in) runs with zero
// approval friction on BOTH runtimes - the native gateway executes directly,
// and the ACP pre-ask permission is allowed without creating an approval.
func TestParityUngatedToolNeedsNoApprovalOnEitherRuntime(t *testing.T) {
	t.Parallel()

	nativeApproval := &parityApproval{}
	executed := false
	source := parityNativeSource(nativeApproval, nil, "create_schedule", &executed)
	result, err := source.CallTool(context.Background(), parityNativeSession("stream-1"), "create_schedule", map[string]any{"cron": "0 9 * * *"})
	if err != nil {
		t.Fatalf("native CallTool error = %v", err)
	}
	if isErr, _ := result["isError"].(bool); isErr || !executed {
		t.Fatalf("native ungated tool result = %#v executed=%v, want success", result, executed)
	}

	acpApproval := &parityApproval{}
	callbacks, _ := parityCallbacks(acpApproval, "stream-1", "create_schedule")
	resp, err := callbacks.RequestPermission(context.Background(), acp.RequestPermissionRequest{
		ToolCall: acp.ToolCallUpdate{
			ToolCallId: acp.ToolCallId("call-1"),
			Title:      acp.Ptr("Approve MCP tool call"),
			Kind:       acp.Ptr(acp.ToolKind("other")),
			RawInput: map[string]any{
				"server_name": memohToolsMCPServerName,
				"method":      "tools/call",
				"params": map[string]any{
					"name":      "create_schedule",
					"arguments": map[string]any{"cron": "0 9 * * *"},
				},
			},
		},
		Options: []acp.PermissionOption{
			{Kind: acp.PermissionOptionKindAllowOnce, Name: "Allow", OptionId: acp.PermissionOptionId("allow")},
			{Kind: acp.PermissionOptionKindRejectOnce, Name: "Reject", OptionId: acp.PermissionOptionId("reject")},
		},
	})
	if err != nil {
		t.Fatalf("ACP RequestPermission error = %v", err)
	}
	if resp.Outcome.Selected == nil || resp.Outcome.Selected.OptionId != acp.PermissionOptionId("allow") {
		t.Fatalf("ACP outcome = %#v, want allow", resp.Outcome)
	}

	resp, err = callbacks.RequestPermission(context.Background(), acp.RequestPermissionRequest{
		ToolCall: acp.ToolCallUpdate{
			ToolCallId: acp.ToolCallId("call-claude-title"),
			Title:      acp.Ptr("mcp__Memoh_Tools__create_schedule"),
			Kind:       acp.Ptr(acp.ToolKind("other")),
			RawInput:   map[string]any{"cron": "0 9 * * *"},
		},
		Options: []acp.PermissionOption{
			{Kind: acp.PermissionOptionKindAllowOnce, Name: "Allow", OptionId: acp.PermissionOptionId("allow")},
			{Kind: acp.PermissionOptionKindRejectOnce, Name: "Reject", OptionId: acp.PermissionOptionId("reject")},
		},
	})
	if err != nil {
		t.Fatalf("Claude-shaped ACP RequestPermission error = %v", err)
	}
	if resp.Outcome.Selected == nil || resp.Outcome.Selected.OptionId != acp.PermissionOptionId("allow") {
		t.Fatalf("Claude-shaped ACP outcome = %#v, want allow", resp.Outcome)
	}

	if n, a := len(nativeApproval.createdInputs()), len(acpApproval.createdInputs()); n != 0 || a != 0 {
		t.Fatalf("approvals created: native=%d acp=%d, want 0/0 - ungated tools must be frictionless on both runtimes", n, a)
	}
}

// TestParityGatedWriteProducesEquivalentApprovalCard: the same gated write
// asks for approval on both runtimes with the same tool identity and target.
func TestParityGatedWriteProducesEquivalentApprovalCard(t *testing.T) {
	t.Parallel()

	nativeApproval := &parityApproval{decision: toolapproval.Request{Status: toolapproval.StatusApproved}}
	executed := false
	source := parityNativeSource(nativeApproval, nil, "write", &executed)
	result, err := source.CallTool(context.Background(), parityNativeSession("stream-1"), "write", map[string]any{
		"path":    "notes.txt",
		"content": "hello",
	})
	if err != nil {
		t.Fatalf("native CallTool error = %v", err)
	}
	if isErr, _ := result["isError"].(bool); isErr || !executed {
		t.Fatalf("native approved write result = %#v executed=%v, want success", result, executed)
	}

	acpApproval := &parityApproval{decision: toolapproval.Request{Status: toolapproval.StatusApproved}}
	callbacks, _ := parityCallbacks(acpApproval, "stream-1")
	resp, err := callbacks.RequestPermission(context.Background(), acp.RequestPermissionRequest{
		ToolCall: acp.ToolCallUpdate{
			ToolCallId: acp.ToolCallId("call-1"),
			Title:      acp.Ptr("Write notes.txt"),
			Kind:       acp.Ptr(acp.ToolKindEdit),
			RawInput:   map[string]any{"file_path": "notes.txt", "content": "hello"},
		},
		Options: []acp.PermissionOption{
			{Kind: acp.PermissionOptionKindAllowOnce, Name: "Allow", OptionId: acp.PermissionOptionId("allow")},
			{Kind: acp.PermissionOptionKindRejectOnce, Name: "Reject", OptionId: acp.PermissionOptionId("reject")},
		},
	})
	if err != nil {
		t.Fatalf("ACP RequestPermission error = %v", err)
	}
	if resp.Outcome.Selected == nil || resp.Outcome.Selected.OptionId != acp.PermissionOptionId("allow") {
		t.Fatalf("ACP outcome = %#v, want allow", resp.Outcome)
	}

	nativeCards := nativeApproval.createdInputs()
	acpCards := acpApproval.createdInputs()
	if len(nativeCards) != 1 || len(acpCards) != 1 {
		t.Fatalf("approval cards: native=%d acp=%d, want exactly one each", len(nativeCards), len(acpCards))
	}
	native, acpCard := nativeCards[0], acpCards[0]
	if native.ToolName != acpCard.ToolName {
		t.Fatalf("tool name parity broken: native=%q acp=%q", native.ToolName, acpCard.ToolName)
	}
	if native.BotID != acpCard.BotID || native.SessionID != acpCard.SessionID || native.ChannelIdentityID != acpCard.ChannelIdentityID {
		t.Fatalf("approval context parity broken: native=%+v acp=%+v", native, acpCard)
	}
	nativeInput := native.ToolInput.(map[string]any)
	acpInput := acpCard.ToolInput.(map[string]any)
	for _, key := range []string{"path", "content"} {
		if nativeInput[key] != acpInput[key] {
			t.Fatalf("approval input[%q] parity broken: native=%v acp=%v", key, nativeInput[key], acpInput[key])
		}
	}
}

// TestParityRejectionMessageIdentical: when a live user rejects, both
// runtimes report the EXACT same text to the agent. Diverging texts send one
// agent into retry loops the other never sees.
func TestParityRejectionMessageIdentical(t *testing.T) {
	t.Parallel()

	decision := toolapproval.Request{Status: toolapproval.StatusRejected, DecisionReason: "not now", DecidedByUser: true}

	nativeApproval := &parityApproval{decision: decision}
	executed := false
	source := parityNativeSource(nativeApproval, nil, "write", &executed)
	result, err := source.CallTool(context.Background(), parityNativeSession("stream-1"), "write", map[string]any{
		"path":    "notes.txt",
		"content": "hello",
	})
	if err != nil {
		t.Fatalf("native CallTool error = %v", err)
	}
	if executed {
		t.Fatalf("rejected native write must not execute")
	}
	nativeText := mcpErrorText(t, result)

	acpApproval := &parityApproval{decision: decision}
	callbacks, _ := parityCallbacks(acpApproval, "stream-1")
	_, acpErr := callbacks.WriteTextFile(context.Background(), acp.WriteTextFileRequest{
		Path:    "/data/notes.txt",
		Content: "hello",
	})
	if acpErr == nil {
		t.Fatalf("rejected ACP write must return an error")
	}

	if nativeText != acpErr.Error() {
		t.Fatalf("rejection text parity broken:\n  native: %q\n  acp:    %q", nativeText, acpErr.Error())
	}
	if want := "tool execution rejected by user: not now"; nativeText != want {
		t.Fatalf("rejection text = %q, want %q", nativeText, want)
	}
}

// TestParityNonInteractiveAutoRejectIdentical covers the no-live-stream path
// for both runtime integrations.
func TestParityNonInteractiveAutoRejectIdentical(t *testing.T) {
	t.Parallel()

	nativeApproval := &parityApproval{}
	executed := false
	source := parityNativeSource(nativeApproval, nil, "write", &executed)
	result, err := source.CallTool(context.Background(), parityNativeSession(""), "write", map[string]any{
		"path":    "notes.txt",
		"content": "hello",
	})
	if err != nil {
		t.Fatalf("native CallTool error = %v", err)
	}
	if executed {
		t.Fatalf("non-interactive native write must not execute")
	}
	nativeText := mcpErrorText(t, result)

	acpApproval := &parityApproval{}
	callbacks, _ := parityCallbacks(acpApproval, "")
	_, acpErr := callbacks.WriteTextFile(context.Background(), acp.WriteTextFileRequest{
		Path:    "/data/notes.txt",
		Content: "hello",
	})
	if acpErr == nil {
		t.Fatalf("non-interactive ACP write must return an error")
	}

	if nativeText != acpErr.Error() {
		t.Fatalf("non-interactive text parity broken:\n  native: %q\n  acp:    %q", nativeText, acpErr.Error())
	}
	if !strings.HasPrefix(nativeText, "tool execution was not approved") {
		t.Fatalf("non-interactive rejection must not claim a user decision: %q", nativeText)
	}
	if nNative, nACP := len(nativeApproval.rejects), len(acpApproval.rejects); nNative != 1 || nACP != 1 || nativeApproval.rejects[0] != acpApproval.rejects[0] {
		t.Fatalf("recorded reject reasons diverge: native=%v acp=%v", nativeApproval.rejects, acpApproval.rejects)
	}
}

// TestParityApprovalStreamPayloadIdentical: the approval card payload both
// runtimes publish to clients (the "approval" object with id/short_id/status/
// can_approve) must be byte-for-byte the same shape, pending and decided.
func TestParityApprovalStreamPayloadIdentical(t *testing.T) {
	t.Parallel()

	decision := toolapproval.Request{Status: toolapproval.StatusApproved}

	nativeApproval := &parityApproval{decision: decision}
	nativeToolEvents := &parityToolEvents{deliver: true}
	source := parityNativeSource(nativeApproval, nativeToolEvents, "write", nil)
	if _, err := source.CallTool(context.Background(), parityNativeSession("stream-1"), "write", map[string]any{
		"path":    "notes.txt",
		"content": "hello",
	}); err != nil {
		t.Fatalf("native CallTool error = %v", err)
	}
	var nativePayloads []map[string]any
	for _, ev := range nativeToolEvents.snapshot() {
		if ev.Type != "tool_approval_request" {
			continue
		}
		raw, err := json.Marshal(ev.Metadata["approval"])
		if err != nil {
			t.Fatalf("marshal native approval payload: %v", err)
		}
		var approval map[string]any
		if err := json.Unmarshal(raw, &approval); err != nil {
			t.Fatalf("unmarshal native approval payload: %v", err)
		}
		nativePayloads = append(nativePayloads, approval)
	}

	acpApproval := &parityApproval{decision: decision}
	callbacks, collector := parityCallbacks(acpApproval, "stream-1")
	if _, err := callbacks.RequestPermission(context.Background(), acp.RequestPermissionRequest{
		ToolCall: acp.ToolCallUpdate{
			ToolCallId: acp.ToolCallId("call-1"),
			Title:      acp.Ptr("Write notes.txt"),
			Kind:       acp.Ptr(acp.ToolKindEdit),
			RawInput:   map[string]any{"file_path": "notes.txt", "content": "hello"},
		},
		Options: []acp.PermissionOption{
			{Kind: acp.PermissionOptionKindAllowOnce, Name: "Allow", OptionId: acp.PermissionOptionId("allow")},
		},
	}); err != nil {
		t.Fatalf("ACP RequestPermission error = %v", err)
	}
	var acpPayloads []map[string]any
	for _, ev := range collector.result().Events {
		if ev.Type != event.ToolApprovalRequest {
			continue
		}
		raw, err := json.Marshal(ev.Metadata["approval"])
		if err != nil {
			t.Fatalf("marshal ACP approval payload: %v", err)
		}
		var approval map[string]any
		if err := json.Unmarshal(raw, &approval); err != nil {
			t.Fatalf("unmarshal ACP approval payload: %v", err)
		}
		acpPayloads = append(acpPayloads, approval)
	}

	if len(nativePayloads) != 2 || len(acpPayloads) != 2 {
		t.Fatalf("approval payloads: native=%d acp=%d, want pending+decided on both", len(nativePayloads), len(acpPayloads))
	}
	for i := range nativePayloads {
		if !reflect.DeepEqual(nativePayloads[i], acpPayloads[i]) {
			t.Fatalf("approval payload %d parity broken:\n  native: %#v\n  acp:    %#v", i, nativePayloads[i], acpPayloads[i])
		}
	}
}
