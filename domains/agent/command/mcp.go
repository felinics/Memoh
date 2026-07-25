package command

import (
	"fmt"
	"strings"
)

func (h *Handler) buildMCPGroup() *CommandGroup {
	g := newCommandGroup("mcp", "Manage MCP connections")
	g.DefaultAction = "list" // bare /mcp lands on the connection list
	g.Register(SubCommand{
		Name:  "list",
		Usage: "list - List all MCP connections",
		ResultHandler: func(cc CommandContext) (*Result, error) {
			items, err := h.mcpConnService.ListByBot(cc.Ctx, cc.BotID)
			if err != nil {
				return nil, err
			}
			if len(items) == 0 {
				return WithButtons(
					&Result{Text: cc.T("cmd.mcp.empty")},
					ListItem{Label: cc.T("cmd.common.allCommands"), Action: &ItemAction{Resource: "help", Action: "mcp"}},
				), nil
			}
			records := make([]listRecord, 0, len(items))
			for _, item := range items {
				records = append(records, listRecord{
					fields: []kv{
						{cc.T("cmd.common.fieldName"), item.Name},
						{cc.T("cmd.common.fieldStatus"), humanizeStatusT(cc, item.Status)},
						{cc.T("cmd.mcp.fieldType"), item.Type},
					},
					// Tap a connection to open its details — no typing of /mcp get.
					action: &ItemAction{Resource: "mcp", Action: "get", Args: []string{item.Name}},
				})
			}
			result := buildListResult(cc.T("cmd.mcp.title"), "mcp", "list", nil, records, cc.Page, defaultListLimit, cc.L)
			if result.Interactive != nil && result.Interactive.List != nil {
				result.Interactive.List.HintVerb = HintVerbDetails
			}
			return WithExtraActions(result,
				ListItem{Label: cc.T("cmd.common.allCommands"), Action: &ItemAction{Resource: "help", Action: "mcp"}},
			), nil
		},
	})
	g.Register(SubCommand{
		Name:  "get",
		Usage: "get <name> - Get MCP connection details",
		ResultHandler: func(cc CommandContext) (*Result, error) {
			if len(cc.Args) < 1 {
				return &Result{Text: cc.T("cmd.mcp.getUsage", map[string]any{"command": CmdRef("mcp get <name>")})}, nil
			}
			name := cc.Args[0]
			items, err := h.mcpConnService.ListByBot(cc.Ctx, cc.BotID)
			if err != nil {
				return nil, err
			}
			for _, item := range items {
				if strings.EqualFold(item.Name, name) {
					toolNames := make([]string, 0, len(item.ToolsCache))
					for _, t := range item.ToolsCache {
						toolNames = append(toolNames, t.Name)
					}
					toolsStr := cc.T("cmd.common.none")
					if len(toolNames) > 0 {
						toolsStr = strings.Join(toolNames, ", ")
					}
					authType := item.AuthType
					if strings.EqualFold(strings.TrimSpace(authType), "none") {
						authType = ""
					}
					return WithButtons(
						&Result{Text: formatKVTitled(item.Name, []kv{
							{cc.T("cmd.common.fieldStatus"), humanizeStatusT(cc, item.Status)},
							{cc.T("cmd.mcp.fieldReason"), item.StatusMessage},
							{cc.T("cmd.mcp.fieldType"), item.Type},
							{cc.T("cmd.mcp.fieldActive"), boolStrT(cc, item.Active)},
							{cc.T("cmd.mcp.fieldAuth"), authType},
							{cc.T("cmd.mcp.fieldTools"), toolsStr},
							{cc.T("cmd.common.fieldCreated"), humanizeTimeT(cc, item.CreatedAt)},
							{cc.T("cmd.common.fieldUpdated"), humanizeTimeT(cc, item.UpdatedAt)},
						})},
						ListItem{Label: cc.T("cmd.mcp.back"), Action: &ItemAction{Resource: "mcp", Action: "list"}},
						ListItem{Label: cc.T("cmd.common.allCommands"), Action: &ItemAction{Resource: "help", Action: "mcp"}},
					), nil
				}
			}
			return &Result{Text: cc.T("cmd.mcp.notFound", map[string]any{"name": fmt.Sprintf("%q", name), "command": CmdRef("mcp list")})}, nil
		},
	})
	g.Register(SubCommand{
		Name:    "delete",
		Usage:   "delete <name> - Delete an MCP connection",
		IsWrite: true,
		Handler: func(cc CommandContext) (string, error) {
			if len(cc.Args) < 1 {
				return cc.T("cmd.mcp.deleteUsage"), nil
			}
			name := cc.Args[0]
			items, err := h.mcpConnService.ListByBot(cc.Ctx, cc.BotID)
			if err != nil {
				return "", err
			}
			for _, item := range items {
				if strings.EqualFold(item.Name, name) {
					if err := h.mcpConnService.Delete(cc.Ctx, cc.BotID, item.ID); err != nil {
						return "", err
					}
					return cc.T("cmd.mcp.deleted", map[string]any{"name": MdCode(item.Name)}), nil
				}
			}
			return cc.T("cmd.mcp.notFound", map[string]any{"name": fmt.Sprintf("%q", name), "command": CmdRef("mcp list")}), nil
		},
	})
	return g
}
