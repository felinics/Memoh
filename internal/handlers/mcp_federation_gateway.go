package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	mcpgw "github.com/felinics/memoh/internal/mcp"
)

type MCPFederationGateway struct {
	handler      *ContainerdHandler
	logger       *slog.Logger
	client       *http.Client
	oauthService *mcpgw.OAuthService
}

func NewMCPFederationGateway(log *slog.Logger, handler *ContainerdHandler) *MCPFederationGateway {
	if log == nil {
		log = slog.Default()
	}
	return &MCPFederationGateway{
		handler: handler,
		logger:  log.With(slog.String("gateway", "mcp_federation")),
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// SetOAuthService injects the OAuth service for token-based authentication.
func (g *MCPFederationGateway) SetOAuthService(svc *mcpgw.OAuthService) {
	g.oauthService = svc
}

func (g *MCPFederationGateway) ListHTTPConnectionTools(ctx context.Context, connection mcpgw.Connection) ([]mcpgw.ToolDescriptor, error) {
	session, err := g.connectStreamableSession(ctx, connection)
	if err != nil {
		return nil, err
	}
	defer func() { _ = session.Close() }()
	result, err := session.ListTools(ctx, &sdkmcp.ListToolsParams{})
	if err != nil {
		return nil, err
	}
	return convertSDKTools(result.Tools), nil
}

func (g *MCPFederationGateway) CallHTTPConnectionTool(ctx context.Context, connection mcpgw.Connection, toolName string, args map[string]any) (map[string]any, error) {
	session, err := g.connectStreamableSession(ctx, connection)
	if err != nil {
		return nil, err
	}
	defer func() { _ = session.Close() }()
	result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      strings.TrimSpace(toolName),
		Arguments: args,
	})
	if err != nil {
		return nil, err
	}
	return wrapSDKToolResult(result)
}

func (g *MCPFederationGateway) ListSSEConnectionTools(ctx context.Context, connection mcpgw.Connection) ([]mcpgw.ToolDescriptor, error) {
	session, err := g.connectSSESession(ctx, connection)
	if err != nil {
		return nil, err
	}
	defer func() { _ = session.Close() }()
	result, err := session.ListTools(ctx, &sdkmcp.ListToolsParams{})
	if err != nil {
		return nil, err
	}
	return convertSDKTools(result.Tools), nil
}

func (g *MCPFederationGateway) CallSSEConnectionTool(ctx context.Context, connection mcpgw.Connection, toolName string, args map[string]any) (map[string]any, error) {
	session, err := g.connectSSESession(ctx, connection)
	if err != nil {
		return nil, err
	}
	defer func() { _ = session.Close() }()
	result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      strings.TrimSpace(toolName),
		Arguments: args,
	})
	if err != nil {
		return nil, err
	}
	return wrapSDKToolResult(result)
}

func (g *MCPFederationGateway) connectStreamableSession(ctx context.Context, connection mcpgw.Connection) (*sdkmcp.ClientSession, error) {
	url := strings.TrimSpace(anyToString(connection.Config["url"]))
	if url == "" {
		return nil, errors.New("http mcp url is required")
	}
	client := sdkmcp.NewClient(&sdkmcp.Implementation{
		Name:    "memoh-federation-client",
		Version: "v1",
	}, nil)
	transport := &sdkmcp.StreamableClientTransport{
		Endpoint:   url,
		HTTPClient: g.connectionHTTPClient(ctx, connection),
		MaxRetries: -1,
	}
	return client.Connect(ctx, transport, nil)
}

func (g *MCPFederationGateway) connectSSESession(ctx context.Context, connection mcpgw.Connection) (*sdkmcp.ClientSession, error) {
	endpoints := resolveSSEEndpointCandidates(connection.Config)
	if len(endpoints) == 0 {
		return nil, errors.New("sse mcp url is required")
	}
	var lastErr error
	for _, endpoint := range endpoints {
		client := sdkmcp.NewClient(&sdkmcp.Implementation{
			Name:    "memoh-federation-client",
			Version: "v1",
		}, nil)
		transport := &sdkmcp.SSEClientTransport{
			Endpoint:   endpoint,
			HTTPClient: g.connectionHTTPClient(ctx, connection),
		}
		session, err := client.Connect(ctx, transport, nil)
		if err == nil {
			return session, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("no sse endpoint candidate available")
	}
	return nil, fmt.Errorf("connect sse mcp failed: %w", lastErr)
}

func resolveSSEEndpointCandidates(config map[string]any) []string {
	if config == nil {
		return []string{}
	}

	seen := map[string]struct{}{}
	out := make([]string, 0, 4)
	appendEndpoint := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}

	for _, key := range []string{"sse_url", "sseUrl"} {
		appendEndpoint(anyToString(config[key]))
	}

	baseURL := strings.TrimSpace(anyToString(config["url"]))
	appendEndpoint(baseURL)

	var messageURL string
	for _, key := range []string{"message_url", "messageUrl"} {
		if value := strings.TrimSpace(anyToString(config[key])); value != "" {
			messageURL = value
			break
		}
	}
	if messageURL != "" {
		normalized := strings.TrimSuffix(messageURL, "/")
		if strings.HasSuffix(normalized, "/message") {
			appendEndpoint(strings.TrimSuffix(normalized, "/message") + "/sse")
		}
		appendEndpoint(messageURL)
	}

	if baseURL != "" {
		normalized := strings.TrimSuffix(baseURL, "/")
		if strings.HasSuffix(normalized, "/message") {
			appendEndpoint(strings.TrimSuffix(normalized, "/message") + "/sse")
		}
	}

	return out
}

func (g *MCPFederationGateway) connectionHTTPClient(ctx context.Context, connection mcpgw.Connection) *http.Client {
	base := g.client
	if base == nil {
		base = &http.Client{Timeout: 30 * time.Second}
	}
	headers := normalizeHeaderMap(connection.Config["headers"])

	if strings.TrimSpace(connection.AuthType) == "oauth" && g.oauthService != nil {
		token, err := g.oauthService.GetValidToken(ctx, connection.ID)
		if err != nil {
			g.logger.Warn("failed to get OAuth token for connection",
				slog.String("connection_id", connection.ID),
				slog.Any("error", err))
		} else if token != "" {
			if headers == nil {
				headers = map[string]string{}
			}
			headers["Authorization"] = "Bearer " + token
		}
	}

	if len(headers) == 0 {
		return base
	}
	transport := base.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &http.Client{
		Timeout:       base.Timeout,
		CheckRedirect: base.CheckRedirect,
		Jar:           base.Jar,
		Transport: &staticHeaderRoundTripper{
			next:    transport,
			headers: headers,
		},
	}
}

func (g *MCPFederationGateway) ListStdioConnectionTools(ctx context.Context, botID string, connection mcpgw.Connection) ([]mcpgw.ToolDescriptor, error) {
	sess, err := g.startStdioConnectionSession(ctx, botID, connection)
	if err != nil {
		return nil, err
	}
	defer sess.Close()

	result, err := sess.session.ListTools(ctx, &sdkmcp.ListToolsParams{})
	if err != nil {
		return nil, sess.enrichError(err)
	}
	return convertSDKTools(result.Tools), nil
}

func (g *MCPFederationGateway) CallStdioConnectionTool(ctx context.Context, botID string, connection mcpgw.Connection, toolName string, args map[string]any) (map[string]any, error) {
	sess, err := g.startStdioConnectionSession(ctx, botID, connection)
	if err != nil {
		return nil, err
	}
	defer sess.Close()

	result, err := sess.session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      strings.TrimSpace(toolName),
		Arguments: args,
	})
	if err != nil {
		return nil, sess.enrichError(err)
	}
	return wrapSDKToolResult(result)
}

func (g *MCPFederationGateway) startStdioConnectionSession(ctx context.Context, botID string, connection mcpgw.Connection) (*mcpStdioClient, error) {
	if g.handler == nil {
		return nil, errors.New("containerd handler not configured")
	}
	if err := g.handler.manager.EnsureRunning(ctx, botID); err != nil {
		return nil, err
	}
	containerID, err := g.handler.manager.ContainerID(ctx, botID)
	if err != nil {
		return nil, err
	}

	command := strings.TrimSpace(anyToString(connection.Config["command"]))
	if command == "" {
		return nil, errors.New("stdio mcp command is required")
	}
	request := MCPStdioRequest{
		Name:    strings.TrimSpace(connection.Name),
		Command: command,
		Args:    normalizeStringSlice(connection.Config["args"]),
		Env:     normalizeStringMap(connection.Config["env"]),
		Cwd:     strings.TrimSpace(anyToString(connection.Config["cwd"])),
	}
	// Federation sessions are short-lived and unregistered — no onClose. Memoh
	// is the real client here, so the handshake uses the default identity.
	return g.handler.startContainerdMCPCommandSession(ctx, botID, containerID, request, nil, defaultStdioSDKClient())
}

func convertSDKTools(items []*sdkmcp.Tool) []mcpgw.ToolDescriptor {
	if len(items) == 0 {
		return []mcpgw.ToolDescriptor{}
	}
	tools := make([]mcpgw.ToolDescriptor, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		tools = append(tools, mcpgw.ToolDescriptor{
			Name:        name,
			Description: strings.TrimSpace(item.Description),
			InputSchema: normalizeToolInputSchema(item.InputSchema),
		})
	}
	return tools
}

func normalizeToolInputSchema(raw any) map[string]any {
	if schema, ok := raw.(map[string]any); ok && schema != nil {
		return schema
	}
	if raw != nil {
		payload, err := json.Marshal(raw)
		if err == nil {
			var schema map[string]any
			if err := json.Unmarshal(payload, &schema); err == nil && schema != nil {
				return schema
			}
		}
	}
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}

func wrapSDKToolResult(result *sdkmcp.CallToolResult) (map[string]any, error) {
	if result == nil {
		return map[string]any{
			"result": mcpgw.BuildToolSuccessResult(map[string]any{"ok": true}),
		}, nil
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	var parsed map[string]any
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return nil, err
	}
	if parsed == nil {
		parsed = map[string]any{}
	}
	return map[string]any{"result": parsed}, nil
}

func normalizeHeaderMap(raw any) map[string]string {
	switch value := raw.(type) {
	case map[string]string:
		return value
	case map[string]any:
		out := make(map[string]string, len(value))
		for k, v := range value {
			key := strings.TrimSpace(k)
			val := strings.TrimSpace(anyToString(v))
			if key == "" || val == "" {
				continue
			}
			out[key] = val
		}
		return out
	default:
		return map[string]string{}
	}
}

func normalizeStringSlice(raw any) []string {
	switch value := raw.(type) {
	case []string:
		out := make([]string, 0, len(value))
		for _, item := range value {
			item = strings.TrimSpace(item)
			if item != "" {
				out = append(out, item)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			val := strings.TrimSpace(anyToString(item))
			if val != "" {
				out = append(out, val)
			}
		}
		return out
	default:
		return []string{}
	}
}

func normalizeStringMap(raw any) map[string]string {
	switch value := raw.(type) {
	case map[string]string:
		return value
	case map[string]any:
		out := make(map[string]string, len(value))
		for k, v := range value {
			key := strings.TrimSpace(k)
			val := strings.TrimSpace(anyToString(v))
			if key == "" {
				continue
			}
			out[key] = val
		}
		return out
	default:
		return map[string]string{}
	}
}

func anyToString(v any) string {
	if v == nil {
		return ""
	}
	switch value := v.(type) {
	case string:
		return value
	default:
		return fmt.Sprintf("%v", v)
	}
}

type staticHeaderRoundTripper struct {
	next    http.RoundTripper
	headers map[string]string
}

func (t *staticHeaderRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	next := t.next
	if next == nil {
		next = http.DefaultTransport
	}
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	for key, value := range t.headers {
		headerKey := strings.TrimSpace(key)
		headerVal := strings.TrimSpace(value)
		if headerKey == "" || headerVal == "" {
			continue
		}
		clone.Header.Set(headerKey, headerVal)
	}
	return next.RoundTrip(clone)
}
