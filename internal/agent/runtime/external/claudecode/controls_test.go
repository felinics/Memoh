package claudecode

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/decision/approval"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/external/claudecode/claudecfg"
)

type controlProcess struct {
	writes chan []byte
	done   chan struct{}
}

func (*controlProcess) Read([]byte) (int, error) { return 0, io.EOF }
func (p *controlProcess) Write(b []byte) (int, error) {
	p.writes <- append([]byte(nil), b...)
	return len(b), nil
}
func (*controlProcess) CloseStdin()             {}
func (*controlProcess) Kill()                   {}
func (*controlProcess) Close() error            { return nil }
func (p *controlProcess) Done() <-chan struct{} { return p.done }
func (*controlProcess) StderrTail() string      { return "" }

func controlTestRunner() (*turnRunner, *controlProcess) {
	p := &controlProcess{writes: make(chan []byte, 16), done: make(chan struct{})}
	r := newTurnRunner(context.Background(), external.PromptInput{Sink: &recordingSink{}, RuntimeMetadata: map[string]any{}}, p, nil, nil, slog.Default())
	return r, p
}

func receiveControl(t *testing.T, p *controlProcess) (string, map[string]any) {
	t.Helper()
	select {
	case line := <-p.writes:
		var request struct {
			ID      string         `json:"request_id"`
			Request map[string]any `json:"request"`
		}
		if err := json.Unmarshal(line, &request); err != nil {
			t.Fatal(err)
		}
		return request.ID, request.Request
	case <-time.After(time.Second):
		t.Fatal("no control request")
		return "", nil
	}
}

func replyControl(t *testing.T, r *turnRunner, id string, response map[string]any) {
	t.Helper()
	line, err := controlSuccessResponse(id, response)
	if err != nil {
		t.Fatal(err)
	}
	feed(t, r, string(line))
}

func TestClaudeControlErrorsAndCancellation(t *testing.T) {
	r, p := controlTestRunner()
	defer r.close()
	done := make(chan error, 1)
	go func() { _, err := r.callControl(context.Background(), "initialize", nil); done <- err }()
	id, _ := receiveControl(t, p)
	line, _ := controlErrorResponse(id, "private diagnostic")
	feed(t, r, string(line))
	if err := <-done; err == nil {
		t.Fatal("control error was treated as success")
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _, err := r.callControl(ctx, "initialize", nil); done <- err }()
	_, _ = receiveControl(t, p)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	r.s.mu.Lock()
	pending := len(r.s.pending)
	r.s.mu.Unlock()
	if pending != 0 {
		t.Fatalf("abandoned control waiters: %d", pending)
	}
}

func TestClaudePlanPreservesConfiguredPermissions(t *testing.T) {
	r, p := controlTestRunner()
	defer r.close()
	r.input.RuntimeMetadata["permission_mode"] = "acceptEdits"
	r.input.RuntimeMetadata["collaboration_mode"] = "plan"
	done := make(chan error, 1)
	go func() { done <- r.configureModes(context.Background(), claudecfg.Config{}, nil) }()
	id, request := receiveControl(t, p)
	if request["subtype"] != "set_permission_mode" || request["mode"] != "acceptEdits" {
		t.Fatalf("request: %v", request)
	}
	replyControl(t, r, id, map[string]any{"mode": "acceptEdits"})
	id, request = receiveControl(t, p)
	if request["mode"] != "plan" {
		t.Fatalf("request: %v", request)
	}
	replyControl(t, r, id, map[string]any{"mode": "plan"})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// ExitPlanMode restores Claude's own saved mode and reports the transition.
	feed(t, r, `{"type":"system","subtype":"status","permissionMode":"acceptEdits"}`)
	if r.runtimeMetadata["collaboration_mode"] != "default" || r.runtimeMetadata["claude_effective_permission_mode"] != "acceptEdits" {
		t.Fatalf("plan still active: %v", r.runtimeMetadata)
	}
	if len(p.writes) != 0 {
		t.Fatal("host issued an extra permission restore")
	}
}

type explicitRuntimeApproval struct {
	cancelAwareApproval
	status       string
	policyCalls  int
	pendingCalls int
}

func (a *explicitRuntimeApproval) EvaluatePolicy(context.Context, approval.CreatePendingInput) (approval.Evaluation, error) {
	a.policyCalls++
	return approval.Evaluation{Decision: approval.DecisionBypass}, nil
}

func (a *explicitRuntimeApproval) CreatePending(ctx context.Context, input approval.CreatePendingInput) (approval.Request, error) {
	a.pendingCalls++
	// Match the real approval service's storage boundary.
	if _, ok := approval.OperationForTool(input.ToolName); !ok {
		return approval.Request{}, errors.New("unsupported tool approval operation")
	}
	return a.cancelAwareApproval.CreatePending(ctx, input)
}

func (a *explicitRuntimeApproval) WaitForDecision(context.Context, string) (approval.Request, error) {
	return approval.Request{ID: "approval-1", Status: a.status, DecidedByUser: true}, nil
}

// Native questions require an explicit decision. Memoh MCP wrappers skip the
// native card because the tool gateway enforces Memoh's policy on execution.
func TestClaudeNativeApprovalBoundary(t *testing.T) {
	for _, tc := range []struct {
		tool, status, behavior string
		pending                int
	}{
		{"Bash", approval.StatusApproved, "allow", 1},
		{"Write", approval.StatusApproved, "allow", 1},
		{"mcp__external__lookup", approval.StatusRejected, "deny", 1},
		{"ExitPlanMode", approval.StatusApproved, "allow", 1},
		{"ExitPlanMode", approval.StatusRejected, "deny", 1},
		{"mcp__memoh__exec", approval.StatusRejected, "allow", 0},
	} {
		t.Run(tc.tool+"/"+tc.status, func(t *testing.T) {
			r, p := controlTestRunner()
			defer r.close()
			svc := &explicitRuntimeApproval{status: tc.status}
			r.approval = svc
			r.input.CanRequestUserInput = true
			r.permissionMode = "plan"
			input := map[string]any{"command": "ls"}
			if tc.tool == "Write" {
				input = map[string]any{"file_path": "/data/result.txt"}
			}
			go r.handleCanUseTool(r.ctx, "approval", &controlRequestPayload{ToolName: tc.tool, ToolUseID: "tool-1", Input: input})
			var response struct {
				Response struct {
					Response struct {
						Behavior     string         `json:"behavior"`
						UpdatedInput map[string]any `json:"updatedInput"`
					} `json:"response"`
				} `json:"response"`
			}
			select {
			case line := <-p.writes:
				if err := json.Unmarshal(line, &response); err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("no permission response")
			}
			if svc.policyCalls != 0 || svc.pendingCalls != tc.pending {
				t.Fatalf("wrong approval boundary: %+v", svc)
			}
			if response.Response.Response.Behavior != tc.behavior || r.permissionMode != "plan" {
				t.Fatalf("response %+v, mode %s", response, r.permissionMode)
			}
			if tc.behavior == "allow" && !maps.Equal(response.Response.Response.UpdatedInput, input) {
				t.Fatalf("approval presentation changed the native input: %+v", response)
			}
		})
	}
}

func (*controlProcess) Err() error { return nil }

// A process that left the workspace's mode in an earlier turn returns to it
// when the next turn inherits.
func TestInheritedPermissionsReturnAfterAnEarlierTurnChangedThem(t *testing.T) {
	r, p := controlTestRunner()
	defer r.close()
	r.s.mode = "acceptEdits"
	r.input.RuntimeMetadata["permission_mode"] = "inherit"
	done := make(chan error, 1)
	go func() { done <- r.configureModes(context.Background(), claudecfg.Config{}, nil) }()
	id, request := receiveControl(t, p)
	if request["subtype"] != "get_settings" {
		t.Fatalf("request: %v", request)
	}
	replyControl(t, r, id, map[string]any{"effective": map[string]any{"permissions": map[string]any{"defaultMode": "default"}}})
	id, request = receiveControl(t, p)
	if request["subtype"] != "set_permission_mode" || request["mode"] != "default" {
		t.Fatalf("request: %v", request)
	}
	replyControl(t, r, id, map[string]any{"mode": "default"})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
