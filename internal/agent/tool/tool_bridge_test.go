package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/toolexec"
)

// bridgeTestTool is an on-demand candidate large enough that deferring it
// pays for the two bridge tools.
func bridgeTestTool(name ToolName, required ...string) toolexec.Tool {
	properties := map[string]any{}
	for _, property := range required {
		properties[property] = map[string]any{"type": "string", "description": strings.Repeat("detail ", 40)}
	}
	return toolexec.Tool{
		Name:        name.String(),
		Description: name.String() + ": " + strings.Repeat("long description ", 60),
		Parameters: toolexec.SchemaFromValue(map[string]any{
			"type":       "object",
			"properties": properties,
			"required":   stringsAsAny(required),
		}),
		Execute: func(ctx *toolexec.ToolExecContext, input sdk.ToolArguments) (sdk.ToolOutput, error) {
			return toolexec.OutputFromValue(map[string]any{
				"ran":     ctx.ToolName,
				"call_id": ctx.ToolCallID,
				"input":   toolexec.ArgumentsValue(input),
			}), nil
		},
	}
}

func bridgeTestSet() []toolexec.Tool {
	return []toolexec.Tool{
		bridgeTestTool(ToolRead(), "path"),
		bridgeTestTool(ToolCreateSchedule(), "name", "pattern"),
		bridgeTestTool(ToolListSchedule()),
		bridgeTestTool(ToolMCPManage(), "action"),
	}
}

func bridgeTool(t *testing.T, b *ToolBridge, name ToolName) toolexec.Tool {
	t.Helper()
	for _, tool := range b.Tools() {
		if tool.Name == name.String() {
			return tool
		}
	}
	t.Fatalf("bridge has no %s", name.String())
	return toolexec.Tool{}
}

func outputMap(t *testing.T, output sdk.ToolOutput) map[string]any {
	t.Helper()
	value, ok := toolexec.OutputValue(output).(map[string]any)
	if !ok {
		t.Fatalf("output = %#v, want an object", toolexec.OutputValue(output))
	}
	return value
}

func TestPlanToolBridgeDefersOnlyOnDemandGroups(t *testing.T) {
	t.Parallel()

	b := PlanToolBridge(bridgeTestSet())
	if b == nil {
		t.Fatal("PlanToolBridge = nil, want a bridge for schedule and capability tools")
	}
	want := []string{ToolCreateSchedule().String(), ToolListSchedule().String(), ToolMCPManage().String()}
	if got := b.OnDemandToolNames(); !reflect.DeepEqual(got, want) {
		t.Fatalf("on-demand tools = %v, want %v", got, want)
	}
	if b.Defers(ToolRead().String()) {
		t.Fatal("read must stay in the request")
	}
	visible := b.Bind(bridgeTestSet())
	if got := toolNames(visible); !reflect.DeepEqual(got, []string{ToolRead().String()}) {
		t.Fatalf("visible tools = %v, want [read]", got)
	}
}

func TestPlanToolBridgeSkipsSmallOrApprovalGatedTools(t *testing.T) {
	t.Parallel()

	if b := PlanToolBridge([]toolexec.Tool{bridgeTestTool(ToolRead(), "path")}); b != nil {
		t.Fatalf("bridge without on-demand tools = %v, want nil", b.OnDemandToolNames())
	}
	small := toolexec.Tool{Name: ToolGetSchedule().String(), Description: "Get one.", Execute: bridgeTestTool(ToolGetSchedule()).Execute}
	if b := PlanToolBridge([]toolexec.Tool{small}); b != nil {
		t.Fatal("deferring a small tool costs more than the bridge saves")
	}
	gated := bridgeTestTool(ToolCreateSchedule(), "name")
	gated.RequireApproval = true
	if b := PlanToolBridge([]toolexec.Tool{gated}); b != nil {
		t.Fatal("a tool that needs approval before it runs must stay in the request")
	}
}

func TestToolBridgeDefinitionsAreStableAndListTheDirectory(t *testing.T) {
	t.Parallel()

	first, err := json.Marshal(PlanToolBridge(bridgeTestSet()).Tools())
	if err != nil {
		t.Fatal(err)
	}
	second, err := json.Marshal(PlanToolBridge(bridgeTestSet()).Tools())
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("bridge definitions must be byte-identical for the same tool set")
	}
	describe := bridgeTool(t, PlanToolBridge(bridgeTestSet()), ToolDescribe())
	for _, want := range []string{"- schedule:", "list_schedule, create_schedule", "- capability:", "mcp_manage"} {
		if !strings.Contains(describe.Description, want) {
			t.Fatalf("tool_describe description lacks %q:\n%s", want, describe.Description)
		}
	}
	if strings.Contains(describe.Description, "browser") {
		t.Fatal("the directory lists only groups present in the session")
	}
}

func TestToolDescribeReturnsDefinitionsAndUsage(t *testing.T) {
	t.Parallel()

	b := PlanToolBridge(bridgeTestSet())
	b.AddUsage(ToolCreateSchedule().String(), "### Scheduled tasks\n\nConfirm and end the turn.")
	describe := bridgeTool(t, b, ToolDescribe())
	output, err := describe.Execute(&toolexec.ToolExecContext{ToolName: describe.Name}, toolexec.ArgumentsFromValue(map[string]any{
		"names": []any{"schedule", "nope"},
	}))
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	result := outputMap(t, output)
	described, _ := result["tools"].([]any)
	if len(described) != 2 {
		t.Fatalf("described = %#v, want both schedule tools", result["tools"])
	}
	first, _ := described[0].(map[string]any)
	if first["name"] != ToolListSchedule().String() || first["group"] != "schedule" {
		t.Fatalf("first described tool = %#v, want list_schedule in group schedule", first)
	}
	if _, ok := first["parameters"].(map[string]any); !ok {
		t.Fatalf("described tool lacks parameters: %#v", first)
	}
	usage, _ := result["usage"].(map[string]any)
	if !strings.Contains(usage["schedule"].(string), "Confirm and end the turn.") {
		t.Fatalf("usage = %#v, want the schedule guidance", result["usage"])
	}
	if unknown, _ := result["unknown"].([]any); len(unknown) != 1 || unknown[0] != "nope" {
		t.Fatalf("unknown = %#v, want [nope]", result["unknown"])
	}
}

func TestToolCallRunsTheBoundExecutable(t *testing.T) {
	t.Parallel()

	b := PlanToolBridge(bridgeTestSet())
	wrapped := bridgeTestSet()
	for i := range wrapped {
		execute := wrapped[i].Execute
		wrapped[i].Execute = func(ctx *toolexec.ToolExecContext, input sdk.ToolArguments) (sdk.ToolOutput, error) {
			output, err := execute(ctx, input)
			value := toolexec.OutputValue(output).(map[string]any)
			value["wrapped"] = true
			return toolexec.OutputFromValue(value), err
		}
	}
	b.Bind(wrapped)
	call := bridgeTool(t, b, ToolCall())
	output, err := call.Execute(&toolexec.ToolExecContext{Context: context.Background(), ToolCallID: "call-1", ToolName: call.Name}, toolexec.ArgumentsFromValue(map[string]any{
		"name":      ToolCreateSchedule().String(),
		"arguments": `{"name":"daily","pattern":"0 9 * * *"}`,
	}))
	if err != nil {
		t.Fatalf("tool_call: %v", err)
	}
	result := outputMap(t, output)
	if result["ran"] != ToolCreateSchedule().String() || result["call_id"] != "call-1" || result["wrapped"] != true {
		t.Fatalf("result = %#v, want create_schedule run through its bound chain under the outer call id", result)
	}
	if input, _ := result["input"].(map[string]any); input["pattern"] != "0 9 * * *" {
		t.Fatalf("inner input = %#v, want the decoded arguments", result["input"])
	}
}

func TestToolCallRejectsMissingRequiredArgumentsWithTheSchema(t *testing.T) {
	t.Parallel()

	call := bridgeTool(t, PlanToolBridge(bridgeTestSet()), ToolCall())
	_, err := call.Execute(&toolexec.ToolExecContext{ToolName: call.Name}, toolexec.ArgumentsFromValue(map[string]any{
		"name":      ToolCreateSchedule().String(),
		"arguments": map[string]any{"name": "daily"},
	}))
	if err == nil || !strings.Contains(err.Error(), "missing required pattern") || !strings.Contains(err.Error(), "Expected parameters") {
		t.Fatalf("err = %v, want a missing-pattern error with the parameters", err)
	}
}

func TestToolCallRejectsToolsOutsideTheBridge(t *testing.T) {
	t.Parallel()

	call := bridgeTool(t, PlanToolBridge(bridgeTestSet()), ToolCall())
	for _, name := range []string{ToolRead().String(), ToolCall().String(), "unknown"} {
		_, err := call.Execute(&toolexec.ToolExecContext{ToolName: call.Name}, toolexec.ArgumentsFromValue(map[string]any{
			"name": name, "arguments": map[string]any{},
		}))
		if err == nil {
			t.Fatalf("tool_call %q succeeded, want an error", name)
		}
	}
}

func TestToolBridgeDefersAllOnlyForFullyDeferredProviders(t *testing.T) {
	t.Parallel()

	b := PlanToolBridge(bridgeTestSet())
	if !b.DefersAll([]string{ToolCreateSchedule().String(), ToolListSchedule().String()}) {
		t.Fatal("schedule provider tools all load on demand")
	}
	if b.DefersAll([]string{ToolCreateSchedule().String(), ToolRead().String()}) {
		t.Fatal("a provider with a visible tool keeps its guidance in the system prompt")
	}
	var nilBridge *ToolBridge
	if nilBridge.DefersAll([]string{ToolCreateSchedule().String()}) || nilBridge.Defers(ToolCreateSchedule().String()) {
		t.Fatal("a nil bridge defers nothing")
	}
	if got := nilBridge.Bind(bridgeTestSet()); len(got) != len(bridgeTestSet()) {
		t.Fatal("a nil bridge keeps every tool")
	}
}
