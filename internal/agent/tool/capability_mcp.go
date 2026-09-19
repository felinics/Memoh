package tools

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/mcp"
)

func (p *CapabilityProvider) manageMCP(ctx *sdk.ToolExecContext, session SessionContext, args map[string]any) (any, error) {
	action := StringArg(args, "action")
	if action == "list" {
		list, err := p.opts.Connections.ListByBot(ctx.Context, session.BotID)
		if err != nil {
			return nil, err
		}
		page, limit := capabilityPagination(args)
		start := min((page-1)*limit, len(list))
		end := min(start+limit, len(list))
		items := []map[string]any{}
		for _, conn := range list[start:end] {
			item, err := p.connectionSummary(ctx.Context, conn)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		message := capabilityPageMessage(len(list), len(items), page, limit, "MCP connection", "MCP connections", "Use connection_id with get, update, delete, probe, or authorize.", "If the user provided an MCP server, add it with action=create.")
		return map[string]any{"items": items, "total": len(list), "page": page, "limit": limit, "message": message}, nil
	}
	id := StringArg(args, "connection_id")
	var conn mcp.Connection
	var err error
	if action != "create" {
		conn, err = p.opts.Connections.Get(ctx.Context, session.BotID, id)
		if err != nil {
			return nil, err
		}
	}
	if action == "get" {
		p.changed(session.BotID)
		result, err := p.connectionSummary(ctx.Context, conn)
		if err != nil {
			return nil, err
		}
		result["message"] = mcpConnectionMessage(action, conn, len(conn.ToolsCache), false)
		return result, nil
	}
	var req mcp.UpsertRequest
	if action == "create" || action == "update" {
		req, err = capabilityMCPRequest(conn, args)
		if err != nil {
			return nil, err
		}
	}
	prepared := cloneCapabilityArgs(args)
	if action != "create" {
		prepared["connection_name"] = conn.Name
		prepared["transport_type"] = conn.Type
	}
	if err := p.approve(ctx, session, ToolMCPManage().String(), prepared); err != nil {
		return nil, err
	}
	// Do not apply an approved change to a connection edited while approval was pending.
	if action != "create" {
		current, err := p.opts.Connections.Get(ctx.Context, session.BotID, id)
		if err != nil {
			return nil, err
		}
		if !reflect.DeepEqual(current, conn) {
			return nil, errors.New("connection changed during approval")
		}
	}
	defer p.changed(session.BotID)
	switch action {
	case "create":
		conn, err = p.opts.Connections.Create(ctx.Context, session.BotID, req)
	case "update":
		conn, err = p.opts.Connections.Update(ctx.Context, session.BotID, id, req)
	case "delete":
		if err := p.opts.Connections.Delete(ctx.Context, session.BotID, id); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "status": "deleted", "connection_id": id, "message": "The MCP connection was deleted. Its tools are no longer available to this bot."}, nil
	case "authorize":
		if StringArg(args, "auth_method") == "api_key" || conn.Type == "stdio" {
			return map[string]any{"status": "needs_configuration", "settings_url": p.settingsPath(session.BotID, "mcp"), "message": "This MCP connection needs manual credential or process setup. Ask the user to open settings_url and configure it there, then probe the connection."}, nil
		}
		serverURL, _ := conn.Config["url"].(string)
		discovery, err := p.opts.OAuth.Discover(ctx.Context, serverURL)
		if err != nil {
			return nil, err
		}
		if err := p.opts.OAuth.SaveDiscovery(ctx.Context, id, discovery); err != nil {
			return nil, err
		}
		auth, err := p.opts.OAuth.StartAuthorization(ctx.Context, id, "", "", "")
		if err != nil {
			return map[string]any{"status": "needs_configuration", "settings_url": p.settingsPath(session.BotID, "mcp"), "message": "OAuth could not be started automatically for this MCP server. Ask the user to open settings_url and finish configuration there, then probe the connection."}, nil
		}
		return map[string]any{"status": "authorization_pending", "authorization_url": auth.AuthorizationURL, "connection_id": id, "message": "MCP authorization has started but is not complete. Ask the user to open authorization_url; after they finish, call get to confirm auth_status and probe to verify connectivity."}, nil
	case "probe":
		descriptors, probeErr := p.opts.Probe(ctx.Context, session.BotID, conn)
		status := "connected"
		detail := ""
		if probeErr != nil {
			status = "error"
			detail = "Connection probe failed."
			descriptors = nil
		}
		if err := p.opts.Connections.UpdateProbeResult(ctx.Context, session.BotID, id, status, descriptors, detail); err != nil {
			return nil, err
		}
		conn.Status = status
		conn.ToolsCache = descriptors
		result, err := p.connectionSummary(ctx.Context, conn)
		if err != nil {
			return nil, err
		}
		if probeErr != nil {
			result["probe_failed"] = true
		}
		result["message"] = mcpConnectionMessage(action, conn, len(descriptors), probeErr != nil)
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	result, err := p.connectionSummary(ctx.Context, conn)
	if err != nil {
		return nil, err
	}
	result["message"] = mcpConnectionMessage(action, conn, len(conn.ToolsCache), false)
	return result, nil
}

func (p *CapabilityProvider) connectionSummary(ctx context.Context, conn mcp.Connection) (map[string]any, error) {
	state := "unknown"
	if conn.Status == "connected" {
		state = "not_required"
	}
	status, err := p.opts.OAuth.GetStatus(ctx, conn.ID)
	if err != nil {
		return nil, err
	}
	if conn.AuthType == "oauth" || status.Configured {
		state = "needs_authorization"
		if status.HasToken {
			state = "authorized"
			if status.Expired {
				state = "needs_reauthorization"
			}
		}
	} else if lenMap(conn.Config["headers"]) > 0 || lenMap(conn.Config["env"]) > 0 {
		state = "credentials_configured"
	}
	names := make([]string, 0, len(conn.ToolsCache))
	for _, tool := range conn.ToolsCache {
		names = append(names, tool.Name)
	}
	result := map[string]any{"connection_id": conn.ID, "name": conn.Name, "transport": conn.Type, "is_active": conn.Active, "status": conn.Status, "auth_status": state, "tool_names": names, "settings_url": p.settingsPath(conn.BotID, "mcp")}
	// Secrets may be present even in legacy URLs and command arguments. Only
	// expose the URL's origin and non-secret structural metadata.
	if raw, ok := conn.Config["url"].(string); ok {
		if u, err := url.Parse(raw); err == nil {
			result["server_origin"] = u.Scheme + "://" + u.Host
		}
	}
	result["has_headers"] = lenMap(conn.Config["headers"]) > 0
	result["has_environment"] = lenMap(conn.Config["env"]) > 0
	return result, nil
}

func lenMap(value any) int {
	switch v := value.(type) {
	case map[string]any:
		return len(v)
	case map[string]string:
		return len(v)
	default:
		return 0
	}
}

func capabilityMCPRequest(conn mcp.Connection, args map[string]any) (mcp.UpsertRequest, error) {
	// Rehydrate the internal config without serializing it into tool output.
	data, err := json.Marshal(conn.Config)
	if err != nil {
		return mcp.UpsertRequest{}, err
	}
	var req mcp.UpsertRequest
	if err = json.Unmarshal(data, &req); err != nil {
		return req, err
	}
	req.Name = conn.Name
	active := conn.Active
	req.Active = &active
	if conn.Type == "sse" {
		req.Transport = "sse"
	}
	if conn.ID == "" {
		active = true
	}
	for key, value := range args {
		switch key {
		case "name":
			req.Name = value.(string)
		case "command":
			req.Command = value.(string)
		case "url":
			req.URL = value.(string)
		case "transport":
			req.Transport = value.(string)
		case "cwd":
			req.Cwd = value.(string)
		case "is_active":
			active = value.(bool)
		case "args":
			data, _ := json.Marshal(value)
			if err := json.Unmarshal(data, &req.Args); err != nil {
				return req, invalidCapability()
			}
		}
	}
	if strings.TrimSpace(req.Name) == "" || (strings.TrimSpace(req.Command) == "") == (strings.TrimSpace(req.URL) == "") {
		return req, invalidCapability()
	}
	if req.URL != "" {
		u, err := url.Parse(req.URL)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return req, invalidCapability()
		}
	}
	// Do not carry a credential to a changed server or executable. The user
	// must configure new secrets through Settings for the new target.
	if conn.ID != "" && ((args["url"] != nil && req.URL != conn.Config["url"]) || (args["command"] != nil && req.Command != conn.Config["command"])) {
		req.Headers = nil
		req.Env = nil
	}
	if req.Command != "" && req.Transport != "" {
		return req, invalidCapability()
	}
	if StringArg(args, "action") == "update" && len(args) <= 2 {
		return req, invalidCapability()
	}
	return req, nil
}
