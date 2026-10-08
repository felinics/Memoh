package toolexec

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
)

type defineArgs struct {
	TaskID  string   `json:"task_id" jsonschema:"Background task ID"`
	Timeout *float64 `json:"timeout,omitempty" jsonschema:"Max seconds"`
	Tags    []string `json:"tags,omitempty"`
	Nested  []struct {
		Label string `json:"label"`
	} `json:"nested,omitempty"`
}

func TestDefineInfersTheHandWrittenSchemaShape(t *testing.T) {
	tool := Define("probe", "desc", func(_ *ToolExecContext, args defineArgs) (sdk.ToolOutput, error) {
		if args.Timeout == nil {
			return sdk.TextOutput(args.TaskID + ":absent"), nil
		}
		return sdk.TextOutput(args.TaskID), nil
	}, Range("timeout", 1, 600), Enum("task_id", "a", "b"))

	got, _ := json.Marshal(SchemaValue(tool.Parameters))
	want := `{"properties":{"nested":{"items":{"properties":{"label":{"type":"string"}},"required":["label"],"type":"object"},"type":"array"},"tags":{"items":{"type":"string"},"type":"array"},"task_id":{"description":"Background task ID","enum":["a","b"],"type":"string"},"timeout":{"description":"Max seconds","maximum":600,"minimum":1,"type":"number"}},"required":["task_id"],"type":"object"}`
	if string(got) != want {
		t.Fatalf("schema\n got  %s\n want %s", got, want)
	}

	out, err := tool.Execute(&ToolExecContext{Context: context.Background()}, sdk.ParseToolArguments(`{"task_id":"t1"}`))
	if err != nil || out.Text != "t1:absent" {
		t.Fatalf("execute = %#v, %v", out, err)
	}
	// A number for a string property is stringified, as the map helpers did.
	out, err = tool.Execute(&ToolExecContext{Context: context.Background()}, sdk.ParseToolArguments(`{"task_id":5}`))
	if err != nil || out.Text != "5:absent" {
		t.Fatalf("execute(number for string) = %#v, %v", out, err)
	}
	if _, err := tool.Execute(&ToolExecContext{Context: context.Background()}, sdk.ParseToolArguments(`{"task_id":{"x":1}}`)); err == nil {
		t.Fatal("a document that does not decode into the struct must fail")
	}
}

func TestDefineEmptyArgumentsKeepProperties(t *testing.T) {
	tool := Define("noargs", "desc", func(*ToolExecContext, struct{}) (sdk.ToolOutput, error) {
		return sdk.TextOutput("ok"), nil
	})
	got, _ := json.Marshal(SchemaValue(tool.Parameters))
	if string(got) != `{"properties":{},"type":"object"}` {
		t.Fatalf("schema = %s", got)
	}
}

type coercionArgs struct {
	Limit   int     `json:"limit"`
	Offset  *int    `json:"offset,omitempty"`
	Ratio   float64 `json:"ratio,omitempty"`
	Name    string  `json:"name"`
	Verbose bool    `json:"verbose,omitempty"`
	Nested  struct {
		Count int `json:"count"`
	} `json:"nested"`
	IDs []int `json:"ids,omitempty"`
}

// The map-based helpers accepted whole-number floats and numeric strings for
// integers and stringified numbers for strings; the typed decode keeps that.
func TestTypedCoercesLenientScalars(t *testing.T) {
	var got coercionArgs
	execute := Typed(func(_ *ToolExecContext, args coercionArgs) (sdk.ToolOutput, error) {
		got = args
		return sdk.ToolOutput{}, nil
	})
	input := sdk.ParseToolArguments(`{"limit": 50.0, "offset": " 2 ", "ratio": "0.5", "name": 123, "verbose": "true", "nested": {"count": 3.0}, "ids": [1.0, "2"]}`)
	if _, err := execute(&ToolExecContext{ToolName: "probe"}, input); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got.Limit != 50 || got.Offset == nil || *got.Offset != 2 || got.Ratio != 0.5 || got.Name != "123" || !got.Verbose || got.Nested.Count != 3 {
		t.Fatalf("decoded = %+v", got)
	}
	if len(got.IDs) != 2 || got.IDs[0] != 1 || got.IDs[1] != 2 {
		t.Fatalf("ids = %v", got.IDs)
	}
}

func TestTypedReportsDecodeErrorsByProperty(t *testing.T) {
	execute := Typed(func(*ToolExecContext, coercionArgs) (sdk.ToolOutput, error) { return sdk.ToolOutput{}, nil })
	_, err := execute(&ToolExecContext{ToolName: "probe"}, sdk.ParseToolArguments(`{"limit": 2.5}`))
	if err == nil || err.Error() != "invalid arguments for probe: limit must be an integer, got number 2.5" {
		t.Fatalf("error = %v", err)
	}
	_, err = execute(&ToolExecContext{ToolName: "probe"}, sdk.ParseToolArguments(`{"name": {"x": 1}}`))
	if err == nil || err.Error() != "invalid arguments for probe: name must be a string, got object" {
		t.Fatalf("error = %v", err)
	}
	// A nil context must not panic; the tool name falls back.
	_, err = execute(nil, sdk.ParseToolArguments(`[1]`))
	if err == nil || err.Error() != "invalid arguments for tool: arguments must be an object, got array" {
		t.Fatalf("nil ctx error = %v", err)
	}
	_, err = execute(&ToolExecContext{ToolName: "probe"}, sdk.ParseToolArguments(`not json`))
	if err == nil || err.Error() != "invalid arguments for probe: not a JSON object" {
		t.Fatalf("invalid text error = %v", err)
	}
}

type ownDecoder struct{ raw string }

func (d *ownDecoder) UnmarshalJSON(data []byte) error { d.raw = string(data); return nil }

// A field with its own UnmarshalJSON defines its own tolerance; coercion must
// not rewrite what it receives.
func TestTypedLeavesCustomDecodersAlone(t *testing.T) {
	type args struct {
		Value ownDecoder `json:"value"`
		Limit int        `json:"limit"`
	}
	var got args
	execute := Typed(func(_ *ToolExecContext, a args) (sdk.ToolOutput, error) { got = a; return sdk.ToolOutput{}, nil })
	// The SDK canonicalizes the document first (2.0 would already read 2), so
	// the custom decoder is probed with a value canonical form keeps and the
	// integer field with the string form only coercion accepts.
	if _, err := execute(&ToolExecContext{ToolName: "probe"}, sdk.ParseToolArguments(`{"value": 2.5, "limit": "2"}`)); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got.Value.raw != "2.5" || got.Limit != 2 {
		t.Fatalf("decoded = %+v", got)
	}
}

type coercionEdgeArgs struct {
	coercionEmbedded
	N     int64   `json:"n"`
	F     float64 `json:"f,omitempty"`
	Limit int     `json:"limit,omitempty"`
}

type coercionEmbedded struct {
	Count int `json:"count"`
}

// Integer strings are taken exactly, never through binary64; a float string
// is accepted only as a JSON number literal; embedded struct fields are
// coerced against the same object.
func TestTypedCoercionEdges(t *testing.T) {
	var got coercionEdgeArgs
	execute := Typed(func(_ *ToolExecContext, a coercionEdgeArgs) (sdk.ToolOutput, error) {
		got = a
		return sdk.ToolOutput{}, nil
	})
	if _, err := execute(&ToolExecContext{ToolName: "probe"}, sdk.ParseToolArguments(`{"n":"9007199254740993","f":"1.5","count":"3","limit":"2"}`)); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got.N != 9007199254740993 || got.F != 1.5 || got.Count != 3 || got.Limit != 2 {
		t.Fatalf("decoded = %+v", got)
	}
	// A float string that is not a JSON number must not abort coercion of
	// the rest of the document; it is reported on its own property.
	_, err := execute(&ToolExecContext{ToolName: "probe"}, sdk.ParseToolArguments(`{"n":"5","f":"+1"}`))
	if err == nil || err.Error() != "invalid arguments for probe: f must be a number, got string" {
		t.Fatalf("error = %v", err)
	}
}

type strictDecoder struct{ v int }

func (d *strictDecoder) UnmarshalJSON(data []byte) error {
	var n int
	if err := json.Unmarshal(data, &n); err != nil {
		return fmt.Errorf("max_calls must be an integer or null: %w", err)
	}
	d.v = n
	return nil
}

// A custom decoder's own message names the property; it is kept rather than
// rewritten into a root-level "arguments must be …".
func TestTypedKeepsCustomDecoderMessage(t *testing.T) {
	type args struct {
		MaxCalls strictDecoder `json:"max_calls"`
	}
	execute := Typed(func(*ToolExecContext, args) (sdk.ToolOutput, error) { return sdk.ToolOutput{}, nil })
	_, err := execute(&ToolExecContext{ToolName: "update_schedule"}, sdk.ParseToolArguments(`{"max_calls":"5"}`))
	if err == nil || !strings.Contains(err.Error(), "max_calls must be an integer or null") || strings.Contains(err.Error(), "arguments must be") {
		t.Fatalf("error = %v", err)
	}
}

// encoding/json matches object members to fields case-insensitively. The
// approval policy and hook payloads read the document by its exact keys, so a
// member that differs only in case must be rejected instead of silently
// binding to a property the policy never saw.
func TestTypedRejectsCaseVariantKeys(t *testing.T) {
	type nested struct {
		Path string `json:"path"`
	}
	type args struct {
		Path    string            `json:"path"`
		Targets []nested          `json:"targets,omitempty"`
		Extra   map[string]nested `json:"extra,omitempty"`
		Raw     ownDecoder        `json:"raw,omitempty"`
	}
	execute := Typed(func(*ToolExecContext, args) (sdk.ToolOutput, error) { return sdk.ToolOutput{}, nil })
	for name, tc := range map[string]struct {
		doc  string
		want string
	}{
		"top level":  {`{"Path":"/etc/passwd"}`, `invalid arguments for probe: unknown property "Path" (did you mean "path")`},
		"array item": {`{"path":"a","targets":[{"PATH":"b"}]}`, `invalid arguments for probe: unknown property "PATH" (did you mean "path")`},
		"map value":  {`{"path":"a","extra":{"k":{"pAth":"b"}}}`, `invalid arguments for probe: unknown property "pAth" (did you mean "path")`},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := execute(&ToolExecContext{ToolName: "probe"}, sdk.ParseToolArguments(tc.doc))
			if err == nil || err.Error() != tc.want {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
	// Exact keys, unknown keys, and members inside a custom decoder's value
	// are accepted as before.
	if _, err := execute(&ToolExecContext{ToolName: "probe"}, sdk.ParseToolArguments(`{"path":"a","unknown":1,"raw":{"Path":"kept"}}`)); err != nil {
		t.Fatalf("exact keys rejected: %v", err)
	}
}
