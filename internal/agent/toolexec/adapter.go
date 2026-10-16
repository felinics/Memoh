package toolexec

import (
	"encoding/json"
	"fmt"
	"reflect"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/jsonschema-go/jsonschema"
)

// ArgumentsValue is the arguments as a plain JSON value: an object decodes to
// map[string]any, the zero value is the empty object, and invalid arguments
// are their text verbatim (a tool never sees them through ExecuteTools, which
// answers the model first; a direct caller may).
func ArgumentsValue(args sdk.ToolArguments) any {
	if !args.Valid() {
		return args.Text
	}
	if args.JSON == nil {
		return map[string]any{}
	}
	var value any
	if err := json.Unmarshal(args.JSON, &value); err != nil {
		return string(args.JSON)
	}
	return value
}

// ArgumentsFromValue types a plain JSON value as tool arguments. Text is
// classified the way a provider classifies the model's argument text; any
// other value is encoded.
func ArgumentsFromValue(value any) sdk.ToolArguments {
	switch v := value.(type) {
	case nil:
		return sdk.ToolArguments{}
	case sdk.ToolArguments:
		return v
	case string:
		return sdk.ParseToolArguments(v)
	case json.RawMessage:
		return sdk.ParseToolArguments(string(v))
	case []byte:
		return sdk.ParseToolArguments(string(v))
	}
	args, err := sdk.ToolArgumentsJSON(value)
	if err != nil {
		return sdk.ToolArguments{Text: fmt.Sprint(value)}
	}
	return args
}

// OutputValue is the output as a plain JSON value: a JSON document decodes,
// text stays a string, and no output at all is nil, the value a tool that
// returned nothing has always produced for hooks, events and rows.
func OutputValue(output sdk.ToolOutput) any {
	if !output.IsJSON() {
		if output.Text == "" {
			return nil
		}
		return output.Text
	}
	var value any
	if err := json.Unmarshal(output.JSON, &value); err != nil {
		return string(output.JSON)
	}
	return value
}

// EncodeOutput types a plain value as a tool output: a string is text, a
// byte slice is its text, an encoded document is kept, anything else is
// encoded. The error is the encoding failure of a value that cannot be JSON.
func EncodeOutput(value any) (sdk.ToolOutput, error) {
	switch v := value.(type) {
	case nil:
		return sdk.ToolOutput{}, nil
	case sdk.ToolOutput:
		return v, nil
	case string:
		return sdk.TextOutput(v), nil
	case []byte:
		return sdk.TextOutput(string(v)), nil
	case json.RawMessage:
		// Bytes that are not one JSON document would make the tool message
		// itself unmarshalable and drop it from history; carry them as text.
		output, err := sdk.RawJSONOutput(v)
		if err != nil {
			return sdk.TextOutput(string(v)), nil
		}
		return output, nil
	}
	return sdk.JSONOutput(value)
}

// SchemaFromValue resolves a tool's parameter schema from the shapes Memoh
// builds it in: an already resolved schema, a JSON object (map or raw
// document), or a struct type whose schema is inferred. A value that resolves
// to nothing yields the empty object schema so the definition is never sent
// without parameters. Use it for schemas Memoh writes itself; a schema that
// arrives as data (an MCP tool, a memory provider descriptor) goes through
// ResolveSchema so a document that does not parse is not silently replaced.
func SchemaFromValue(value any) *jsonschema.Schema {
	if schema, err := ResolveSchema(value); err == nil && schema != nil {
		return schema
	}
	return &jsonschema.Schema{Type: "object"}
}

// ResolveSchema is SchemaFromValue that reports why a value did not resolve.
// A nil value resolves to the empty object schema; a JSON document that is
// not a valid schema is the error.
func ResolveSchema(value any) (*jsonschema.Schema, error) {
	switch v := value.(type) {
	case nil:
		return &jsonschema.Schema{Type: "object"}, nil
	case *jsonschema.Schema:
		if v == nil {
			return &jsonschema.Schema{Type: "object"}, nil
		}
		return v, nil
	case jsonschema.Schema:
		return &v, nil
	case map[string]any:
		return schemaFromJSON(v)
	case json.RawMessage:
		return schemaFromJSON(v)
	case []byte:
		return schemaFromJSON(json.RawMessage(v))
	}
	typ := reflect.TypeOf(value)
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return nil, fmt.Errorf("toolexec: cannot build a schema from %T", value)
	}
	return jsonschema.ForType(typ, nil)
}

func schemaFromJSON(value any) (*jsonschema.Schema, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("toolexec: encode schema: %w", err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(data, &schema); err != nil {
		return nil, fmt.Errorf("toolexec: schema is not valid JSON Schema: %w", err)
	}
	return &schema, nil
}

// OutputFromValue is EncodeOutput for a value that is already JSON-shaped
// (decoded from a row, an event or a decision record): a value that still
// cannot be encoded becomes its printed form as text rather than an error.
func OutputFromValue(value any) sdk.ToolOutput {
	output, err := EncodeOutput(value)
	if err != nil {
		return sdk.TextOutput(fmt.Sprint(value))
	}
	return output
}

// SchemaValue is a parameter schema as the JSON object it encodes to, the
// shape Memoh's MCP gateway and its tests inspect; nil is the empty object.
func SchemaValue(schema *jsonschema.Schema) map[string]any {
	if schema == nil {
		return map[string]any{}
	}
	data, err := json.Marshal(schema)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil || out == nil {
		return map[string]any{}
	}
	return out
}

// OutputPair types the two results of a helper that still returns a plain
// value, so a handler can end with `return toolexec.OutputPair(helper(...))`.
// A failed call carries no output.
func OutputPair(value any, err error) (sdk.ToolOutput, error) {
	if err != nil {
		return sdk.ToolOutput{}, err
	}
	return OutputFromValue(value), nil
}
