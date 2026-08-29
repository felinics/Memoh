package native

import (
	"encoding/json"
	"reflect"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/jsonschema-go/jsonschema"
)

// canonicalizeProviderToolSchemas mirrors Twilight's buildConfig schema
// resolution before provider-attempt budgeting and hashing. Twilight treats
// an already-resolved *jsonschema.Schema as final, so the SDK cannot rewrite
// the audited tool payload later.
func canonicalizeProviderToolSchemas(tools []sdk.Tool) []sdk.Tool {
	if len(tools) == 0 {
		return tools
	}
	out := append([]sdk.Tool(nil), tools...)
	for i := range out {
		if schema, ok := canonicalProviderToolSchema(out[i].Parameters); ok {
			out[i].Parameters = schema
		}
	}
	return out
}

func canonicalProviderToolSchema(value any) (*jsonschema.Schema, bool) {
	if value == nil {
		return nil, true
	}
	if schema, ok := value.(*jsonschema.Schema); ok {
		return schema, true
	}
	if raw, ok := value.(map[string]any); ok {
		data, err := json.Marshal(raw)
		if err != nil {
			return nil, false
		}
		var schema jsonschema.Schema
		if err := json.Unmarshal(data, &schema); err != nil {
			return nil, false
		}
		return &schema, true
	}
	typ := reflect.TypeOf(value)
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return nil, false
	}
	schema, err := jsonschema.ForType(typ, nil)
	if err != nil {
		return nil, false
	}
	return schema, true
}
