package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	pluginspkg "github.com/memohai/memoh/domains/agent/extension/plugins"
	skillset "github.com/memohai/memoh/domains/agent/extension/skills"
	"github.com/memohai/memoh/domains/runtime/bridge/bridgepb"
	bridge "github.com/memohai/memoh/domains/runtime/bridge/client"
)

const hookTestBufSize = 1 << 20

type fakeToolRunner struct {
	calls []fakeToolCall
	fn    func(context.Context, string, map[string]any) (any, error)
}

type fakeToolCall struct {
	name  string
	input map[string]any
}

func (r *fakeToolRunner) RunHookTool(ctx context.Context, toolName string, input map[string]any) (any, error) {
	r.calls = append(r.calls, fakeToolCall{name: toolName, input: input})
	if r.fn != nil {
		return r.fn(ctx, toolName, input)
	}
	return map[string]any{"decision": DecisionAllow}, nil
}

func TestRunConfigMatchesEveryCatalogEvent(t *testing.T) {
	t.Parallel()

	events := EventCatalog()
	cfg := Config{
		Version: 1,
		Hooks:   make([]Hook, 0, len(events)),
	}
	for _, event := range events {
		cfg.Hooks = append(cfg.Hooks, Hook{
			Name:  "record-" + event,
			Event: event,
			Actions: []HookAction{{
				Type: ActionTool,
				Tool: "record_event",
				Input: map[string]any{
					"event": event,
				},
			}},
		})
	}

	service := NewService(nil, nil)
	runner := &fakeToolRunner{}
	for _, event := range events {
		result, err := service.RunConfig(context.Background(), cfg, Request{Event: event, BotID: "bot-1"}, runner)
		if err != nil {
			t.Fatalf("RunConfig(%s) returned error: %v", event, err)
		}
		if result.HooksMatched != 1 {
			t.Fatalf("RunConfig(%s) matched %d hooks, want 1", event, result.HooksMatched)
		}
		if result.ActionsRun != 1 {
			t.Fatalf("RunConfig(%s) ran %d actions, want 1", event, result.ActionsRun)
		}
		if result.Decision != DecisionAllow {
			t.Fatalf("RunConfig(%s) decision = %q, want %q", event, result.Decision, DecisionAllow)
		}
		if result.RuntimeSupported != RuntimeSupported(event) {
			t.Fatalf("RunConfig(%s) runtime_supported = %v, want %v", event, result.RuntimeSupported, RuntimeSupported(event))
		}
	}
	if len(runner.calls) != len(events) {
		t.Fatalf("runner calls = %d, want %d", len(runner.calls), len(events))
	}
}

func TestRunConfigMergesDecisionsInPriorityOrder(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Version: 1,
		Hooks: []Hook{
			{
				Name:     "append-context",
				Event:    EventPreToolUse,
				Matcher:  "^exec$",
				Priority: 30,
				Actions: []HookAction{{
					Type: ActionTool,
					Tool: "append",
				}},
			},
			{
				Name:     "ask",
				Event:    EventPreToolUse,
				Matcher:  "^exec$",
				Priority: 20,
				Actions: []HookAction{{
					Type: ActionTool,
					Tool: "ask",
				}},
			},
			{
				Name:     "deny",
				Event:    EventPreToolUse,
				Matcher:  "^exec$",
				Priority: 10,
				Actions: []HookAction{{
					Type: ActionTool,
					Tool: "deny",
				}},
			},
		},
	}
	runner := &fakeToolRunner{
		fn: func(_ context.Context, name string, _ map[string]any) (any, error) {
			switch name {
			case "append":
				return map[string]any{
					"decision":       DecisionAppendContext,
					"append_context": "extra guardrail",
				}, nil
			case "ask":
				return map[string]any{
					"decision": DecisionAskApproval,
					"reason":   "needs human approval",
				}, nil
			case "deny":
				return map[string]any{
					"decision": DecisionDeny,
					"reason":   "blocked by policy",
				}, nil
			default:
				return nil, nil
			}
		},
	}

	result, err := NewService(nil, nil).RunConfig(context.Background(), cfg, Request{
		Event: EventPreToolUse,
		Tool:  &ToolPayload{Name: "exec"},
	}, runner)
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("RunConfig error = %v, want ErrDenied", err)
	}
	if result.Decision != DecisionDeny {
		t.Fatalf("decision = %q, want %q", result.Decision, DecisionDeny)
	}
	if result.Reason != "blocked by policy" {
		t.Fatalf("reason = %q, want deny reason", result.Reason)
	}
	if result.AppendContext != "extra guardrail" {
		t.Fatalf("append_context = %q, want merged context", result.AppendContext)
	}
	if got, want := len(runner.calls), 3; got != want {
		t.Fatalf("runner calls = %d, want %d", got, want)
	}
	if got := []string{runner.calls[0].name, runner.calls[1].name, runner.calls[2].name}; !slices.Equal(got, []string{"append", "ask", "deny"}) {
		t.Fatalf("runner call order = %v, want priority order", got)
	}
}

func TestRunConfigLimitsToolAppendContext(t *testing.T) {
	t.Parallel()

	large := "HEAD\n" + strings.Repeat("context detail ", 300) + "\nTAIL"
	cfg := Config{
		Version: 1,
		Defaults: Defaults{
			MaxOutputBytes: 192,
		},
		Hooks: []Hook{{
			Name:  "append-context",
			Event: EventBeforeModelCall,
			Actions: []HookAction{{
				Type: ActionTool,
				Tool: "append",
			}},
		}},
	}
	runner := &fakeToolRunner{
		fn: func(context.Context, string, map[string]any) (any, error) {
			return map[string]any{
				"decision":       DecisionAppendContext,
				"append_context": large,
			}, nil
		},
	}

	result, err := NewService(nil, nil).RunConfig(context.Background(), cfg, Request{Event: EventBeforeModelCall}, runner)
	if err != nil {
		t.Fatalf("RunConfig returned error: %v", err)
	}
	if len(result.AppendContext) > 192 {
		t.Fatalf("append_context bytes = %d, want <= 192", len(result.AppendContext))
	}
	if len(result.AppendContext) >= len(large) {
		t.Fatalf("append_context was not limited")
	}
	assertHookTextPreservesHeadTail(t, result.AppendContext)
}

func TestRunConfigLimitsAggregatedAppendContext(t *testing.T) {
	t.Parallel()

	large := "HEAD\n" + strings.Repeat("context detail ", 300) + "\nTAIL"
	cfg := Config{
		Version: 1,
		Defaults: Defaults{
			MaxOutputBytes: 192,
		},
		Hooks: []Hook{{
			Name:  "append-context",
			Event: EventBeforeModelCall,
			Actions: []HookAction{
				{Type: ActionTool, Tool: "append_one"},
				{Type: ActionTool, Tool: "append_two"},
			},
		}},
	}
	runner := &fakeToolRunner{
		fn: func(context.Context, string, map[string]any) (any, error) {
			return map[string]any{
				"decision":       DecisionAppendContext,
				"append_context": large,
			}, nil
		},
	}

	result, err := NewService(nil, nil).RunConfig(context.Background(), cfg, Request{Event: EventBeforeModelCall}, runner)
	if err != nil {
		t.Fatalf("RunConfig returned error: %v", err)
	}
	if len(result.AppendContext) > 192 {
		t.Fatalf("append_context bytes = %d, want <= 192", len(result.AppendContext))
	}
	if len(result.AppendContext) >= len(large) {
		t.Fatalf("append_context was not limited after aggregation")
	}
	assertHookTextPreservesHeadTail(t, result.AppendContext)
}

func TestRunConfigLimitsPluginAppendContextWithSourceCap(t *testing.T) {
	t.Parallel()

	large := "HEAD\n" + strings.Repeat("plugin context detail ", 300) + "\nTAIL"
	cfg := Config{
		Version: 1,
		Defaults: Defaults{
			MaxOutputBytes: 4096,
		},
		Hooks: []Hook{{
			Name:  "plugin append-context",
			Event: EventBeforeModelCall,
			source: hookSource{
				Kind:           sourceKindPlugin,
				PluginID:       "github",
				MaxOutputBytes: 192,
			},
			Actions: []HookAction{
				{Type: ActionTool, Tool: "append_one"},
				{Type: ActionTool, Tool: "append_two"},
			},
		}},
	}
	runner := &fakeToolRunner{
		fn: func(context.Context, string, map[string]any) (any, error) {
			return map[string]any{
				"decision":       DecisionAppendContext,
				"append_context": large,
			}, nil
		},
	}

	result, err := NewService(nil, nil).RunConfig(context.Background(), cfg, Request{Event: EventBeforeModelCall}, runner)
	if err != nil {
		t.Fatalf("RunConfig returned error: %v", err)
	}
	if len(result.AppendContext) > 192 {
		t.Fatalf("append_context bytes = %d, want <= plugin cap 192", len(result.AppendContext))
	}
	if len(result.AppendContext) >= len(large) {
		t.Fatalf("append_context was not limited by plugin cap")
	}
	assertHookTextPreservesHeadTail(t, result.AppendContext)
}

func TestRunConfigLimitsPluginAppendContextWithGlobalCap(t *testing.T) {
	t.Parallel()

	large := "HEAD\n" + strings.Repeat("plugin context detail ", 300) + "\nTAIL"
	cfg := Config{
		Version: 1,
		Defaults: Defaults{
			MaxOutputBytes: 192,
		},
		Hooks: []Hook{{
			Name:  "plugin append-context",
			Event: EventBeforeModelCall,
			source: hookSource{
				Kind:           sourceKindPlugin,
				PluginID:       "github",
				MaxOutputBytes: 4096,
			},
			Actions: []HookAction{{
				Type: ActionTool,
				Tool: "append",
			}},
		}},
	}
	runner := &fakeToolRunner{
		fn: func(context.Context, string, map[string]any) (any, error) {
			return map[string]any{
				"decision":       DecisionAppendContext,
				"append_context": large,
			}, nil
		},
	}

	result, err := NewService(nil, nil).RunConfig(context.Background(), cfg, Request{Event: EventBeforeModelCall}, runner)
	if err != nil {
		t.Fatalf("RunConfig returned error: %v", err)
	}
	if len(result.AppendContext) > 192 {
		t.Fatalf("append_context bytes = %d, want <= global cap 192", len(result.AppendContext))
	}
	if len(result.AppendContext) >= len(large) {
		t.Fatalf("append_context was not limited by global cap")
	}
	assertHookTextPreservesHeadTail(t, result.AppendContext)
}

func TestRunConfigOnErrorBlockDenies(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Version: 1,
		Hooks: []Hook{{
			Name:  "block-on-error",
			Event: EventPreToolUse,
			Actions: []HookAction{{
				Type:    ActionTool,
				Tool:    "failing_tool",
				OnError: OnErrorBlock,
			}},
		}},
	}
	runner := &fakeToolRunner{
		fn: func(context.Context, string, map[string]any) (any, error) {
			return nil, errors.New("tool failed")
		},
	}

	result, err := NewService(nil, nil).RunConfig(context.Background(), cfg, Request{Event: EventPreToolUse}, runner)
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("RunConfig error = %v, want ErrDenied", err)
	}
	if result.Decision != DecisionDeny {
		t.Fatalf("decision = %q, want %q", result.Decision, DecisionDeny)
	}
	if result.Reason != "tool failed" {
		t.Fatalf("reason = %q, want tool error", result.Reason)
	}
}

func TestRunLimitsCommandAppendContext(t *testing.T) {
	t.Parallel()

	large := "HEAD\n" + strings.Repeat("context detail ", 300) + "\nTAIL"
	cfg := `{
		"version": 1,
		"enabled": true,
		"defaults": {
			"timeout": "3s",
			"max_output_bytes": 192
		},
		"hooks": [{
			"name": "command context",
			"event": "BeforeModelCall",
			"actions": [{
				"type": "command",
				"command": "node /data/.memoh/hook.js"
			}]
		}]
	}`
	payload, err := json.Marshal(map[string]string{
		"decision":       DecisionAppendContext,
		"append_context": large,
	})
	if err != nil {
		t.Fatalf("marshal hook output: %v", err)
	}
	server := &hookBridgeTestServer{
		files: map[string][]byte{
			DefaultConfigPath: []byte(cfg),
		},
		stdout: string(payload),
	}
	service := NewService(nil, hookBridgeProvider{client: newHookBridgeTestClient(t, server)})

	result, err := service.Run(context.Background(), Request{
		Event: EventBeforeModelCall,
		BotID: "bot-1",
		Workspace: WorkspaceInfo{
			CWD:     "/data/workspace",
			Runtime: "container",
		},
	}, nil)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(result.AppendContext) > 192 {
		t.Fatalf("append_context bytes = %d, want <= 192", len(result.AppendContext))
	}
	if len(result.AppendContext) >= len(large) {
		t.Fatalf("append_context was not limited")
	}
	assertHookTextPreservesHeadTail(t, result.AppendContext)
}

func TestRunLimitsCommandMetadataAppendContext(t *testing.T) {
	t.Parallel()

	large := "HEAD\n" + strings.Repeat("metadata context detail ", 300) + "\nTAIL"
	cfg := `{
		"version": 1,
		"enabled": true,
		"defaults": {
			"timeout": "3s",
			"max_output_bytes": 192
		},
		"hooks": [{
			"name": "command metadata context",
			"event": "BeforeModelCall",
			"actions": [{
				"type": "command",
				"command": "node /data/.memoh/hook.js"
			}]
		}]
	}`
	payload, err := json.Marshal(map[string]any{
		"decision": DecisionAppendContext,
		"metadata": map[string]any{
			"append_context": large,
		},
	})
	if err != nil {
		t.Fatalf("marshal hook output: %v", err)
	}
	server := &hookBridgeTestServer{
		files: map[string][]byte{
			DefaultConfigPath: []byte(cfg),
		},
		stdout: string(payload),
	}
	service := NewService(nil, hookBridgeProvider{client: newHookBridgeTestClient(t, server)})

	result, err := service.Run(context.Background(), Request{
		Event: EventBeforeModelCall,
		BotID: "bot-1",
		Workspace: WorkspaceInfo{
			CWD:     "/data/workspace",
			Runtime: "container",
		},
	}, nil)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(result.AppendContext) > 192 {
		t.Fatalf("append_context bytes = %d, want <= 192", len(result.AppendContext))
	}
	if len(result.AppendContext) >= len(large) {
		t.Fatalf("metadata append_context was not limited")
	}
	assertHookTextPreservesHeadTail(t, result.AppendContext)
}

func TestRunLimitsCommandRawStdoutMetadata(t *testing.T) {
	t.Parallel()

	large := "HEAD\n" + strings.Repeat("raw stdout ", 300) + "\nTAIL"
	largeErr := "HEAD\n" + strings.Repeat("raw stderr ", 300) + "\nTAIL"
	cfg := `{
		"version": 1,
		"enabled": true,
		"defaults": {
			"timeout": "3s",
			"max_output_bytes": 192
		},
		"hooks": [{
			"name": "command raw stdout",
			"event": "BeforeModelCall",
			"actions": [{
				"type": "command",
				"command": "node /data/.memoh/hook.js"
			}]
		}]
	}`
	server := &hookBridgeTestServer{
		files: map[string][]byte{
			DefaultConfigPath: []byte(cfg),
		},
		stdout: large,
		stderr: largeErr,
	}
	service := NewService(nil, hookBridgeProvider{client: newHookBridgeTestClient(t, server)})

	result, err := service.Run(context.Background(), Request{
		Event: EventBeforeModelCall,
		BotID: "bot-1",
		Workspace: WorkspaceInfo{
			CWD:     "/data/workspace",
			Runtime: "container",
		},
	}, nil)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	action := result.ActionResults[0]
	raw, ok := action.Metadata["raw_stdout"].(string)
	if !ok {
		t.Fatalf("raw_stdout metadata = %#v, want string", action.Metadata["raw_stdout"])
	}
	if len(raw) > 192 {
		t.Fatalf("raw_stdout bytes = %d, want <= 192", len(raw))
	}
	if len(raw) >= len(large) {
		t.Fatalf("raw_stdout was not limited")
	}
	assertHookTextPreservesHeadTail(t, raw)
	if len(action.Stdout) > 192 {
		t.Fatalf("stdout bytes = %d, want <= 192", len(action.Stdout))
	}
	if len(action.Stderr) > 192 {
		t.Fatalf("stderr bytes = %d, want <= 192", len(action.Stderr))
	}
	assertHookTextPreservesHeadTail(t, action.Stdout)
	assertHookTextPreservesHeadTail(t, action.Stderr)
}

func assertHookTextPreservesHeadTail(t *testing.T, text string) {
	t.Helper()
	for _, want := range []string{"[memoh pruned]", "HEAD", "TAIL"} {
		if !strings.Contains(text, want) {
			t.Fatalf("hook text missing %q:\n%s", want, text)
		}
	}
}

func TestParseConfigRejectsReservedMCPTool(t *testing.T) {
	t.Parallel()

	_, err := ParseConfig([]byte(`{
		"version": 1,
		"hooks": [{
			"name": "future mcp",
			"event": "PreToolUse",
			"actions": [{"type": "mcp_tool", "server": "example", "tool": "ping"}]
		}]
	}`))
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("ParseConfig error = %v, want ErrUnsupported", err)
	}
}

func TestParseConfigRejectsInvalidNumericTimeout(t *testing.T) {
	t.Parallel()

	_, err := ParseConfig([]byte(`{
		"version": 1,
		"hooks": [{
			"name": "bad timeout",
			"event": "PreToolUse",
			"actions": [{"type": "tool", "tool": "noop", "timeout": "10wat"}]
		}]
	}`))
	if err == nil {
		t.Fatal("ParseConfig returned nil error for invalid timeout")
	}
}

func TestRunLoadsConfigAndExecutesCommandHook(t *testing.T) {
	t.Parallel()

	cfg := `{
		"version": 1,
		"enabled": true,
		"defaults": {
			"timeout": "3s",
			"max_output_bytes": 4096
		},
		"env": {
			"HOOK_ENV": "enabled"
		},
		"hooks": [{
			"name": "command logger",
			"event": "PostToolUse",
			"matcher": "^exec$",
			"actions": [{
				"type": "command",
				"command": "node /data/.memoh/hook.js",
				"work_dir": "/data/project"
			}]
		}]
	}`
	server := &hookBridgeTestServer{
		files: map[string][]byte{
			DefaultConfigPath: []byte(cfg),
		},
		stdout: `{"decision":"append_context","append_context":"from command","metadata":{"source":"test"}}`,
	}
	client := newHookBridgeTestClient(t, server)
	service := NewService(nil, hookBridgeProvider{client: client})

	result, err := service.Run(context.Background(), Request{
		Event: EventPostToolUse,
		BotID: "bot-1",
		Tool:  &ToolPayload{Name: "exec", Result: "ok"},
		Workspace: WorkspaceInfo{
			CWD:     "/data/workspace",
			Runtime: "container",
		},
	}, nil)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Decision != DecisionAppendContext {
		t.Fatalf("decision = %q, want %q", result.Decision, DecisionAppendContext)
	}
	if result.AppendContext != "from command" {
		t.Fatalf("append_context = %q, want command context", result.AppendContext)
	}

	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.execs) != 1 {
		t.Fatalf("exec count = %d, want 1", len(server.execs))
	}
	exec := server.execs[0]
	if exec.command != "node /data/.memoh/hook.js" {
		t.Fatalf("command = %q", exec.command)
	}
	if exec.workDir != "/data/project" {
		t.Fatalf("work_dir = %q, want explicit hook work_dir", exec.workDir)
	}
	if exec.timeout != 3 {
		t.Fatalf("timeout = %d, want 3", exec.timeout)
	}
	if !slices.Contains(exec.env, "HOOK_ENV=enabled") || !slices.Contains(exec.env, "MEMOH_HOOK_EVENT=PostToolUse") {
		t.Fatalf("env = %v, missing hook env values", exec.env)
	}
	var req Request
	if err := json.Unmarshal(exec.stdin, &req); err != nil {
		t.Fatalf("stdin is not hook request JSON: %v", err)
	}
	if req.HookName != "command logger" || req.Tool == nil || req.Tool.Name != "exec" {
		t.Fatalf("stdin request = %+v", req)
	}
}

func TestRunCommandHookDoesNotUseTargetWorkspaceAsExecutionDirectory(t *testing.T) {
	t.Parallel()

	cfg := `{
		"version": 1,
		"enabled": true,
		"hooks": [{
			"name": "remote guard",
			"event": "BeforeWorkspaceCommand",
			"actions": [{
				"type": "command",
				"command": "node /data/.memoh/guard.js"
			}]
		}]
	}`
	server := &hookBridgeTestServer{
		files:  map[string][]byte{DefaultConfigPath: []byte(cfg)},
		stdout: `{"decision":"allow"}`,
	}
	service := NewService(nil, hookBridgeProvider{client: newHookBridgeTestClient(t, server)})

	_, err := service.Run(context.Background(), Request{
		Event: EventBeforeWorkspaceCommand,
		BotID: "bot-1",
		Workspace: WorkspaceInfo{
			CWD:     `C:\Users\alice\project`,
			Runtime: bridge.WorkspaceBackendRemote,
		},
	}, nil)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.execs) != 1 {
		t.Fatalf("exec count = %d, want 1", len(server.execs))
	}
	exec := server.execs[0]
	if exec.workDir != DefaultWorkDir {
		t.Fatalf("hook work_dir = %q, want primary workspace %q", exec.workDir, DefaultWorkDir)
	}
	var req Request
	if err := json.Unmarshal(exec.stdin, &req); err != nil {
		t.Fatalf("stdin is not hook request JSON: %v", err)
	}
	if req.Workspace.CWD != `C:\Users\alice\project` || req.Workspace.Runtime != bridge.WorkspaceBackendRemote {
		t.Fatalf("hook request lost target workspace metadata: %+v", req.Workspace)
	}
}

func TestRunLoadsReadyPluginHooksWithPluginRuntimeDefaults(t *testing.T) {
	t.Parallel()

	pluginRoot, err := skillset.PluginDirForID("github")
	if err != nil {
		t.Fatalf("plugin root: %v", err)
	}
	pluginHooksPath, err := skillset.PluginHooksPathForID("github")
	if err != nil {
		t.Fatalf("plugin hooks path: %v", err)
	}
	disabledHooksPath, err := skillset.PluginHooksPathForID("disabled")
	if err != nil {
		t.Fatalf("disabled hooks path: %v", err)
	}
	needsAuthHooksPath, err := skillset.PluginHooksPathForID("needsauth")
	if err != nil {
		t.Fatalf("needs auth hooks path: %v", err)
	}
	forgedHooksPath, err := skillset.PluginHooksPathForID("forged")
	if err != nil {
		t.Fatalf("forged hooks path: %v", err)
	}

	largePluginContext := "HEAD\n" + strings.Repeat("plugin context detail ", 300) + "\nTAIL"
	pluginPayload, err := json.Marshal(map[string]string{
		"decision":       DecisionAppendContext,
		"append_context": largePluginContext,
	})
	if err != nil {
		t.Fatalf("marshal plugin hook output: %v", err)
	}

	userCfg := `{
		"version": 1,
		"defaults": {"max_output_bytes": 4096},
		"env": {"USER_ENV": "enabled"},
		"hooks": [{
			"name": "user logger",
			"event": "PostToolUse",
			"matcher": "^exec$",
			"priority": 10,
			"actions": [{"type": "tool", "tool": "user_hook"}]
		}]
	}`
	pluginCfg := `{
		"version": 1,
		"defaults": {"max_output_bytes": 192},
		"env": {"PLUGIN_ENV": "enabled"},
		"hooks": [{
			"name": "plugin logger",
			"event": "PostToolUse",
			"matcher": "^exec$",
			"priority": 10,
			"actions": [{"type": "command", "command": "python scripts/hook.py"}]
		}]
	}`
	ignoredPluginCfg := `{
		"version": 1,
		"hooks": [{
			"name": "ignored",
			"event": "PostToolUse",
			"priority": 100,
			"actions": [{"type": "command", "command": "python ignored.py"}]
		}]
	}`
	server := &hookBridgeTestServer{
		files: map[string][]byte{
			DefaultConfigPath:  []byte(userCfg),
			pluginHooksPath:    []byte(pluginCfg),
			disabledHooksPath:  []byte(ignoredPluginCfg),
			needsAuthHooksPath: []byte(ignoredPluginCfg),
			forgedHooksPath:    []byte(ignoredPluginCfg),
		},
		stdout: string(pluginPayload),
	}
	client := newHookBridgeTestClient(t, server)
	service := NewService(nil, hookBridgeProvider{client: client})
	service.SetPluginService(fakePluginInstallationLister{items: []pluginspkg.Installation{
		{PluginID: "github", Enabled: true, Status: pluginspkg.StatusReady},
		{PluginID: "disabled", Enabled: false, Status: pluginspkg.StatusReady},
		{PluginID: "needsauth", Enabled: true, Status: pluginspkg.StatusNeedsAuth},
	}})
	runner := &fakeToolRunner{}

	result, err := service.Run(context.Background(), Request{
		Event: EventPostToolUse,
		BotID: "bot-1",
		Tool:  &ToolPayload{Name: "exec", Result: "ok"},
		Workspace: WorkspaceInfo{
			CWD:     "/data/workspace",
			Runtime: "container",
		},
	}, runner)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.HooksMatched != 2 || result.ActionsRun != 2 {
		t.Fatalf("hooks matched/actions run = %d/%d, want 2/2", result.HooksMatched, result.ActionsRun)
	}
	if got := []string{result.ActionResults[0].Name, result.ActionResults[1].Name}; !slices.Equal(got, []string{"user_hook", "python scripts/hook.py"}) {
		t.Fatalf("action order = %v, want user hook before plugin hook", got)
	}
	if len(runner.calls) != 1 || runner.calls[0].name != "user_hook" {
		t.Fatalf("tool runner calls = %+v, want user_hook only", runner.calls)
	}
	server.mu.Lock()
	execs := append([]capturedExec(nil), server.execs...)
	server.mu.Unlock()
	if len(execs) != 1 {
		t.Fatalf("exec count = %d, want 1", len(execs))
	}
	exec := execs[0]
	if exec.command != "python scripts/hook.py" {
		t.Fatalf("command = %q, want plugin script command", exec.command)
	}
	if exec.workDir != pluginRoot {
		t.Fatalf("work_dir = %q, want plugin root %q", exec.workDir, pluginRoot)
	}
	for _, want := range []string{
		"PLUGIN_ENV=enabled",
		"MEMOH_PLUGIN_ID=github",
		"MEMOH_PLUGIN_DIR=" + pluginRoot,
		"MEMOH_HOOK_NAME=plugin:github:plugin logger",
	} {
		if !slices.Contains(exec.env, want) {
			t.Fatalf("env = %v, missing %q", exec.env, want)
		}
	}
	if slices.Contains(exec.env, "USER_ENV=enabled") {
		t.Fatalf("plugin command env leaked user env: %v", exec.env)
	}
	var req Request
	if err := json.Unmarshal(exec.stdin, &req); err != nil {
		t.Fatalf("stdin is not hook request JSON: %v", err)
	}
	if req.HookName != "plugin:github:plugin logger" {
		t.Fatalf("stdin hook name = %q, want plugin-prefixed hook name", req.HookName)
	}
	sources, ok := result.Metadata["hook_sources"].([]map[string]any)
	if !ok {
		t.Fatalf("hook_sources metadata = %#v, want []map[string]any", result.Metadata["hook_sources"])
	}
	if len(sources) != 2 || sources[0]["source_kind"] != sourceKindUser || sources[1]["source_kind"] != sourceKindPlugin || sources[1]["plugin_id"] != "github" {
		t.Fatalf("hook_sources = %#v, want user then github plugin", sources)
	}
	if len(result.AppendContext) > 192 {
		t.Fatalf("plugin append_context bytes = %d, want <= plugin cap 192", len(result.AppendContext))
	}
	if len(result.AppendContext) >= len(largePluginContext) {
		t.Fatalf("plugin append_context was not limited by loaded plugin cap")
	}
	assertHookTextPreservesHeadTail(t, result.AppendContext)
}

func TestLoadCreatesEmptyConfigWhenMissing(t *testing.T) {
	t.Parallel()

	server := &hookBridgeTestServer{files: map[string][]byte{}}
	client := newHookBridgeTestClient(t, server)
	service := NewService(nil, hookBridgeProvider{client: client})

	cfg, exists, err := service.Load(context.Background(), "bot-1")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if !exists {
		t.Fatal("Load exists = false, want true after creating default config")
	}
	if cfg.Version != 1 {
		t.Fatalf("version = %d, want 1", cfg.Version)
	}
	if cfg.Enabled == nil || !*cfg.Enabled {
		t.Fatalf("enabled = %v, want true", cfg.Enabled)
	}
	if len(cfg.Hooks) != 0 {
		t.Fatalf("hooks len = %d, want 0", len(cfg.Hooks))
	}

	server.mu.Lock()
	raw := append([]byte(nil), server.files[DefaultConfigPath]...)
	server.mu.Unlock()
	if len(raw) == 0 {
		t.Fatalf("default hooks file was not written at %s", DefaultConfigPath)
	}
	var written struct {
		Version int   `json:"version"`
		Enabled bool  `json:"enabled"`
		Hooks   []any `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &written); err != nil {
		t.Fatalf("written config is not JSON: %v", err)
	}
	if written.Version != 1 || !written.Enabled {
		t.Fatalf("written config = %+v, want version 1 and enabled true", written)
	}
	if written.Hooks == nil {
		t.Fatal("written config omitted hooks field, want empty hooks array")
	}
	if len(written.Hooks) != 0 {
		t.Fatalf("written hooks len = %d, want 0", len(written.Hooks))
	}
}

type hookBridgeProvider struct {
	client *bridge.Client
}

func (p hookBridgeProvider) MCPClient(context.Context, string) (*bridge.Client, error) {
	return p.client, nil
}

type fakePluginInstallationLister struct {
	items []pluginspkg.Installation
	err   error
}

func (l fakePluginInstallationLister) List(context.Context, string) ([]pluginspkg.Installation, error) {
	return l.items, l.err
}

type capturedExec struct {
	command string
	workDir string
	timeout int32
	stdin   []byte
	env     []string
}

type hookBridgeTestServer struct {
	bridgepb.UnimplementedContainerServiceServer
	files    map[string][]byte
	stdout   string
	stderr   string
	exitCode int32

	mu    sync.Mutex
	execs []capturedExec
}

func (s *hookBridgeTestServer) ReadRaw(req *bridgepb.ReadRawRequest, stream bridgepb.ContainerService_ReadRawServer) error {
	s.mu.Lock()
	data, ok := s.files[req.GetPath()]
	data = append([]byte(nil), data...)
	s.mu.Unlock()
	if !ok {
		return status.Errorf(codes.NotFound, "open: open %s: no such file or directory", req.GetPath())
	}
	if len(data) == 0 {
		return nil
	}
	return stream.Send(&bridgepb.DataChunk{Data: data})
}

func (s *hookBridgeTestServer) WriteFile(_ context.Context, req *bridgepb.WriteFileRequest) (*bridgepb.WriteFileResponse, error) {
	if strings.TrimSpace(req.GetPath()) == "" {
		return nil, status.Error(codes.InvalidArgument, "path is required")
	}
	s.mu.Lock()
	if s.files == nil {
		s.files = map[string][]byte{}
	}
	s.files[req.GetPath()] = append([]byte(nil), req.GetContent()...)
	s.mu.Unlock()
	return &bridgepb.WriteFileResponse{}, nil
}

func (s *hookBridgeTestServer) Exec(stream bridgepb.ContainerService_ExecServer) error {
	input, err := stream.Recv()
	if err != nil {
		return err
	}
	var stdin []byte
	if data := input.GetStdinData(); len(data) > 0 {
		stdin = append(stdin, data...)
	}
	for {
		msg, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			return recvErr
		}
		if data := msg.GetStdinData(); len(data) > 0 {
			stdin = append(stdin, data...)
		}
	}
	s.mu.Lock()
	s.execs = append(s.execs, capturedExec{
		command: input.GetCommand(),
		workDir: input.GetWorkDir(),
		timeout: input.GetTimeoutSeconds(),
		stdin:   stdin,
		env:     append([]string(nil), input.GetEnv()...),
	})
	s.mu.Unlock()

	if strings.TrimSpace(s.stdout) != "" {
		if err := stream.Send(&bridgepb.ExecOutput{Stream: bridgepb.ExecOutput_STDOUT, Data: []byte(s.stdout)}); err != nil {
			return err
		}
	}
	if strings.TrimSpace(s.stderr) != "" {
		if err := stream.Send(&bridgepb.ExecOutput{Stream: bridgepb.ExecOutput_STDERR, Data: []byte(s.stderr)}); err != nil {
			return err
		}
	}
	return stream.Send(&bridgepb.ExecOutput{Stream: bridgepb.ExecOutput_EXIT, ExitCode: s.exitCode})
}

func newHookBridgeTestClient(t *testing.T, server bridgepb.ContainerServiceServer) *bridge.Client {
	t.Helper()

	listener := bufconn.Listen(hookTestBufSize)
	grpcServer := grpc.NewServer()
	bridgepb.RegisterContainerServiceServer(grpcServer, server)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = grpcServer.Serve(listener)
	}()
	t.Cleanup(func() {
		grpcServer.Stop()
		<-done
	})

	dialer := func(ctx context.Context, _ string) (net.Conn, error) {
		return listener.DialContext(ctx)
	}
	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
	})
	return bridge.NewClientFromConn(conn)
}
