package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	ToolHeaderAuthorization      = "Authorization" //nolint:gosec // G101: HTTP header name, not a credential.
	ToolHeaderBotID              = "X-Memoh-Bot-Id"
	ToolHeaderChatID             = "X-Memoh-Chat-Id"
	ToolHeaderRuntimeID          = "X-Memoh-Runtime-Id"
	ToolHeaderRuntimeToken       = "X-Memoh-Runtime-Token" //nolint:gosec // G101: HTTP header name, not a credential.
	ToolHeaderSessionID          = "X-Memoh-Session-Id"
	ToolHeaderStreamID           = "X-Memoh-Stream-Id"
	ToolHeaderSessionType        = "X-Memoh-Session-Type"
	ToolHeaderRouteID            = "X-Memoh-Route-Id"
	ToolHeaderChannelIdentityID  = "X-Memoh-Channel-Identity-Id"
	ToolHeaderSessionToken       = "X-Memoh-Session-Token" //nolint:gosec // G101: HTTP header name, not a credential.
	ToolHeaderCurrentPlatform    = "X-Memoh-Current-Platform"
	ToolHeaderReplyTarget        = "X-Memoh-Reply-Target"
	ToolHeaderConversationType   = "X-Memoh-Conversation-Type"
	ToolHeaderIsSubagent         = "X-Memoh-Is-Subagent"
	ToolHeaderSupportsImageInput = "X-Memoh-Supports-Image-Input"
)

func ToolSessionContextFromHTTP(req *http.Request, fallbackBotID string) ToolSessionContext {
	if req == nil {
		return ToolSessionContext{BotID: strings.TrimSpace(fallbackBotID), ChatID: strings.TrimSpace(fallbackBotID)}
	}
	sessionBotID := strings.TrimSpace(fallbackBotID)
	chatID := firstNonEmptyHTTPHeader(req, ToolHeaderChatID)
	if chatID == "" {
		chatID = sessionBotID
	}
	return ToolSessionContext{
		BotID:              sessionBotID,
		ChatID:             chatID,
		RuntimeID:          firstNonEmptyHTTPHeader(req, ToolHeaderRuntimeID),
		RuntimeToken:       strings.TrimSpace(req.Header.Get(ToolHeaderRuntimeToken)),
		SessionID:          firstNonEmptyHTTPHeader(req, ToolHeaderSessionID),
		StreamID:           firstNonEmptyHTTPHeader(req, ToolHeaderStreamID),
		SessionType:        firstNonEmptyHTTPHeader(req, ToolHeaderSessionType),
		RouteID:            firstNonEmptyHTTPHeader(req, ToolHeaderRouteID),
		ChannelIdentityID:  firstNonEmptyHTTPHeader(req, ToolHeaderChannelIdentityID),
		SessionToken:       strings.TrimSpace(req.Header.Get(ToolHeaderSessionToken)),
		CurrentPlatform:    strings.TrimSpace(req.Header.Get(ToolHeaderCurrentPlatform)),
		ReplyTarget:        strings.TrimSpace(req.Header.Get(ToolHeaderReplyTarget)),
		ConversationType:   strings.TrimSpace(req.Header.Get(ToolHeaderConversationType)),
		IsSubagent:         strings.EqualFold(strings.TrimSpace(req.Header.Get(ToolHeaderIsSubagent)), "true"),
		SupportsImageInput: strings.EqualFold(strings.TrimSpace(req.Header.Get(ToolHeaderSupportsImageInput)), "true"),
	}
}

func firstNonEmptyHTTPHeader(req *http.Request, names ...string) string {
	if req == nil {
		return ""
	}
	for _, name := range names {
		if value := strings.TrimSpace(req.Header.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

func ServeToolMCPHTTP(w http.ResponseWriter, req *http.Request, log *slog.Logger, gateway *ToolGatewayService, contexts *ToolSessionContextStore, session ToolSessionContext) {
	serveToolMCPHTTP(w, req, log, gateway, contexts, session, true)
}

// ServeToolMCPHTTPWithoutContextMerge serves a tool MCP request using the
// request-carried session context without merging long-lived ACP session state.
func ServeToolMCPHTTPWithoutContextMerge(w http.ResponseWriter, req *http.Request, log *slog.Logger, gateway *ToolGatewayService, contexts *ToolSessionContextStore, session ToolSessionContext) {
	serveToolMCPHTTP(w, req, log, gateway, contexts, session, false)
}

func serveToolMCPHTTP(w http.ResponseWriter, req *http.Request, log *slog.Logger, gateway *ToolGatewayService, contexts *ToolSessionContextStore, session ToolSessionContext, mergeContext bool) {
	if gateway == nil {
		http.Error(w, "tool gateway not configured", http.StatusServiceUnavailable)
		return
	}
	EnsureStreamableAcceptHeader(req)
	if contexts != nil && mergeContext {
		session = contexts.Merge(session)
	}
	handler := sdkmcp.NewStreamableHTTPHandler(
		func(*http.Request) *sdkmcp.Server {
			return BuildToolMCPServer(gateway, contexts, session)
		},
		&sdkmcp.StreamableHTTPOptions{
			Stateless:    true,
			JSONResponse: true,
			Logger:       log,
		},
	)
	handler.ServeHTTP(w, req)
}

func EnsureStreamableAcceptHeader(req *http.Request) {
	if req == nil {
		return
	}
	acceptValues := req.Header.Values("Accept")
	joined := strings.ToLower(strings.Join(acceptValues, ","))
	hasJSON := strings.Contains(joined, "application/json") || strings.Contains(joined, "application/*") || strings.Contains(joined, "*/*")
	hasStream := strings.Contains(joined, "text/event-stream") || strings.Contains(joined, "text/*") || strings.Contains(joined, "*/*")
	if hasJSON && hasStream {
		return
	}

	base := strings.TrimSpace(strings.Join(acceptValues, ","))
	parts := make([]string, 0, 3)
	if base != "" {
		parts = append(parts, base)
	}
	if !hasJSON {
		parts = append(parts, "application/json")
	}
	if !hasStream {
		parts = append(parts, "text/event-stream")
	}
	if len(parts) == 0 {
		parts = append(parts, "application/json", "text/event-stream")
	}
	req.Header.Set("Accept", strings.Join(parts, ", "))
}

func BuildToolMCPServer(gateway *ToolGatewayService, contexts *ToolSessionContextStore, session ToolSessionContext) *sdkmcp.Server {
	if gateway == nil {
		return nil
	}
	server := sdkmcp.NewServer(
		&sdkmcp.Implementation{
			Name:    "memoh-tools-gateway",
			Version: "1.0.0",
		},
		&sdkmcp.ServerOptions{
			Capabilities: &sdkmcp.ServerCapabilities{
				Tools: &sdkmcp.ToolCapabilities{
					ListChanged: false,
				},
			},
		},
	)
	server.AddReceivingMiddleware(ToolGatewayMiddleware(gateway, contexts, session))
	return server
}

func ToolGatewayMiddleware(gateway *ToolGatewayService, contexts *ToolSessionContextStore, session ToolSessionContext) sdkmcp.Middleware {
	return func(next sdkmcp.MethodHandler) sdkmcp.MethodHandler {
		return func(ctx context.Context, method string, req sdkmcp.Request) (sdkmcp.Result, error) {
			switch strings.TrimSpace(method) {
			case "tools/list":
				tools, err := gateway.ListTools(ctx, session)
				if err != nil {
					return nil, err
				}
				return &sdkmcp.ListToolsResult{
					Tools: ConvertGatewayToolsToSDK(tools),
				}, nil
			case "tools/call":
				if strings.TrimSpace(session.RuntimeID) != "" && !session.RuntimeActive {
					return nil, errors.New("ACP runtime is not processing a prompt")
				}
				callReq, ok := req.(*sdkmcp.ServerRequest[*sdkmcp.CallToolParamsRaw])
				if !ok || callReq == nil || callReq.Params == nil {
					return nil, errors.New("tools/call params is required")
				}
				payload, err := BuildToolCallPayloadFromRaw(callReq.Params)
				if err != nil {
					return nil, err
				}
				toolCallID := "mcp-http-" + uuid.NewString()
				recordToolEvent(contexts, session, ToolStreamEvent{
					Type:       "tool_call_start",
					ToolCallID: toolCallID,
					ToolName:   payload.Name,
					Input:      payload.Arguments,
				})
				callSession := session
				callSession.ToolCallID = toolCallID
				result, err := gateway.CallTool(ctx, callSession, payload)
				if err != nil {
					recordToolEvent(contexts, session, ToolStreamEvent{
						Type:       "tool_call_end",
						ToolCallID: toolCallID,
						ToolName:   payload.Name,
						Input:      payload.Arguments,
						Result:     BuildToolErrorResult(err.Error()),
						Error:      err.Error(),
					})
					return nil, err
				}
				converted, err := ConvertGatewayCallResultToSDK(result)
				if err != nil {
					recordToolEvent(contexts, session, ToolStreamEvent{
						Type:       "tool_call_end",
						ToolCallID: toolCallID,
						ToolName:   payload.Name,
						Input:      payload.Arguments,
						Result:     BuildToolErrorResult(err.Error()),
						Error:      err.Error(),
					})
					return nil, err
				}
				recordToolEvent(contexts, session, ToolStreamEvent{
					Type:       "tool_call_end",
					ToolCallID: toolCallID,
					ToolName:   payload.Name,
					Input:      payload.Arguments,
					Result:     result,
				})
				return converted, nil
			default:
				return next(ctx, method, req)
			}
		}
	}
}

func BuildToolCallPayloadFromRaw(params *sdkmcp.CallToolParamsRaw) (ToolCallPayload, error) {
	if params == nil {
		return ToolCallPayload{}, errors.New("tools/call params is required")
	}
	name := strings.TrimSpace(params.Name)
	if name == "" {
		return ToolCallPayload{}, errors.New("tools/call name is required")
	}
	arguments := map[string]any{}
	if len(params.Arguments) > 0 {
		if err := json.Unmarshal(params.Arguments, &arguments); err != nil {
			return ToolCallPayload{}, err
		}
	}
	if arguments == nil {
		arguments = map[string]any{}
	}
	return ToolCallPayload{
		Name:      name,
		Arguments: arguments,
	}, nil
}

func ConvertGatewayToolsToSDK(items []ToolDescriptor) []*sdkmcp.Tool {
	if len(items) == 0 {
		return []*sdkmcp.Tool{}
	}
	tools := make([]*sdkmcp.Tool, 0, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		inputSchema := item.InputSchema
		if inputSchema == nil {
			inputSchema = map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			}
		}
		tools = append(tools, &sdkmcp.Tool{
			Name:        name,
			Description: strings.TrimSpace(item.Description),
			InputSchema: inputSchema,
		})
	}
	return tools
}

func ConvertGatewayCallResultToSDK(result map[string]any) (*sdkmcp.CallToolResult, error) {
	if result == nil {
		result = BuildToolSuccessResult(map[string]any{"ok": true})
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	var out sdkmcp.CallToolResult
	if err := json.Unmarshal(payload, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func recordToolEvent(contexts *ToolSessionContextStore, session ToolSessionContext, event ToolStreamEvent) {
	if contexts == nil {
		return
	}
	contexts.AppendToolEvent(session, event)
}
