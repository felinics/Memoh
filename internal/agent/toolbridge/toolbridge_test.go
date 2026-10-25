package toolbridge

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestUnwrapReturnsTheBridgedTool(t *testing.T) {
	t.Parallel()

	args := map[string]any{"id": "s1"}
	for _, input := range []any{
		map[string]any{"name": "get_schedule", "arguments": args},
		json.RawMessage(`{"name":"get_schedule","arguments":{"id":"s1"}}`),
		[]byte(`{"name":"get_schedule","arguments":{"id":"s1"}}`),
		`{"name":"get_schedule","arguments":{"id":"s1"}}`,
	} {
		name, got, ok := Unwrap(CallToolName, input)
		if !ok || name != "get_schedule" || !reflect.DeepEqual(got, args) {
			t.Fatalf("Unwrap(%T) = %q, %#v, %v; want get_schedule, %#v, true", input, name, got, ok, args)
		}
	}
}

func TestUnwrapDefaultsMissingArgumentsToAnEmptyObject(t *testing.T) {
	t.Parallel()

	name, got, ok := Unwrap(CallToolName, map[string]any{"name": "list_schedule"})
	if !ok || name != "list_schedule" || !reflect.DeepEqual(got, map[string]any{}) {
		t.Fatalf("Unwrap = %q, %#v, %v; want list_schedule, {}, true", name, got, ok)
	}
}

func TestUnwrapLeavesOtherCallsAlone(t *testing.T) {
	t.Parallel()

	input := map[string]any{"name": "get_schedule"}
	for _, tc := range []struct {
		name  string
		input any
	}{
		{name: "read", input: input},
		{name: DescribeToolName, input: input},
		{name: CallToolName, input: nil},
		{name: CallToolName, input: `{"name":`},
		{name: CallToolName, input: map[string]any{"arguments": map[string]any{}}},
		{name: CallToolName, input: map[string]any{"name": CallToolName}},
		{name: CallToolName, input: map[string]any{"name": DescribeToolName}},
	} {
		name, got, ok := Unwrap(tc.name, tc.input)
		if ok || name != tc.name || !reflect.DeepEqual(got, tc.input) {
			t.Fatalf("Unwrap(%q, %#v) = %q, %#v, %v; want it unchanged", tc.name, tc.input, name, got, ok)
		}
	}
}
