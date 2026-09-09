package tools

import sdk "github.com/felinics/twilight/sdk"

// FilterSubagentTools applies the small denylist for capabilities that require
// direct parent/user coordination. All other native and federated tools remain
// available and keep their existing capability/configuration gates.
func FilterSubagentTools(toolList []sdk.Tool) []sdk.Tool {
	if len(toolList) == 0 {
		return toolList
	}
	blocked := map[string]struct{}{
		ToolAskUser().String():     {},
		ToolSend().String():        {},
		ToolReact().String():       {},
		ToolSpawnAgent().String():  {},
		ToolSendMessage().String(): {},
		ToolListAgents().String():  {},
		ToolListModels().String():  {},
	}
	filtered := make([]sdk.Tool, 0, len(toolList))
	for _, tool := range toolList {
		if _, denied := blocked[tool.Name]; denied {
			continue
		}
		filtered = append(filtered, tool)
	}
	return filtered
}
