package toolexec

import (
	"encoding/json"
	"errors"
	"fmt"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/jsonschema-go/jsonschema"
)

// ToolDefinitionFromTool is the provider-facing definition of a Tool: the
// name, description, schema and cache control, without the execute handler.
func ToolDefinitionFromTool(tool Tool) (sdk.ToolDefinition, error) {
	if tool.Name == "" {
		return sdk.ToolDefinition{}, errors.New("twilightai: tool definition requires a name")
	}
	return sdk.ToolDefinition{
		Name:         tool.Name,
		Description:  tool.Description,
		Parameters:   cloneSchema(tool.Parameters),
		CacheControl: cloneCacheControl(tool.CacheControl),
	}, nil
}

// ToolDefinitionsFromTools converts every tool; nil in, nil out.
func ToolDefinitionsFromTools(tools []Tool) ([]sdk.ToolDefinition, error) {
	if tools == nil {
		return nil, nil
	}
	out := make([]sdk.ToolDefinition, len(tools))
	for i, tool := range tools {
		def, err := ToolDefinitionFromTool(tool)
		if err != nil {
			return nil, fmt.Errorf("twilightai: tool %q: %w", tool.Name, err)
		}
		out[i] = def
	}
	return out, nil
}

func cloneCacheControl(c *sdk.CacheControl) *sdk.CacheControl {
	if c == nil {
		return nil
	}
	cc := *c
	return &cc
}

// cloneSchema copies a schema through JSON, the only complete copy of the
// schema type's nested structure.
func cloneSchema(s *jsonschema.Schema) *jsonschema.Schema {
	if s == nil {
		return nil
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return s
	}
	var out jsonschema.Schema
	if err := json.Unmarshal(raw, &out); err != nil {
		return s
	}
	return &out
}
