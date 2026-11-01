// Package toolbridge names the two tools that stand in for on-demand tools
// and maps a bridged call back to the tool it ran.
//
// Rarely used tools stay out of the provider request: the model reads their
// definitions with tool_describe and runs them with tool_call. The request
// prefix never changes when a tool is loaded, so prompt caching keeps
// hitting on every provider, including those without native deferred
// loading. History keeps the tool_call the model emitted; surfaces that
// render calls by tool name (the Web view, channel progress messages) use
// Unwrap to show the tool that actually ran.
package toolbridge

import (
	"encoding/json"
	"strings"
)

const (
	// DescribeToolName loads the definitions of on-demand tools.
	DescribeToolName = "tool_describe"
	// CallToolName runs one on-demand tool.
	CallToolName = "tool_call"
)

// Unwrap returns the tool a tool_call invocation targets and the arguments it
// passed. ok is false for any other tool, and for a tool_call whose input does
// not name a tool yet (a call still streaming its arguments).
func Unwrap(name string, input any) (string, any, bool) {
	if strings.TrimSpace(name) != CallToolName {
		return name, input, false
	}
	fields, ok := inputObject(input)
	if !ok {
		return name, input, false
	}
	target, _ := fields["name"].(string)
	target = strings.TrimSpace(target)
	if target == "" || target == CallToolName || target == DescribeToolName {
		return name, input, false
	}
	arguments, present := fields["arguments"]
	if !present || arguments == nil {
		arguments = map[string]any{}
	}
	return target, arguments, true
}

func inputObject(input any) (map[string]any, bool) {
	switch v := input.(type) {
	case map[string]any:
		return v, true
	case json.RawMessage:
		return decodeObject(v)
	case []byte:
		return decodeObject(v)
	case string:
		return decodeObject([]byte(v))
	}
	return nil, false
}

func decodeObject(raw []byte) (map[string]any, bool) {
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, false
	}
	return fields, true
}
