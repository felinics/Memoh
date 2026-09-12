package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/codex/protocol"
	"github.com/felinics/memoh/internal/agent/runtime/external"
)

func TestPermissionPresetsAndThreadOverride(t *testing.T) {
	for _, mode := range []string{"strict", "policy", "yolo"} {
		p, err := permissions(Config{PermissionMode: "yolo"}, external.PromptInput{RuntimeMetadata: map[string]any{"permission_mode": mode}})
		if err != nil {
			t.Fatal(err)
		}
		if mode == "yolo" {
			if p.approval.Unit != protocol.AskForApprovalUnitNever || p.sandbox != protocol.SandboxModeDangerFullAccess || p.policy.DangerFullAccess == nil {
				t.Fatalf("full access: %+v", p)
			}
		} else {
			if p.approval.Unit != protocol.AskForApprovalUnitOnRequest || p.sandbox != protocol.SandboxModeWorkspaceWrite || p.policy.WorkspaceWrite == nil {
				t.Fatalf("sandbox preset: %+v", p)
			}
			expected := protocol.ApprovalsReviewerUser
			if mode == "policy" {
				expected = protocol.ApprovalsReviewerAutoReview
			}
			if p.reviewer != expected {
				t.Fatalf("reviewer = %s", p.reviewer)
			}
		}
	}
	if _, err := permissions(Config{}, external.PromptInput{RuntimeMetadata: map[string]any{"permission_mode": "unknown"}}); !errors.Is(err, external.ErrModeUnavailable) {
		t.Fatalf("unknown mode: %v", err)
	}
}

func TestCompactionWaitsForCompletionAndPreservesFailure(t *testing.T) {
	for _, outcome := range []string{"completed", "failed"} {
		t.Run(outcome, func(t *testing.T) {
			client, server := net.Pipe()
			defer func() { _ = server.Close() }()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			turn := newTurnState(ctx, external.PromptInput{}, "thread", nil, nil, nil, nil, slog.Default())
			defer turn.close()
			owner := &appServer{logger: slog.Default(), turns: map[string]*turnState{"thread": turn}}
			connection := newConn(client, owner, slog.Default())
			defer func() { _ = connection.Close() }()
			exited := make(chan struct{})
			result := make(chan error, 1)
			go func() {
				_, err := awaitCompaction(ctx, connection, turn, exited, func() {})
				result <- err
			}()
			scanner := bufio.NewScanner(server)
			if !scanner.Scan() {
				t.Fatal("missing compact request")
			}
			var req struct {
				ID     json.RawMessage
				Method string
			}
			if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
				t.Fatal(err)
			}
			if req.Method != protocol.MethodThreadCompactStart {
				t.Fatalf("method %s", req.Method)
			}
			if _, err := fmt.Fprintf(server, "{\"id\":%s,\"result\":{}}\n", req.ID); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-result:
				t.Fatalf("ack completed operation: %v", err)
			case <-time.After(15 * time.Millisecond):
			}
			turn.handleNotification(&protocol.TurnCompletedNotification{ThreadID: "thread", Turn: protocol.Turn{ID: "operation", Status: protocol.TurnStatus(outcome)}})
			select {
			case err := <-result:
				if outcome == "completed" && err != nil {
					t.Fatal(err)
				}
				if outcome != "completed" && err == nil {
					t.Fatal("false success")
				}
			case <-time.After(time.Second):
				t.Fatal("operation did not settle")
			}
		})
	}
}
