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
	"github.com/felinics/memoh/internal/agent/runtime/claudecode/claudecfg"
	"github.com/felinics/memoh/internal/agent/runtime/external"
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

func TestClaudeDefaultsOnlyProbeSelectedModel(t *testing.T) {
	r, p := controlTestRunner()
	defer r.close()
	catalog := external.ModelCatalog{ConfiguredModelID: "a", Models: []external.ModelOption{
		{ID: "a", ResolvedModelID: "native-a"},
		{ID: "b", ResolvedModelID: "native-b", ReasoningEfforts: []external.ReasoningEffortOption{{ID: "high"}}},
		{ID: "unrelated"},
	}}
	settings := settingsResponse{}
	settings.Applied.Model = "native-a"
	done := make(chan error, 1)
	go func() { done <- r.resolveModelDefaults(t.Context(), &catalog, "b", settings) }()
	id, request := receiveControl(t, p)
	if request["subtype"] != "set_model" || request["model"] != "b" {
		t.Fatalf("unexpected model probe: %v", request)
	}
	replyControl(t, r, id, nil)
	id, request = receiveControl(t, p)
	if request["subtype"] != "get_settings" {
		t.Fatalf("unexpected control: %v", request)
	}
	replyControl(t, r, id, map[string]any{"applied": map[string]any{"model": "native-b", "effort": "high"}})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if catalog.Models[1].DefaultReasoningEffort != "high" || len(p.writes) != 0 {
		t.Fatalf("defaults did not stay scoped to the selected model: %+v", catalog)
	}
	if err := r.resolveModelDefaults(t.Context(), &catalog, "a", settings); err != nil || len(p.writes) != 0 {
		t.Fatalf("probed settings already observed at initialization: %v", err)
	}
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
	r.mu.Lock()
	pending := len(r.pendingCtrl)
	r.mu.Unlock()
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

func TestClaudePlanOverrideDoesNotReplayInheritedPermissions(t *testing.T) {
	for _, tc := range []struct{ plan, configured, requested string }{
		{"plan", "", "plan"},
		{"default", "bypassPermissions", ""},
		{"default", "auto", ""},
		{"default", "acceptEdits", ""},
		{"default", "plan", "default"},
	} {
		t.Run(tc.plan+"/"+tc.configured, func(t *testing.T) {
			r, p := controlTestRunner()
			defer r.close()
			r.input.RuntimeMetadata["collaboration_mode"] = tc.plan
			done := make(chan error, 1)
			go func() { done <- r.configureModes(context.Background(), claudecfg.Config{}, nil) }()
			if tc.plan == "default" {
				id, request := receiveControl(t, p)
				if request["subtype"] != "get_settings" {
					t.Fatalf("request: %v", request)
				}
				replyControl(t, r, id, map[string]any{"effective": map[string]any{"permissions": map[string]any{"defaultMode": tc.configured}}})
			}
			if tc.requested != "" {
				id, request := receiveControl(t, p)
				if request["subtype"] != "set_permission_mode" || request["mode"] != tc.requested {
					t.Fatalf("request: %v", request)
				}
				replyControl(t, r, id, map[string]any{"mode": tc.requested})
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("host replayed inherited permissions")
			}
		})
	}
}

func TestClaudeInheritedPermissionsFollowWorkspaceAcrossTurns(t *testing.T) {
	metadata := map[string]any{"permission_mode": "inherit"}
	for _, mode := range []string{"acceptEdits", "plan", "default"} {
		r, p := controlTestRunner()
		defer r.close()
		r.input.RuntimeMetadata = metadata

		if err := r.configureModes(context.Background(), claudecfg.Config{}, nil); err != nil {
			t.Fatal(err)
		}
		feed(t, r, `{"type":"system","subtype":"init","permissionMode":"`+mode+`"}`)
		if metadata["collaboration_mode"] != nil || len(p.writes) != 0 {
			t.Fatalf("initial snapshot pinned a choice: %v", metadata)
		}
		for key, value := range r.runtimeMetadata {
			metadata[key] = value
		}
		state, err := (&Driver{}).PlanMode(context.Background(), r.input)
		if err != nil || (state.CurrentModeID == "plan") != (mode == "plan") {
			t.Fatalf("inherited Plan display: %+v, %v", state, err)
		}
		if mode == "default" {
			feed(t, r, `{"type":"system","subtype":"status","permissionMode":"plan"}`)
			if r.runtimeMetadata["collaboration_mode"] != "plan" {
				t.Fatal("native Plan entry was not preserved for the next turn")
			}
		}
	}
}

func TestClaudeCompactionRequiresManualBoundary(t *testing.T) {
	for _, boundary := range []string{"", "auto", "manual"} {
		t.Run(boundary, func(t *testing.T) {
			r := newTestRunner(&recordingSink{})
			r.input.Command = "compact"
			if boundary != "" {
				feed(t, r, `{"type":"system","subtype":"compact_boundary","compact_metadata":{"trigger":"`+boundary+`"}}`)
			}
			feed(t, r, `{"type":"result","subtype":"success","is_error":false}`)
			result, err := r.buildResult("")
			if (err == nil) != (boundary == "manual") || result.TurnCompleted != (boundary == "manual") {
				t.Fatalf("result %+v, error %v", result, err)
			}
		})
	}
}

func TestClaudeModesAndCommandBoundary(t *testing.T) {
	r, p := controlTestRunner()
	defer r.close()
	r.input.ModelID = "model-a"
	r.input.RuntimeMetadata["permission_mode"] = "auto"
	for _, plan := range []string{"default", "plan"} {
		r.input.RuntimeMetadata["collaboration_mode"] = plan
		if err := r.configureModes(context.Background(), claudecfg.Config{}, []initializeModel{{Value: "model-a"}}); !errors.Is(err, external.ErrModeUnavailable) {
			t.Fatalf("unsupported model accepted Auto: %v", err)
		}
	}
	if len(p.writes) != 0 {
		t.Fatal("sent a native mode change for an unsupported model")
	}
	for _, mode := range []string{"inherit", "default", "acceptEdits", "auto", "bypassPermissions"} {
		if _, err := claudeModes(mode); err != nil {
			t.Fatal(err)
		}
	}
	for _, mode := range []string{"strict", "policy", "yolo", "plan"} {
		if _, err := claudeModes(mode); err == nil {
			t.Fatalf("accepted %q", mode)
		}
	}
	d := &Driver{}
	commands, err := d.Commands(context.Background(), external.PromptInput{})
	if err != nil {
		t.Fatal(err)
	}
	if command, ok := external.FindCommand(commands, "goal"); !ok || command.Kind != external.CommandTurn {
		t.Fatal("new sessions cannot submit a native goal")
	}
	commands, err = d.Commands(context.Background(), external.PromptInput{RuntimeMetadata: map[string]any{
		"claude_commands": []initializeCommand{
			{Name: "clear"},
			{Name: "code-review"},
			{Name: "goal", Description: "Native goal description", ArgumentHint: "Native goal hint"},
			{Name: "project:review", Aliases: []string{"pr-review"}},
			{Name: "legacy-check"},
			{Name: "loop", Aliases: []string{"proactive"}},
		},
		"claude_skills": []string{"project:review", "legacy-check", "loop", "hidden-skill"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := external.FindCommand(commands, "code-review"); !ok {
		t.Fatal("missing advertised review")
	}
	for _, name := range []string{"project:review", "pr-review", "legacy-check"} {
		if command, ok := external.FindCommand(commands, name); !ok || command.Kind != external.CommandTurn {
			t.Fatalf("missing native skill %q", name)
		}
	}
	goalCount := 0
	for _, command := range commands {
		if command.Name == "goal" {
			if command.Description != "Native goal description" || command.InputHint != "Native goal hint" {
				t.Fatal("host copy replaced the native command description")
			}
			goalCount++
		}
	}
	if goalCount != 1 {
		t.Fatalf("goal command count after discovery: %d", goalCount)
	}
	for _, name := range []string{"clear", "loop", "proactive", "hidden-skill"} {
		if _, ok := external.FindCommand(commands, name); ok {
			t.Fatalf("published unsupported control %q", name)
		}
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

func TestClaudeResultUsageAndMetadataAreStable(t *testing.T) {
	r := newTestRunner(&recordingSink{})
	// Keep the run open across two native results.
	feed(t, r, `{"type":"result","uuid":"first","subtype":"success","queued_turn_count":1,"usage":{"input_tokens":100,"output_tokens":10,"cache_read_input_tokens":20}}`)
	feed(t, r, `{"type":"result","uuid":"first","subtype":"success","queued_turn_count":1,"usage":{"input_tokens":100,"output_tokens":10,"cache_read_input_tokens":20}}`)
	feed(t, r, `{"type":"result","uuid":"second","subtype":"success","usage":{"input_tokens":200,"output_tokens":30,"cache_read_input_tokens":40}}`)
	r.runtimeMetadata["model"] = "first"
	result, err := r.buildResult("")
	if err != nil {
		t.Fatal(err)
	}
	if result.Usage.InputTokens != 300 || result.Usage.OutputTokens != 40 || result.Usage.CachedInputTokens != 60 {
		t.Fatalf("usage: %+v", result.Usage)
	}
	r.runtimeMetadata["model"] = "later"
	if result.RuntimeMetadata["model"] != "first" {
		t.Fatal("result shares mutable runtime metadata")
	}
	r.close()
	feed(t, r, `{"type":"system","subtype":"status","permissionMode":"plan"}`)
	if r.permissionMode != "" {
		t.Fatal("closed run accepted a late mode update")
	}
}

func (*controlProcess) Err() error { return nil }

type drainingProcess struct {
	controlProcess
	eof     chan struct{}
	exitErr error
}

func (p *drainingProcess) CloseStdin() { close(p.eof) }
func (p *drainingProcess) Err() error  { return p.exitErr }

func TestClaudeDrainWaitsForTranscriptFlush(t *testing.T) {
	p := &drainingProcess{controlProcess: controlProcess{done: make(chan struct{})}, eof: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- drainCLI(p, time.Second) }()
	<-p.eof
	select {
	case err := <-done:
		t.Fatalf("drained before native flush: %v", err)
	default:
	}
	p.exitErr = errors.New("flush failed")
	close(p.done)
	if err := <-done; !errors.Is(err, p.exitErr) {
		t.Fatalf("lost exit failure: %v", err)
	}
}

func TestClaudeReadCommandsPreserveStructuredObservations(t *testing.T) {
	d := &Driver{}
	usage := map[string]any{"input_tokens": 12701, "future_counter": 12}
	input := external.PromptInput{Command: "status", RuntimeMetadata: map[string]any{
		metadataSessionIDKey: "native-session", "claude_model": "native-model",
		"claude_effective_permission_mode": "bypassPermissions", "claude_usage": usage,
	}}
	result, err := d.ReadCommand(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	data, ok := result.Data.(map[string]any)
	if !ok || result.Text != "" || result.Notice != "last_observed" {
		t.Fatalf("runtime formatted a presentation: %+v", result)
	}
	if data["usage"].(map[string]any)["future_counter"] != 12 || data["model"] != "native-model" {
		t.Fatalf("lost native observation: %+v", data)
	}
}
