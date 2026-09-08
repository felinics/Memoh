package tools

import (
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	contextlimit "github.com/felinics/memoh/internal/agent/context/limit"
	"github.com/felinics/memoh/internal/agent/context/trajectory"
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
			var recorder *trajectory.Recorder
			var original trajectory.Block
			if ctx != nil && ctx.Context != nil {
				recorder = trajectory.FromContext(ctx.Context)
				if recorder != nil {
					original = toolResultCapture("original", output, err)
				}
			}
			if err != nil {
				err = LimitToolError(err, label, limit)
			} else {
				output = LimitToolOutput(output, label, limit)
			}
			if recorder != nil {
				recorder.Record(ctx.Context, "tool_output_limit", nil,
					trajectory.JSONBlock("tool_call", toolName, map[string]any{"tool_call_id": ctx.ToolCallID, "tool_name": toolName, "input": input, "limit": limit}),
					original, toolResultCapture("limited", output, err),
				)
			}
			return output, err
		}
	}
	return wrapped
}

func toolResultCapture(label string, output any, err error) trajectory.Block {
	message := ""
	if err != nil {
		message = err.Error()
	}
	return trajectory.JSONBlock("tool_result", label, map[string]any{"output": output, "error": message})
}
