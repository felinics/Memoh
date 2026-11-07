package tools

import (
	"context"
	"encoding/json"
	"fmt"
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
	reqAny := make([]any, len(required))
	for i, r := range required {
		reqAny[i] = r
	}
	return toolexec.Tool{
		Name:        name.String(),
		Description: name.String() + ": " + strings.Repeat("long description ", 60),
		Parameters: toolexec.SchemaFromValue(map[string]any{
			"type":       "object",
			"properties": properties,
			"required":   reqAny,
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

func TestToolBridgeDefersLargeMCPGroupWithCompactCatalogAndQuerySearch(t *testing.T) {
	t.Parallel()

	tools := []toolexec.Tool{bridgeTestTool(ToolRead(), "path")}
	for i := 1; i <= 12; i++ {
		name := fmt.Sprintf("notion_action_%d", i)
		desc := fmt.Sprintf("[Notion] Action %d details\n\nExtra paragraph %s", i, strings.Repeat("schema text ", 30))
		if i == 3 {
			name = "notion_create_page"
			desc = "[Notion] Create a new database page\n\n" + strings.Repeat("schema text ", 30)
		}
		tool := bridgeTestTool(ToolRead(), "page_id")
		tool.Name = name
		tool.Description = desc
		tool.Group = "mcp:notion"
		tools = append(tools, tool)
	}

	b := PlanToolBridge(tools)
	if b == nil {
		t.Fatal("PlanToolBridge = nil, want a bridge for MCP tools")
	}
	describe := bridgeTool(t, b, ToolDescribe())
	if !strings.Contains(describe.Description, `- mcp:notion: External MCP tools from Notion. (12 tools — pass "mcp:notion" or a query to browse)`) {
		t.Fatalf("unexpected large-group directory line:\n%s", describe.Description)
	}
	if strings.Contains(describe.Description, "notion_action_1") {
		t.Fatalf("large group must not inline every tool name in the top-level description:\n%s", describe.Description)
	}

	// 1. Browsing the whole large group returns a compact catalog without parameter schemas.
	catalogOut, err := describe.Execute(&toolexec.ToolExecContext{ToolName: describe.Name}, toolexec.ArgumentsFromValue(map[string]any{
		"names": []any{"notion"},
	}))
	if err != nil {
		t.Fatalf("describe group: %v", err)
	}
	catalog := outputMap(t, catalogOut)
	items, _ := catalog["tools"].([]any)
	if len(items) != 12 {
		t.Fatalf("catalog tools = %d, want 12", len(items))
	}
	firstItem, _ := items[0].(map[string]any)
	if _, hasParams := firstItem["parameters"]; hasParams {
		t.Fatalf("compact catalog entry must omit parameters: %#v", firstItem)
	}

	// 2. Filtering by query narrows to matching tools and includes full parameter schemas.
	searchOut, err := describe.Execute(&toolexec.ToolExecContext{ToolName: describe.Name}, toolexec.ArgumentsFromValue(map[string]any{
		"names": []any{"mcp:notion"},
		"query": "create page",
	}))
	if err != nil {
		t.Fatalf("describe query: %v", err)
	}
	searchRes := outputMap(t, searchOut)
	matched, _ := searchRes["tools"].([]any)
	if len(matched) != 1 {
		t.Fatalf("matched tools = %#v, want [notion_create_page]", matched)
	}
	pageTool, _ := matched[0].(map[string]any)
	if pageTool["name"] != "notion_create_page" {
		t.Fatalf("matched tool = %#v, want notion_create_page", pageTool)
	}
	if _, hasParams := pageTool["parameters"].(map[string]any); !hasParams {
		t.Fatalf("narrowed query result must include parameters: %#v", pageTool)
	}
}

func TestToolBridgeGroupsConnectorToolsBySourceAndMatchesShortSelector(t *testing.T) {
	t.Parallel()

	specs := []struct {
		name  string
		group string
		desc  string
	}{
		{name: "list_email", group: "connector:email", desc: "List emails in the mailbox. " + strings.Repeat("detail ", 40)},
		{name: "list_email_accounts", group: "connector:email", desc: "List configured email accounts. " + strings.Repeat("detail ", 40)},
		{name: "send_email", group: "connector:email", desc: "Send an email message. " + strings.Repeat("detail ", 40)},
		{name: "get_issue", group: "connector:github", desc: "Get a repository issue by number. " + strings.Repeat("detail ", 40)},
	}
	tools := []toolexec.Tool{bridgeTestTool(ToolRead(), "path")}
	for _, spec := range specs {
		tool := bridgeTestTool(ToolRead(), "id")
		tool.Name = spec.name
		tool.Description = spec.desc
		tool.Group = spec.group
		tools = append(tools, tool)
	}

	b := PlanToolBridge(tools)
	if b == nil {
		t.Fatal("PlanToolBridge = nil, want bridge for connector tools")
	}
	describe := bridgeTool(t, b, ToolDescribe())
	for _, wantLine := range []string{
		"- connector:email: Connected App tools from email. (list_email, list_email_accounts, send_email)",
		"- connector:github: Connected App tools from github. (get_issue)",
	} {
		if !strings.Contains(describe.Description, wantLine) {
			t.Fatalf("describe directory missing %q:\n%s", wantLine, describe.Description)
		}
	}
	for _, forbidden := range []string{"mcp:list", "mcp:send", "mcp:get"} {
		if strings.Contains(describe.Description, forbidden) {
			t.Fatalf("connector tools must not be grouped by verb (%s):\n%s", forbidden, describe.Description)
		}
	}

	out, err := describe.Execute(&toolexec.ToolExecContext{ToolName: describe.Name}, toolexec.ArgumentsFromValue(map[string]any{
		"names": []any{"email"},
	}))
	if err != nil {
		t.Fatalf("describe email group: %v", err)
	}
	described, _ := outputMap(t, out)["tools"].([]any)
	if len(described) != 3 {
		t.Fatalf("described email tools = %#v, want 3 tools", described)
	}
}
