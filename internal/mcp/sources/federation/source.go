package federation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	mcpgw "github.com/felinics/memoh/internal/mcp"
	"github.com/felinics/memoh/internal/toolcontext"
)

// ErrToolListingDegraded marks a listing that is missing tools: at least one
// connection failed and had no last-good snapshot to fall back to. The
// returned descriptors are still the source's best effort (failed
// connections that previously succeeded keep serving their last successful
// list), but callers that cache registries must treat the error as "do not
// cache, serve your own fallback" so a partial listing never becomes the new
// baseline.
var ErrToolListingDegraded = errors.New("federation tool listing degraded")

const cacheTTL = 5 * time.Second

// mcpCallTimeout caps individual MCP tool calls and tool listing to prevent
// stuck external MCP servers from blocking the agent indefinitely.
const mcpCallTimeout = 60 * time.Second

type ConnectionLister interface {
	ListActiveByBot(ctx context.Context, botID string) ([]mcpgw.Connection, error)
}

type Gateway interface {
	ListHTTPConnectionTools(ctx context.Context, connection mcpgw.Connection) ([]mcpgw.ToolDescriptor, error)
	CallHTTPConnectionTool(ctx context.Context, connection mcpgw.Connection, toolName string, args map[string]any) (map[string]any, error)

	ListSSEConnectionTools(ctx context.Context, connection mcpgw.Connection) ([]mcpgw.ToolDescriptor, error)
	CallSSEConnectionTool(ctx context.Context, connection mcpgw.Connection, toolName string, args map[string]any) (map[string]any, error)

	ListStdioConnectionTools(ctx context.Context, botID string, connection mcpgw.Connection) ([]mcpgw.ToolDescriptor, error)
	CallStdioConnectionTool(ctx context.Context, botID string, connection mcpgw.Connection, toolName string, args map[string]any) (map[string]any, error)
}

type toolRoute struct {
	sourceType   string
	originalName string
	connection   mcpgw.Connection
}

type cacheEntry struct {
	expiresAt time.Time
	routes    map[string]toolRoute
	tools     []mcpgw.ToolDescriptor
	degraded  bool
}

// fallbackTool is one tool of a connection's last successful listing: the
// final (prefixed, de-duplicated) descriptor plus the remote name the route
// needs to call it.
type fallbackTool struct {
	descriptor   mcpgw.ToolDescriptor
	originalName string
}

type Source struct {
	logger      *slog.Logger
	gateway     Gateway
	connections ConnectionLister
	reserved    func(string) bool

	mu    sync.Mutex
	cache map[string]cacheEntry
	// fallback holds each connection's last successful tool listing, keyed by
	// connection ID. A connection that fails to list keeps contributing its
	// last known tools so one unreachable MCP server never rewrites the
	// agent's tool documentation (Context Epoch: 工具列表失败沿用旧版).
	fallback map[string][]fallbackTool
}

type SourceOption func(*Source)

func WithReservedToolName(fn func(string) bool) SourceOption {
	return func(s *Source) {
		s.reserved = fn
	}
}

func NewSource(log *slog.Logger, gateway Gateway, connections ConnectionLister, opts ...SourceOption) *Source {
	if log == nil {
		log = slog.Default()
	}
	source := &Source{
		logger:      log.With(slog.String("tool_source", "federated_mcp_tool")),
		gateway:     gateway,
		connections: connections,
		cache:       map[string]cacheEntry{},
		fallback:    map[string][]fallbackTool{},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(source)
		}
	}
	return source
}

// Invalidate forgets discovery after a management change.
func (s *Source) Invalidate(botID string) { s.mu.Lock(); delete(s.cache, botID); s.mu.Unlock() }

func (s *Source) ListTools(ctx context.Context, session mcpgw.ToolSessionContext) ([]mcpgw.ToolDescriptor, error) {
	botID := strings.TrimSpace(session.BotID)
	if botID == "" || s.gateway == nil {
		return []mcpgw.ToolDescriptor{}, nil
	}
	if cached, ok := s.getCache(botID); ok {
		if cached.degraded {
			return cloneTools(cached.tools), ErrToolListingDegraded
		}
		return cloneTools(cached.tools), nil
	}
	tools, routes, degraded := s.buildToolsAndRoutes(ctx, botID)
	s.setCache(botID, cacheEntry{
		expiresAt: time.Now().Add(cacheTTL),
		routes:    routes,
		tools:     tools,
		degraded:  degraded,
	})
	if degraded {
		return cloneTools(tools), ErrToolListingDegraded
	}
	return cloneTools(tools), nil
}

func (s *Source) CallTool(ctx context.Context, session mcpgw.ToolSessionContext, toolName string, arguments map[string]any) (map[string]any, error) {
	if s.gateway == nil {
		return mcpgw.BuildToolErrorResult("federation gateway not available"), nil
	}
	botID := strings.TrimSpace(session.BotID)
	if botID == "" {
		return mcpgw.BuildToolErrorResult("bot_id is required"), nil
	}

	route, ok := s.getRoute(botID, toolName)
	if !ok {
		// Refresh route cache; result intentionally discarded.
		if _, err := s.ListTools(ctx, session); err != nil {
			s.logger.WarnContext(ctx, "federation: refresh tools cache failed", slog.Any("error", err))
		}
		route, ok = s.getRoute(botID, toolName)
		if !ok {
			return nil, mcpgw.ErrToolNotFound
		}
	}
	connections, listErr := s.connections.ListActiveByBot(ctx, botID)
	if listErr != nil {
		return nil, listErr
	}
	valid := false
	for _, connection := range connections {
		if connection.ID == route.connection.ID && connection.UpdatedAt.Equal(route.connection.UpdatedAt) {
			valid = true
			route.connection = connection
			break
		}
	}
	if !valid {
		s.Invalidate(botID)
		return nil, mcpgw.ErrToolNotFound
	}
	if arguments == nil {
		arguments = map[string]any{}
	}

	callCtx, callCancel := context.WithTimeout(ctx, mcpCallTimeout)
	defer callCancel()
	if err := toolcontext.ValidateRuntimeGuard(callCtx, session); err != nil {
		return nil, err
	}

	var (
		payload map[string]any
		err     error
	)
	switch route.sourceType {
	case "http":
		payload, err = s.gateway.CallHTTPConnectionTool(callCtx, route.connection, route.originalName, arguments)
	case "sse":
		payload, err = s.gateway.CallSSEConnectionTool(callCtx, route.connection, route.originalName, arguments)
	case "stdio":
		payload, err = s.gateway.CallStdioConnectionTool(callCtx, botID, route.connection, route.originalName, arguments)
	default:
		return mcpgw.BuildToolErrorResult("unsupported federated source"), nil
	}
	if err != nil {
		return mcpgw.BuildToolErrorResult(err.Error()), nil
	}
	if err := mcpgw.PayloadError(payload); err != nil {
		return mcpgw.BuildToolErrorResult(err.Error()), nil
	}
	if result, ok := payload["result"].(map[string]any); ok {
		return result, nil
	}
	return mcpgw.BuildToolSuccessResult(payload), nil
}

func (s *Source) buildToolsAndRoutes(ctx context.Context, botID string) ([]mcpgw.ToolDescriptor, map[string]toolRoute, bool) {
	routes := map[string]toolRoute{}
	tools := make([]mcpgw.ToolDescriptor, 0, 16)
	degraded := false
	fallbackUpdates := map[string][]fallbackTool{}

	addTool := func(descriptor mcpgw.ToolDescriptor, route toolRoute) (string, bool) {
		name := strings.TrimSpace(descriptor.Name)
		if name == "" {
			return "", false
		}
		finalName := name
		if _, exists := routes[finalName]; exists || s.isReservedToolName(finalName) {
			seed := strings.ReplaceAll(finalName, ".", "_")
			if seed == "" {
				seed = "tool"
			}
			for i := 2; ; i++ {
				candidate := seed + "_" + strconv.Itoa(i)
				if _, ok := routes[candidate]; ok || s.isReservedToolName(candidate) {
					continue
				}
				finalName = candidate
				break
			}
		}
		descriptor.Name = finalName
		routes[finalName] = route
		tools = append(tools, descriptor)
		return finalName, true
	}

	if s.connections == nil {
		return tools, routes, false
	}
	items, err := s.connections.ListActiveByBot(ctx, botID)
	if err != nil {
		// Without the connection list nothing can be rebuilt — not even the
		// fallback snapshot, because deleted connections must not resurrect.
		// Report degraded so callers serve their own last-good registry.
		s.logger.WarnContext(ctx, "list mcp connections failed", slog.String("bot_id", botID), slog.Any("error", err))
		return tools, routes, true
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Name == items[j].Name {
			return items[i].ID < items[j].ID
		}
		return items[i].Name < items[j].Name
	})
	for _, connection := range items {
		var connTools []mcpgw.ToolDescriptor
		listCtx, listCancel := context.WithTimeout(ctx, mcpCallTimeout)
		var listErr error
		switch strings.ToLower(strings.TrimSpace(connection.Type)) {
		case "http":
			connTools, listErr = s.gateway.ListHTTPConnectionTools(listCtx, connection)
		case "sse":
			connTools, listErr = s.gateway.ListSSEConnectionTools(listCtx, connection)
		case "stdio":
			connTools, listErr = s.gateway.ListStdioConnectionTools(listCtx, botID, connection)
		default:
			listCancel()
			s.logger.WarnContext(ctx, "unsupported mcp connection type", slog.String("connection_id", connection.ID), slog.String("type", connection.Type))
			continue
		}
		listCancel()
		if listErr != nil {
			// One unreachable connection must not blank its tools: serve the
			// last successful listing (spec: 工具列表失败沿用旧版). A
			// connection that never listed successfully has no snapshot, so
			// the listing is degraded and callers must not cache it as their
			// new baseline.
			if snapshot, ok := s.getFallback(connection.ID); ok {
				s.logger.WarnContext(ctx, "list tools from connection failed; serving last successful listing", slog.String("connection_id", connection.ID), slog.String("name", connection.Name), slog.Int("tools", len(snapshot)), slog.Any("error", listErr))
				for _, entry := range snapshot {
					// The fallback descriptor carries its previous final
					// (prefixed, de-duplicated) name; addTool keeps it while
					// the slot is free, so the rendered tool documentation
					// stays byte stable across the outage.
					addTool(entry.descriptor, toolRoute{
						sourceType:   strings.ToLower(strings.TrimSpace(connection.Type)),
						originalName: entry.originalName,
						connection:   connection,
					})
				}
				continue
			}
			degraded = true
			s.logger.WarnContext(ctx, "list tools from connection failed", slog.String("connection_id", connection.ID), slog.String("name", connection.Name), slog.Any("error", listErr))
			continue
		}
		prefix := sanitizePrefix(connection.Name)
		snapshot := make([]fallbackTool, 0, len(connTools))
		for _, tool := range connTools {
			origin := strings.TrimSpace(tool.Name)
			alias := origin
			if prefix != "" {
				alias = prefix + "_" + origin
			}
			tool.Name = alias
			if strings.TrimSpace(tool.Description) != "" {
				tool.Description = "[" + strings.TrimSpace(connection.Name) + "] " + tool.Description
			} else {
				tool.Description = "[" + strings.TrimSpace(connection.Name) + "] " + origin
			}
			finalName, added := addTool(tool, toolRoute{
				sourceType:   strings.ToLower(strings.TrimSpace(connection.Type)),
				originalName: origin,
				connection:   connection,
			})
			if !added {
				continue
			}
			tool.Name = finalName
			snapshot = append(snapshot, fallbackTool{descriptor: tool, originalName: origin})
		}
		fallbackUpdates[connection.ID] = snapshot
	}
	s.mergeFallback(fallbackUpdates)
	return tools, routes, degraded
}

// getFallback returns a connection's last successful listing.
func (s *Source) getFallback(connectionID string) ([]fallbackTool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, ok := s.fallback[connectionID]
	return snapshot, ok
}

// mergeFallback records the successful listings of one rebuild. Connections
// that failed keep their previous snapshot; connections that disappeared
// (deleted or deactivated) keep theirs until restart, which is harmless —
// they only serve if the connection reappears and fails.
func (s *Source) mergeFallback(updates map[string][]fallbackTool) {
	if len(updates) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for connectionID, snapshot := range updates {
		s.fallback[connectionID] = snapshot
	}
}

func (s *Source) isReservedToolName(name string) bool {
	if s == nil || s.reserved == nil {
		return false
	}
	return s.reserved(strings.TrimSpace(name))
}

func sanitizePrefix(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return "mcp"
	}
	builder := strings.Builder{}
	for _, ch := range raw {
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '_' || ch == '-' {
			builder.WriteRune(ch)
			continue
		}
		builder.WriteRune('_')
	}
	normalized := strings.Trim(builder.String(), "._-")
	if normalized == "" {
		return "mcp"
	}
	return normalized
}

func cloneTools(items []mcpgw.ToolDescriptor) []mcpgw.ToolDescriptor {
	if len(items) == 0 {
		return []mcpgw.ToolDescriptor{}
	}
	out := make([]mcpgw.ToolDescriptor, 0, len(items))
	for _, item := range items {
		out = append(out, mcpgw.ToolDescriptor{
			Name:        item.Name,
			Description: item.Description,
			InputSchema: item.InputSchema,
		})
	}
	return out
}

func (s *Source) getCache(botID string) (cacheEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cached, ok := s.cache[botID]
	if !ok || time.Now().After(cached.expiresAt) {
		return cacheEntry{}, false
	}
	return cached, true
}

func (s *Source) setCache(botID string, entry cacheEntry) {
	s.mu.Lock()
	s.cache[botID] = entry
	s.mu.Unlock()
}

func (s *Source) getRoute(botID, toolName string) (toolRoute, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cached, ok := s.cache[botID]
	if !ok || time.Now().After(cached.expiresAt) {
		return toolRoute{}, false
	}
	route, exists := cached.routes[strings.TrimSpace(toolName)]
	return route, exists
}

func (s *Source) String() string {
	return fmt.Sprintf("FederationSource(%p)", s)
}
