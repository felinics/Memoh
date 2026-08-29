package tools

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/mcp"
)

// FederationProvider adapts a mcp.ToolSource (federated MCP connections)
// into the ToolProvider interface so the agent can load external MCP tools
// alongside built-in tools.
type FederationProvider struct {
	source mcp.ToolSource
	logger *slog.Logger
}

func NewFederationProvider(log *slog.Logger, source mcp.ToolSource) *FederationProvider {
	if log == nil {
		log = slog.Default()
	}
	return &FederationProvider{
		source: source,
		logger: log.With(slog.String("tool", "federation")),
	}
}

func (*FederationProvider) ProviderLabel() string { return "mcp" }

func (f *FederationProvider) Tools(ctx context.Context, session SessionContext) ([]sdk.Tool, error) {
	if f.source == nil {
		return nil, nil
	}
	mcpSession := toMCPSession(session)
	descriptors, err := f.source.ListTools(ctx, mcpSession)
	if err != nil {
		f.logger.Warn("federation list tools failed", slog.Any("error", err))
		return nil, nil
	}
	tools := make([]sdk.Tool, 0, len(descriptors))
	for _, desc := range descriptors {
		name := strings.TrimSpace(desc.Name)
		if name == "" || IsBuiltInToolName(name) {
			continue
		}
		desc := desc
		src := f.source
		sess := mcpSession
		tools = append(tools, sdk.Tool{
			Name:        desc.Name,
			Description: desc.Description,
			Parameters:  desc.InputSchema,
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
				args := inputAsMap(input)
				result, err := src.CallTool(ctx.Context, sess, desc.Name, args)
				if err != nil {
					return nil, err
				}
				return normalizeMCPResult(result), nil
			},
		})
	}
	return tools, nil
}

func normalizeMCPResult(result map[string]any) any {
	if result == nil {
		return map[string]any{"ok": true}
	}
	if isErr, ok := result["isError"].(bool); ok && isErr {
		if structured, ok := result["structuredContent"].(map[string]any); ok &&
			structured["error"] == "reauth_required" {
			// Keep the actionable Connect-It signal at the top level so the
			// agent can tell the user to reauthorize the bot connector.
			return structured
		}
		return result
	}
	if sc, ok := result["structuredContent"]; ok && sc != nil {
		return sc
	}
	if content, ok := result["content"]; ok {
		if items, ok := content.([]map[string]any); ok && len(items) == 1 {
			if text, ok := items[0]["text"].(string); ok {
				var parsed any
				if json.Unmarshal([]byte(text), &parsed) == nil {
					return parsed
				}
				return text
			}
		}
	}
	return result
}
