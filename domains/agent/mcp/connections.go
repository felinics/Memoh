package mcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Connection represents a stored MCP connection for a bot.
type Connection struct {
	ID                            string           `json:"id"`
	BotID                         string           `json:"bot_id"`
	Name                          string           `json:"name"`
	Type                          string           `json:"type"`
	Config                        map[string]any   `json:"config"`
	Active                        bool             `json:"is_active"`
	Status                        string           `json:"status"`
	ToolsCache                    []ToolDescriptor `json:"tools_cache"`
	LastProbedAt                  *time.Time       `json:"last_probed_at,omitempty"`
	StatusMessage                 string           `json:"status_message"`
	AuthType                      string           `json:"auth_type"`
	ManagedByPluginInstallationID string           `json:"managed_by_plugin_installation_id,omitempty"`
	ManagedResourceKey            string           `json:"managed_resource_key,omitempty"`
	Visible                       bool             `json:"visible"`
	Metadata                      map[string]any   `json:"metadata,omitempty"`
	CreatedAt                     time.Time        `json:"created_at"`
	UpdatedAt                     time.Time        `json:"updated_at"`
}

// UpsertRequest accepts standard mcpServers item format.
// Type is auto-inferred: command present -> stdio, url present -> http (default) or sse (if transport:"sse").
type UpsertRequest struct {
	Name      string            `json:"name"`
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Cwd       string            `json:"cwd,omitempty"`
	URL       string            `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Transport string            `json:"transport,omitempty"`
	Active    *bool             `json:"is_active,omitempty"`
	AuthType  string            `json:"auth_type,omitempty"`
}

// ImportRequest accepts a standard mcpServers dict for batch import.
type ImportRequest struct {
	MCPServers map[string]MCPServerEntry `json:"mcpServers"`
}

// MCPServerEntry is one entry in the standard mcpServers dict.
type MCPServerEntry struct {
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Cwd       string            `json:"cwd,omitempty"`
	URL       string            `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Transport string            `json:"transport,omitempty"`
}

// ListResponse wraps MCP connection list responses.
type ListResponse struct {
	Items []Connection `json:"items"`
}

// ExportResponse returns connections in standard mcpServers format.
type ExportResponse struct {
	MCPServers map[string]MCPServerEntry `json:"mcpServers"`
}

type ManagedConnectionRequest struct {
	InstallationID string
	ResourceKey    string
	Visible        bool
	Metadata       map[string]any
}

// ConnectionService handles CRUD operations for MCP connections.
type ConnectionService struct {
	store  ConnectionStore
	logger *slog.Logger
}

// NewConnectionService creates a ConnectionService backed by its persistence port.
func NewConnectionService(log *slog.Logger, store ConnectionStore) *ConnectionService {
	if log == nil {
		log = slog.Default()
	}
	return &ConnectionService{
		store:  store,
		logger: log.With(slog.String("service", "mcp_connections")),
	}
}

// ListByBot returns all MCP connections for a bot.
func (s *ConnectionService) ListByBot(ctx context.Context, botID string) ([]Connection, error) {
	if s.store == nil {
		return nil, errors.New("mcp queries not configured")
	}
	return s.store.ListConnections(ctx, botID)
}

// ListActiveByBot returns active MCP connections for a bot.
func (s *ConnectionService) ListActiveByBot(ctx context.Context, botID string) ([]Connection, error) {
	items, err := s.ListByBot(ctx, botID)
	if err != nil {
		return nil, err
	}
	active := make([]Connection, 0, len(items))
	for _, item := range items {
		if item.Active {
			active = append(active, item)
		}
	}
	return active, nil
}

// Get returns a specific MCP connection for a bot.
func (s *ConnectionService) Get(ctx context.Context, botID, id string) (Connection, error) {
	if s.store == nil {
		return Connection{}, errors.New("mcp queries not configured")
	}
	return s.store.GetConnection(ctx, botID, id)
}

// Create inserts a new MCP connection for a bot.
func (s *ConnectionService) Create(ctx context.Context, botID string, req UpsertRequest) (Connection, error) {
	if s.store == nil {
		return Connection{}, errors.New("mcp queries not configured")
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return Connection{}, errors.New("name is required")
	}
	mcpType, config, err := inferTypeAndConfig(req)
	if err != nil {
		return Connection{}, err
	}
	active := true
	if req.Active != nil {
		active = *req.Active
	}
	authType := strings.TrimSpace(req.AuthType)
	if authType == "" {
		authType = "none"
	}
	return s.store.CreateConnection(ctx, ConnectionWrite{
		BotID:    botID,
		Name:     name,
		Type:     mcpType,
		Config:   config,
		Active:   active,
		AuthType: authType,
	})
}

func (s *ConnectionService) CreateManaged(ctx context.Context, botID string, req UpsertRequest, managed ManagedConnectionRequest) (Connection, error) {
	if s.store == nil {
		return Connection{}, errors.New("mcp queries not configured")
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return Connection{}, errors.New("name is required")
	}
	resourceKey := strings.TrimSpace(managed.ResourceKey)
	if resourceKey == "" {
		return Connection{}, errors.New("managed resource key is required")
	}
	mcpType, config, err := inferTypeAndConfig(req)
	if err != nil {
		return Connection{}, err
	}
	active := true
	if req.Active != nil {
		active = *req.Active
	}
	authType := strings.TrimSpace(req.AuthType)
	if authType == "" {
		authType = "none"
	}
	return s.store.CreateManagedConnection(ctx, ManagedConnectionWrite{
		ConnectionWrite: ConnectionWrite{
			BotID:    botID,
			Name:     name,
			Type:     mcpType,
			Config:   config,
			Active:   active,
			AuthType: authType,
		},
		InstallationID: managed.InstallationID,
		ResourceKey:    resourceKey,
		Visible:        managed.Visible,
		Metadata:       normalizeMap(managed.Metadata),
	})
}

// Update modifies an existing MCP connection.
func (s *ConnectionService) Update(ctx context.Context, botID, id string, req UpsertRequest) (Connection, error) {
	if s.store == nil {
		return Connection{}, errors.New("mcp queries not configured")
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return Connection{}, errors.New("name is required")
	}
	mcpType, config, err := inferTypeAndConfig(req)
	if err != nil {
		return Connection{}, err
	}
	active := true
	if req.Active != nil {
		active = *req.Active
	}
	authType := strings.TrimSpace(req.AuthType)
	if authType == "" {
		authType = "none"
	}
	return s.store.UpdateConnection(ctx, ConnectionWrite{
		BotID:    botID,
		ID:       id,
		Name:     name,
		Type:     mcpType,
		Config:   config,
		Active:   active,
		AuthType: authType,
	})
}

// Import performs a declarative sync from a standard mcpServers dict.
// Existing connections (matched by name) get config updated but is_active preserved.
// New connections are created with is_active=true.
// Connections not in the input are left untouched.
func (s *ConnectionService) Import(ctx context.Context, botID string, req ImportRequest) ([]Connection, error) {
	if s.store == nil {
		return nil, errors.New("mcp queries not configured")
	}
	if len(req.MCPServers) == 0 {
		return []Connection{}, nil
	}
	results := make([]Connection, 0, len(req.MCPServers))
	for name, entry := range req.MCPServers {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		upsert := entryToUpsertRequest(name, entry)
		mcpType, config, err := inferTypeAndConfig(upsert)
		if err != nil {
			return nil, fmt.Errorf("server %q: %w", name, err)
		}
		conn, err := s.store.UpsertConnectionByName(ctx, ConnectionWrite{
			BotID:  botID,
			Name:   name,
			Type:   mcpType,
			Config: config,
		})
		if err != nil {
			return nil, fmt.Errorf("server %q: %w", name, err)
		}
		results = append(results, conn)
	}
	return results, nil
}

// ExportByBot returns all connections for a bot in standard mcpServers format.
func (s *ConnectionService) ExportByBot(ctx context.Context, botID string) (ExportResponse, error) {
	items, err := s.ListByBot(ctx, botID)
	if err != nil {
		return ExportResponse{}, err
	}
	servers := make(map[string]MCPServerEntry, len(items))
	for _, conn := range items {
		servers[conn.Name] = connectionToExportEntry(conn)
	}
	return ExportResponse{MCPServers: servers}, nil
}

// Delete removes an MCP connection.
func (s *ConnectionService) Delete(ctx context.Context, botID, id string) error {
	if s.store == nil {
		return errors.New("mcp queries not configured")
	}
	return s.store.DeleteConnection(ctx, botID, id)
}

func (s *ConnectionService) SetPluginConnectionsActive(ctx context.Context, botID, installationID string, active bool) error {
	if s.store == nil {
		return errors.New("mcp queries not configured")
	}
	return s.store.SetPluginConnectionsActive(ctx, botID, installationID, active)
}

func (s *ConnectionService) DeleteByPlugin(ctx context.Context, botID, installationID string) error {
	if s.store == nil {
		return errors.New("mcp queries not configured")
	}
	return s.store.DeletePluginConnections(ctx, botID, installationID)
}

// BatchDelete removes multiple MCP connections by IDs. Invalid IDs are skipped; at least one must succeed for no error.
func (s *ConnectionService) BatchDelete(ctx context.Context, botID string, ids []string) error {
	if s.store == nil {
		return errors.New("mcp queries not configured")
	}
	if len(ids) == 0 {
		return nil
	}
	var lastErr error
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if err := s.Delete(ctx, botID, id); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

// UpdateProbeResult persists the result of a probe operation.
func (s *ConnectionService) UpdateProbeResult(ctx context.Context, botID, id, status string, tools []ToolDescriptor, message string) error {
	if s.store == nil {
		return errors.New("mcp queries not configured")
	}
	return s.store.SaveConnectionProbe(ctx, ConnectionProbeWrite{
		BotID:         botID,
		ConnectionID:  id,
		Status:        status,
		Tools:         tools,
		StatusMessage: message,
	})
}

func normalizeMap(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	return value
}

// inferTypeAndConfig builds internal type + config from a standard mcpServers item.
func inferTypeAndConfig(req UpsertRequest) (string, map[string]any, error) {
	hasCommand := strings.TrimSpace(req.Command) != ""
	hasURL := strings.TrimSpace(req.URL) != ""

	if !hasCommand && !hasURL {
		return "", nil, errors.New("command or url is required")
	}
	if hasCommand && hasURL {
		return "", nil, errors.New("command and url are mutually exclusive")
	}

	config := map[string]any{}

	if hasCommand {
		config["command"] = strings.TrimSpace(req.Command)
		if len(req.Args) > 0 {
			config["args"] = req.Args
		}
		if len(req.Env) > 0 {
			config["env"] = req.Env
		}
		if strings.TrimSpace(req.Cwd) != "" {
			config["cwd"] = strings.TrimSpace(req.Cwd)
		}
		return "stdio", config, nil
	}

	config["url"] = strings.TrimSpace(req.URL)
	if len(req.Headers) > 0 {
		config["headers"] = req.Headers
	}
	transport := strings.ToLower(strings.TrimSpace(req.Transport))
	if transport == "sse" {
		return "sse", config, nil
	}
	return "http", config, nil
}

// entryToUpsertRequest converts a named MCPServerEntry to an UpsertRequest.
func entryToUpsertRequest(name string, entry MCPServerEntry) UpsertRequest {
	return UpsertRequest{
		Name:      name,
		Command:   entry.Command,
		Args:      entry.Args,
		Env:       entry.Env,
		Cwd:       entry.Cwd,
		URL:       entry.URL,
		Headers:   entry.Headers,
		Transport: entry.Transport,
	}
}

// connectionToExportEntry converts a stored connection to standard mcpServers entry.
func connectionToExportEntry(conn Connection) MCPServerEntry {
	entry := MCPServerEntry{}
	switch conn.Type {
	case "stdio":
		entry.Command, _ = conn.Config["command"].(string)
		if rawArgs, ok := conn.Config["args"]; ok {
			switch v := rawArgs.(type) {
			case []any:
				for _, a := range v {
					if s, ok := a.(string); ok {
						entry.Args = append(entry.Args, s)
					}
				}
			case []string:
				entry.Args = v
			}
		}
		if rawEnv, ok := conn.Config["env"]; ok {
			if m, ok := rawEnv.(map[string]any); ok {
				entry.Env = make(map[string]string, len(m))
				for k, v := range m {
					if s, ok := v.(string); ok {
						entry.Env[k] = s
					}
				}
			}
		}
		if cwd, ok := conn.Config["cwd"].(string); ok && cwd != "" {
			entry.Cwd = cwd
		}
	case "http", "sse":
		entry.URL, _ = conn.Config["url"].(string)
		if rawHeaders, ok := conn.Config["headers"]; ok {
			if m, ok := rawHeaders.(map[string]any); ok {
				entry.Headers = make(map[string]string, len(m))
				for k, v := range m {
					if s, ok := v.(string); ok {
						entry.Headers[k] = s
					}
				}
			}
		}
		if conn.Type == "sse" {
			entry.Transport = "sse"
		}
	}
	return entry
}
