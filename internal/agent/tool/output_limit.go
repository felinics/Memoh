package tools

import (
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	contextlimit "github.com/felinics/memoh/internal/agent/context/limit"
)

type ToolOutputLimit = contextlimit.ToolOutputLimit

func LimitToolOutput(output any, label string, limit ToolOutputLimit) any {
	return contextlimit.LimitToolOutput(output, label, limit)
}

func LimitToolError(err error, label string, limit ToolOutputLimit) error {
	return contextlimit.LimitError(err, label, limit)
}

func WrapToolOutputLimits(sdkTools []sdk.Tool, limit ToolOutputLimit) []sdk.Tool {
	if len(sdkTools) == 0 {
		return sdkTools
	}
	wrapped := make([]sdk.Tool, len(sdkTools))
	copy(wrapped, sdkTools)
	for i := range wrapped {
		execute := wrapped[i].Execute
		if execute == nil {
			continue
		}
		toolName := strings.TrimSpace(wrapped[i].Name)
		label := "tool result"
		if toolName != "" {
			label = "tool result (" + toolName + ")"
		}
		wrapped[i].Execute = func(ctx *sdk.ToolExecContext, input any) (any, error) {
			output, err := execute(ctx, input)
			if err != nil {
				return output, LimitToolError(err, label, limit)
			}
			return LimitToolOutput(output, label, limit), nil
		}
	}
	return wrapped
}
