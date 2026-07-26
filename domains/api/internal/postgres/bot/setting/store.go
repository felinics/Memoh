// Package setting implements Settings-owned PostgreSQL projections.
package setting

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	settingpersistence "github.com/memohai/memoh/domains/api/bot/setting/persistence"
	apisqlc "github.com/memohai/memoh/domains/api/internal/postgres/sqlc"
	"github.com/memohai/memoh/domains/runtime/workspace"
	"github.com/memohai/memoh/internal/db"
)

type queries interface {
	GetSettingsByBotID(context.Context, pgtype.UUID) (apisqlc.GetSettingsByBotIDRow, error)
	GetBotByID(context.Context, pgtype.UUID) (apisqlc.GetBotByIDRow, error)
	GetBotOverlayConfig(context.Context, pgtype.UUID) (apisqlc.GetBotOverlayConfigRow, error)
	UpsertBotSettings(context.Context, apisqlc.UpsertBotSettingsParams) (apisqlc.UpsertBotSettingsRow, error)
	DeleteSettingsByBotID(context.Context, pgtype.UUID) error
}

// Store exposes Runtime's narrow, read-only projections of API-owned setting.
type Store struct {
	queries queries
}

var (
	_ settingpersistence.Store           = (*Store)(nil)
	_ workspace.BotRuntimeSettingsReader = (*Store)(nil)
)

func newStore(queries queries) *Store {
	return &Store{queries: queries}
}

func (s *Store) Get(ctx context.Context, botID string) (settingpersistence.Record, error) {
	row, err := s.get(ctx, botID)
	if err != nil {
		return settingpersistence.Record{}, err
	}
	return readRecord(row), nil
}

func (s *Store) GetBot(ctx context.Context, botID string) (settingpersistence.BotRecord, error) {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return settingpersistence.BotRecord{}, err
	}
	row, err := s.queries.GetBotByID(ctx, id)
	if err != nil {
		return settingpersistence.BotRecord{}, mapQueryError(err)
	}
	return settingpersistence.BotRecord{
		OwnerUserID:         db.UUIDString(row.OwnerUserID),
		Metadata:            cloneBytes(row.Metadata),
		Language:            row.Language,
		ReasoningEnabled:    row.ReasoningEnabled,
		ReasoningEffort:     row.ReasoningEffort,
		HeartbeatEnabled:    row.HeartbeatEnabled,
		HeartbeatInterval:   row.HeartbeatInterval,
		CompactionEnabled:   row.CompactionEnabled,
		CompactionThreshold: row.CompactionThreshold,
		CompactionRatio:     row.CompactionRatio,
	}, nil
}

func (s *Store) GetOverlay(ctx context.Context, botID string) (settingpersistence.OverlayRecord, error) {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return settingpersistence.OverlayRecord{}, err
	}
	row, err := s.queries.GetBotOverlayConfig(ctx, id)
	if err != nil {
		return settingpersistence.OverlayRecord{}, mapQueryError(err)
	}
	return settingpersistence.OverlayRecord{
		Enabled:  row.OverlayEnabled,
		Provider: row.OverlayProvider,
		Config:   cloneBytes(row.OverlayConfig),
	}, nil
}

func (s *Store) Upsert(ctx context.Context, input settingpersistence.UpsertInput) (settingpersistence.Record, error) {
	id, err := db.ParseUUID(input.BotID)
	if err != nil {
		return settingpersistence.Record{}, err
	}
	chatModelID, err := optionalUUID(input.ChatModelID)
	if err != nil {
		return settingpersistence.Record{}, err
	}
	heartbeatModelID, err := optionalUUID(input.HeartbeatModelID)
	if err != nil {
		return settingpersistence.Record{}, err
	}
	compactionModelID, err := optionalUUID(input.CompactionModelID)
	if err != nil {
		return settingpersistence.Record{}, err
	}
	imageModelID, err := optionalUUID(input.ImageModelID)
	if err != nil {
		return settingpersistence.Record{}, err
	}
	searchProviderID, err := optionalUUID(input.SearchProviderID)
	if err != nil {
		return settingpersistence.Record{}, err
	}
	fetchProviderID, err := optionalUUID(input.FetchProviderID)
	if err != nil {
		return settingpersistence.Record{}, err
	}
	memoryProviderID, err := optionalUUID(input.MemoryProviderID)
	if err != nil {
		return settingpersistence.Record{}, err
	}
	ttsModelID, err := optionalUUID(input.TTSModelID)
	if err != nil {
		return settingpersistence.Record{}, err
	}
	transcriptionModelID, err := optionalUUID(input.TranscriptionModelID)
	if err != nil {
		return settingpersistence.Record{}, err
	}
	videoModelID, err := optionalUUID(input.VideoModelID)
	if err != nil {
		return settingpersistence.Record{}, err
	}
	row, err := s.queries.UpsertBotSettings(ctx, apisqlc.UpsertBotSettingsParams{
		ID:                     id,
		Timezone:               optionalText(input.Timezone),
		Language:               input.Language,
		CommandUiLanguage:      input.CommandUILanguage,
		ReasoningEnabled:       input.ReasoningEnabled,
		ReasoningEffort:        input.ReasoningEffort,
		HeartbeatEnabled:       input.HeartbeatEnabled,
		HeartbeatInterval:      input.HeartbeatInterval,
		HeartbeatPrompt:        "",
		CompactionEnabled:      input.CompactionEnabled,
		CompactionThreshold:    input.CompactionThreshold,
		CompactionRatio:        input.CompactionRatio,
		ChatModelID:            chatModelID,
		ChatRuntime:            input.ChatRuntime,
		ChatAcpAgentID:         text(input.ChatACPAgentID),
		ChatAcpProjectPath:     input.ChatACPProjectPath,
		ChatAcpProjectMode:     input.ChatACPProjectMode,
		HeartbeatModelID:       heartbeatModelID,
		CompactionModelID:      compactionModelID,
		ImageModelID:           imageModelID,
		SearchProviderID:       searchProviderID,
		FetchProviderIDSet:     input.FetchProviderIDSet,
		FetchProviderID:        fetchProviderID,
		MemoryProviderID:       memoryProviderID,
		TtsModelID:             ttsModelID,
		TranscriptionModelID:   transcriptionModelID,
		VideoModelID:           videoModelID,
		PersistFullToolResults: input.PersistFullToolResults,
		ShowToolCallsInIm:      input.ShowToolCallsInIM,
		ToolApprovalConfig:     cloneBytes(input.ToolApprovalConfig),
		DisplayEnabled:         input.DisplayEnabled,
		OverlayProvider:        input.OverlayProvider,
		OverlayEnabled:         input.OverlayEnabled,
		OverlayConfig:          cloneBytes(input.OverlayConfig),
	})
	if err != nil {
		return settingpersistence.Record{}, mapQueryError(err)
	}
	return writeRecord(row), nil
}

func (s *Store) Delete(ctx context.Context, botID string) error {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return err
	}
	return mapQueryError(s.queries.DeleteSettingsByBotID(ctx, id))
}

func (s *Store) FindBotRuntimeSettings(ctx context.Context, botID string) (workspace.BotRuntimeSettings, error) {
	row, err := s.get(ctx, botID)
	if err != nil {
		return workspace.BotRuntimeSettings{}, err
	}
	approval := settingpersistence.DefaultToolApprovalConfig()
	if len(row.ToolApprovalConfig) > 0 {
		if err := json.Unmarshal(row.ToolApprovalConfig, &approval); err != nil {
			return workspace.BotRuntimeSettings{}, err
		}
	}
	return workspace.BotRuntimeSettings{
		ToolApprovalConfig: settingpersistence.NormalizeToolApprovalConfig(approval),
		DisplayEnabled:     row.DisplayEnabled,
	}, nil
}

func (s *Store) get(ctx context.Context, botID string) (apisqlc.GetSettingsByBotIDRow, error) {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return apisqlc.GetSettingsByBotIDRow{}, err
	}
	row, err := s.queries.GetSettingsByBotID(ctx, id)
	return row, mapQueryError(err)
}

func readRecord(row apisqlc.GetSettingsByBotIDRow) settingpersistence.Record {
	return settingpersistence.Record{
		Language:               row.Language,
		CommandUILanguage:      row.CommandUiLanguage,
		ReasoningEnabled:       row.ReasoningEnabled,
		ReasoningEffort:        row.ReasoningEffort,
		HeartbeatEnabled:       row.HeartbeatEnabled,
		HeartbeatInterval:      row.HeartbeatInterval,
		CompactionEnabled:      row.CompactionEnabled,
		CompactionThreshold:    row.CompactionThreshold,
		CompactionRatio:        row.CompactionRatio,
		Timezone:               db.TextToString(row.Timezone),
		ChatModelID:            db.UUIDString(row.ChatModelID),
		ChatRuntime:            row.ChatRuntime,
		ChatACPAgentID:         db.TextToString(row.ChatAcpAgentID),
		ChatACPProjectPath:     row.ChatAcpProjectPath,
		ChatACPProjectMode:     row.ChatAcpProjectMode,
		HeartbeatModelID:       db.UUIDString(row.HeartbeatModelID),
		CompactionModelID:      db.UUIDString(row.CompactionModelID),
		ImageModelID:           db.UUIDString(row.ImageModelID),
		SearchProviderID:       db.UUIDString(row.SearchProviderID),
		FetchProviderID:        db.UUIDString(row.FetchProviderID),
		MemoryProviderID:       db.UUIDString(row.MemoryProviderID),
		TTSModelID:             db.UUIDString(row.TtsModelID),
		TranscriptionModelID:   db.UUIDString(row.TranscriptionModelID),
		VideoModelID:           db.UUIDString(row.VideoModelID),
		PersistFullToolResults: row.PersistFullToolResults,
		ShowToolCallsInIM:      row.ShowToolCallsInIm,
		ToolApprovalConfig:     cloneBytes(row.ToolApprovalConfig),
		DisplayEnabled:         row.DisplayEnabled,
		OverlayProvider:        row.OverlayProvider,
		OverlayEnabled:         row.OverlayEnabled,
		OverlayConfig:          cloneBytes(row.OverlayConfig),
	}
}

func writeRecord(row apisqlc.UpsertBotSettingsRow) settingpersistence.Record {
	return settingpersistence.Record{
		Language:               row.Language,
		CommandUILanguage:      row.CommandUiLanguage,
		ReasoningEnabled:       row.ReasoningEnabled,
		ReasoningEffort:        row.ReasoningEffort,
		HeartbeatEnabled:       row.HeartbeatEnabled,
		HeartbeatInterval:      row.HeartbeatInterval,
		CompactionEnabled:      row.CompactionEnabled,
		CompactionThreshold:    row.CompactionThreshold,
		CompactionRatio:        row.CompactionRatio,
		Timezone:               db.TextToString(row.Timezone),
		ChatModelID:            db.UUIDString(row.ChatModelID),
		ChatRuntime:            row.ChatRuntime,
		ChatACPAgentID:         db.TextToString(row.ChatAcpAgentID),
		ChatACPProjectPath:     row.ChatAcpProjectPath,
		ChatACPProjectMode:     row.ChatAcpProjectMode,
		HeartbeatModelID:       db.UUIDString(row.HeartbeatModelID),
		CompactionModelID:      db.UUIDString(row.CompactionModelID),
		ImageModelID:           db.UUIDString(row.ImageModelID),
		SearchProviderID:       db.UUIDString(row.SearchProviderID),
		FetchProviderID:        db.UUIDString(row.FetchProviderID),
		MemoryProviderID:       db.UUIDString(row.MemoryProviderID),
		TTSModelID:             db.UUIDString(row.TtsModelID),
		TranscriptionModelID:   db.UUIDString(row.TranscriptionModelID),
		VideoModelID:           db.UUIDString(row.VideoModelID),
		PersistFullToolResults: row.PersistFullToolResults,
		ShowToolCallsInIM:      row.ShowToolCallsInIm,
		ToolApprovalConfig:     cloneBytes(row.ToolApprovalConfig),
		DisplayEnabled:         row.DisplayEnabled,
		OverlayProvider:        row.OverlayProvider,
		OverlayEnabled:         row.OverlayEnabled,
		OverlayConfig:          cloneBytes(row.OverlayConfig),
	}
}

func mapQueryError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.Join(settingpersistence.ErrNotFound, err)
	}
	return err
}

func optionalUUID(value string) (pgtype.UUID, error) {
	if value == "" {
		return pgtype.UUID{}, nil
	}
	return db.ParseUUID(value)
}

func optionalText(value *string) pgtype.Text {
	if value == nil || *value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *value, Valid: true}
}

func text(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}

func cloneBytes(value []byte) []byte {
	return append([]byte(nil), value...)
}

// NewStore builds a Store backed by API-owned generated queries.
func NewStore(pool *pgxpool.Pool) *Store {
	if pool == nil {
		return newStore(nil)
	}
	return newStore(apisqlc.New(pool))
}
