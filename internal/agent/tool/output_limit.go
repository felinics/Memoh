package tools

import (
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	contextlimit "github.com/felinics/memoh/internal/agent/context/limit"
	"github.com/felinics/memoh/internal/agent/toolexec"
)

type ToolOutputLimit = contextlimit.ToolOutputLimit

func LimitToolOutput(output any, label string, limit ToolOutputLimit) any {
	return contextlimit.LimitToolOutput(output, label, limit)
}

func LimitToolError(err error, label string, limit ToolOutputLimit) error {
	return contextlimit.LimitError(err, label, limit)
}

func WrapToolOutputLimits(sdkTools []toolexec.Tool, limit ToolOutputLimit) []toolexec.Tool {
	if len(sdkTools) == 0 {
		return sdkTools
	}
	wrapped := make([]toolexec.Tool, len(sdkTools))
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
		wrapped[i].Execute = func(ctx *toolexec.ToolExecContext, input sdk.ToolArguments) (sdk.ToolOutput, error) {
			output, err := execute(ctx, input)
			if err != nil {
				return output, LimitToolError(err, label, limit)
			}
			return toolexec.OutputFromValue(LimitToolOutput(toolexec.OutputValue(output), label, limit)), nil
		}
	}
	return wrapped
}
