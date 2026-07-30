package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/memohai/memoh/internal/acl"
	acpfeedback "github.com/memohai/memoh/internal/agent/decision/feedback"
	acpprofile "github.com/memohai/memoh/internal/agent/runtime/acp/profile"
	"github.com/memohai/memoh/internal/db"
	"github.com/memohai/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/memohai/memoh/internal/db/store"
	netctl "github.com/memohai/memoh/internal/network"
	tzutil "github.com/memohai/memoh/internal/timezone"
)

type Service struct {
	queries dbstore.Queries
	acl     *acl.Service
	network *netctl.Service
	logger  *slog.Logger
}

var (
	ErrModelIDAmbiguous = errors.New("model_id is ambiguous across providers")
	ErrInvalidModelRef  = errors.New("invalid model reference")
)

func NewService(log *slog.Logger, queries dbstore.Queries, aclService *acl.Service, networkService *netctl.Service) *Service {
	return &Service{
		queries: queries,
		acl:     aclService,
		network: networkService,
		logger:  log.With(slog.String("service", "settings")),
	}
}

func (s *Service) GetBot(ctx context.Context, botID string) (Settings, error) {
	pgID, err := db.ParseUUID(botID)
	if err != nil {
		return Settings{}, err
	}
	row, err := s.queries.GetSettingsByBotID(ctx, pgID)
	if err != nil {
		return Settings{}, err
	}
	settings := normalizeBotSettingsReadRow(row)
	aclDefaultEffect, err := s.getDefaultEffect(ctx, botID)
	if err != nil {
		return Settings{}, err
	}
	settings.AclDefaultEffect = aclDefaultEffect
	return settings, nil
}

// GetCommandUILanguage returns just the command_ui_language setting for a bot.
// Avoids the second getDefaultEffect query that GetBot triggers — the locale
// is resolved once per slash command + once per interactive callback tap, so
// keeping it single-query matters for paginated lists where users tap Prev/Next
// repeatedly. Returns the raw value (caller is responsible for i18n.Resolve to
// map "auto"/unknown → server default).
func (s *Service) GetCommandUILanguage(ctx context.Context, botID string) (string, error) {
	pgID, err := db.ParseUUID(botID)
	if err != nil {
		return "", err
	}
	row, err := s.queries.GetSettingsByBotID(ctx, pgID)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(row.CommandUiLanguage), nil
}

func (s *Service) UpsertBot(ctx context.Context, botID string, req UpsertRequest) (Settings, error) {
	if s.queries == nil {
		return Settings{}, errors.New("settings queries not configured")
	}
	pgID, err := db.ParseUUID(botID)
	if err != nil {
		return Settings{}, err
	}
	botRow, err := s.queries.GetBotByID(ctx, pgID)
	if err != nil {
		return Settings{}, err
	}
	overlayBindingRow, err := s.queries.GetBotOverlayConfig(ctx, pgID)
	if err != nil {
		return Settings{}, err
	}
	previousOverlayConfig := settingsOverlayConfigFromRow(overlayBindingRow)
	if s.network != nil {
		if resolvedPrevious, resolveErr := s.network.GetBotConfig(ctx, botID); resolveErr == nil {
			previousOverlayConfig = resolvedPrevious
		}
	}
	aclDefaultEffect, err := s.getDefaultEffect(ctx, botID)
	if err != nil {
		return Settings{}, err
	}
	current := normalizeBotSetting(botRow.Language, "", aclDefaultEffect, botRow.ReasoningEnabled, botRow.ReasoningEffort, botRow.HeartbeatEnabled, botRow.HeartbeatInterval, botRow.CompactionEnabled, botRow.CompactionThreshold, botRow.CompactionRatio)
	// A read error here must abort: falling through would leave `current` at the
	// model defaults and silently overwrite a saved chat_runtime=acp_agent (and
	// its agent id) on the next save. ErrNoRows is impossible because the bot
	// row was already fetched, but treat it as "keep defaults" defensively and
	// only fail on a real DB error.
	if settingsRow, settingsErr := s.queries.GetSettingsByBotID(ctx, pgID); settingsErr != nil {
		if !errors.Is(settingsErr, pgx.ErrNoRows) {
			return Settings{}, settingsErr
		}
	} else {
		existingSettings := normalizeBotSettingsReadRow(settingsRow)
		current.ChatModelID = existingSettings.ChatModelID
		current.ChatRuntime = existingSettings.ChatRuntime
		current.ChatACPAgentID = existingSettings.ChatACPAgentID
		current.ChatACPProjectPath = existingSettings.ChatACPProjectPath
		current.ChatACPProjectMode = existingSettings.ChatACPProjectMode
		current.ToolApprovalConfig = parseToolApprovalConfig(settingsRow.ToolApprovalConfig)
		current.DisplayEnabled = settingsRow.DisplayEnabled
		current.CommandUILanguage = settingsRow.CommandUiLanguage
	}
	current.OverlayEnabled = overlayBindingRow.OverlayEnabled
	current.OverlayProvider = strings.TrimSpace(overlayBindingRow.OverlayProvider)
	current.OverlayConfig = normalizeJSONObject(overlayBindingRow.OverlayConfig)
	if strings.TrimSpace(req.Language) != "" {
		current.Language = strings.TrimSpace(req.Language)
	}
	if strings.TrimSpace(req.CommandUILanguage) != "" {
		current.CommandUILanguage = strings.TrimSpace(req.CommandUILanguage)
	}
	if effect := strings.TrimSpace(req.AclDefaultEffect); effect != "" {
		current.AclDefaultEffect = effect
	}
	if req.ReasoningEnabled != nil {
		current.ReasoningEnabled = *req.ReasoningEnabled
	}
	if req.ReasoningEffort != nil && isValidReasoningEffort(*req.ReasoningEffort) {
		current.ReasoningEffort = *req.ReasoningEffort
	}
	if req.HeartbeatEnabled != nil {
		current.HeartbeatEnabled = *req.HeartbeatEnabled
	}
	if req.HeartbeatInterval != nil && *req.HeartbeatInterval > 0 {
		current.HeartbeatInterval = *req.HeartbeatInterval
	}
	if req.CompactionEnabled != nil {
		current.CompactionEnabled = *req.CompactionEnabled
	}
	if req.CompactionThreshold != nil && *req.CompactionThreshold >= 0 {
		current.CompactionThreshold = *req.CompactionThreshold
	}
	if req.CompactionRatio != nil && *req.CompactionRatio >= 1 && *req.CompactionRatio <= 100 {
		current.CompactionRatio = *req.CompactionRatio
	}
	if req.PersistFullToolResults != nil {
		current.PersistFullToolResults = *req.PersistFullToolResults
	}
	if req.ShowToolCallsInIM != nil {
		current.ShowToolCallsInIM = *req.ShowToolCallsInIM
	}
	if req.ToolApprovalConfig != nil {
		current.ToolApprovalConfig = NormalizeToolApprovalConfig(*req.ToolApprovalConfig)
	}
	if req.DisplayEnabled != nil {
		current.DisplayEnabled = *req.DisplayEnabled
	}
	if req.ChatRuntime != nil {
		current.ChatRuntime = normalizeChatRuntime(*req.ChatRuntime)
		if current.ChatRuntime == "" {
			return Settings{}, acpfeedback.New(
				acpfeedback.CodeInvalidChatRuntime,
				"invalid_chat_runtime",
				400,
				"chat.acp.invalidChatRuntime",
				"invalid chat_runtime",
				nil,
			)
		}
	}
	if req.ChatACPAgentID != nil {
		current.ChatACPAgentID = acpprofile.NormalizeAgentID(*req.ChatACPAgentID)
	}
	if req.ChatACPProjectPath != nil {
		current.ChatACPProjectPath = strings.TrimSpace(*req.ChatACPProjectPath)
	}
	if req.ChatACPProjectMode != nil {
		current.ChatACPProjectMode = normalizeACPProjectMode(*req.ChatACPProjectMode)
		if current.ChatACPProjectMode == "" {
			return Settings{}, acpfeedback.New(
				acpfeedback.CodeProjectModeInvalid,
				"invalid_project_mode",
				400,
				"chat.acp.projectModeInvalid",
				"invalid chat_acp_project_mode",
				map[string]string{"project_mode": strings.TrimSpace(*req.ChatACPProjectMode)},
			)
		}
	}
	timezoneValue := pgtype.Text{}
	if req.Timezone != nil {
		normalized, err := normalizeOptionalTimezone(*req.Timezone)
		if err != nil {
			return Settings{}, err
		}
		timezoneValue = normalized
	}
	if req.OverlayEnabled != nil {
		current.OverlayEnabled = *req.OverlayEnabled
	}
	if req.OverlayProvider != nil {
		current.OverlayProvider = strings.TrimSpace(*req.OverlayProvider)
	}
	if req.OverlayConfig != nil {
		current.OverlayConfig = req.OverlayConfig
	}
	chatModelUUID := pgtype.UUID{}
	if value := strings.TrimSpace(req.ChatModelID); value != "" {
		modelID, err := s.resolveModelUUID(ctx, value)
		if err != nil {
			return Settings{}, err
		}
		chatModelUUID = modelID
		if modelID.Valid {
			current.ChatModelID = uuid.UUID(modelID.Bytes).String()
		}
	}
	heartbeatModelUUID := pgtype.UUID{}
	if value := strings.TrimSpace(req.HeartbeatModelID); value != "" {
		modelID, err := s.resolveModelUUID(ctx, value)
		if err != nil {
			return Settings{}, err
		}
		heartbeatModelUUID = modelID
	}
	compactionModelUUID := pgtype.UUID{}
	compactionModelIDSet := req.CompactionModelID != nil
	if req.CompactionModelID != nil {
		if value := strings.TrimSpace(*req.CompactionModelID); value != "" {
			modelID, err := s.resolveModelUUID(ctx, value)
			if err != nil {
				return Settings{}, err
			}
			compactionModelUUID = modelID
		}
	}
	imageModelUUID := pgtype.UUID{}
	if value := strings.TrimSpace(req.ImageModelID); value != "" {
		modelID, err := s.resolveModelUUID(ctx, value)
		if err != nil {
			return Settings{}, err
		}
		imageModelUUID = modelID
	}
	searchProviderUUID := pgtype.UUID{}
	if value := strings.TrimSpace(req.SearchProviderID); value != "" {
		providerID, err := db.ParseUUID(value)
		if err != nil {
			return Settings{}, err
		}
		searchProviderUUID = providerID
	}
	fetchProviderUUID := pgtype.UUID{}
	fetchProviderIDSet := req.FetchProviderID != nil
	if req.FetchProviderID != nil {
		if value := strings.TrimSpace(*req.FetchProviderID); value != "" {
			providerID, err := db.ParseUUID(value)
			if err != nil {
				return Settings{}, err
			}
			fetchProviderUUID = providerID
		}
	}
	memoryProviderUUID := pgtype.UUID{}
	if value := strings.TrimSpace(req.MemoryProviderID); value != "" {
		providerID, err := db.ParseUUID(value)
		if err != nil {
			return Settings{}, err
		}
		memoryProviderUUID = providerID
	}
	ttsModelUUID := pgtype.UUID{}
	if value := strings.TrimSpace(req.TtsModelID); value != "" {
		modelID, err := db.ParseUUID(value)
		if err != nil {
			return Settings{}, err
		}
		ttsModelUUID = modelID
	}
	transcriptionModelUUID := pgtype.UUID{}
	if value := strings.TrimSpace(req.TranscriptionModelID); value != "" {
		modelID, err := db.ParseUUID(value)
		if err != nil {
			return Settings{}, err
		}
		transcriptionModelUUID = modelID
	}
	videoModelUUID := pgtype.UUID{}
	if value := strings.TrimSpace(req.VideoModelID); value != "" {
		modelID, err := db.ParseUUID(value)
		if err != nil {
			return Settings{}, err
		}
		videoModelUUID = modelID
	}
	current = normalizeChatRuntimeFields(current)
	if err := validateChatRuntimeSettings(botRow.Metadata, current); err != nil {
		return Settings{}, err
	}
	toolApprovalConfig, err := json.Marshal(current.ToolApprovalConfig)
	if err != nil {
		return Settings{}, err
	}

	normalizedNetwork, err := s.normalizeOverlayConfig(current)
	if err != nil {
		return Settings{}, err
	}
	nextOverlayConfig := settingsOverlayConfigFromSettings(normalizedNetwork)
	networkChanged := s.network != nil && !overlayConfigsEqual(previousOverlayConfig, nextOverlayConfig)
	rollbackNetworkChange := func(cause error) error {
		if !networkChanged {
			return cause
		}
		if rollbackErr := s.reconcileBotNetwork(ctx, botID, nextOverlayConfig, previousOverlayConfig); rollbackErr != nil {
			return errors.Join(cause, fmt.Errorf("network rollback failed: %w", rollbackErr))
		}
		return cause
	}
	if networkChanged {
		if err := s.reconcileBotNetwork(ctx, botID, previousOverlayConfig, nextOverlayConfig); err != nil {
			if rollbackErr := s.reconcileBotNetwork(ctx, botID, nextOverlayConfig, previousOverlayConfig); rollbackErr != nil {
				return Settings{}, errors.Join(fmt.Errorf("reconcile bot network: %w", err), fmt.Errorf("network rollback failed: %w", rollbackErr))
			}
			return Settings{}, err
		}
	}
	overlayConfigJSON, err := json.Marshal(normalizedNetwork.OverlayConfig)
	if err != nil {
		return Settings{}, rollbackNetworkChange(fmt.Errorf("marshal network config: %w", err))
	}
	updated, err := s.queries.UpsertBotSettings(ctx, sqlc.UpsertBotSettingsParams{
		ID:                     pgID,
		Timezone:               timezoneValue,
		Language:               current.Language,
		CommandUiLanguage:      current.CommandUILanguage,
		ReasoningEnabled:       current.ReasoningEnabled,
		ReasoningEffort:        current.ReasoningEffort,
		HeartbeatEnabled:       current.HeartbeatEnabled,
		HeartbeatInterval:      int32(current.HeartbeatInterval), //nolint:gosec // bounded by positive-only setter above
		HeartbeatPrompt:        "",
		CompactionEnabled:      current.CompactionEnabled,
		CompactionThreshold:    int32(current.CompactionThreshold), //nolint:gosec // bounded by non-negative setter above
		CompactionRatio:        int32(current.CompactionRatio),     //nolint:gosec // bounded 1-100 above
		ChatModelID:            chatModelUUID,
		ChatRuntime:            current.ChatRuntime,
		ChatAcpAgentID:         nullableText(current.ChatACPAgentID),
		ChatAcpProjectPath:     current.ChatACPProjectPath,
		ChatAcpProjectMode:     current.ChatACPProjectMode,
		HeartbeatModelID:       heartbeatModelUUID,
		CompactionModelIDSet:   compactionModelIDSet,
		CompactionModelID:      compactionModelUUID,
		ImageModelID:           imageModelUUID,
		SearchProviderID:       searchProviderUUID,
		FetchProviderIDSet:     fetchProviderIDSet,
		FetchProviderID:        fetchProviderUUID,
		MemoryProviderID:       memoryProviderUUID,
		TtsModelID:             ttsModelUUID,
		TranscriptionModelID:   transcriptionModelUUID,
		VideoModelID:           videoModelUUID,
		PersistFullToolResults: current.PersistFullToolResults,
		ShowToolCallsInIm:      current.ShowToolCallsInIM,
		ToolApprovalConfig:     toolApprovalConfig,
		DisplayEnabled:         current.DisplayEnabled,
		OverlayProvider:        normalizedNetwork.OverlayProvider,
		OverlayEnabled:         normalizedNetwork.OverlayEnabled,
		OverlayConfig:          overlayConfigJSON,
	})
	if err != nil {
		return Settings{}, rollbackNetworkChange(err)
	}
	createdByUserID := ""
	if botRow.OwnerUserID.Valid {
		createdByUserID = uuid.UUID(botRow.OwnerUserID.Bytes).String()
	}
	_ = createdByUserID
	if err := s.setDefaultEffect(ctx, botID, current.AclDefaultEffect); err != nil {
		return Settings{}, err
	}
	settings := normalizeBotSettingsWriteRow(updated)
	settings.AclDefaultEffect = current.AclDefaultEffect
	return settings, nil
}

func (s *Service) Delete(ctx context.Context, botID string) error {
	if s.queries == nil {
		return errors.New("settings queries not configured")
	}
	pgID, err := db.ParseUUID(botID)
	if err != nil {
		return err
	}
	if err := s.queries.DeleteSettingsByBotID(ctx, pgID); err != nil {
		return err
	}
	return nil
}

func normalizeBotSetting(language string, commandUILanguage string, aclDefaultEffect string, reasoningEnabled bool, reasoningEffort string, heartbeatEnabled bool, heartbeatInterval int32, compactionEnabled bool, compactionThreshold int32, compactionRatio int32) Settings {
	settings := Settings{
		Language:            strings.TrimSpace(language),
		CommandUILanguage:   strings.TrimSpace(commandUILanguage),
		AclDefaultEffect:    strings.TrimSpace(aclDefaultEffect),
		ReasoningEnabled:    reasoningEnabled,
		ReasoningEffort:     strings.TrimSpace(reasoningEffort),
		HeartbeatEnabled:    heartbeatEnabled,
		HeartbeatInterval:   int(heartbeatInterval),
		CompactionEnabled:   compactionEnabled,
		CompactionThreshold: int(compactionThreshold),
		CompactionRatio:     int(compactionRatio),
		ToolApprovalConfig:  DefaultToolApprovalConfig(),
		ChatRuntime:         ChatRuntimeModel,
		ChatACPProjectPath:  DefaultACPProjectPath,
		ChatACPProjectMode:  DefaultACPProjectMode,
	}
	if settings.Language == "" {
		settings.Language = DefaultLanguage
	}
	if settings.CommandUILanguage == "" {
		settings.CommandUILanguage = DefaultCommandUILanguage
	}
	if settings.AclDefaultEffect == "" {
		settings.AclDefaultEffect = "allow"
	}
	if !isValidReasoningEffort(settings.ReasoningEffort) {
		settings.ReasoningEffort = DefaultReasoningEffort
	}
	if settings.HeartbeatInterval <= 0 {
		settings.HeartbeatInterval = DefaultHeartbeatInterval
	}
	if settings.CompactionThreshold < 0 {
		settings.CompactionThreshold = 0
	}
	if settings.CompactionRatio < 1 || settings.CompactionRatio > 100 {
		settings.CompactionRatio = 80
	}
	settings.OverlayConfig = map[string]any{}
	return settings
}

// isValidReasoningEffort accepts the full effort tier range. Effort is now a
// free-form tier string (models expose their own supported levels via capability
// discovery), so we only reject empty/whitespace values here; the specific tiers
// a given model accepts are enforced upstream, not by this generic setting.
func isValidReasoningEffort(effort string) bool {
	return strings.TrimSpace(effort) != ""
}

func normalizeBotSettingsReadRow(row sqlc.GetSettingsByBotIDRow) Settings {
	return normalizeBotSettingsFields(
		row.Language,
		row.CommandUiLanguage,
		row.ReasoningEnabled,
		row.ReasoningEffort,
		row.HeartbeatEnabled,
		row.HeartbeatInterval,
		row.CompactionEnabled,
		row.CompactionThreshold,
		row.CompactionRatio,
		row.Timezone,
		row.ChatModelID,
		row.ChatRuntime,
		row.ChatAcpAgentID,
		row.ChatAcpProjectPath,
		row.ChatAcpProjectMode,
		row.HeartbeatModelID,
		row.CompactionModelID,
		row.ImageModelID,
		row.SearchProviderID,
		row.FetchProviderID,
		row.MemoryProviderID,
		row.TtsModelID,
		row.TranscriptionModelID,
		row.VideoModelID,
		row.PersistFullToolResults,
		row.ShowToolCallsInIm,
		row.ToolApprovalConfig,
		row.DisplayEnabled,
		row.OverlayProvider,
		row.OverlayEnabled,
		row.OverlayConfig,
	)
}

func normalizeBotSettingsWriteRow(row sqlc.UpsertBotSettingsRow) Settings {
	return normalizeBotSettingsFields(
		row.Language,
		row.CommandUiLanguage,
		row.ReasoningEnabled,
		row.ReasoningEffort,
		row.HeartbeatEnabled,
		row.HeartbeatInterval,
		row.CompactionEnabled,
		row.CompactionThreshold,
		row.CompactionRatio,
		row.Timezone,
		row.ChatModelID,
		row.ChatRuntime,
		row.ChatAcpAgentID,
		row.ChatAcpProjectPath,
		row.ChatAcpProjectMode,
		row.HeartbeatModelID,
		row.CompactionModelID,
		row.ImageModelID,
		row.SearchProviderID,
		row.FetchProviderID,
		row.MemoryProviderID,
		row.TtsModelID,
		row.TranscriptionModelID,
		row.VideoModelID,
		row.PersistFullToolResults,
		row.ShowToolCallsInIm,
		row.ToolApprovalConfig,
		row.DisplayEnabled,
		row.OverlayProvider,
		row.OverlayEnabled,
		row.OverlayConfig,
	)
}

func normalizeBotSettingsFields(
	language string,
	commandUILanguage string,
	reasoningEnabled bool,
	reasoningEffort string,
	heartbeatEnabled bool,
	heartbeatInterval int32,
	compactionEnabled bool,
	compactionThreshold int32,
	compactionRatio int32,
	timezone pgtype.Text,
	chatModelID pgtype.UUID,
	chatRuntime string,
	chatACPAgentID pgtype.Text,
	chatACPProjectPath string,
	chatACPProjectMode string,
	heartbeatModelID pgtype.UUID,
	compactionModelID pgtype.UUID,
	imageModelID pgtype.UUID,
	searchProviderID pgtype.UUID,
	fetchProviderID pgtype.UUID,
	memoryProviderID pgtype.UUID,
	ttsModelID pgtype.UUID,
	transcriptionModelID pgtype.UUID,
	videoModelID pgtype.UUID,
	persistFullToolResults bool,
	showToolCallsInIM bool,
	toolApprovalConfig []byte,
	displayEnabled bool,
	overlayProvider string,
	overlayEnabled bool,
	overlayConfig []byte,
) Settings {
	settings := normalizeBotSetting(language, commandUILanguage, "", reasoningEnabled, reasoningEffort, heartbeatEnabled, heartbeatInterval, compactionEnabled, compactionThreshold, compactionRatio)
	if timezone.Valid {
		settings.Timezone = timezone.String
	}
	if chatModelID.Valid {
		settings.ChatModelID = uuid.UUID(chatModelID.Bytes).String()
	}
	settings.ChatRuntime = normalizeChatRuntimeValue(chatRuntime)
	if settings.ChatRuntime == "" {
		settings.ChatRuntime = ChatRuntimeModel
	}
	if chatACPAgentID.Valid {
		settings.ChatACPAgentID = acpprofile.NormalizeAgentID(chatACPAgentID.String)
	}
	settings.ChatACPProjectPath = strings.TrimSpace(chatACPProjectPath)
	if settings.ChatACPProjectPath == "" {
		settings.ChatACPProjectPath = DefaultACPProjectPath
	}
	settings.ChatACPProjectMode = normalizeACPProjectMode(chatACPProjectMode)
	if settings.ChatACPProjectMode == "" {
		settings.ChatACPProjectMode = DefaultACPProjectMode
	}
	if heartbeatModelID.Valid {
		settings.HeartbeatModelID = uuid.UUID(heartbeatModelID.Bytes).String()
	}
	if compactionModelID.Valid {
		settings.CompactionModelID = uuid.UUID(compactionModelID.Bytes).String()
	}
	if imageModelID.Valid {
		settings.ImageModelID = uuid.UUID(imageModelID.Bytes).String()
	}
	if searchProviderID.Valid {
		settings.SearchProviderID = uuid.UUID(searchProviderID.Bytes).String()
	}
	if fetchProviderID.Valid {
		settings.FetchProviderID = uuid.UUID(fetchProviderID.Bytes).String()
	}
	if memoryProviderID.Valid {
		settings.MemoryProviderID = uuid.UUID(memoryProviderID.Bytes).String()
	}
	if ttsModelID.Valid {
		settings.TtsModelID = uuid.UUID(ttsModelID.Bytes).String()
	}
	if transcriptionModelID.Valid {
		settings.TranscriptionModelID = uuid.UUID(transcriptionModelID.Bytes).String()
	}
	if videoModelID.Valid {
		settings.VideoModelID = uuid.UUID(videoModelID.Bytes).String()
	}
	settings.PersistFullToolResults = persistFullToolResults
	settings.ShowToolCallsInIM = showToolCallsInIM
	settings.ToolApprovalConfig = parseToolApprovalConfig(toolApprovalConfig)
	settings.DisplayEnabled = displayEnabled
	settings.OverlayProvider = strings.TrimSpace(overlayProvider)
	settings.OverlayEnabled = overlayEnabled
	settings.OverlayConfig = normalizeJSONObject(overlayConfig)
	return settings
}

func parseToolApprovalConfig(raw []byte) ToolApprovalConfig {
	if len(raw) == 0 {
		return DefaultToolApprovalConfig()
	}
	var cfg ToolApprovalConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return DefaultToolApprovalConfig()
	}
	return NormalizeToolApprovalConfig(cfg)
}

func normalizeJSONObject(raw []byte) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return map[string]any{}
	}
	return out
}

func normalizeChatRuntime(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", ChatRuntimeModel:
		return ChatRuntimeModel
	case ChatRuntimeACPAgent:
		return ChatRuntimeACPAgent
	default:
		return ""
	}
}

func normalizeChatRuntimeValue(raw string) string {
	if normalized := normalizeChatRuntime(raw); normalized != "" {
		return normalized
	}
	return ChatRuntimeModel
}

func normalizeACPProjectMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", DefaultACPProjectMode:
		return DefaultACPProjectMode
	case "none":
		return "none"
	default:
		return ""
	}
}

func nullableText(value string) pgtype.Text {
	value = strings.TrimSpace(value)
	if value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}

func normalizeChatRuntimeFields(current Settings) Settings {
	current.ChatRuntime = normalizeChatRuntimeValue(current.ChatRuntime)
	current.ChatACPAgentID = acpprofile.NormalizeAgentID(current.ChatACPAgentID)
	current.ChatACPProjectPath = strings.TrimSpace(current.ChatACPProjectPath)
	if current.ChatACPProjectPath == "" {
		current.ChatACPProjectPath = DefaultACPProjectPath
	}
	current.ChatACPProjectMode = normalizeACPProjectMode(current.ChatACPProjectMode)
	if current.ChatACPProjectMode == "" {
		current.ChatACPProjectMode = DefaultACPProjectMode
	}
	return current
}

func validateChatRuntimeSettings(botMetadata []byte, current Settings) error {
	current = normalizeChatRuntimeFields(current)
	if current.ChatRuntime != ChatRuntimeACPAgent {
		return nil
	}
	agentID := acpprofile.NormalizeAgentID(current.ChatACPAgentID)
	if agentID == "" {
		return acpfeedback.New(
			acpfeedback.CodeAgentNotConfigured,
			"missing_agent_id",
			400,
			"chat.acp.agentNotConfigured",
			"chat_acp_agent_id is required when chat_runtime is acp_agent",
			nil,
		)
	}
	if current.ChatACPProjectMode == "none" {
		return acpfeedback.New(
			acpfeedback.CodeProjectModeInvalid,
			"none_not_supported_for_default_chat",
			400,
			"chat.acp.projectModeInvalid",
			"chat_acp_project_mode=none is not supported for default chat runtime",
			map[string]string{"agent_id": agentID, "project_mode": current.ChatACPProjectMode},
		)
	}
	if !strings.HasPrefix(strings.TrimSpace(current.ChatACPProjectPath), "/") {
		return acpfeedback.New(
			acpfeedback.CodeProjectPathInvalid,
			"project_path_must_be_absolute",
			400,
			"chat.acp.projectPathInvalid",
			"chat_acp_project_path must be absolute",
			map[string]string{"agent_id": agentID},
		)
	}
	profile, ok := acpprofile.Lookup(agentID)
	if !ok {
		return acpfeedback.New(
			acpfeedback.CodeAgentNotFound,
			"unknown_agent",
			400,
			"chat.acp.agentNotFound",
			fmt.Sprintf("unknown ACP agent %q", agentID),
			map[string]string{"agent_id": agentID, "agent_name": agentID},
		)
	}
	metadata := normalizeJSONObject(botMetadata)
	setup := acpprofile.ParseAgentSetup(metadata, agentID)
	if !setup.Enabled {
		return acpfeedback.New(
			acpfeedback.CodeAgentNotEnabled,
			"agent_disabled",
			400,
			"chat.acp.agentNotEnabled",
			fmt.Sprintf("ACP agent %q is not enabled for this bot", agentID),
			map[string]string{"agent_id": agentID, "agent_name": profile.DisplayName},
		)
	}
	if field, missing := acpprofile.MissingRequiredManagedFieldForPreflight(profile, setup); missing {
		return acpfeedback.New(
			acpfeedback.CodeAgentNotConfigured,
			"missing_"+acpprofile.NormalizeAgentID(field.ID),
			400,
			"chat.acp.agentNotConfigured",
			fmt.Sprintf("ACP agent %q is missing required field %q", agentID, field.ID),
			map[string]string{"agent_id": agentID, "agent_name": profile.DisplayName, "field_id": field.ID, "field_label": field.Label},
		)
	}
	return nil
}

func (s *Service) normalizeOverlayConfig(current Settings) (Settings, error) {
	current.OverlayProvider = strings.TrimSpace(current.OverlayProvider)
	current.OverlayConfig = cloneSettingsMap(current.OverlayConfig)
	if current.OverlayProvider == "" {
		if current.OverlayEnabled {
			return Settings{}, errors.New("network provider is required when network is enabled")
		}
		current.OverlayConfig = map[string]any{}
		return current, nil
	}
	if s.network == nil {
		return Settings{}, errors.New("network service not configured")
	}
	cfg, err := s.network.PrepareBotConfigForWrite(netctl.BotOverlayConfig{
		Enabled:  current.OverlayEnabled,
		Provider: current.OverlayProvider,
		Config:   current.OverlayConfig,
	})
	if err != nil {
		return Settings{}, err
	}
	current.OverlayEnabled = cfg.Enabled
	current.OverlayProvider = cfg.Provider
	current.OverlayConfig = cfg.Config
	return current, nil
}

func cloneSettingsMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func settingsOverlayConfigFromRow(row sqlc.GetBotOverlayConfigRow) netctl.BotOverlayConfig {
	return netctl.BotOverlayConfig{
		Enabled:  row.OverlayEnabled,
		Provider: strings.TrimSpace(row.OverlayProvider),
		Config:   normalizeJSONObject(row.OverlayConfig),
	}
}

func settingsOverlayConfigFromSettings(current Settings) netctl.BotOverlayConfig {
	return netctl.BotOverlayConfig{
		Enabled:  current.OverlayEnabled,
		Provider: strings.TrimSpace(current.OverlayProvider),
		Config:   cloneSettingsMap(current.OverlayConfig),
	}
}

func overlayConfigsEqual(left, right netctl.BotOverlayConfig) bool {
	if left.Enabled != right.Enabled || left.Provider != right.Provider {
		return false
	}
	return jsonEqual(left.Config, right.Config)
}

func (s *Service) reconcileBotNetwork(ctx context.Context, botID string, previous, next netctl.BotOverlayConfig) error {
	if s.network == nil || overlayConfigsEqual(previous, next) {
		return nil
	}
	return s.network.ReconcileBot(ctx, botID, previous, next)
}

func jsonEqual(left, right map[string]any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return string(leftJSON) == string(rightJSON)
}

func (s *Service) getDefaultEffect(ctx context.Context, botID string) (string, error) {
	if s.acl == nil {
		return "allow", nil
	}
	return s.acl.GetDefaultEffect(ctx, botID)
}

func (s *Service) setDefaultEffect(ctx context.Context, botID, effect string) error {
	if s.acl == nil {
		return nil
	}
	if effect == "" {
		return nil
	}
	return s.acl.SetDefaultEffect(ctx, botID, effect)
}

func (s *Service) resolveModelUUID(ctx context.Context, modelID string) (pgtype.UUID, error) {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return pgtype.UUID{}, fmt.Errorf("%w: model_id is required", ErrInvalidModelRef)
	}

	// Preferred path: when caller already passes the model UUID.
	if parsed, err := db.ParseUUID(modelID); err == nil {
		if _, err := s.queries.GetModelByID(ctx, parsed); err == nil {
			return parsed, nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return pgtype.UUID{}, err
		}
	}

	rows, err := s.queries.ListModelsByModelID(ctx, modelID)
	if err != nil {
		return pgtype.UUID{}, err
	}
	if len(rows) == 0 {
		return pgtype.UUID{}, fmt.Errorf("%w: model not found: %s", ErrInvalidModelRef, modelID)
	}
	if len(rows) > 1 {
		return pgtype.UUID{}, fmt.Errorf("%w: %s", ErrModelIDAmbiguous, modelID)
	}
	return rows[0].ID, nil
}

func normalizeOptionalTimezone(raw string) (pgtype.Text, error) {
	normalized := strings.TrimSpace(raw)
	if normalized == "" {
		return pgtype.Text{}, nil
	}
	loc, _, err := tzutil.Resolve(normalized)
	if err != nil {
		return pgtype.Text{}, fmt.Errorf("invalid timezone: %w", err)
	}
	return pgtype.Text{String: loc.String(), Valid: true}, nil
}
