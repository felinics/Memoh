package codex

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/agent/decision/approval"
	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/external/codex/protocol"
)

type recordingApprovalFlow struct {
	closingApprovalFlow
	input approval.CreatePendingInput
}

func (f *recordingApprovalFlow) CreatePending(ctx context.Context, in approval.CreatePendingInput) (approval.Request, error) {
	f.input = in
	return f.closingApprovalFlow.CreatePending(ctx, in)
}

func TestPermissionsRequestReachesTheUserAsAPermissionCard(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	flow := &recordingApprovalFlow{}
	st := newTurnState(ctx, external.PromptInput{CanRequestUserInput: true, Sink: external.EventSinkFunc(func(event.StreamEvent) {})}, "native-thread", flow, nil, nil, nil, slog.Default())
	defer st.close()

	reason := "Needs write access"
	st.decidePermissions(ctx, &protocol.PermissionsRequestApprovalParams{
		ItemID: "item", Reason: &reason,
		Permissions: protocol.RequestPermissionProfile{Network: &protocol.AdditionalNetworkPermissions{}},
	})

	if op, ok := approval.OperationForTool(flow.input.ToolName); !ok || op != approval.OperationPermission {
		t.Fatalf("tool name %q is no permission approval: the service would refuse the card", flow.input.ToolName)
	}
	input, _ := flow.input.ToolInput.(map[string]any)
	request, _ := input["request"].(string)
	if input["title"] != reason || !strings.Contains(request, "network") {
		t.Fatalf("card input = %+v, want the reason as title and the asked permissions as request", input)
	}
}

func TestPermissionsApprovalGrantsWhatWasAskedOrNothing(t *testing.T) {
	network := &protocol.AdditionalNetworkPermissions{}
	params := &protocol.PermissionsRequestApprovalParams{Permissions: protocol.RequestPermissionProfile{Network: network}}

	for _, tc := range []struct {
		result approval.FlowResult
		scope  protocol.PermissionGrantScope
		grant  bool
	}{
		{approval.FlowResult{Approved: true, SelectedOptionID: "turn"}, protocol.PermissionGrantScopeTurn, true},
		{approval.FlowResult{Approved: true, SelectedOptionID: "session"}, protocol.PermissionGrantScopeSession, true},
		{approval.FlowResult{Status: approval.StatusRejected, SelectedOptionID: "decline"}, protocol.PermissionGrantScopeTurn, false},
		{approval.FlowResult{Status: approval.StatusCancelled}, protocol.PermissionGrantScopeTurn, false},
	} {
		got := permissionsApprovalResponse(params, tc.result)
		if got.Scope == nil || *got.Scope != tc.scope || (got.Permissions.Network != nil) != tc.grant || got.Permissions.FileSystem != nil {
			t.Errorf("%+v: response = %+v", tc.result, got)
		}
	}
}
