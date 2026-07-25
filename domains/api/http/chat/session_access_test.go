package chat

import (
	"testing"

	session "github.com/memohai/memoh/domains/agent/chat/thread"
	"github.com/memohai/memoh/domains/api/bot"
)

func TestCanAccessSessionScopesChatToCreator(t *testing.T) {
	userID := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	otherUserID := "cccccccc-cccc-cccc-cccc-cccccccccccc"

	if !canAccessSession(session.Thread{Type: session.TypeChat, CreatedByUserID: userID}, userID, []string{bot.PermissionChat}) {
		t.Fatal("chat permission should access own chat session")
	}
	if canAccessSession(session.Thread{Type: session.TypeChat, CreatedByUserID: otherUserID}, userID, []string{bot.PermissionChat}) {
		t.Fatal("chat permission should not access another user's chat session")
	}
	if canAccessSession(session.Thread{Type: session.TypeChat}, userID, []string{bot.PermissionChat}) {
		t.Fatal("chat permission should not access legacy sessions without a creator")
	}
}

func TestCanAccessSessionAllowsChatOwnerToReadOwnSubagent(t *testing.T) {
	userID := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	otherUserID := "cccccccc-cccc-cccc-cccc-cccccccccccc"
	sess := session.Thread{Type: session.TypeSubagent, CreatedByUserID: userID}

	if !canAccessSession(sess, userID, []string{bot.PermissionChat}) {
		t.Fatal("chat permission should access own subagent session")
	}
	if canAccessSession(sess, otherUserID, []string{bot.PermissionChat}) {
		t.Fatal("chat permission should not access another user's subagent session")
	}
	if !canAccessSession(sess, otherUserID, []string{bot.PermissionManage}) {
		t.Fatal("manage should access all subagent sessions")
	}
}

func TestCanAccessSessionAllowsWorkspaceExecForOwnACP(t *testing.T) {
	userID := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	sess := session.Thread{Type: session.TypeACPAgent, CreatedByUserID: userID}

	if canAccessSession(sess, userID, []string{bot.PermissionChat}) {
		t.Fatal("chat permission should not access ACP sessions")
	}
	if !canAccessSession(sess, userID, []string{bot.PermissionWorkspaceExec}) {
		t.Fatal("workspace_exec should access own ACP sessions")
	}
	if !canAccessSession(sess, "other", []string{bot.PermissionManage}) {
		t.Fatal("manage should access all sessions")
	}
}

func TestRequiredPermissionForACPRuntimeKeepsSystemModesManaged(t *testing.T) {
	workspaceModes := []string{session.TypeChat, session.TypeDiscuss, session.TypeACPAgent}
	for _, mode := range workspaceModes {
		if got := requiredPermissionForSessionRuntime(mode, session.RuntimeACPAgent); got != bot.PermissionWorkspaceExec {
			t.Fatalf("%s ACP permission = %q, want workspace_exec", mode, got)
		}
	}

	managedModes := []string{session.TypeHeartbeat, session.TypeSchedule, session.TypeSubagent}
	for _, mode := range managedModes {
		if got := requiredPermissionForSessionRuntime(mode, session.RuntimeACPAgent); got != bot.PermissionManage {
			t.Fatalf("%s ACP permission = %q, want manage", mode, got)
		}
	}
}

func TestRequiredReadPermissionForACPRuntimeAllowsUserFacingModes(t *testing.T) {
	workspaceModes := []string{session.TypeChat, session.TypeDiscuss, session.TypeACPAgent}
	for _, mode := range workspaceModes {
		if got := requiredReadPermissionForSessionRuntime(mode, session.RuntimeACPAgent); got != bot.PermissionWorkspaceExec {
			t.Fatalf("%s ACP read permission = %q, want workspace_exec", mode, got)
		}
	}

	if got := requiredReadPermissionForSessionRuntime(session.TypeSubagent, session.RuntimeACPAgent); got != bot.PermissionChat {
		t.Fatalf("subagent ACP read permission = %q, want chat", got)
	}
}
