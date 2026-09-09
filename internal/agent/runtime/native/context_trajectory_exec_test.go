package native

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/background"
	"github.com/felinics/memoh/internal/agent/context/trajectory"
	agenttools "github.com/felinics/memoh/internal/agent/tool"
)

func TestTrajectoryRetainsExecOutputBeforeInternalPruning(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		name := "synchronous"
		if streaming {
			name = "streaming"
		}
		t.Run(name, func(t *testing.T) {
			stdout := strings.Repeat("left ", 40000) + "RAW_STDOUT_MIDDLE" + strings.Repeat(" right", 40000)
			stderr := strings.Repeat("error ", 40000) + "RAW_STDERR_MIDDLE" + strings.Repeat(" tail", 40000)
			service := newMockExecContainerService()
			service.setBehavior("large-output", execBehavior{stdout: stdout, stderr: stderr, exitCode: 17})
			bridgeProvider, cleanup := setupExecTestInfra(t, service)
			t.Cleanup(cleanup)
			var manager *background.Manager
			if streaming {
				manager = background.New(nil)
			}
			provider := agenttools.NewContainerProvider(nil, bridgeProvider, manager, "/data")
			tools, err := provider.Tools(t.Context(), agenttools.SessionContext{BotID: "bot", SessionID: "session"})
			if err != nil {
				t.Fatal(err)
			}
			sink := &nativeTrajectorySink{}
			recorder := trajectory.NewRecorder(sink)
			recorder.Bind("run", "session")
			ctx := &sdk.ToolExecContext{Context: trajectory.WithRecorder(t.Context(), recorder), ToolCallID: "exec-capture-call"}
			var output any
			for _, tool := range tools {
				if tool.Name == "exec" {
					output, err = tool.Execute(ctx, map[string]any{"command": "large-output"})
					if err != nil {
						t.Fatal(err)
					}
					withoutCapture, plainErr := tool.Execute(&sdk.ToolExecContext{Context: t.Context()}, map[string]any{"command": "large-output"})
					if plainErr != nil || !reflect.DeepEqual(output, withoutCapture) {
						t.Fatal("trajectory capture changed the tool return value")
					}
					break
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(output)
			if err != nil || output == nil || strings.Contains(string(encoded), "RAW_STDOUT_MIDDLE") || strings.Contains(string(encoded), "RAW_STDERR_MIDDLE") {
				t.Fatal("fixture did not execute and prune the real container tool")
			}
			seen := map[string]bool{}
			for _, event := range sink.events {
				for _, block := range event.Blocks {
					var content strings.Builder
					for _, hash := range block.Chunks {
						content.WriteString(sink.contents[hash])
					}
					body := content.String()
					seen["stdout"] = seen["stdout"] || body == stdout
					seen["stderr"] = seen["stderr"] || body == stderr
					seen["call"] = seen["call"] || (strings.Contains(body, `"tool_call_id":"exec-capture-call"`) && strings.Contains(body, "large-output"))
				}
			}
			if !seen["stdout"] || !seen["stderr"] || !seen["call"] {
				t.Fatalf("exec source capture missing: %v, captured stages=%d", seen, len(sink.events))
			}
		})
	}
}
