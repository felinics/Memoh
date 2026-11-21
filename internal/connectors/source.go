package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	connectsdk "github.com/felinics/connect-it/sdk/go"
	"github.com/modelcontextprotocol/go-sdk/auth"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	mcpgw "github.com/felinics/memoh/internal/mcp"
	"github.com/felinics/memoh/internal/toolcontext"
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

// listFailure keeps the error that opened a backoff window so turns inside
// the window report the same cause instead of an empty tool list.
type listFailure struct {
	until time.Time
	err   error
}

// Source exposes all enabled Connect-It connections for a bot as one
// aggregate MCP tool source.
type Source struct {
	logger  *slog.Logger
	service *Service

	mu         sync.Mutex
	auths      map[string]cachedSessionAuth
	tools      map[string]cachedTools
	retryAfter map[string]listFailure
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
		retryAfter: map[string]listFailure{},
	}
}

// Invalidate drops the discovery cache after capability management.
func (s *Source) Invalidate(botID string) {
	s.mu.Lock()
	delete(s.tools, botID)
	delete(s.auths, botID)
	delete(s.retryAfter, botID)
	s.mu.Unlock()
	if s.service != nil {
		s.service.ClearToolDiscovery(botID)
	}
}

// ToolDiagnostics explains a degraded connector tool list to the model.
func (s *Source) ToolDiagnostics(_ context.Context, session mcpgw.ToolSessionContext) []string {
	if s.service == nil {
		return nil
	}
	return s.service.ToolDiagnostics(strings.TrimSpace(session.BotID))
}

func (s *Source) ListTools(ctx context.Context, session mcpgw.ToolSessionContext) ([]mcpgw.ToolDescriptor, error) {
	botID := strings.TrimSpace(session.BotID)
	if cached, ok := s.cachedToolList(botID); ok {
		return cloneToolList(cached), nil
	}
	if failure, backingOff := s.inFailureBackoff(botID); backingOff {
		// Still failing as far as anyone knows: report the cause rather than
		// an empty list, so the agent does not mistake the backoff for "no
		// connectors".
		return nil, fmt.Errorf("connector tools unavailable, retrying later: %w", failure.err)
	}
	callCtx, cancel := context.WithTimeout(ctx, connectorCallTimeout)
	defer cancel()
	clientSession, connections, ok, err := s.connect(callCtx, botID)
	if err != nil {
		s.noteListFailure(botID, err)
		return nil, err
	}
	if !ok {
		s.service.ClearToolDiscovery(botID)
		return []mcpgw.ToolDescriptor{}, nil
	}
	defer func() { _ = clientSession.Close() }()

	result, err := clientSession.ListTools(callCtx, &sdkmcp.ListToolsParams{})
	if err != nil {
		s.noteListFailure(botID, err)
		return nil, err
	}
	s.service.RecordToolDiscoverySuccess(botID)
	groups := s.service.ConnectorGroups(callCtx, botID, connections)
	tools := make([]mcpgw.ToolDescriptor, 0, len(result.Tools))
	for _, tool := range result.Tools {
		if tool == nil || strings.TrimSpace(tool.Name) == "" {
			continue
		}
		inputSchema, err := schemaObject(tool.InputSchema)
		if err != nil {
			s.logger.WarnContext(ctx,
				"skip connect-it tool with invalid input schema",
				slog.String("tool", tool.Name),
				slog.Any("error", err),
			)
			continue
		}
		name := strings.TrimSpace(tool.Name)
		tools = append(tools, mcpgw.ToolDescriptor{
			Name:        name,
			Description: strings.TrimSpace(tool.Description),
			InputSchema: inputSchema,
			Group:       connectorToolGroup(name, groups),
		})
	}
	s.storeToolList(botID, tools)
	return cloneToolList(tools), nil
}

func connectorToolGroup(toolName string, groups map[string]string) string {
	if alias, _, ok := strings.Cut(strings.TrimSpace(toolName), "__"); ok {
		if group := strings.TrimSpace(groups[alias]); group != "" {
			return group
		}
		if slug := normalizeAlias(alias); slug != "" {
			return "connector:" + slug
		}
	}
	if group := singleConnectorGroup(groups); group != "" {
		return group
	}
	return "connector"
}

func singleConnectorGroup(groups map[string]string) string {
	unique := ""
	for _, group := range groups {
		group = strings.TrimSpace(group)
		if group == "" {
			continue
		}
		if unique == "" {
			unique = group
			continue
		}
		if unique != group {
			return ""
		}
	}
	return unique
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

func (s *Source) inFailureBackoff(botID string) (listFailure, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	failure, ok := s.retryAfter[botID]
	if !ok || !time.Now().Before(failure.until) {
		return listFailure{}, false
	}
	return failure, true
}

func (s *Source) noteListFailure(botID string, err error) {
	s.mu.Lock()
	s.retryAfter[botID] = listFailure{until: time.Now().Add(listFailureBackoff), err: err}
	s.mu.Unlock()
	if s.service != nil {
		s.service.RecordToolDiscoveryFailure(botID, err)
	}
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
	if err := toolcontext.ValidateRuntimeGuard(callCtx, session); err != nil {
		return nil, err
	}
	clientSession, _, ok, err := s.connect(callCtx, strings.TrimSpace(session.BotID))
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

func (s *Source) connect(ctx context.Context, botID string) (*sdkmcp.ClientSession, map[string]string, bool, error) {
	if botID == "" || !s.service.Configured() {
		return nil, nil, false, nil
	}
	connections, err := s.service.SessionConnections(ctx, botID)
	if err != nil {
		return nil, nil, false, err
	}
	if len(connections) == 0 {
		return nil, nil, false, nil
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
		return nil, nil, false, err
	}
	return clientSession, connections, true, nil
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
		// Connect-It issues the session even when some connections could
		// not list their tools; record those so the gap is reported per
		// connection instead of looking like the connector has no tools.
		OnSessionCreated: func(session connectsdk.MCPSession) {
			s.service.RecordToolDiscoverySession(botID, discoveryFailuresFrom(session.FailedConnections))
		},
	})
	s.auths[botID] = cachedSessionAuth{
		fingerprint: fingerprint,
		handler:     handler,
	}
	return handler
}

func discoveryFailuresFrom(failed []connectsdk.FailedConnection) []ToolDiscoveryFailure {
	if len(failed) == 0 {
		return nil
	}
	out := make([]ToolDiscoveryFailure, 0, len(failed))
	for _, failure := range failed {
		out = append(out, ToolDiscoveryFailure{
			Alias:        strings.TrimSpace(failure.Alias),
			ConnectionID: strings.TrimSpace(failure.ConnectionID),
			Error:        discoveryErrorText(errors.New(failure.Error)),
		})
	}
	return out
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
