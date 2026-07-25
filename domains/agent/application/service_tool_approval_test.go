package application

import (
	"context"
	"errors"
	"testing"

	toolapproval "github.com/memohai/memoh/domains/agent/decision/approval"
	"github.com/memohai/memoh/domains/api/bot"
)

type recordingToolApprovalPermissionChecker struct {
	permission string
	allow      bool
}

func (c *recordingToolApprovalPermissionChecker) HasBotPermission(_ context.Context, _, _, permission string) (bool, error) {
	c.permission = permission
	return c.allow, nil
}

func TestAuthorizeToolApprovalResponseMapsOperationPermission(t *testing.T) {
	t.Parallel()

	cases := []struct {
		operation  string
		permission string
	}{
		{operation: toolapproval.OperationRead, permission: bot.PermissionWorkspaceRead},
		{operation: toolapproval.OperationWrite, permission: bot.PermissionWorkspaceWrite},
		{operation: toolapproval.OperationExec, permission: bot.PermissionWorkspaceExec},
	}
	for _, tc := range cases {
		t.Run(tc.operation, func(t *testing.T) {
			t.Parallel()
			checker := &recordingToolApprovalPermissionChecker{allow: true}
			resolver := &Service{botPermissions: checker}
			err := resolver.authorizeToolApprovalResponse(context.Background(), toolapproval.Request{
				BotID:     "bot-1",
				Operation: tc.operation,
			}, ToolApprovalResponseInput{ActorUserID: "user-1"})
			if err != nil {
				t.Fatalf("authorizeToolApprovalResponse() error = %v", err)
			}
			if checker.permission != tc.permission {
				t.Fatalf("checked permission = %q, want %q", checker.permission, tc.permission)
			}
		})
	}
}

func TestAuthorizeToolApprovalResponseFailsClosed(t *testing.T) {
	t.Parallel()

	checker := &recordingToolApprovalPermissionChecker{allow: true}
	resolver := &Service{botPermissions: checker}
	for _, target := range []toolapproval.Request{
		{BotID: "bot-1", Operation: "unknown"},
		{Operation: toolapproval.OperationRead},
	} {
		if err := resolver.authorizeToolApprovalResponse(context.Background(), target, ToolApprovalResponseInput{ActorUserID: "user-1"}); !errors.Is(err, toolapproval.ErrForbidden) {
			t.Fatalf("authorizeToolApprovalResponse(%+v) error = %v, want forbidden", target, err)
		}
	}

	checker.allow = false
	if err := resolver.authorizeToolApprovalResponse(context.Background(), toolapproval.Request{
		BotID:     "bot-1",
		Operation: toolapproval.OperationWrite,
	}, ToolApprovalResponseInput{ActorUserID: "user-1"}); !errors.Is(err, toolapproval.ErrForbidden) {
		t.Fatalf("denied permission error = %v, want forbidden", err)
	}
}
