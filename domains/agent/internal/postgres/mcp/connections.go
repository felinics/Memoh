// Package mcp implements MCP-owned PostgreSQL persistence.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	dbsqlc "github.com/memohai/memoh/domains/agent/internal/postgres/sqlc"
	mcppersistence "github.com/memohai/memoh/domains/agent/mcp/persistence"
	"github.com/memohai/memoh/internal/db"
)

type connectionQueries interface {
	ListMCPConnectionsByBotID(context.Context, pgtype.UUID) ([]dbsqlc.AgentMcpConnection, error)
	GetMCPConnectionByID(context.Context, dbsqlc.GetMCPConnectionByIDParams) (dbsqlc.AgentMcpConnection, error)
	CreateMCPConnection(context.Context, dbsqlc.CreateMCPConnectionParams) (dbsqlc.AgentMcpConnection, error)
	CreateManagedMCPConnection(context.Context, dbsqlc.CreateManagedMCPConnectionParams) (dbsqlc.AgentMcpConnection, error)
	UpdateMCPConnection(context.Context, dbsqlc.UpdateMCPConnectionParams) (dbsqlc.AgentMcpConnection, error)
	UpsertMCPConnectionByName(context.Context, dbsqlc.UpsertMCPConnectionByNameParams) (dbsqlc.AgentMcpConnection, error)
	DeleteMCPConnection(context.Context, dbsqlc.DeleteMCPConnectionParams) error
	UpdateMCPConnectionsActiveByPlugin(context.Context, dbsqlc.UpdateMCPConnectionsActiveByPluginParams) error
	DeleteMCPConnectionsByPlugin(context.Context, dbsqlc.DeleteMCPConnectionsByPluginParams) error
	UpdateMCPConnectionProbeResult(context.Context, dbsqlc.UpdateMCPConnectionProbeResultParams) error
}

// ConnectionStore adapts Agent-owner SQLC statements to MCP's connection
// persistence port.
type ConnectionStore struct {
	queries connectionQueries
}

var _ mcppersistence.ConnectionStore = (*ConnectionStore)(nil)

func NewConnectionStore(queries connectionQueries) *ConnectionStore {
	return &ConnectionStore{queries: queries}
}

func NewConnectionStoreFromDB(db dbsqlc.DBTX) *ConnectionStore {
	return NewConnectionStore(dbsqlc.New(db))
}

func (s *ConnectionStore) ListConnections(ctx context.Context, botID string) ([]mcppersistence.Connection, error) {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListMCPConnectionsByBotID(ctx, id)
	if err != nil {
		return nil, mapNotFound(err)
	}
	items := make([]mcppersistence.Connection, 0, len(rows))
	for _, row := range rows {
		item, err := connection(row)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *ConnectionStore) GetConnection(ctx context.Context, botID, connectionID string) (mcppersistence.Connection, error) {
	bot, err := db.ParseUUID(botID)
	if err != nil {
		return mcppersistence.Connection{}, err
	}
	id, err := db.ParseUUID(connectionID)
	if err != nil {
		return mcppersistence.Connection{}, err
	}
	row, err := s.queries.GetMCPConnectionByID(ctx, dbsqlc.GetMCPConnectionByIDParams{BotID: bot, ID: id})
	if err != nil {
		return mcppersistence.Connection{}, mapNotFound(err)
	}
	return connection(row)
}

func (s *ConnectionStore) CreateConnection(ctx context.Context, input mcppersistence.ConnectionWrite) (mcppersistence.Connection, error) {
	bot, config, err := connectionPayload(input)
	if err != nil {
		return mcppersistence.Connection{}, err
	}
	row, err := s.queries.CreateMCPConnection(ctx, dbsqlc.CreateMCPConnectionParams{
		BotID: bot, Name: input.Name, Type: input.Type, Config: config,
		IsActive: input.Active, AuthType: input.AuthType,
	})
	if err != nil {
		return mcppersistence.Connection{}, mapNotFound(err)
	}
	return connection(row)
}

func (s *ConnectionStore) CreateManagedConnection(ctx context.Context, input mcppersistence.ManagedConnectionWrite) (mcppersistence.Connection, error) {
	bot, config, err := connectionPayload(input.ConnectionWrite)
	if err != nil {
		return mcppersistence.Connection{}, err
	}
	installationID, err := db.ParseUUID(input.InstallationID)
	if err != nil {
		return mcppersistence.Connection{}, err
	}
	metadata, err := json.Marshal(input.Metadata)
	if err != nil {
		return mcppersistence.Connection{}, err
	}
	row, err := s.queries.CreateManagedMCPConnection(ctx, dbsqlc.CreateManagedMCPConnectionParams{
		BotID: bot, Name: input.Name, Type: input.Type, Config: config,
		IsActive: input.Active, AuthType: input.AuthType,
		ManagedByPluginInstallationID: installationID,
		ManagedResourceKey:            input.ResourceKey,
		Visible:                       input.Visible,
		Metadata:                      metadata,
	})
	if err != nil {
		return mcppersistence.Connection{}, mapNotFound(err)
	}
	return connection(row)
}

func (s *ConnectionStore) UpdateConnection(ctx context.Context, input mcppersistence.ConnectionWrite) (mcppersistence.Connection, error) {
	bot, config, err := connectionPayload(input)
	if err != nil {
		return mcppersistence.Connection{}, err
	}
	id, err := db.ParseUUID(input.ID)
	if err != nil {
		return mcppersistence.Connection{}, err
	}
	row, err := s.queries.UpdateMCPConnection(ctx, dbsqlc.UpdateMCPConnectionParams{
		BotID: bot, ID: id, Name: input.Name, Type: input.Type, Config: config,
		IsActive: input.Active, AuthType: input.AuthType,
	})
	if err != nil {
		return mcppersistence.Connection{}, mapNotFound(err)
	}
	return connection(row)
}

func (s *ConnectionStore) UpsertConnectionByName(ctx context.Context, input mcppersistence.ConnectionWrite) (mcppersistence.Connection, error) {
	bot, config, err := connectionPayload(input)
	if err != nil {
		return mcppersistence.Connection{}, err
	}
	row, err := s.queries.UpsertMCPConnectionByName(ctx, dbsqlc.UpsertMCPConnectionByNameParams{
		BotID: bot, Name: input.Name, Type: input.Type, Config: config,
	})
	if err != nil {
		return mcppersistence.Connection{}, mapNotFound(err)
	}
	return connection(row)
}

func (s *ConnectionStore) DeleteConnection(ctx context.Context, botID, connectionID string) error {
	bot, err := db.ParseUUID(botID)
	if err != nil {
		return err
	}
	id, err := db.ParseUUID(connectionID)
	if err != nil {
		return err
	}
	return mapNotFound(s.queries.DeleteMCPConnection(ctx, dbsqlc.DeleteMCPConnectionParams{BotID: bot, ID: id}))
}

func (s *ConnectionStore) SetPluginConnectionsActive(ctx context.Context, botID, installationID string, active bool) error {
	bot, installation, err := pluginIDs(botID, installationID)
	if err != nil {
		return err
	}
	return mapNotFound(s.queries.UpdateMCPConnectionsActiveByPlugin(ctx, dbsqlc.UpdateMCPConnectionsActiveByPluginParams{
		BotID: bot, ManagedByPluginInstallationID: installation, IsActive: active,
	}))
}

func (s *ConnectionStore) DeletePluginConnections(ctx context.Context, botID, installationID string) error {
	bot, installation, err := pluginIDs(botID, installationID)
	if err != nil {
		return err
	}
	return mapNotFound(s.queries.DeleteMCPConnectionsByPlugin(ctx, dbsqlc.DeleteMCPConnectionsByPluginParams{
		BotID: bot, ManagedByPluginInstallationID: installation,
	}))
}

func (s *ConnectionStore) SaveConnectionProbe(ctx context.Context, input mcppersistence.ConnectionProbeWrite) error {
	bot, err := db.ParseUUID(input.BotID)
	if err != nil {
		return err
	}
	id, err := db.ParseUUID(input.ConnectionID)
	if err != nil {
		return err
	}
	tools, err := json.Marshal(input.Tools)
	if err != nil {
		return err
	}
	return mapNotFound(s.queries.UpdateMCPConnectionProbeResult(ctx, dbsqlc.UpdateMCPConnectionProbeResultParams{
		BotID: bot, ID: id, Status: input.Status, ToolsCache: tools, StatusMessage: input.StatusMessage,
	}))
}

func mapNotFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return mcppersistence.ErrNotFound
	}
	return err
}

func connectionPayload(input mcppersistence.ConnectionWrite) (pgtype.UUID, []byte, error) {
	bot, err := db.ParseUUID(input.BotID)
	if err != nil {
		return pgtype.UUID{}, nil, err
	}
	config, err := json.Marshal(input.Config)
	return bot, config, err
}

func pluginIDs(botID, installationID string) (pgtype.UUID, pgtype.UUID, error) {
	bot, err := db.ParseUUID(botID)
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, err
	}
	installation, err := db.ParseUUID(installationID)
	return bot, installation, err
}

func connection(row dbsqlc.AgentMcpConnection) (mcppersistence.Connection, error) {
	config, err := jsonObject(row.Config)
	if err != nil {
		return mcppersistence.Connection{}, err
	}
	metadata, err := jsonObject(row.Metadata)
	if err != nil {
		return mcppersistence.Connection{}, err
	}
	var tools []mcppersistence.ToolDescriptor
	if len(row.ToolsCache) == 0 || json.Unmarshal(row.ToolsCache, &tools) != nil {
		tools = []mcppersistence.ToolDescriptor{}
	}
	var lastProbedAt *time.Time
	if row.LastProbedAt.Valid {
		value := db.TimeFromPg(row.LastProbedAt)
		lastProbedAt = &value
	}
	managedBy := ""
	if row.ManagedByPluginInstallationID.Valid {
		managedBy = row.ManagedByPluginInstallationID.String()
	}
	return mcppersistence.Connection{
		ID: row.ID.String(), BotID: row.BotID.String(), Name: strings.TrimSpace(row.Name),
		Type: strings.TrimSpace(row.Type), Config: config, Active: row.IsActive,
		Status: strings.TrimSpace(row.Status), ToolsCache: tools, LastProbedAt: lastProbedAt,
		StatusMessage: strings.TrimSpace(row.StatusMessage), AuthType: strings.TrimSpace(row.AuthType),
		ManagedByPluginInstallationID: managedBy, ManagedResourceKey: strings.TrimSpace(row.ManagedResourceKey),
		Visible: row.Visible, Metadata: metadata, CreatedAt: db.TimeFromPg(row.CreatedAt), UpdatedAt: db.TimeFromPg(row.UpdatedAt),
	}, nil
}

func jsonObject(raw []byte) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	if value == nil {
		value = map[string]any{}
	}
	return value, nil
}
