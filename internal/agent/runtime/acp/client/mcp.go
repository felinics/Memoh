package client

import (
	"strings"

	acp "github.com/coder/acp-go-sdk"

	mcpgw "github.com/felinics/memoh/internal/mcp"
)

const (
	memohToolsMCPServerName       = "Memoh Tools"
	memohToolsMCPServerSlug       = "Memoh_Tools"
	memohHeaderBotID              = mcpgw.ToolHeaderBotID
	memohHeaderChatID             = mcpgw.ToolHeaderChatID
	memohHeaderRuntimeID          = mcpgw.ToolHeaderRuntimeID
	memohHeaderRuntimeToken       = mcpgw.ToolHeaderRuntimeToken
	memohHeaderSessionID          = mcpgw.ToolHeaderSessionID
	memohHeaderRunID              = mcpgw.ToolHeaderRunID
	memohHeaderSessionType        = mcpgw.ToolHeaderSessionType
	memohHeaderRouteID            = mcpgw.ToolHeaderRouteID
	memohHeaderChannelIdentityID  = mcpgw.ToolHeaderChannelIdentityID
	memohHeaderCurrentPlatform    = mcpgw.ToolHeaderCurrentPlatform
	memohHeaderReplyTarget        = mcpgw.ToolHeaderReplyTarget
	memohHeaderConversationType   = mcpgw.ToolHeaderConversationType
	memohHeaderIsSubagent         = mcpgw.ToolHeaderIsSubagent
	memohHeaderSupportsImageInput = mcpgw.ToolHeaderSupportsImageInput
)

func memohToolsHTTPMCPServer(rawURL string, session mcpgw.ToolSessionContext) acp.McpServer {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return acp.McpServer{}
	}
	return acp.McpServer{
		Http: &acp.McpServerHttpInline{
			Name:    memohToolsMCPServerName,
			Url:     rawURL,
			Headers: memohToolsHTTPHeaders(session),
		},
	}
}

// MemohToolsMCPServer names the Memoh tool gateway for a session whose route
// resolves the caller's identity itself, so no identity headers travel with
// it. It carries the identifier form of the name: an agent that qualifies
// tool names by server (`server__tool`) drops every tool of a server whose
// name is not an identifier. Permission preflight recognizes both forms.
func MemohToolsMCPServer(url string) acp.McpServer {
	return acp.McpServer{Http: &acp.McpServerHttpInline{
		Name:    memohToolsMCPServerSlug,
		Url:     strings.TrimSpace(url),
		Headers: []acp.HttpHeader{},
	}}
}

func memohToolsHTTPHeaders(session mcpgw.ToolSessionContext) []acp.HttpHeader {
	headers := make([]acp.HttpHeader, 0, 11)
	add := func(name, value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		headers = append(headers, acp.HttpHeader{Name: name, Value: value})
	}

	add(memohHeaderBotID, session.BotID)
	add(memohHeaderChatID, session.ChatID)
	add(memohHeaderRuntimeID, session.RuntimeID)
	add(memohHeaderRuntimeToken, session.RuntimeToken)
	add(memohHeaderSessionID, session.SessionID)
	add(memohHeaderRunID, session.RunID)
	add(memohHeaderSessionType, session.SessionType)
	add(memohHeaderRouteID, session.RouteID)
	add(memohHeaderChannelIdentityID, session.ChannelIdentityID)
	add(memohHeaderCurrentPlatform, session.CurrentPlatform)
	add(memohHeaderReplyTarget, session.ReplyTarget)
	add(memohHeaderConversationType, session.ConversationType)
	if session.IsSubagent {
		add(memohHeaderIsSubagent, "true")
	}
	if session.SupportsImageInput {
		add(memohHeaderSupportsImageInput, "true")
	}
	return headers
}

// IsMemohToolsMCPServerName reports a name an agent may use for the Memoh
// tool gateway, in either of the forms MemohToolsMCPServer and the
// identity-header variant give it.
func IsMemohToolsMCPServerName(name string) bool { return isMemohToolsMCPServerName(name) }

func isMemohToolsMCPServerName(name string) bool {
	name = strings.TrimSpace(name)
	return strings.EqualFold(name, memohToolsMCPServerName) ||
		strings.EqualFold(name, memohToolsMCPServerSlug)
}
