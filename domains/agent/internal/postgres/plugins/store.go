// Package plugins implements Plugins-owned PostgreSQL persistence.
package plugins

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	pluginspersistence "github.com/memohai/memoh/domains/agent/extension/plugins/persistence"
	dbsqlc "github.com/memohai/memoh/domains/agent/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

type queries interface {
	CreateBotPluginInstallation(context.Context, dbsqlc.CreateBotPluginInstallationParams) (dbsqlc.AgentBotPluginInstallation, error)
	DeleteBotPluginInstallation(context.Context, dbsqlc.DeleteBotPluginInstallationParams) error
	DeleteBotPluginResources(context.Context, pgtype.UUID) error
	GetBotPluginInstallationByID(context.Context, dbsqlc.GetBotPluginInstallationByIDParams) (dbsqlc.AgentBotPluginInstallation, error)
	ListBotPluginInstallations(context.Context, pgtype.UUID) ([]dbsqlc.AgentBotPluginInstallation, error)
	ListBotPluginResources(context.Context, pgtype.UUID) ([]dbsqlc.AgentBotPluginResource, error)
	UpdateBotPluginInstallationStatus(context.Context, dbsqlc.UpdateBotPluginInstallationStatusParams) (dbsqlc.AgentBotPluginInstallation, error)
	UpsertBotPluginResource(context.Context, dbsqlc.UpsertBotPluginResourceParams) (dbsqlc.AgentBotPluginResource, error)
}

// Store adapts Agent-owner SQLC statements to the Plugins persistence port.
type Store struct {
	queries queries
}

var _ pluginspersistence.Store = (*Store)(nil)

func NewStore(q queries) *Store {
	return &Store{queries: q}
}

func NewStoreFromDB(db dbsqlc.DBTX) *Store {
	return NewStore(dbsqlc.New(db))
}

func (s *Store) ListInstallations(ctx context.Context, botID string) ([]pluginspersistence.InstallationRecord, error) {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListBotPluginInstallations(ctx, id)
	if err != nil {
		return nil, mapNotFound(err)
	}
	items := make([]pluginspersistence.InstallationRecord, 0, len(rows))
	for _, row := range rows {
		items = append(items, installationRecord(row))
	}
	return items, nil
}

func (s *Store) CreateInstallation(ctx context.Context, input pluginspersistence.CreateInstallationInput) (pluginspersistence.InstallationRecord, error) {
	botID, err := db.ParseUUID(input.BotID)
	if err != nil {
		return pluginspersistence.InstallationRecord{}, err
	}
	row, err := s.queries.CreateBotPluginInstallation(ctx, dbsqlc.CreateBotPluginInstallationParams{
		BotID:      botID,
		PluginID:   input.PluginID,
		PluginName: input.PluginName,
		Version:    input.Version,
		Status:     input.Status,
		Enabled:    input.Enabled,
		Config:     cloneBytes(input.Config),
		Metadata:   cloneBytes(input.Metadata),
		Manifest:   cloneBytes(input.Manifest),
	})
	if err != nil {
		return pluginspersistence.InstallationRecord{}, mapNotFound(err)
	}
	return installationRecord(row), nil
}

func (s *Store) FindInstallation(ctx context.Context, botID, installationID string) (pluginspersistence.InstallationRecord, error) {
	bot, installation, err := parseInstallationIDs(botID, installationID)
	if err != nil {
		return pluginspersistence.InstallationRecord{}, err
	}
	row, err := s.queries.GetBotPluginInstallationByID(ctx, dbsqlc.GetBotPluginInstallationByIDParams{
		BotID: bot,
		ID:    installation,
	})
	if err != nil {
		return pluginspersistence.InstallationRecord{}, mapNotFound(err)
	}
	return installationRecord(row), nil
}

func (s *Store) UpdateInstallationStatus(ctx context.Context, update pluginspersistence.InstallationStatusUpdate) (pluginspersistence.InstallationRecord, error) {
	botID, installationID, err := parseInstallationIDs(update.BotID, update.InstallationID)
	if err != nil {
		return pluginspersistence.InstallationRecord{}, err
	}
	row, err := s.queries.UpdateBotPluginInstallationStatus(ctx, dbsqlc.UpdateBotPluginInstallationStatusParams{
		BotID:   botID,
		ID:      installationID,
		Status:  update.Status,
		Enabled: update.Enabled,
	})
	if err != nil {
		return pluginspersistence.InstallationRecord{}, mapNotFound(err)
	}
	return installationRecord(row), nil
}

func (s *Store) DeleteInstallation(ctx context.Context, botID, installationID string) error {
	bot, installation, err := parseInstallationIDs(botID, installationID)
	if err != nil {
		return err
	}
	return mapNotFound(s.queries.DeleteBotPluginInstallation(ctx, dbsqlc.DeleteBotPluginInstallationParams{
		BotID: bot,
		ID:    installation,
	}))
}

func (s *Store) ListResources(ctx context.Context, installationID string) ([]pluginspersistence.ResourceRecord, error) {
	id, err := db.ParseUUID(installationID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListBotPluginResources(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return []pluginspersistence.ResourceRecord{}, nil
	}
	if err != nil {
		return nil, mapNotFound(err)
	}
	items := make([]pluginspersistence.ResourceRecord, 0, len(rows))
	for _, row := range rows {
		items = append(items, resourceRecord(row))
	}
	return items, nil
}

func (s *Store) UpsertResource(ctx context.Context, resource pluginspersistence.ResourceUpsert) error {
	installationID, err := db.ParseUUID(resource.InstallationID)
	if err != nil {
		return err
	}
	_, err = s.queries.UpsertBotPluginResource(ctx, dbsqlc.UpsertBotPluginResourceParams{
		InstallationID: installationID,
		ResourceType:   resource.Type,
		ResourceKey:    resource.Key,
		ResourceID:     resource.ResourceID,
		Status:         resource.Status,
		Metadata:       cloneBytes(resource.Metadata),
	})
	return mapNotFound(err)
}

func (s *Store) DeleteResources(ctx context.Context, installationID string) error {
	id, err := db.ParseUUID(installationID)
	if err != nil {
		return err
	}
	return mapNotFound(s.queries.DeleteBotPluginResources(ctx, id))
}

func mapNotFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return pluginspersistence.ErrNotFound
	}
	return err
}

func parseInstallationIDs(botID, installationID string) (pgtype.UUID, pgtype.UUID, error) {
	bot, err := db.ParseUUID(botID)
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, err
	}
	installation, err := db.ParseUUID(installationID)
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, err
	}
	return bot, installation, nil
}

func installationRecord(row dbsqlc.AgentBotPluginInstallation) pluginspersistence.InstallationRecord {
	return pluginspersistence.InstallationRecord{
		ID:          db.UUIDString(row.ID),
		BotID:       db.UUIDString(row.BotID),
		PluginID:    row.PluginID,
		PluginName:  row.PluginName,
		Version:     row.Version,
		Status:      row.Status,
		Enabled:     row.Enabled,
		Config:      cloneBytes(row.Config),
		Metadata:    cloneBytes(row.Metadata),
		Manifest:    cloneBytes(row.Manifest),
		InstalledAt: db.TimeFromPg(row.InstalledAt),
		UpdatedAt:   db.TimeFromPg(row.UpdatedAt),
	}
}

func resourceRecord(row dbsqlc.AgentBotPluginResource) pluginspersistence.ResourceRecord {
	return pluginspersistence.ResourceRecord{
		ID:             db.UUIDString(row.ID),
		InstallationID: db.UUIDString(row.InstallationID),
		Type:           row.ResourceType,
		Key:            row.ResourceKey,
		ResourceID:     row.ResourceID,
		Status:         row.Status,
		Metadata:       cloneBytes(row.Metadata),
		CreatedAt:      db.TimeFromPg(row.CreatedAt),
		UpdatedAt:      db.TimeFromPg(row.UpdatedAt),
	}
}

func cloneBytes(value []byte) []byte {
	return append([]byte(nil), value...)
}
