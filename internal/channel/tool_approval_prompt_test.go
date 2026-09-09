package channel

import (
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/agent/decision/approval"
)

func TestBuildToolApprovalPromptIncludesStableExecutionLocationName(t *testing.T) {
	t.Parallel()

	prompt := BuildToolApprovalPrompt(approval.Request{
		ID:         "approval-1",
		ShortID:    7,
		ToolCallID: "call-1",
		ToolName:   "exec",
		Operation:  approval.OperationExec,
		ToolInput:  map[string]any{"command": "pwd"},
		ExecutionLocation: &approval.ExecutionLocation{
			TargetID: "internal-runtime-id",
			Kind:     "remote",
			Name:     "Office Mac",
		},
	})

	if !strings.Contains(prompt.Text, "Location: Office Mac") {
		t.Fatalf("prompt = %q, want stable execution location", prompt.Text)
	}
	if strings.Contains(prompt.Text, "internal-runtime-id") || strings.Contains(prompt.Text, "/Users/alice") {
		t.Fatalf("prompt leaked internal execution details: %q", prompt.Text)
	}
}
