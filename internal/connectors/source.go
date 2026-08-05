package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	connectsdk "github.com/memohai/connect-it/sdk/go"
	"github.com/modelcontextprotocol/go-sdk/auth"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	mcpgw "github.com/memohai/memoh/internal/mcp"
)

const (
	connectorCallTimeout = 60 * time.Second
	// toolsCacheTTL matches the MCP federation source: agent turns arriving
	// within it reuse the last listing instead of redialing Connect-It.
	toolsCacheTTL = 5 * time.Second
	// listFailureBackoff keeps one unreachable Connect-It from stalling
	// every turn for the full call timeout: after a failed listing the
	// source stays quiet for this window instead of redialing.
	listFailureBackoff = 30 * time.Second
)

type cachedSessionAuth struct {
	fingerprint string
	handler     auth.OAuthHandler
}

type cachedTools struct {
	expiresAt time.Time
	tools     []mcpgw.ToolDescriptor
}

// Source exposes all enabled Connect-It connections for a bot as one
// aggregate MCP tool source.
type Source struct {
	logger  *slog.Logger
	service *Service

	mu         sync.Mutex
	auths      map[string]cachedSessionAuth
	tools      map[string]cachedTools
	retryAfter map[string]time.Time
}

func NewSource(log *slog.Logger, service *Service) *Source {
	if log == nil {
		log = slog.Default()
	}
	return &Source{
		logger:     log.With(slog.String("tool_source", "connect_it")),
		service:    service,
		auths:      map[string]cachedSessionAuth{},
		tools:      map[string]cachedTools{},
		retryAfter: map[string]time.Time{},
	}
}

func (s *Source) ListTools(ctx context.Context, session mcpgw.ToolSessionContext) ([]mcpgw.ToolDescriptor, error) {
	botID := strings.TrimSpace(session.BotID)
	if cached, ok := s.cachedToolList(botID); ok {
		return cloneToolList(cached), nil
	}
	if s.inFailureBackoff(botID) {
		return []mcpgw.ToolDescriptor{}, nil
	}
	callCtx, cancel := context.WithTimeout(ctx, connectorCallTimeout)
	defer cancel()
	clientSession, ok, err := s.connect(callCtx, botID)
	if err != nil {
		s.noteListFailure(botID)
		return nil, err
	}
	if !ok {
		return []mcpgw.ToolDescriptor{}, nil
	}
	defer func() { _ = clientSession.Close() }()

	result, err := clientSession.ListTools(callCtx, &sdkmcp.ListToolsParams{})
	if err != nil {
		s.noteListFailure(botID)
		return nil, err
	}
	tools := make([]mcpgw.ToolDescriptor, 0, len(result.Tools))
	for _, tool := range result.Tools {
		if tool == nil || strings.TrimSpace(tool.Name) == "" {
			continue
		}
		inputSchema, err := schemaObject(tool.InputSchema)
		if err != nil {
			s.logger.Warn(
				"skip connect-it tool with invalid input schema",
				slog.String("tool", tool.Name),
				slog.Any("error", err),
			)
			continue
		}
		tools = append(tools, mcpgw.ToolDescriptor{
			Name:        strings.TrimSpace(tool.Name),
			Description: strings.TrimSpace(tool.Description),
			InputSchema: inputSchema,
		})
	}
	s.storeToolList(botID, tools)
	return cloneToolList(tools), nil
}

func (s *Source) cachedToolList(botID string) ([]mcpgw.ToolDescriptor, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cached, ok := s.tools[botID]
	if !ok || time.Now().After(cached.expiresAt) {
		return nil, false
	}
	return cached.tools, true
}

func (s *Source) storeToolList(botID string, tools []mcpgw.ToolDescriptor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tools[botID] = cachedTools{expiresAt: time.Now().Add(toolsCacheTTL), tools: tools}
	delete(s.retryAfter, botID)
}

func (s *Source) inFailureBackoff(botID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Now().Before(s.retryAfter[botID])
}

func (s *Source) noteListFailure(botID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retryAfter[botID] = time.Now().Add(listFailureBackoff)
}

func cloneToolList(items []mcpgw.ToolDescriptor) []mcpgw.ToolDescriptor {
	out := make([]mcpgw.ToolDescriptor, len(items))
	copy(out, items)
	return out
}

func (s *Source) CallTool(
	ctx context.Context,
	session mcpgw.ToolSessionContext,
	toolName string,
	arguments map[string]any,
) (map[string]any, error) {
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return nil, mcpgw.ErrToolNotFound
	}
	callCtx, cancel := context.WithTimeout(ctx, connectorCallTimeout)
	defer cancel()
	if err := mcpgw.ValidateRuntimeGuard(callCtx, session); err != nil {
		return nil, err
	}
	clientSession, ok, err := s.connect(callCtx, strings.TrimSpace(session.BotID))
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, mcpgw.ErrToolNotFound
	}
	defer func() { _ = clientSession.Close() }()
	if arguments == nil {
		arguments = map[string]any{}
	}

	result, err := clientSession.CallTool(callCtx, &sdkmcp.CallToolParams{
		Name:      toolName,
		Arguments: arguments,
	})
	if err != nil {
		return nil, err
	}
	return callResultObject(result)
}

func (s *Source) connect(ctx context.Context, botID string) (*sdkmcp.ClientSession, bool, error) {
	if botID == "" || !s.service.Configured() {
		return nil, false, nil
	}
	connections, err := s.service.SessionConnections(ctx, botID)
	if err != nil {
		return nil, false, err
	}
	if len(connections) == 0 {
		return nil, false, nil
	}
	handler := s.authHandler(botID, connections)
	mcpClient := sdkmcp.NewClient(&sdkmcp.Implementation{
		Name:    "memoh-connectors",
		Version: "1.0.0",
	}, nil)
	clientSession, err := mcpClient.Connect(ctx, &sdkmcp.StreamableClientTransport{
		Endpoint:             s.service.client.MCPEndpoint(),
		OAuthHandler:         handler,
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		return nil, false, err
	}
	return clientSession, true, nil
}

func (s *Source) authHandler(
	botID string,
	connections map[string]string,
) auth.OAuthHandler {
	fingerprint := connectionFingerprint(connections)
	s.mu.Lock()
	defer s.mu.Unlock()
	if cached, ok := s.auths[botID]; ok && cached.fingerprint == fingerprint {
		return cached.handler
	}
	handler := s.service.client.MCPAuthHandler(connectsdk.MCPSessionConfig{
		Connections: connections,
		TTL:         time.Hour,
	})
	s.auths[botID] = cachedSessionAuth{
		fingerprint: fingerprint,
		handler:     handler,
	}
	return handler
}

func connectionFingerprint(connections map[string]string) string {
	keys := make([]string, 0, len(connections))
	for alias := range connections {
		keys = append(keys, alias)
	}
	sort.Strings(keys)
	var fingerprint strings.Builder
	for _, alias := range keys {
		fingerprint.WriteString(alias)
		fingerprint.WriteByte(0)
		fingerprint.WriteString(connections[alias])
		fingerprint.WriteByte(0)
	}
	return fingerprint.String()
}

func schemaObject(schema any) (map[string]any, error) {
	if schema == nil {
		return map[string]any{"type": "object"}, nil
	}
	if object, ok := schema.(map[string]any); ok {
		return object, nil
	}
	payload, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	var object map[string]any
	if err := json.Unmarshal(payload, &object); err != nil {
		return nil, err
	}
	if object == nil {
		return nil, errors.New("input schema is not an object")
	}
	return object, nil
}

func callResultObject(result *sdkmcp.CallToolResult) (map[string]any, error) {
	if result == nil {
		return nil, nil
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	var object map[string]any
	if err := json.Unmarshal(payload, &object); err != nil {
		return nil, err
	}
	return object, nil
}
