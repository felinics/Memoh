package tools

import (
	"strings"

	sdk "github.com/felinics/twilight/sdk"
)

func withOptionalHistoryArguments(registered []sdk.Tool) []sdk.Tool {
	for i := range registered {
		parameters := registered[i].Parameters.(map[string]any)
		properties := parameters["properties"].(map[string]any)
		stringOptions := make(map[string]bool)
		for name, value := range properties {
			property := value.(map[string]any)
			stringOptions[name] = property["type"] == "string"
			property["type"] = []string{property["type"].(string), "null"}
			if values, ok := property["enum"].([]string); ok {
				nullable := make([]any, 0, len(values)+1)
				for _, value := range values {
					nullable = append(nullable, value)
				}
				property["enum"] = append(nullable, nil)
			}
		}
		registered[i].Description += " Use null for unused options."
		execute := registered[i].Execute
		registered[i].Execute = func(ctx *sdk.ToolExecContext, input any) (any, error) {
			arguments := make(map[string]any)
			for key, value := range inputAsMap(input) {
				if value == nil {
					continue
				}
				if text, ok := value.(string); ok && stringOptions[key] && strings.TrimSpace(text) == "" {
					continue
				}
				arguments[key] = value
			}
			return execute(ctx, arguments)
		}
	}
	return registered
}
