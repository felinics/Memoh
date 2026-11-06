package native

import (
	"context"
	"strings"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	tools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/agent/toolexec"
	"github.com/felinics/memoh/internal/hooks"
)

const onDemandUsageMarker = "ON_DEMAND_USAGE_marker"

// onDemandTestProvider registers schedule tools, which load on demand, with
// definitions large enough that deferring them pays for the bridge.
type onDemandTestProvider struct{}

func (onDemandTestProvider) Tools(context.Context, tools.SessionContext) ([]toolexec.Tool, error) {
	var list []toolexec.Tool
	for _, name := range []tools.ToolName{tools.ToolListSchedule(), tools.ToolCreateSchedule()} {
		list = append(list, toolexec.Tool{
			Name:        name.String(),
			Description: strings.Repeat("schedule tool description ", 80),
			Parameters: toolexec.SchemaFromValue(map[string]any{
				"type":       "object",
				"properties": map[string]any{"name": map[string]any{"type": "string"}},
			}),
			Execute: func(ctx *toolexec.ToolExecContext, _ sdk.ToolArguments) (sdk.ToolOutput, error) {
				return toolexec.OutputFromValue(map[string]any{"ran": ctx.ToolName}), nil
			},
		})
	}
	return list, nil
}

func (onDemandTestProvider) Usage(context.Context, tools.SessionContext, tools.AvailableTools) string {
	return onDemandUsageMarker
}

func TestAssembleToolsMovesOnDemandToolsBehindTheBridge(t *testing.T) {
	t.Parallel()
	a := newTestAgent(&usageTestProvider{emitTool: true, usage: usageMarker}, onDemandTestProvider{})

	assembled, err := a.assembleTools(context.Background(), RunConfig{}, nil, false)
	if err != nil {
		t.Fatalf("assembleTools: %v", err)
	}
	if assembled.bridge == nil {
		t.Fatal("bridge = nil, want schedule tools on demand")
	}
	var wire []string
	for _, def := range assembled.defs {
		wire = append(wire, def.Name)
	}
	want := []string{"fake_tool", tools.ToolDescribe().String(), tools.ToolCall().String()}
	if strings.Join(wire, ",") != strings.Join(want, ",") {
		t.Fatalf("request tool definitions = %v, want %v", wire, want)
	}
	if !strings.Contains(assembled.usage, usageMarker) {
		t.Fatalf("usage = %q, want the visible provider's guidance", assembled.usage)
	}
	if strings.Contains(assembled.usage, onDemandUsageMarker) {
		t.Fatalf("usage = %q, want the on-demand guidance out of the system prompt", assembled.usage)
	}

	exec, _ := a.wrapExecutableTools(context.Background(), RunConfig{}, assembled.tools, assembled.bridge, newToolExecutionMetadataRegistry(nil), nil, newToolAbortRegistry())
	var execNames []string
	for _, tool := range exec {
		execNames = append(execNames, tool.Name)
	}
	if strings.Join(execNames, ",") != strings.Join(want, ",") {
		t.Fatalf("executable tools = %v, want %v", execNames, want)
	}
	describe := findTool(t, exec, tools.ToolDescribe().String())
	output, err := describe.Execute(&toolexec.ToolExecContext{Context: context.Background(), ToolName: describe.Name}, toolexec.ArgumentsFromValue(map[string]any{"names": []any{"schedule"}}))
	if err != nil {
		t.Fatalf("tool_describe: %v", err)
	}
	if !strings.Contains(string(output.JSON), onDemandUsageMarker) {
		t.Fatalf("tool_describe output = %s, want the schedule guidance", output.JSON)
	}
}

func TestAssembleToolsSendsEveryToolWhenOnDemandToolsAreOff(t *testing.T) {
	t.Parallel()
	a := New(Deps{OnDemandTools: "off"})
	a.SetToolProviders([]tools.ToolProvider{onDemandTestProvider{}})

	assembled, err := a.assembleTools(context.Background(), RunConfig{}, nil, false)
	if err != nil {
		t.Fatalf("assembleTools: %v", err)
	}
	if assembled.bridge != nil {
		t.Fatal("bridge must stay off when on_demand_tools is off")
	}
	if len(assembled.defs) != 2 || !strings.Contains(assembled.usage, onDemandUsageMarker) {
		t.Fatalf("defs = %#v usage = %q, want both schedule tools and their guidance", assembled.defs, assembled.usage)
	}
}

func TestBridgedCallRunsTheToolHooksUnderItsOwnName(t *testing.T) {
	t.Parallel()
	svc := &turnHookRecorder{mockExecContainerService: newMockExecContainerService(), requests: make(chan hooks.Request, 4)}
	svc.written[hooks.DefaultConfigPath] = []byte(`{
		"version": 1,
		"enabled": true,
		"hooks": [{
			"name": "after tool",
			"event": "PostToolUse",
			"actions": [{"type": "command", "command": "record-tool"}]
		}]
	}`)
	provider, cleanup := setupExecTestInfra(t, svc)
	t.Cleanup(cleanup)
	a := New(Deps{BridgeProvider: provider, HookService: hooks.NewService(nil, provider)})
	a.SetToolProviders([]tools.ToolProvider{onDemandTestProvider{}})
	cfg := RunConfig{}
	cfg.Identity.BotID = "bot-1"

	assembled, err := a.assembleTools(context.Background(), cfg, nil, false)
	if err != nil {
		t.Fatalf("assembleTools: %v", err)
	}
	exec, _ := a.wrapExecutableTools(context.Background(), cfg, assembled.tools, assembled.bridge, newToolExecutionMetadataRegistry(nil), nil, newToolAbortRegistry())
	call := findTool(t, exec, tools.ToolCall().String())
	output, err := call.Execute(&toolexec.ToolExecContext{Context: context.Background(), ToolCallID: "call-1", ToolName: call.Name}, toolexec.ArgumentsFromValue(map[string]any{
		"name": tools.ToolCreateSchedule().String(), "arguments": map[string]any{"name": "daily"},
	}))
	if err != nil {
		t.Fatalf("tool_call: %v", err)
	}
	if !strings.Contains(string(output.JSON), tools.ToolCreateSchedule().String()) {
		t.Fatalf("output = %s, want create_schedule's own result", output.JSON)
	}
	select {
	case req := <-svc.requests:
		if req.Tool == nil || req.Tool.Name != tools.ToolCreateSchedule().String() || req.Tool.CallID != "call-1" {
			t.Fatalf("hook tool = %#v, want create_schedule under call-1", req.Tool)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("PostToolUse hook did not run for the bridged tool")
	}
	select {
	case req := <-svc.requests:
		t.Fatalf("hook ran again for %#v; the bridge must not run hooks of its own", req.Tool)
	case <-time.After(200 * time.Millisecond):
	}
}

func findTool(t *testing.T, list []toolexec.Tool, name string) toolexec.Tool {
	t.Helper()
	for _, tool := range list {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("no tool %q", name)
	return toolexec.Tool{}
}
