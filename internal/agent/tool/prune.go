package tools

import (
	"context"

	contextlimit "github.com/felinics/memoh/internal/agent/context/limit"
	"github.com/felinics/memoh/internal/agent/context/trajectory"
)

const (
	listMaxEntries        = 200
	listCollapseThreshold = 50
)

func pruneExecOutput(ctx context.Context, toolCallID, command, workDir, stdout, stderr string, exitCode int32) map[string]any {
	limitedStdout := contextlimit.PruneTier.LimitString(stdout, "tool result (exec stdout)")
	limitedStderr := contextlimit.PruneTier.LimitString(stderr, "tool result (exec stderr)")
	if recorder := trajectory.FromContext(ctx); recorder != nil {
		recorder.Record(ctx, "tool_output_limit", nil,
			trajectory.JSONBlock("tool_call", "exec", map[string]any{
				"tool_call_id": toolCallID, "tool_name": "exec", "source": "exec_streams",
				"input": map[string]any{"command": command, "work_dir": workDir},
				"limit": contextlimit.PruneTier, "exit_code": exitCode,
			}),
			trajectory.Block{Kind: "tool_result", Label: "stdout original", Content: stdout},
			trajectory.Block{Kind: "tool_result", Label: "stdout limited", Content: limitedStdout},
			trajectory.Block{Kind: "tool_result", Label: "stderr original", Content: stderr},
			trajectory.Block{Kind: "tool_result", Label: "stderr limited", Content: limitedStderr},
		)
	}
	return map[string]any{"stdout": limitedStdout, "stderr": limitedStderr, "exit_code": exitCode}
}
