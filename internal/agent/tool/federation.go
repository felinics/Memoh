package tools

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/toolexec"
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

func (f *FederationProvider) Tools(ctx context.Context, session SessionContext) ([]toolexec.Tool, error) {
	if f.source == nil {
		return nil, nil
	}
	mcpSession := toMCPSession(session)
	descriptors, err := f.source.ListTools(ctx, mcpSession)
	if err != nil {
		f.logger.WarnContext(ctx, "federation list tools failed", slog.Any("error", err))
		return nil, nil
	}
	tools := make([]toolexec.Tool, 0, len(descriptors))
	for _, desc := range descriptors {
		name := strings.TrimSpace(desc.Name)
		if name == "" || IsBuiltInToolName(name) {
			continue
		}
		desc := desc
		src := f.source
		sess := mcpSession
		schema, err := toolexec.ResolveSchema(desc.InputSchema)
		if err != nil {
			// A tool advertised without its parameters cannot be called
			// correctly; leave it out of this turn rather than mislead the model.
			f.logger.Warn("federation tool schema is not usable; tool skipped", slog.String("tool", desc.Name), slog.Any("error", err))
			continue
		}
		tools = append(tools, toolexec.Tool{
			Name:        desc.Name,
			Description: desc.Description,
			Parameters:  schema,
			Execute: func(ctx *toolexec.ToolExecContext, input sdk.ToolArguments) (sdk.ToolOutput, error) {
				args := inputAsMap(input)
				result, err := src.CallTool(ctx.Context, sess, desc.Name, args)
				if err != nil {
					return sdk.ToolOutput{}, err
				}
				return toolexec.OutputFromValue(normalizeMCPResult(result)), nil
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
