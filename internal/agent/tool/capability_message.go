package tools

import (
	"fmt"
	"strings"

	"github.com/felinics/memoh/internal/apps"
	"github.com/felinics/memoh/internal/mcp"
)

// Capability messages explain the structured result to the model. Stable
// codes and status fields remain the only values callers should branch on.
func capabilityPageMessage(total, shown, page, limit int, singular, plural, guidance, emptyGuidance string) string {
	noun := plural
	if total == 1 {
		noun = singular
	}
	if total == 0 {
		return "No " + plural + " were found. " + emptyGuidance
	}
	start := (page-1)*limit + 1
	if shown == 0 {
		return fmt.Sprintf("Found %d %s, but page %d has no results. Request an earlier page with the same filters and limit=%d. %s", total, noun, page, limit, guidance)
	}
	end := start + shown - 1
	message := fmt.Sprintf("Found %d %s. Showing %d-%d on page %d.", total, noun, start, end, page)
	if end < total {
		message += fmt.Sprintf(" Use page=%d with the same filters and limit=%d to continue.", page+1, limit)
	}
	return message + " " + guidance
}

func mcpConnectionMessage(action string, conn mcp.Connection, toolCount int, probeFailed bool) string {
	name := conn.Name
	if name == "" {
		name = conn.ID
	}
	switch action {
	case "create":
		return fmt.Sprintf("MCP connection %q was created. Authorize it if credentials are required, then probe it before using its tools.", name)
	case "update":
		return fmt.Sprintf("MCP connection %q was updated. Probe it to verify connectivity and refresh its discovered tools.", name)
	case "probe":
		if probeFailed {
			return fmt.Sprintf("MCP connection %q could not be reached. Check its setup in Settings, then probe it again.", name)
		}
		return fmt.Sprintf("MCP connection %q is reachable. It reported %d tools. Call tools by the names advertised in the current session.", name, toolCount)
	default:
		if !conn.Active {
			return fmt.Sprintf("MCP connection %q is disabled and does not contribute tools. Its last probe status is %q; check auth_status separately.", name, conn.Status)
		}
		if strings.TrimSpace(conn.Status) == "" {
			return fmt.Sprintf("MCP connection %q is active but has not been probed. Check auth_status, then probe it before using its tools.", name)
		}
		return fmt.Sprintf("MCP connection %q is active. Its last probe status is %q; check auth_status before using it.", name, conn.Status)
	}
}

func appOperationMessage(action string, item apps.Item) string {
	name := item.RegistryID + "/" + item.AppID
	status := "discovered"
	if item.Installation != nil {
		status = string(item.Installation.Status)
	}
	if status == string(apps.StatusInstalled) {
		verb := map[string]string{"install": "installed", "update": "updated", "resume": "resumed"}[action]
		return fmt.Sprintf("App %s was %s and is now installed. Its Skills and tools are available in this session. Check whether any required connectors still need authorization.", name, verb)
	}
	return fmt.Sprintf("App %s finished the %s operation with status %q. Inspect dependencies and connector authorization, resolve the blocker, then use resume if the installation is partial or failed.", name, action, status)
}

func appProgressMessage(action string, event apps.Event) string {
	verb := map[string]string{"install": "Installing", "update": "Updating", "resume": "Resuming", "uninstall": "Uninstalling"}[action]
	if verb == "" {
		verb = "Managing"
	}
	target := strings.TrimSpace(event.Kind + " " + event.ID)
	if target == "" {
		target = "App operation"
	}
	switch event.Type {
	case apps.EventStarted:
		return fmt.Sprintf("%s App %s.", verb, event.ID)
	case apps.EventStep:
		return fmt.Sprintf("%s %s.", verb, target)
	case apps.EventStepDone:
		return fmt.Sprintf("%s finished with status %q.", target, event.Status)
	case apps.EventDone:
		return fmt.Sprintf("App %s operation finished with status %q.", event.ID, event.Status)
	case apps.EventError:
		return fmt.Sprintf("%s failed. Check the final tool result for a safe error and recovery guidance.", target)
	default:
		return fmt.Sprintf("%s %s.", verb, target)
	}
}
