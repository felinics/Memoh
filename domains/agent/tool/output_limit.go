package tool

import (
	"strings"

	sdk "github.com/memohai/twilight-ai/sdk"

	"github.com/memohai/memoh/domains/agent/chat/context/limit"
)

type ToolOutputLimit = limit.ToolOutputLimit

func LimitToolOutput(output any, label string, maxLimit ToolOutputLimit) any {
	return limit.LimitToolOutput(output, label, maxLimit)
}

func LimitToolError(err error, label string, maxLimit ToolOutputLimit) error {
	return limit.LimitError(err, label, maxLimit)
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
