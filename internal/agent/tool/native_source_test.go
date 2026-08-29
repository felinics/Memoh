package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/sessionmode"
	"github.com/felinics/memoh/internal/mcp"
	sched "github.com/felinics/memoh/internal/schedule"
)

func TestNativeToolSourceAllowlistAndCall(t *testing.T) {
	provider := &nativeSourceTestProvider{
		tools: []sdk.Tool{
			{
				Name:        ToolRead().String(),
				Description: "Safe tool",
				Parameters:  map[string]any{"type": "object"},
				Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
					args, _ := input.(map[string]any)
					return map[string]any{
						"tool":  ctx.ToolName,
						"value": args["value"],
					}, nil
				},
			},
			{
				Name:        ToolExec().String(),
				Description: "Blocked tool",
				Parameters:  map[string]any{"type": "object"},
				Execute: func(_ *sdk.ToolExecContext, _ any) (any, error) {
					return "blocked", nil
				},
			},
		},
	}
	source := NewNativeToolSource(nil, []ToolProvider{provider}, NativeToolSourceOptions{
		AllowTools: map[string]bool{ToolRead().String(): true},
	})
	session := mcp.ToolSessionContext{
		BotID:                    "bot-1",
		ChatID:                   "chat-1",
		SessionID:                "session-1",
		ReasoningStoredEffort:    "low",
		ReasoningRequestedEffort: "high",
	}

	tools, err := source.ListTools(context.Background(), session)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if len(tools) != 1 || tools[0].Name != ToolRead().String() {
		t.Fatalf("ListTools() = %#v, want only read", tools)
	}
	if provider.session.BotID != "bot-1" || provider.session.ChatID != "chat-1" || provider.session.SessionID != "session-1" {
		t.Fatalf("provider session = %#v", provider.session)
	}
	if provider.session.ReasoningStoredEffort != "low" || provider.session.ReasoningRequestedEffort != "high" {
		t.Fatalf("provider reasoning intent = stored %q, requested %q",
			provider.session.ReasoningStoredEffort, provider.session.ReasoningRequestedEffort)
	}

	result, err := source.CallTool(context.Background(), session, ToolRead().String(), map[string]any{"value": "ok"})
	if err != nil {
		t.Fatalf("CallTool(read) error = %v", err)
	}
	structured, ok := result["structuredContent"].(map[string]any)
	if !ok || structured["tool"] != ToolRead().String() || structured["value"] != "ok" {
		t.Fatalf("CallTool structuredContent = %#v", result["structuredContent"])
	}

	if _, err := source.CallTool(context.Background(), session, "exec", map[string]any{}); !errors.Is(err, mcp.ErrToolNotFound) {
		t.Fatalf("CallTool(exec) error = %v, want ErrToolNotFound", err)
	}
}

func TestMCPSessionRoundTripPreservesReasoningIntent(t *testing.T) {
	t.Parallel()

	want := SessionContext{
		BotID:                    "bot-1",
		ReasoningStoredEffort:    "high",
		ReasoningRequestedEffort: "disable",
	}
	got := sessionFromMCP(toMCPSession(want))
	if got.ReasoningStoredEffort != want.ReasoningStoredEffort ||
		got.ReasoningRequestedEffort != want.ReasoningRequestedEffort {
		t.Fatalf("reasoning intent round trip = stored %q, requested %q",
			got.ReasoningStoredEffort, got.ReasoningRequestedEffort)
	}
}

func TestSessionFromMCPPreservesWorkspaceBinding(t *testing.T) {
	session := sessionFromMCP(mcp.ToolSessionContext{
		BotID:               "bot-1",
		WorkspaceTargetID:   "target-1",
		WorkspaceTargetKind: "remote",
		WorkspaceTargetName: "Office Mac",
		WorkdirPath:         "/Users/alice/project",
	})

	if session.WorkspaceTargetID != "target-1" ||
		session.WorkspaceTargetKind != "remote" ||
		session.WorkspaceTargetName != "Office Mac" ||
		session.WorkdirPath != "/Users/alice/project" {
		t.Fatalf("workspace binding = %#v", session)
	}
}

func TestNativeToolSourceAllowlistIgnoresUnknownNames(t *testing.T) {
	provider := &nativeSourceTestProvider{
		tools: []sdk.Tool{{
			Name:        "safe_tool",
			Description: "Safe tool",
			Parameters:  map[string]any{"type": "object"},
			Execute: func(_ *sdk.ToolExecContext, _ any) (any, error) {
				return "ok", nil
			},
		}},
	}
	source := NewNativeToolSource(nil, []ToolProvider{provider}, NativeToolSourceOptions{
		AllowTools: map[string]bool{"safe_tool": true},
	})
	session := mcp.ToolSessionContext{BotID: "bot-1"}

	tools, err := source.ListTools(context.Background(), session)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if len(tools) != 0 {
		t.Fatalf("ListTools() = %#v, want unknown allowlist name ignored", tools)
	}
	if _, err := source.CallTool(context.Background(), session, "safe_tool", nil); !errors.Is(err, mcp.ErrToolNotFound) {
		t.Fatalf("CallTool(safe_tool) error = %v, want ErrToolNotFound", err)
	}
}

func TestNativeToolSourceDefaultsToDenyAll(t *testing.T) {
	provider := &nativeSourceTestProvider{
		tools: []sdk.Tool{{
			Name:        "safe_tool",
			Description: "Safe tool",
			Parameters:  map[string]any{"type": "object"},
			Execute: func(_ *sdk.ToolExecContext, _ any) (any, error) {
				return "ok", nil
			},
		}},
	}
	source := NewNativeToolSource(nil, []ToolProvider{provider}, NativeToolSourceOptions{})

	tools, err := source.ListTools(context.Background(), mcp.ToolSessionContext{BotID: "bot-1"})
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if len(tools) != 0 {
		t.Fatalf("ListTools() = %#v, want default deny", tools)
	}
	if _, err := source.CallTool(context.Background(), mcp.ToolSessionContext{BotID: "bot-1"}, "safe_tool", nil); !errors.Is(err, mcp.ErrToolNotFound) {
		t.Fatalf("CallTool() error = %v, want ErrToolNotFound", err)
	}
}

func TestNativeToolSourceAllowAllOnlyAllowsBuiltIns(t *testing.T) {
	provider := &nativeSourceTestProvider{
		tools: []sdk.Tool{
			{
				Name:        "unknown_dynamic_tool",
				Description: "Unknown dynamic tool",
				Parameters:  map[string]any{"type": "object"},
				Execute: func(_ *sdk.ToolExecContext, _ any) (any, error) {
					return "unknown", nil
				},
			},
			{
				Name:        ToolRead().String(),
				Description: "Built-in tool",
				Parameters:  map[string]any{"type": "object"},
				Execute: func(_ *sdk.ToolExecContext, _ any) (any, error) {
					return map[string]any{"ok": true}, nil
				},
			},
		},
	}
	source := NewNativeToolSource(nil, []ToolProvider{provider}, NativeToolSourceOptions{AllowAll: true})
	session := mcp.ToolSessionContext{BotID: "bot-1"}

	tools, err := source.ListTools(context.Background(), session)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if len(tools) != 1 || tools[0].Name != ToolRead().String() {
		t.Fatalf("ListTools() = %#v, want only built-in read", tools)
	}
	if _, err := source.CallTool(context.Background(), session, "unknown_dynamic_tool", nil); !errors.Is(err, mcp.ErrToolNotFound) {
		t.Fatalf("CallTool(unknown_dynamic_tool) error = %v, want ErrToolNotFound", err)
	}
	result, err := source.CallTool(context.Background(), session, ToolRead().String(), nil)
	if err != nil {
		t.Fatalf("CallTool(read) error = %v", err)
	}
	structured, _ := result["structuredContent"].(map[string]any)
	if structured["ok"] != true {
		t.Fatalf("CallTool(read) result = %#v", result)
	}
}

func TestNativeToolSourceExposesScheduleTools(t *testing.T) {
	source := NewNativeToolSource(nil, []ToolProvider{
		NewScheduleProvider(nil, nativeSourceTestScheduler{}),
	}, NativeToolSourceOptions{AllowAll: true})

	descriptors, err := source.ListTools(context.Background(), mcp.ToolSessionContext{
		BotID:     "bot-1",
		SessionID: "session-1",
	})
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}

	got := map[string]bool{}
	for _, descriptor := range descriptors {
		got[descriptor.Name] = true
	}
	for _, want := range []ToolName{
		ToolListSchedule(),
		ToolGetSchedule(),
		ToolCreateSchedule(),
		ToolUpdateSchedule(),
		ToolDeleteSchedule(),
	} {
		if !got[want.String()] {
			t.Fatalf("schedule tool %q missing from descriptors: %#v", want.String(), descriptors)
		}
	}
}

func TestNativeToolSourceAppendsUsageAfterAllowlistFiltering(t *testing.T) {
	provider := &nativeSourceUsageProvider{}
	source := NewNativeToolSource(nil, []ToolProvider{provider}, NativeToolSourceOptions{
		AllowTools: map[string]bool{
			ToolSpawnAgent().String(): true,
		},
	})

	descriptors, err := source.ListTools(context.Background(), mcp.ToolSessionContext{BotID: "bot-1"})
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if len(descriptors) != 1 || descriptors[0].Name != ToolSpawnAgent().String() {
		t.Fatalf("descriptors = %#v, want only spawn_agent", descriptors)
	}
	description := descriptors[0].Description
	if !strings.Contains(description, "USAGE_HAS_SPAWN") {
		t.Fatalf("description missing allowlist-aware usage:\n%s", description)
	}
	if strings.Contains(description, "USAGE_HAS_SEND_MESSAGE") || strings.Contains(description, ToolSendMessage().String()) {
		t.Fatalf("description mentions filtered-out send_message:\n%s", description)
	}
}

func TestNativeToolSourcePassesSupportsImageInputToProviders(t *testing.T) {
	source := NewNativeToolSource(nil, []ToolProvider{
		NewContainerProvider(nil, nil, nil, ""),
	}, NativeToolSourceOptions{
		AllowTools: map[string]bool{ToolRead().String(): true},
	})

	descriptors, err := source.ListTools(context.Background(), mcp.ToolSessionContext{
		BotID:              "bot-1",
		SupportsImageInput: true,
	})
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if len(descriptors) != 1 || descriptors[0].Name != ToolRead().String() {
		t.Fatalf("descriptors = %#v, want read", descriptors)
	}
	if !strings.Contains(descriptors[0].Description, "Also supports reading image files") {
		t.Fatalf("read description missing image support hint:\n%s", descriptors[0].Description)
	}
}

func TestNativeToolSourceReadMediaReturnsPublicResultOnly(t *testing.T) {
	provider := &nativeSourceTestProvider{
		tools: []sdk.Tool{{
			Name:       ToolRead().String(),
			Parameters: map[string]any{"type": "object"},
			Execute: func(_ *sdk.ToolExecContext, _ any) (any, error) {
				return ReadMediaToolOutput{
					Public: ReadMediaToolResult{
						OK:   true,
						Path: "/workspace/image.png",
						Mime: "image/png",
						Size: 12,
					},
					ImageBase64:    "secret-image-bytes",
					ImageMediaType: "image/png",
				}, nil
			},
		}},
	}
	source := NewNativeToolSource(nil, []ToolProvider{provider}, NativeToolSourceOptions{
		AllowTools: map[string]bool{ToolRead().String(): true},
	})

	result, err := source.CallTool(context.Background(), mcp.ToolSessionContext{BotID: "bot-1"}, ToolRead().String(), map[string]any{
		"path": "/workspace/image.png",
	})
	if err != nil {
		t.Fatalf("CallTool(read) error = %v", err)
	}
	structured, ok := result["structuredContent"].(ReadMediaToolResult)
	if !ok {
		t.Fatalf("structuredContent = %#v, want ReadMediaToolResult", result["structuredContent"])
	}
	if !structured.OK || structured.Path != "/workspace/image.png" || structured.Mime != "image/png" || structured.Size != 12 {
		t.Fatalf("structuredContent = %#v, want public read-media fields", structured)
	}
	if strings.Contains(fmt.Sprintf("%#v", result), "secret-image-bytes") || strings.Contains(fmt.Sprintf("%#v", result), "ImageBase64") || strings.Contains(fmt.Sprintf("%#v", result), "ImageMediaType") {
		t.Fatalf("native MCP result leaked internal image payload: %#v", result)
	}
}

func TestNativeToolSourceLimitsToolOutput(t *testing.T) {
	large := "HEAD\n" + strings.Repeat("0123456789", 200) + "\nTAIL"
	provider := &nativeSourceTestProvider{
		tools: []sdk.Tool{{
			Name:       ToolRead().String(),
			Parameters: map[string]any{"type": "object"},
			Execute: func(_ *sdk.ToolExecContext, _ any) (any, error) {
				return map[string]any{
					"content": large,
					"ok":      true,
				}, nil
			},
		}},
	}
	source := NewNativeToolSource(nil, []ToolProvider{provider}, NativeToolSourceOptions{
		AllowTools:      map[string]bool{ToolRead().String(): true},
		ToolOutputLimit: ToolOutputLimit{MaxBytes: 512, MaxLines: 80},
	})

	result, err := source.CallTool(context.Background(), mcp.ToolSessionContext{BotID: "bot-1"}, ToolRead().String(), map[string]any{
		"path": "big.txt",
	})
	if err != nil {
		t.Fatalf("CallTool(read) error = %v", err)
	}
	assertNativeToolResultJSONBytesAtMost(t, result, 512)
	if !strings.Contains(fmt.Sprintf("%#v", result), "[memoh pruned]") {
		t.Fatalf("result was not pruned:\n%#v", result)
	}
}

func TestNativeToolSourceWaitsForApprovalAndPublishesRequest(t *testing.T) {
	executed := false
	provider := &nativeSourceTestProvider{
		tools: []sdk.Tool{{
			Name:       ToolExec().String(),
			Parameters: map[string]any{"type": "object"},
			Execute: func(_ *sdk.ToolExecContext, input any) (any, error) {
				executed = true
				args, _ := input.(map[string]any)
				return map[string]any{"command": args["command"]}, nil
			},
		}},
	}
	approval := &nativeSourceApproval{
		decision: toolapproval.Request{
			ID:      "approval-1",
			ShortID: 7,
			Status:  toolapproval.StatusApproved,
		},
	}
	toolEvents := &nativeSourceToolEvents{delivered: true}
	source := NewNativeToolSource(nil, []ToolProvider{provider}, NativeToolSourceOptions{
		AllowAll:   true,
		Approval:   approval,
		ToolEvents: toolEvents,
	})

	result, err := source.CallTool(context.Background(), mcp.ToolSessionContext{
		BotID:             "bot-1",
		SessionID:         "session-1",
		RunID:             "run-1",
		ToolCallID:        "mcp-http-call-1",
		ChannelIdentityID: "user-1",
		WorkspaceTargetID: "target-1",
		CurrentPlatform:   "web",
		ReplyTarget:       "reply-1",
		ConversationType:  "private",
	}, "exec", map[string]any{"command": "make test"})
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if !executed {
		t.Fatalf("approved tool was not executed")
	}
	if approval.created.ToolName != "exec" || approval.created.ToolInput.(map[string]any)["command"] != "make test" || approval.created.ConversationType != "private" {
		t.Fatalf("approval input = %#v", approval.created)
	}
	if approval.created.ToolCallID != "mcp-http-call-1" {
		t.Fatalf("approval tool_call_id = %q, want existing MCP tool call id", approval.created.ToolCallID)
	}
	if approval.created.WorkspaceTargetID != "target-1" {
		t.Fatalf("approval workspace target = %q, want session target", approval.created.WorkspaceTargetID)
	}
	if len(toolEvents.events) != 2 {
		t.Fatalf("tool events = %d, want pending and approved approval events", len(toolEvents.events))
	}
	pending := toolEvents.events[0]
	if pending.Type != "tool_approval_request" || pending.ToolCallID != "mcp-http-call-1" || pending.ToolName != "exec" {
		t.Fatalf("pending approval event = %#v", pending)
	}
	approvalPayload, _ := pending.Metadata["approval"].(map[string]any)
	if approvalPayload["approval_id"] != "approval-1" || approvalPayload["status"] != toolapproval.StatusPending {
		t.Fatalf("approval payload = %#v", approvalPayload)
	}
	approved := toolEvents.events[1]
	approvedApproval, _ := approved.Metadata["approval"].(map[string]any)
	if approvedApproval["approval_id"] != "approval-1" ||
		approvedApproval["status"] != toolapproval.StatusApproved ||
		approvedApproval["can_approve"] != false {
		t.Fatalf("approved approval payload = %#v", approvedApproval)
	}
	structured, ok := result["structuredContent"].(map[string]any)
	if !ok || structured["command"] != "make test" {
		t.Fatalf("result = %#v", result)
	}
}

func TestNativeToolSourceRechecksRuntimeGuardAfterApproval(t *testing.T) {
	guardErr := errors.New("runtime ownership lost")
	executed := false
	provider := &nativeSourceTestProvider{tools: []sdk.Tool{{
		Name: ToolExec().String(), Parameters: map[string]any{"type": "object"},
		Execute: func(_ *sdk.ToolExecContext, _ any) (any, error) {
			executed = true
			return "executed", nil
		},
	}}}
	approval := &nativeSourceApproval{decision: toolapproval.Request{
		ID: "approval-guard", Status: toolapproval.StatusApproved,
	}}
	source := NewNativeToolSource(nil, []ToolProvider{provider}, NativeToolSourceOptions{
		AllowAll: true, Approval: approval, ToolEvents: &nativeSourceToolEvents{delivered: true},
	})

	_, err := source.CallTool(context.Background(), mcp.ToolSessionContext{
		BotID: "bot-1", SessionID: "session-1", RunID: "run-1",
		RuntimeGuard: func(context.Context) error { return guardErr },
	}, ToolExec().String(), map[string]any{"command": "touch stale"})
	if !errors.Is(err, guardErr) {
		t.Fatalf("CallTool() error = %v, want runtime guard error", err)
	}
	if approval.created.ToolName != ToolExec().String() {
		t.Fatalf("approval input = %#v, want approval before guard", approval.created)
	}
	if executed {
		t.Fatal("tool executed after runtime guard rejection")
	}
}

func TestNativeToolSourceRejectedApprovalDoesNotExecute(t *testing.T) {
	executed := false
	provider := &nativeSourceTestProvider{
		tools: []sdk.Tool{{
			Name:       ToolWrite().String(),
			Parameters: map[string]any{"type": "object"},
			Execute: func(_ *sdk.ToolExecContext, _ any) (any, error) {
				executed = true
				return "wrote", nil
			},
		}},
	}
	toolEvents := &nativeSourceToolEvents{delivered: true}
	source := NewNativeToolSource(nil, []ToolProvider{provider}, NativeToolSourceOptions{
		AllowAll: true,
		Approval: &nativeSourceApproval{
			decision: toolapproval.Request{
				ID:             "approval-2",
				ShortID:        8,
				Status:         toolapproval.StatusRejected,
				DecisionReason: "HEAD\n" + strings.Repeat("rejected detail ", 300) + "\nTAIL",
			},
		},
		ToolEvents:      toolEvents,
		ToolOutputLimit: ToolOutputLimit{MaxBytes: 512, MaxLines: 80},
	})

	result, err := source.CallTool(context.Background(), mcp.ToolSessionContext{
		BotID:     "bot-1",
		SessionID: "session-1",
		RunID:     "run-1",
	}, "write", map[string]any{"path": "file.txt", "content": "x"})
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if executed {
		t.Fatalf("rejected tool should not execute")
	}
	if isError, _ := result["isError"].(bool); !isError {
		t.Fatalf("result = %#v, want MCP error result", result)
	}
	assertNativeToolResultJSONBytesAtMost(t, result, 512)
	if !strings.Contains(fmt.Sprintf("%#v", result), "[memoh pruned]") {
		t.Fatalf("rejected approval result was not pruned: %#v", result)
	}
	if len(toolEvents.events) != 2 {
		t.Fatalf("tool events = %d, want pending and rejected approval events", len(toolEvents.events))
	}
	approvalPayload, _ := toolEvents.events[1].Metadata["approval"].(map[string]any)
	if approvalPayload["approval_id"] != "approval-2" ||
		approvalPayload["status"] != toolapproval.StatusRejected ||
		approvalPayload["can_approve"] != false {
		t.Fatalf("rejected approval payload = %#v", approvalPayload)
	}
}

func TestNativeToolSourceApprovalNotDeliveredRejectsWithoutWaiting(t *testing.T) {
	executed := false
	provider := &nativeSourceTestProvider{
		tools: []sdk.Tool{{
			Name:       ToolWrite().String(),
			Parameters: map[string]any{"type": "object"},
			Execute: func(_ *sdk.ToolExecContext, _ any) (any, error) {
				executed = true
				return "wrote", nil
			},
		}},
	}
	approval := &nativeSourceApproval{
		decision: toolapproval.Request{
			ID:      "approval-undelivered",
			ShortID: 9,
			Status:  toolapproval.StatusApproved,
		},
	}
	source := NewNativeToolSource(nil, []ToolProvider{provider}, NativeToolSourceOptions{
		AllowAll:   true,
		Approval:   approval,
		ToolEvents: &nativeSourceToolEvents{delivered: false},
	})

	result, err := source.CallTool(context.Background(), mcp.ToolSessionContext{
		BotID:             "bot-1",
		SessionID:         "session-1",
		RunID:             "run-1",
		ToolCallID:        "mcp-http-call-1",
		ChannelIdentityID: "user-1",
	}, "write", map[string]any{"path": "file.txt", "content": "x"})
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if executed {
		t.Fatalf("undelivered approval should not execute the tool")
	}
	if approval.waitCalls != 0 {
		t.Fatalf("wait calls = %d, want 0 after delivery failure", approval.waitCalls)
	}
	if len(approval.rejected) != 1 || approval.rejected[0] != "tool approval request was not delivered to the interactive stream" {
		t.Fatalf("rejected reasons = %#v, want delivery failure", approval.rejected)
	}
	if isError, _ := result["isError"].(bool); !isError {
		t.Fatalf("result = %#v, want MCP error result", result)
	}
}

func TestNativeToolSourceAskUserRequiresInteractiveStream(t *testing.T) {
	provider := NewAskUserProvider(nil)
	source := NewNativeToolSource(nil, []ToolProvider{provider}, NativeToolSourceOptions{
		AllowTools: map[string]bool{ToolAskUser().String(): true},
	})

	tools, err := source.ListTools(context.Background(), mcp.ToolSessionContext{
		BotID:     "bot-1",
		SessionID: "session-1",
	})
	if err != nil {
		t.Fatalf("ListTools without stream: %v", err)
	}
	if len(tools) != 0 {
		t.Fatalf("tools without stream = %#v, want none", tools)
	}

	tools, err = source.ListTools(context.Background(), mcp.ToolSessionContext{
		BotID:            "bot-1",
		SessionID:        "session-1",
		SessionType:      sessionmode.ACPAgent,
		CanListUserInput: true,
	})
	if err != nil {
		t.Fatalf("ListTools with list capability: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != ToolAskUser().String() {
		t.Fatalf("tools with list-only capability = %#v, want ask_user", tools)
	}
	result, err := source.CallTool(context.Background(), mcp.ToolSessionContext{
		BotID:            "bot-1",
		SessionID:        "session-1",
		SessionType:      sessionmode.ACPAgent,
		ToolCallID:       "mcp-http-call-1",
		CanListUserInput: true,
	}, ToolAskUser().String(), map[string]any{
		"questions": []any{
			map[string]any{"text": "Question?", "kind": "text"},
		},
	})
	if err != nil {
		t.Fatalf("CallTool without delivery capability error = %v", err)
	}
	if isError, _ := result["isError"].(bool); !isError {
		t.Fatalf("CallTool without delivery capability result = %#v, want MCP error result", result)
	}
	result, err = source.CallTool(context.Background(), mcp.ToolSessionContext{
		BotID:            "bot-1",
		SessionID:        "session-1",
		SessionType:      sessionmode.ACPAgent,
		CanListUserInput: true,
	}, ToolAskUser().String(), map[string]any{
		"questions": []any{},
	})
	if err != nil {
		t.Fatalf("CallTool invalid args without delivery capability error = %v", err)
	}
	if result != nil && strings.Contains(fmt.Sprintf("%#v", result), "invalid_arguments") {
		t.Fatalf("CallTool invalid args without delivery capability should not return retry guidance: %#v", result)
	}

	tools, err = source.ListTools(context.Background(), mcp.ToolSessionContext{
		BotID:               "bot-1",
		SessionID:           "session-1",
		RunID:               "run-1",
		CanRequestUserInput: true,
	})
	if err != nil {
		t.Fatalf("ListTools with stream: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != ToolAskUser().String() {
		t.Fatalf("tools with stream = %#v, want ask_user", tools)
	}
	if !strings.Contains(tools[0].Description, "## User Input") || !strings.Contains(tools[0].Description, "`ask_user`") {
		t.Fatalf("ask_user description should include usage guidance, got:\n%s", tools[0].Description)
	}
}

func nativeAskUserSession(toolCallID string) mcp.ToolSessionContext {
	return mcp.ToolSessionContext{
		BotID:               "bot-1",
		SessionID:           "session-1",
		RunID:               "run-1",
		ToolCallID:          toolCallID,
		ChannelIdentityID:   "user-1",
		RuntimeID:           "runtime-1",
		CanRequestUserInput: true,
	}
}

func assertNativeToolResultJSONBytesAtMost(t *testing.T, value any, maxBytes int) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal value: %v", err)
	}
	if len(raw) > maxBytes {
		t.Fatalf("JSON bytes = %d, want <= %d\n%s", len(raw), maxBytes, raw)
	}
}

func TestNativeToolSourceAskUserWaitsForInputAndPublishesRequest(t *testing.T) {
	provider := NewAskUserProvider(nil)
	userInput := &nativeSourceUserInput{
		response: userinput.Request{
			ID:         "input-1",
			ToolCallID: "mcp-http-call-1",
			ToolName:   ToolAskUser().String(),
			Status:     userinput.StatusSubmitted,
			Result: map[string]any{
				"status": userinput.StatusSubmitted,
				"answers": []any{
					map[string]any{
						"question_id": "q1",
						"question":    "Pick plans",
						"selected": []any{
							map[string]any{"id": "q1.o1", "label": "Plan A"},
							map[string]any{"id": "q1.o2", "label": "Plan B"},
						},
					},
				},
			},
		},
	}
	toolEvents := &nativeSourceToolEvents{delivered: true}
	// An instant answer must already see a registered waiter, or the
	// responder misjudges the request as orphaned.
	toolEvents.onAppend = func() {
		if len(toolEvents.events) == 0 && userInput.activeWaiters == 0 {
			t.Error("waiter must be registered before the request is announced")
		}
	}
	source := NewNativeToolSource(nil, []ToolProvider{provider}, NativeToolSourceOptions{
		AllowAll:   true,
		UserInput:  userInput,
		ToolEvents: toolEvents,
	})

	result, err := source.CallTool(context.Background(), nativeAskUserSession("mcp-http-call-1"), ToolAskUser().String(), map[string]any{
		"questions": []any{
			map[string]any{
				"text": "Pick plans",
				"kind": "multi_select",
				"options": []any{
					map[string]any{"label": "Plan A"},
					map[string]any{"label": "Plan B"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("CallTool(ask_user) error = %v", err)
	}
	if userInput.created[0].ToolCallID != "mcp-http-call-1" || userInput.created[0].ToolName != ToolAskUser().String() {
		t.Fatalf("created input = %#v", userInput.created)
	}
	if userInput.created[0].ProviderMetadata["source"] != userinput.ProviderSourceACPMCP || userInput.created[0].ProviderMetadata["runtime_id"] != "runtime-1" {
		t.Fatalf("provider metadata = %#v", userInput.created[0].ProviderMetadata)
	}
	// The pending question must travel over the tool event channel with the
	// gateway's tool_call_id so the UI attaches it to the existing tool block
	// instead of rendering a second synthetic message.
	if len(toolEvents.events) != 2 {
		t.Fatalf("tool events = %d, want pending and terminal events", len(toolEvents.events))
	}
	event := toolEvents.events[0]
	if event.Type != "user_input_request" || event.ToolCallID != "mcp-http-call-1" || event.ToolName != ToolAskUser().String() {
		t.Fatalf("tool event = %#v", event)
	}
	if event.UserInputID != "input-1" || event.Status != userinput.StatusPending {
		t.Fatalf("tool event user input = %#v", event)
	}
	uiPayload, ok := event.Metadata["ui_payload"].(userinput.UIPayload)
	if !ok || len(uiPayload.Questions) != 1 || uiPayload.Questions[0].Kind != userinput.QuestionKindMultiSelect {
		t.Fatalf("tool event ui payload = %#v", event.Metadata["ui_payload"])
	}
	if toolEvents.sessions[0].RunID != "run-1" || toolEvents.sessions[0].SessionID != "session-1" {
		t.Fatalf("tool event session = %#v", toolEvents.sessions[0])
	}
	terminal := toolEvents.events[1]
	if terminal.Type != "user_input_request" || terminal.UserInputID != "input-1" || terminal.Status != userinput.StatusSubmitted {
		t.Fatalf("terminal user input event = %#v, want submitted status", terminal)
	}
	if terminal.ToolCallID != "mcp-http-call-1" || terminal.ToolName != ToolAskUser().String() {
		t.Fatalf("terminal user input identity = %#v", terminal)
	}
	structured, ok := result["structuredContent"].(map[string]any)
	if !ok || structured["status"] != userinput.StatusSubmitted {
		t.Fatalf("result = %#v", result)
	}
}

func TestNativeToolSourceAskUserLimitsSubmittedResult(t *testing.T) {
	provider := NewAskUserProvider(nil)
	userInput := &nativeSourceUserInput{
		response: userinput.Request{
			ID:         "input-1",
			ToolCallID: "mcp-http-call-1",
			ToolName:   ToolAskUser().String(),
			Status:     userinput.StatusSubmitted,
			Result: map[string]any{
				"status": userinput.StatusSubmitted,
				"answers": []any{
					map[string]any{
						"question_id": "q1",
						"text":        "HEAD\n" + strings.Repeat("answer detail ", 300) + "\nTAIL",
					},
				},
			},
		},
	}
	source := NewNativeToolSource(nil, []ToolProvider{provider}, NativeToolSourceOptions{
		AllowAll:        true,
		UserInput:       userInput,
		ToolEvents:      &nativeSourceToolEvents{delivered: true},
		ToolOutputLimit: ToolOutputLimit{MaxBytes: 512, MaxLines: 80},
	})

	result, err := source.CallTool(context.Background(), nativeAskUserSession("mcp-http-call-1"), ToolAskUser().String(), map[string]any{
		"questions": []any{
			map[string]any{"text": "Question?", "kind": "text"},
		},
	})
	if err != nil {
		t.Fatalf("CallTool(ask_user) error = %v", err)
	}
	assertNativeToolResultJSONBytesAtMost(t, result, 512)
	if !strings.Contains(fmt.Sprintf("%#v", result), "[memoh pruned]") {
		t.Fatalf("ask_user result was not pruned: %#v", result)
	}
}

func TestNativeToolSourceAskUserRejectsExistingResolvedRequest(t *testing.T) {
	provider := NewAskUserProvider(nil)
	userInput := &nativeSourceUserInput{
		createResponse: userinput.Request{
			ID:         "input-1",
			ToolCallID: "mcp-http-call-1",
			ToolName:   ToolAskUser().String(),
			Status:     userinput.StatusSubmitted,
			Result: map[string]any{
				"status": userinput.StatusSubmitted,
				"answers": []any{
					map[string]any{"question_id": "q1", "text": "already answered"},
				},
			},
		},
	}
	toolEvents := &nativeSourceToolEvents{delivered: true}
	source := NewNativeToolSource(nil, []ToolProvider{provider}, NativeToolSourceOptions{
		AllowAll:   true,
		UserInput:  userInput,
		ToolEvents: toolEvents,
	})

	_, err := source.CallTool(context.Background(), nativeAskUserSession("mcp-http-call-1"), ToolAskUser().String(), map[string]any{
		"questions": []any{
			map[string]any{"text": "Question?", "kind": "text"},
		},
	})
	if !errors.Is(err, userinput.ErrAlreadyDecided) {
		t.Fatalf("CallTool(ask_user) error = %v, want ErrAlreadyDecided", err)
	}
	if len(toolEvents.events) != 0 {
		t.Fatalf("tool events = %d, want no pending event", len(toolEvents.events))
	}
	if userInput.waitCalls != 0 {
		t.Fatalf("wait calls = %d, want 0", userInput.waitCalls)
	}
}

func TestNativeToolSourceAskUserAbortCancelsAfterReleasingWaiter(t *testing.T) {
	provider := NewAskUserProvider(nil)
	userInput := &nativeSourceUserInput{waitErr: context.Canceled}
	toolEvents := &nativeSourceToolEvents{delivered: true}
	source := NewNativeToolSource(nil, []ToolProvider{provider}, NativeToolSourceOptions{
		AllowAll:   true,
		UserInput:  userInput,
		ToolEvents: toolEvents,
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := source.CallTool(ctx, nativeAskUserSession("mcp-http-call-1"), ToolAskUser().String(), map[string]any{
		"questions": []any{
			map[string]any{"text": "Question?", "kind": "text"},
		},
	})
	if err == nil {
		t.Fatal("aborted user input wait must surface an error")
	}
	if len(userInput.canceled) != 1 || userInput.canceled[0] != "user input aborted" {
		t.Fatalf("canceled reasons = %#v, want abort cleanup", userInput.canceled)
	}
	if len(userInput.waitersAtCancel) != 1 || userInput.waitersAtCancel[0] != 0 {
		t.Fatalf("waiters at abort cleanup = %#v, want released before cancel", userInput.waitersAtCancel)
	}
	if len(toolEvents.events) != 2 || toolEvents.events[1].Status != userinput.StatusCanceled {
		t.Fatalf("tool events = %#v, want pending then canceled", toolEvents.events)
	}
}

func TestNativeToolSourceAskUserAbortReturnsLateAnswerWhenCancelLoses(t *testing.T) {
	provider := NewAskUserProvider(nil)
	userInput := &nativeSourceUserInput{
		waitErrs:  []error{context.Canceled, nil},
		cancelErr: userinput.ErrAlreadyDecided,
		response: userinput.Request{
			ID:         "input-1",
			ToolCallID: "mcp-http-call-1",
			ToolName:   ToolAskUser().String(),
			Status:     userinput.StatusSubmitted,
			Result:     map[string]any{"status": userinput.StatusSubmitted},
		},
	}
	source := NewNativeToolSource(nil, []ToolProvider{provider}, NativeToolSourceOptions{
		AllowAll:   true,
		UserInput:  userInput,
		ToolEvents: &nativeSourceToolEvents{delivered: true},
	})

	result, err := source.CallTool(context.Background(), nativeAskUserSession("mcp-http-call-1"), ToolAskUser().String(), map[string]any{
		"questions": []any{
			map[string]any{"text": "Question?", "kind": "text"},
		},
	})
	if err != nil {
		t.Fatalf("CallTool(ask_user) error = %v", err)
	}
	if userInput.waitCalls != 2 {
		t.Fatalf("wait calls = %d, want 2", userInput.waitCalls)
	}
	structured, ok := result["structuredContent"].(map[string]any)
	if !ok || structured["status"] != userinput.StatusSubmitted {
		t.Fatalf("result = %#v", result)
	}
}

func TestNativeToolSourceAskUserNotDeliveredCancelsWithoutWaiting(t *testing.T) {
	provider := NewAskUserProvider(nil)
	userInput := &nativeSourceUserInput{
		response: userinput.Request{
			ID:     "input-1",
			Status: userinput.StatusSubmitted,
			Result: map[string]any{"status": userinput.StatusSubmitted},
		},
	}
	source := NewNativeToolSource(nil, []ToolProvider{provider}, NativeToolSourceOptions{
		AllowAll:   true,
		UserInput:  userInput,
		ToolEvents: &nativeSourceToolEvents{delivered: false},
	})

	result, err := source.CallTool(context.Background(), nativeAskUserSession("mcp-http-call-1"), ToolAskUser().String(), map[string]any{
		"questions": []any{
			map[string]any{"text": "Question?", "kind": "text"},
		},
	})
	if err != nil {
		t.Fatalf("undelivered user input should return canceled result, got error %v", err)
	}
	if len(userInput.canceled) != 1 || userInput.canceled[0] != "user input request was not delivered to the interactive stream" {
		t.Fatalf("canceled reasons = %#v, want delivery cleanup", userInput.canceled)
	}
	if userInput.waitCalls != 0 {
		t.Fatalf("WaitForResponse calls = %d, want 0 when delivery fails", userInput.waitCalls)
	}
	structured, ok := result["structuredContent"].(map[string]any)
	if !ok || structured["status"] != userinput.StatusCanceled {
		t.Fatalf("result = %#v, want canceled tool result", result)
	}
}

type nativeSourceToolEvents struct {
	delivered bool
	onAppend  func()
	sessions  []mcp.ToolSessionContext
	events    []mcp.ToolStreamEvent
}

func (s *nativeSourceToolEvents) AppendToolEvent(session mcp.ToolSessionContext, event mcp.ToolStreamEvent) bool {
	if s.onAppend != nil {
		s.onAppend()
	}
	s.sessions = append(s.sessions, session)
	s.events = append(s.events, event)
	return s.delivered
}

func TestNativeToolSourceAskUserKeepsMultipleRequestsIndependent(t *testing.T) {
	provider := NewAskUserProvider(nil)
	userInput := &nativeSourceUserInput{}
	source := NewNativeToolSource(nil, []ToolProvider{provider}, NativeToolSourceOptions{
		AllowAll:   true,
		UserInput:  userInput,
		ToolEvents: &nativeSourceToolEvents{delivered: true},
	})
	session := mcp.ToolSessionContext{
		BotID:               "bot-1",
		SessionID:           "session-1",
		RunID:               "run-1",
		CanRequestUserInput: true,
	}

	for idx, callID := range []string{"mcp-http-call-1", "mcp-http-call-2"} {
		session.ToolCallID = callID
		result, err := source.CallTool(context.Background(), session, ToolAskUser().String(), map[string]any{
			"questions": []any{
				map[string]any{"text": fmt.Sprintf("Question %d?", idx+1), "kind": "text"},
			},
		})
		if err != nil {
			t.Fatalf("CallTool(%s) error = %v", callID, err)
		}
		structured, ok := result["structuredContent"].(map[string]any)
		if !ok || structured["status"] != userinput.StatusSubmitted {
			t.Fatalf("result %d = %#v", idx, result)
		}
	}

	if len(userInput.created) != 2 {
		t.Fatalf("created requests = %d, want 2", len(userInput.created))
	}
	if userInput.created[0].ToolCallID != "mcp-http-call-1" || userInput.created[1].ToolCallID != "mcp-http-call-2" {
		t.Fatalf("created tool_call_ids = %#v", userInput.created)
	}
	firstQuestion := firstQuestionText(t, userInput.created[0].Input)
	secondQuestion := firstQuestionText(t, userInput.created[1].Input)
	if firstQuestion == secondQuestion {
		t.Fatalf("requests were not independent: %#v", userInput.created)
	}
}

func firstQuestionText(t *testing.T, input any) string {
	t.Helper()
	obj, _ := input.(map[string]any)
	questions, _ := obj["questions"].([]any)
	if len(questions) == 0 {
		t.Fatalf("missing questions in input %#v", input)
	}
	question, _ := questions[0].(map[string]any)
	text, _ := question["text"].(string)
	return text
}

type nativeSourceTestProvider struct {
	tools   []sdk.Tool
	session SessionContext
}

func (p *nativeSourceTestProvider) Tools(_ context.Context, session SessionContext) ([]sdk.Tool, error) {
	p.session = session
	return p.tools, nil
}

type nativeSourceTestScheduler struct{}

func (nativeSourceTestScheduler) List(context.Context, string) ([]sched.Schedule, error) {
	return nil, nil
}

func (nativeSourceTestScheduler) Get(context.Context, string) (sched.Schedule, error) {
	return sched.Schedule{}, nil
}

func (nativeSourceTestScheduler) Create(context.Context, string, sched.CreateRequest) (sched.Schedule, error) {
	return sched.Schedule{}, nil
}

func (nativeSourceTestScheduler) Update(context.Context, string, sched.UpdateRequest) (sched.Schedule, error) {
	return sched.Schedule{}, nil
}

func (nativeSourceTestScheduler) Delete(context.Context, string) error {
	return nil
}

type nativeSourceUsageProvider struct{}

func (*nativeSourceUsageProvider) Tools(context.Context, SessionContext) ([]sdk.Tool, error) {
	return []sdk.Tool{
		{
			Name:        ToolSpawnAgent().String(),
			Description: "Create one managed subagent.",
			Parameters:  emptyObjectSchema(),
			Execute: func(*sdk.ToolExecContext, any) (any, error) {
				return map[string]any{"ok": true}, nil
			},
		},
		{
			Name:        ToolSendMessage().String(),
			Description: "Send a follow-up message.",
			Parameters:  emptyObjectSchema(),
			Execute: func(*sdk.ToolExecContext, any) (any, error) {
				return map[string]any{"ok": true}, nil
			},
		},
	}, nil
}

func (*nativeSourceUsageProvider) Usage(_ context.Context, _ SessionContext, available AvailableTools) string {
	var parts []string
	if available.Has(ToolSpawnAgent()) {
		parts = append(parts, "USAGE_HAS_SPAWN")
	}
	if available.Has(ToolSendMessage()) {
		parts = append(parts, "USAGE_HAS_SEND_MESSAGE")
	}
	return strings.Join(parts, " ")
}

type nativeSourceApproval struct {
	created   toolapproval.CreatePendingInput
	decision  toolapproval.Request
	waitErrs  []error // popped per WaitForDecision call; nil entry -> return decision
	rejectErr error
	rejected  []string
	waitCalls int
	waiters   int
}

type nativeSourceUserInput struct {
	created         []userinput.CreatePendingInput
	byID            map[string]userinput.Request
	createResponse  userinput.Request
	response        userinput.Request
	canceled        []string
	waitersAtCancel []int
	activeWaiters   int
	waitErr         error
	waitErrs        []error
	cancelErr       error
	waitCalls       int
}

func (u *nativeSourceUserInput) RegisterWaiter(string) func() {
	u.activeWaiters++
	return func() { u.activeWaiters-- }
}

func (u *nativeSourceUserInput) CreatePending(_ context.Context, input userinput.CreatePendingInput) (userinput.Request, error) {
	u.created = append(u.created, input)
	if u.createResponse.ID != "" {
		return u.createResponse, nil
	}
	if u.byID == nil {
		u.byID = map[string]userinput.Request{}
	}
	uiPayload, err := userinput.ParseAskUserPayload(input.Input)
	if err != nil {
		return userinput.Request{}, err
	}
	id := fmt.Sprintf("input-%d", len(u.created))
	req := userinput.Request{
		ID:                id,
		BotID:             input.BotID,
		SessionID:         input.SessionID,
		ChannelIdentityID: input.ChannelIdentityID,
		ToolCallID:        input.ToolCallID,
		ToolName:          input.ToolName,
		ShortID:           len(u.created),
		Status:            userinput.StatusPending,
		Input:             input.Input.(map[string]any),
		UIPayload:         uiPayload,
		ProviderMetadata:  input.ProviderMetadata,
	}
	if u.response.ID == "" {
		u.byID[id] = userinput.Request{
			ID:         id,
			ToolCallID: input.ToolCallID,
			ToolName:   input.ToolName,
			Status:     userinput.StatusSubmitted,
			Result: map[string]any{
				"status": userinput.StatusSubmitted,
				"answers": []any{
					map[string]any{
						"question_id": "q1",
						"text":        "answer for " + input.ToolCallID,
					},
				},
			},
		}
	}
	return req, nil
}

func (u *nativeSourceUserInput) Cancel(_ context.Context, input userinput.CancelInput) (userinput.Request, error) {
	u.canceled = append(u.canceled, input.Reason)
	u.waitersAtCancel = append(u.waitersAtCancel, u.activeWaiters)
	if u.cancelErr != nil {
		return userinput.Request{}, u.cancelErr
	}
	return userinput.Request{
		ID:     input.RequestID,
		Status: userinput.StatusCanceled,
		Result: map[string]any{"status": userinput.StatusCanceled},
	}, nil
}

func (u *nativeSourceUserInput) WaitForResponse(_ context.Context, requestID string) (userinput.Request, error) {
	u.waitCalls++
	return u.waitForResponse(requestID)
}

func (u *nativeSourceUserInput) WaitForRegisteredResponse(_ context.Context, requestID string) (userinput.Request, error) {
	u.waitCalls++
	return u.waitForResponse(requestID)
}

func (u *nativeSourceUserInput) waitForResponse(requestID string) (userinput.Request, error) {
	if len(u.waitErrs) > 0 {
		err := u.waitErrs[0]
		u.waitErrs = u.waitErrs[1:]
		if err != nil {
			return userinput.Request{}, err
		}
	}
	if u.waitErr != nil {
		return userinput.Request{}, u.waitErr
	}
	if u.response.ID != "" {
		return u.response, nil
	}
	if req, ok := u.byID[requestID]; ok {
		return req, nil
	}
	return userinput.Request{}, userinput.ErrNotFound
}

func (*nativeSourceApproval) EvaluatePolicy(context.Context, toolapproval.CreatePendingInput) (toolapproval.Evaluation, error) {
	return toolapproval.Evaluation{Decision: toolapproval.DecisionNeedsApproval}, nil
}

func (a *nativeSourceApproval) CreatePending(_ context.Context, input toolapproval.CreatePendingInput) (toolapproval.Request, error) {
	a.created = input
	return toolapproval.Request{
		ID:                a.decision.ID,
		BotID:             input.BotID,
		SessionID:         input.SessionID,
		RouteID:           input.RouteID,
		ChannelIdentityID: input.ChannelIdentityID,
		ToolCallID:        input.ToolCallID,
		ToolName:          input.ToolName,
		ToolInput:         input.ToolInput.(map[string]any),
		ShortID:           a.decision.ShortID,
		Status:            toolapproval.StatusPending,
	}, nil
}

func (a *nativeSourceApproval) Reject(_ context.Context, approvalID, _, reason string) (toolapproval.Request, error) {
	a.rejected = append(a.rejected, reason)
	if a.rejectErr != nil {
		return toolapproval.Request{}, a.rejectErr
	}
	return toolapproval.Request{
		ID:             approvalID,
		Status:         toolapproval.StatusRejected,
		DecisionReason: reason,
	}, nil
}

func (a *nativeSourceApproval) Get(_ context.Context, approvalID string) (toolapproval.Request, error) {
	decision := a.decision
	if decision.ID == "" {
		decision.ID = approvalID
	}
	return decision, nil
}

func (a *nativeSourceApproval) WaitForDecision(context.Context, string) (toolapproval.Request, error) {
	a.waitCalls++
	if len(a.waitErrs) > 0 {
		err := a.waitErrs[0]
		a.waitErrs = a.waitErrs[1:]
		if err != nil {
			return toolapproval.Request{}, err
		}
	}
	return a.decision, nil
}

func (a *nativeSourceApproval) RegisterWaiter(string) func() {
	a.waiters++
	return func() { a.waiters-- }
}
