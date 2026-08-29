package native

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	agenttools "github.com/felinics/memoh/internal/agent/tool"
)

func TestWrapPrepareStepWithForkSnapshotDoesNotRewriteToolResults(t *testing.T) {
	t.Parallel()

	messages := []sdk.Message{sdk.UserMessage("inspect the repository")}
	for i := 0; i < 12; i++ {
		callID := fmt.Sprintf("call-%02d", i)
		messages = append(messages, sdk.Message{
			Role: sdk.MessageRoleAssistant,
			Content: []sdk.MessagePart{sdk.ToolCallPart{
				ToolCallID: callID,
				ToolName:   "exec",
				Input:      map[string]any{"command": fmt.Sprintf("step-%02d", i)},
			}},
		})
		messages = append(messages, sdk.ToolMessage(sdk.ToolResultPart{
			ToolCallID: callID,
			ToolName:   "exec",
			Result:     strings.Repeat(string(rune('a'+i)), 2048),
		}))
	}
	before, err := json.Marshal(messages)
	if err != nil {
		t.Fatal(err)
	}

	snapshot := agenttools.NewMessageSnapshot(nil)
	prepareStep := wrapPrepareStepWithForkSnapshot(context.Background(), nil, snapshot)
	params := &sdk.GenerateParams{Messages: messages}
	got := prepareStep(params)
	if got != params {
		t.Fatal("prepare step unexpectedly replaced the params")
	}

	after, err := json.Marshal(got.Messages)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("prepare step rewrote prior tool results:\nbefore: %s\nafter:  %s", before, after)
	}

	snapshotMessages, err := snapshot.Messages()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshotMessages) != len(messages) {
		t.Fatalf("fork snapshot messages = %d, want %d", len(snapshotMessages), len(messages))
	}
}
