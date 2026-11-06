package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/jsonschema-go/jsonschema"

	"github.com/felinics/memoh/internal/agent/toolexec"
)

// onDemandGroup is a set of tools whose definitions stay out of the provider
// request until the model loads them with tool_describe.
type onDemandGroup struct {
	id      string
	summary string
	tools   []ToolName
}

// onDemandGroups lists the groups loaded on demand, in directory order. A
// group qualifies when its definitions are large and most sessions never call
// it. Tools that run in most turns, that ask the user, that deliver messages,
// or that need approval before they run stay in the request.
func onDemandGroups() []onDemandGroup {
	return []onDemandGroup{
		{
			id:      "schedule",
			summary: "Create, inspect, change or delete cron-scheduled tasks and reminders.",
			tools:   []ToolName{ToolListSchedule(), ToolGetSchedule(), ToolCreateSchedule(), ToolUpdateSchedule(), ToolDeleteSchedule()},
		},
		{
			id:      "browser",
			summary: "Open and operate web pages in the workspace browser, inspect or drive the workspace desktop, and take screenshots.",
			tools:   []ToolName{ToolBrowserObserve(), ToolBrowserAction(), ToolComputerObserve(), ToolComputerAction(), ToolBrowserRemoteSession()},
		},
		{
			id:      "capability",
			summary: "Connect or manage MCP servers, and find, install, update or authorize Apps from the catalog.",
			tools:   []ToolName{ToolAppSearch(), ToolAppManage(), ToolMCPManage()},
		},
	}
}

// IsBridgeTool reports whether name is one of the two tools that expose
// on-demand tools. They are plumbing: hooks and per-tool policy apply to the
// tool a bridged call runs, not to the bridge.
func IsBridgeTool(name string) bool {
	name = strings.TrimSpace(name)
	return name == ToolDescribe().String() || name == ToolCall().String()
}

// ToolBridge keeps rarely used tools out of the provider request. The model
// reads their definitions with tool_describe and runs them with tool_call, so
// loading a tool appends to the conversation instead of changing the tools
// array or the system prompt, and the cached request prefix stays intact.
type ToolBridge struct {
	groups []bridgeGroup
	group  map[string]string
	defs   map[string]toolexec.Tool
	usage  map[string][]string

	mu   sync.RWMutex
	exec map[string]toolexec.Tool
}

type bridgeGroup struct {
	id      string
	summary string
	tools   []string
}

// PlanToolBridge decides which of the assembled tools load on demand. It
// returns nil when no on-demand group is present, or when the deferred
// definitions are too small to pay for the two bridge tools.
func PlanToolBridge(assembled []toolexec.Tool) *ToolBridge {
	byName := make(map[string]toolexec.Tool, len(assembled))
	for _, tool := range assembled {
		byName[strings.TrimSpace(tool.Name)] = tool
	}
	b := &ToolBridge{
		group: map[string]string{},
		defs:  map[string]toolexec.Tool{},
		usage: map[string][]string{},
	}
	deferredBytes := 0
	for _, spec := range onDemandGroups() {
		group := bridgeGroup{id: spec.id, summary: spec.summary}
		for _, name := range spec.tools {
			tool, ok := byName[name.String()]
			if !ok || tool.Execute == nil || tool.RequireApproval {
				continue
			}
			group.tools = append(group.tools, tool.Name)
			b.group[tool.Name] = spec.id
			b.defs[tool.Name] = tool
			deferredBytes += definitionBytes(tool)
		}
		if len(group.tools) > 0 {
			b.groups = append(b.groups, group)
		}
	}
	if len(b.groups) == 0 {
		return nil
	}
	bridgeBytes := 0
	for _, tool := range b.Tools() {
		bridgeBytes += definitionBytes(tool)
	}
	if deferredBytes < 2*bridgeBytes {
		return nil
	}
	return b
}

func definitionBytes(tool toolexec.Tool) int {
	data, err := json.Marshal(tool)
	if err != nil {
		return len(tool.Name) + len(tool.Description)
	}
	return len(data)
}

// Defers reports whether the bridge keeps name out of the request.
func (b *ToolBridge) Defers(name string) bool {
	if b == nil {
		return false
	}
	_, ok := b.group[strings.TrimSpace(name)]
	return ok
}

// DefersAll reports whether every named tool loads on demand, and so whether
// guidance written for exactly those tools belongs with their definitions.
func (b *ToolBridge) DefersAll(names []string) bool {
	if b == nil || len(names) == 0 {
		return false
	}
	for _, name := range names {
		if !b.Defers(name) {
			return false
		}
	}
	return true
}

// AddUsage files usage guidance for an on-demand tool's group. tool_describe
// returns it with the definitions, so the guidance reaches the model when the
// tools do instead of sitting in the system prompt.
func (b *ToolBridge) AddUsage(toolName, text string) {
	if b == nil {
		return
	}
	group, ok := b.group[strings.TrimSpace(toolName)]
	text = strings.TrimSpace(text)
	if !ok || text == "" {
		return
	}
	b.usage[group] = append(b.usage[group], text)
}

// Partition splits tools into those sent to the provider and those the bridge
// keeps back.
func (b *ToolBridge) Partition(tools []toolexec.Tool) (visible, deferred []toolexec.Tool) {
	if b == nil {
		return tools, nil
	}
	visible = make([]toolexec.Tool, 0, len(tools))
	for _, tool := range tools {
		if b.Defers(tool.Name) {
			deferred = append(deferred, tool)
			continue
		}
		visible = append(visible, tool)
	}
	return visible, deferred
}

// Bind takes the executable chain (output limits, hooks, loop guard) the
// runtime built over every tool, keeps the deferred tools' executables for
// tool_call, and returns the tools the provider request carries.
func (b *ToolBridge) Bind(tools []toolexec.Tool) []toolexec.Tool {
	if b == nil {
		return tools
	}
	visible, deferred := b.Partition(tools)
	bound := make(map[string]toolexec.Tool, len(deferred))
	for _, tool := range deferred {
		bound[tool.Name] = tool
	}
	b.mu.Lock()
	b.exec = bound
	b.mu.Unlock()
	return visible
}

func (b *ToolBridge) executable(name string) (toolexec.Tool, bool) {
	b.mu.RLock()
	tool, ok := b.exec[name]
	b.mu.RUnlock()
	if ok {
		return tool, true
	}
	tool, ok = b.defs[name]
	return tool, ok
}

func (b *ToolBridge) names() []string {
	names := make([]string, 0, len(b.defs))
	for _, group := range b.groups {
		names = append(names, group.tools...)
	}
	return names
}

// Tools returns the two bridge tools. Their definitions depend only on which
// tools load on demand, so they stay byte-identical for a session.
func (b *ToolBridge) Tools() []toolexec.Tool {
	if b == nil {
		return nil
	}
	names := b.names()
	selectors := make([]string, 0, len(names)+len(b.groups))
	for _, group := range b.groups {
		selectors = append(selectors, group.id)
	}
	selectors = append(selectors, names...)
	describe := toolexec.Define(ToolDescribe().String(), b.describeDescription(),
		func(_ *toolexec.ToolExecContext, args describeArgs) (sdk.ToolOutput, error) {
			return toolexec.OutputPair(b.describe(args.Names))
		},
		toolexec.Items("names", func(s *jsonschema.Schema) { s.Enum = stringsAsAny(selectors) }),
	)
	call := toolexec.Tool{
		Name: ToolCall().String(),
		Description: "Run an on-demand tool after loading its definition with `" + ToolDescribe().String() + "`. " +
			"`name` is the tool; `arguments` is the object its parameters describe. The result is that tool's own result.",
		Parameters: toolexec.SchemaFromValue(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "On-demand tool to run.",
					"enum":        stringsAsAny(names),
				},
				"arguments": map[string]any{
					"type":        "object",
					"description": "Arguments matching the tool's parameters.",
				},
			},
			"required": []any{"name", "arguments"},
		}),
		Execute: b.call,
	}
	return []toolexec.Tool{describe, call}
}

func stringsAsAny(values []string) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return out
}

func (b *ToolBridge) describeDescription() string {
	var sb strings.Builder
	sb.WriteString("Load on-demand tools: their full description, JSON parameters and usage notes. ")
	sb.WriteString("They are not in your tool list. Load a tool before its first use, then run it with `")
	sb.WriteString(ToolCall().String())
	sb.WriteString("`. Pass tool names, or a group name for the whole group. Loading runs nothing.\n\nOn-demand tools:")
	for _, group := range b.groups {
		sb.WriteString("\n- ")
		sb.WriteString(group.id)
		sb.WriteString(": ")
		sb.WriteString(group.summary)
		sb.WriteString(" (")
		sb.WriteString(strings.Join(group.tools, ", "))
		sb.WriteString(")")
	}
	return sb.String()
}

type describeArgs struct {
	Names []string `json:"names" jsonschema:"Tool names, or group names to load every tool in the group."`
}

type describedTool struct {
	Name        string         `json:"name"`
	Group       string         `json:"group"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

func (b *ToolBridge) describe(selectors []string) (any, error) {
	if len(selectors) == 0 {
		return nil, errors.New("names is required: pass tool names or group names from the list")
	}
	wanted := map[string]bool{}
	var unknown []string
	for _, raw := range selectors {
		selector := strings.TrimSpace(raw)
		matched := false
		for _, group := range b.groups {
			if group.id == selector {
				for _, name := range group.tools {
					wanted[name] = true
				}
				matched = true
			}
		}
		if _, ok := b.defs[selector]; ok {
			wanted[selector] = true
			matched = true
		}
		if !matched {
			unknown = append(unknown, selector)
		}
	}
	result := map[string]any{}
	var described []describedTool
	usage := map[string]string{}
	for _, group := range b.groups {
		for _, name := range group.tools {
			if !wanted[name] {
				continue
			}
			def := b.defs[name]
			described = append(described, describedTool{
				Name:        name,
				Group:       group.id,
				Description: def.Description,
				Parameters:  toolexec.SchemaValue(def.Parameters),
			})
			if texts := b.usage[group.id]; len(texts) > 0 {
				usage[group.id] = strings.Join(texts, "\n\n")
			}
		}
	}
	result["tools"] = described
	if len(usage) > 0 {
		result["usage"] = usage
	}
	if len(unknown) > 0 {
		result["unknown"] = unknown
		result["available"] = b.names()
	}
	return result, nil
}

type callArgs struct {
	Name      string `json:"name"`
	Arguments any    `json:"arguments"`
}

func (b *ToolBridge) call(ctx *toolexec.ToolExecContext, input sdk.ToolArguments) (sdk.ToolOutput, error) {
	args, err := toolexec.DecodeArguments[callArgs](ToolCall().String(), input)
	if err != nil {
		return sdk.ToolOutput{}, err
	}
	name := strings.TrimSpace(args.Name)
	def, ok := b.defs[name]
	if !ok {
		if IsBridgeTool(name) {
			return sdk.ToolOutput{}, fmt.Errorf("%s runs on-demand tools; call %s directly", ToolCall().String(), name)
		}
		return sdk.ToolOutput{}, fmt.Errorf("%q is not an on-demand tool; call it directly if it is in your tool list. On-demand tools: %s", name, strings.Join(b.names(), ", "))
	}
	arguments, err := callArguments(args.Arguments)
	if err != nil {
		return sdk.ToolOutput{}, fmt.Errorf("invalid arguments for %s: %w", name, err)
	}
	if err := validateArguments(def, arguments); err != nil {
		return sdk.ToolOutput{}, err
	}
	tool, ok := b.executable(name)
	if !ok || tool.Execute == nil {
		return sdk.ToolOutput{}, fmt.Errorf("tool %q has no execute handler", name)
	}
	inner := &toolexec.ToolExecContext{ToolName: name}
	if ctx != nil {
		inner.Context = ctx.Context
		inner.ToolCallID = ctx.ToolCallID
		inner.SendProgress = ctx.SendProgress
	}
	return tool.Execute(inner, toolexec.ArgumentsFromValue(arguments))
}

// callArguments accepts the arguments object, and the same object encoded as
// a JSON string, which weaker models send for a nested object.
func callArguments(raw any) (map[string]any, error) {
	switch v := raw.(type) {
	case nil:
		return map[string]any{}, nil
	case map[string]any:
		return v, nil
	case string:
		var decoded map[string]any
		if err := json.Unmarshal([]byte(v), &decoded); err != nil || decoded == nil {
			return nil, errors.New("arguments must be an object")
		}
		return decoded, nil
	}
	return nil, errors.New("arguments must be an object")
}

// validateArguments reports required properties the call left out, with the
// tool's parameters so the model can retry. A provider enforces required
// properties for tools in the request; types and enums stay with the tool's
// own decoder, which accepts the lenient forms it always has.
func validateArguments(def toolexec.Tool, arguments map[string]any) error {
	if def.Parameters == nil {
		return nil
	}
	var missing []string
	for _, property := range def.Parameters.Required {
		if _, ok := arguments[property]; !ok {
			missing = append(missing, property)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("invalid arguments for %s: missing required %s. Expected parameters: %s", def.Name, strings.Join(missing, ", "), compactSchema(def.Parameters))
}

func compactSchema(schema *jsonschema.Schema) string {
	value := toolexec.SchemaValue(schema)
	data, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	const limit = 2000
	if len(data) > limit {
		return string(data[:limit]) + "…"
	}
	return string(data)
}

// OnDemandToolNames returns the tool names the bridge keeps back, sorted, for
// diagnostics and tests.
func (b *ToolBridge) OnDemandToolNames() []string {
	if b == nil {
		return nil
	}
	names := b.names()
	sort.Strings(names)
	return names
}
