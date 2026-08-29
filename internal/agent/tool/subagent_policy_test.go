package tools

import (
	"reflect"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
)

func TestFilterSubagentToolsBlocksOnlyDirectInteractionAndDelegation(t *testing.T) {
	names := []string{
		ToolAskUser().String(), ToolSend().String(), ToolReact().String(),
		ToolSpawnAgent().String(), ToolSendMessage().String(), ToolListAgents().String(), ToolListModels().String(),
		ToolSpeak().String(), ToolCreateSchedule().String(), ToolSearchMemory().String(), ToolUseSkill().String(),
		ToolBrowserAction().String(), ToolComputerAction().String(), ToolSendEmail().String(),
		ToolGenerateImage().String(), ToolGenerateVideo().String(), ToolTranscribeAudio().String(),
		"external_mcp_tool",
	}
	toolList := make([]sdk.Tool, 0, len(names))
	for _, name := range names {
		toolList = append(toolList, sdk.Tool{Name: name})
	}
	filtered := FilterSubagentTools(toolList)
	got := make([]string, 0, len(filtered))
	for _, tool := range filtered {
		got = append(got, tool.Name)
	}
	want := []string{
		ToolSpeak().String(), ToolCreateSchedule().String(), ToolSearchMemory().String(), ToolUseSkill().String(),
		ToolBrowserAction().String(), ToolComputerAction().String(), ToolSendEmail().String(),
		ToolGenerateImage().String(), ToolGenerateVideo().String(), ToolTranscribeAudio().String(),
		"external_mcp_tool",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected subagent tool policy: got %v want %v", got, want)
	}
}
