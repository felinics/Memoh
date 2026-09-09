package channel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/team"
)

// ErrChannelConfigNotFound indicates the bot has no persisted config for the channel type.
var ErrChannelConfigNotFound = errors.New("channel config not found")

// ErrChannelDiscoveryFailed indicates that a platform-side self identity check failed.
var ErrChannelDiscoveryFailed = errors.New("channel identity discovery failed")

// Store provides CRUD operations for channel configurations, user bindings, and sessions.
type Store struct {
	queries  dbstore.Queries
	registry *Registry
}

// NewStore creates a Store backed by the given database queries and adapter registry.
func NewStore(queries dbstore.Queries, registry *Registry) *Store {
	if registry == nil {
		registry = NewRegistry()
	}
	return &Store{queries: queries, registry: registry}
}

// UpsertConfig creates or updates a bot's channel configuration.
func (s *Store) UpsertConfig(ctx context.Context, botID string, channelType ChannelType, req UpsertConfigRequest) (ChannelConfig, error) {
	if s.queries == nil {
		return ChannelConfig{}, errors.New("channel queries not configured")
	}
	if channelType == "" {
		return ChannelConfig{}, errors.New("channel type is required")
	}
	normalized, err := s.registry.NormalizeConfig(channelType, req.Credentials)
	if err != nil {
		return ChannelConfig{}, err
	}
	credentialsPayload, err := json.Marshal(normalized)
	if err != nil {
		return ChannelConfig{}, err
	}
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return ChannelConfig{}, err
	}
	disabled := false
	if req.Disabled != nil {
		disabled = *req.Disabled
	}
	externalIdentity := strings.TrimSpace(req.ExternalIdentity)
	policy := s.registry.SelfIdentityPolicy(channelType)
	var selfIdentity map[string]any
	if policy.RequireDiscoveryOnEnable {
		var (
			previous    ChannelConfig
			hadPrevious bool
		)
		previous, hadPrevious, err = s.getPreviousConfig(ctx, botID, channelType)
		if err != nil {
			return ChannelConfig{}, err
		}
		selfIdentity, externalIdentity, err = s.prepareSelfIdentity(ctx, channelType, normalized, req, disabled, previous, hadPrevious, policy)
		if err != nil {
			return ChannelConfig{}, err
		}
	} else {
		selfIdentity = req.SelfIdentity
		if selfIdentity == nil {
			selfIdentity = map[string]any{}
		}
		if discovered, extID, err := s.registry.DiscoverSelf(ctx, channelType, normalized); err == nil && discovered != nil {
			for k, v := range discovered {
				if _, exists := selfIdentity[k]; !exists {
					selfIdentity[k] = v
				}
			}
			if externalIdentity == "" && strings.TrimSpace(extID) != "" {
				externalIdentity = strings.TrimSpace(extID)
			}
		}
	}
	selfPayload, err := json.Marshal(selfIdentity)
	if err != nil {
		return ChannelConfig{}, err
	}
	routing := req.Routing
	if routing == nil {
		routing = map[string]any{}
	}
	routingPayload, err := json.Marshal(routing)
	if err != nil {
		return ChannelConfig{}, err
	}
	verifiedAt := pgtype.Timestamptz{Valid: false}
	if req.VerifiedAt != nil {
		verifiedAt = pgtype.Timestamptz{Time: req.VerifiedAt.UTC(), Valid: true}
	}
	row, err := s.queries.UpsertBotChannelConfig(ctx, sqlc.UpsertBotChannelConfigParams{
		BotID:       botUUID,
		ChannelType: channelType.String(),
		Credentials: credentialsPayload,
		ExternalIdentity: pgtype.Text{
			String: externalIdentity,
			Valid:  externalIdentity != "",
		},
		SelfIdentity: selfPayload,
		Routing:      routingPayload,
		Capabilities: []byte("{}"),
		Disabled:     disabled,
		VerifiedAt:   verifiedAt,
	})
	if err != nil {
		if db.IsUniqueViolation(err) && strings.TrimSpace(policy.DuplicateExternalIdentityMessage) != "" {
			return ChannelConfig{}, errors.New(policy.DuplicateExternalIdentityMessage)
		}
		return ChannelConfig{}, err
	}
	return normalizeChannelConfigFromRow(row)
}

// DeleteConfig removes a bot's channel configuration.
func (s *Store) DeleteConfig(ctx context.Context, botID string, channelType ChannelType) error {
	if s.queries == nil {
		return errors.New("channel queries not configured")
	}
	if channelType == "" {
		return errors.New("channel type is required")
	}
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return err
	}
	return s.queries.DeleteBotChannelConfig(ctx, sqlc.DeleteBotChannelConfigParams{
		BotID:       botUUID,
		ChannelType: channelType.String(),
	})
}

// UpdateConfigDisabled updates only the disabled flag for a bot channel config and returns latest config.
func (s *Store) UpdateConfigDisabled(ctx context.Context, botID string, channelType ChannelType, disabled bool) (ChannelConfig, error) {
	if s.queries == nil {
		return ChannelConfig{}, errors.New("channel queries not configured")
	}
	if channelType == "" {
		return ChannelConfig{}, errors.New("channel type is required")
	}
	if s.registry.SelfIdentityPolicy(channelType).RequireDiscoveryOnEnable && !disabled {
		cfg, err := s.ResolveEffectiveConfig(ctx, botID, channelType)
		if err != nil {
			return ChannelConfig{}, err
		}
		req := upsertRequestFromConfig(cfg)
		req.Disabled = &disabled
		return s.UpsertConfig(ctx, botID, channelType, req)
	}
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return ChannelConfig{}, err
	}
	row, err := s.queries.UpdateBotChannelConfigDisabled(ctx, sqlc.UpdateBotChannelConfigDisabledParams{
		BotID:       botUUID,
		ChannelType: channelType.String(),
		Disabled:    disabled,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ChannelConfig{}, fmt.Errorf("%w", ErrChannelConfigNotFound)
		}
		return ChannelConfig{}, err
	}
	return normalizeChannelConfigFromRow(row)
}

func (s *Store) getPreviousConfig(ctx context.Context, botID string, channelType ChannelType) (ChannelConfig, bool, error) {
	cfg, err := s.ResolveEffectiveConfig(ctx, botID, channelType)
	if err == nil {
		return cfg, true, nil
	}
	if errors.Is(err, ErrChannelConfigNotFound) {
		return ChannelConfig{}, false, nil
	}
	return ChannelConfig{}, false, err
}

func (s *Store) prepareSelfIdentity(
	ctx context.Context,
	channelType ChannelType,
	normalized map[string]any,
	req UpsertConfigRequest,
	disabled bool,
	previous ChannelConfig,
	hadPrevious bool,
	policy SelfIdentityPolicy,
) (map[string]any, string, error) {
	credsChanged := policy.RefreshOnCredentialsChange && (!hadPrevious || !reflect.DeepEqual(normalized, previous.Credentials))
	selfIdentity := cloneAnyMap(req.SelfIdentity)
	externalIdentity := strings.TrimSpace(req.ExternalIdentity)

	if credsChanged {
		selfIdentity = map[string]any{}
		externalIdentity = ""
	} else {
		if req.SelfIdentity == nil {
			selfIdentity = cloneAnyMap(previous.SelfIdentity)
		}
		if externalIdentity == "" {
			externalIdentity = strings.TrimSpace(previous.ExternalIdentity)
		}
	}

	discovered, extID, discoverErr := s.registry.DiscoverSelf(ctx, channelType, normalized)
	if discoverErr != nil {
		if disabled {
			return selfIdentity, externalIdentity, nil
		}
		message := strings.TrimSpace(policy.DiscoveryErrorMessage)
		if message == "" {
			message = fmt.Sprintf("%s identity discovery failed", channelType)
		}
		return nil, "", fmt.Errorf("%s: %w: %w", message, ErrChannelDiscoveryFailed, discoverErr)
	}
	for key, value := range discovered {
		selfIdentity[key] = value
	}
	if value := strings.TrimSpace(extID); value != "" {
		externalIdentity = value
	}
	if externalIdentity == "" {
		externalIdentity = readAnyMapString(selfIdentity, policy.RequiredSelfIdentityKey)
	}
	if !disabled {
		if readAnyMapString(selfIdentity, policy.RequiredSelfIdentityKey) == "" || strings.TrimSpace(externalIdentity) == "" {
			message := strings.TrimSpace(policy.MissingIdentityMessage)
			if message == "" {
				message = fmt.Sprintf("%s identity discovery returned no required identity", channelType)
			}
			return nil, "", errors.New(message)
		}
	}
	return selfIdentity, externalIdentity, nil
}

func readAnyMapString(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	value, ok := values[key]
	if !ok {
		return ""
	}
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

// ListConfigs returns all persisted channel configurations for a bot.
func (s *Store) ListConfigs(ctx context.Context, botID string) ([]ChannelConfig, error) {
	if s.queries == nil {
		return nil, errors.New("channel queries not configured")
	}
	types := s.registry.Types()
	items := make([]ChannelConfig, 0, len(types))
	for _, channelType := range types {
		if s.registry.IsConfigless(channelType) {
			continue
		}
		item, err := s.ResolveEffectiveConfig(ctx, botID, channelType)
		if err != nil {
			if errors.Is(err, ErrChannelConfigNotFound) {
				continue
			}
			return nil, err
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].ChannelType < items[j].ChannelType
	})
	return items, nil
}

// SaveMatrixSyncSinceToken persists the Matrix /sync cursor without mutating channel config updated_at.
func (s *Store) SaveMatrixSyncSinceToken(ctx context.Context, configID string, since string) error {
	if s.queries == nil {
		return errors.New("channel queries not configured")
	}
	pgConfigID, err := db.ParseUUID(configID)
	if err != nil {
		return err
	}
	rows, err := s.queries.SaveMatrixSyncSinceToken(ctx, sqlc.SaveMatrixSyncSinceTokenParams{
		ID:         pgConfigID,
		SinceToken: strings.TrimSpace(since),
	})
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("%w", ErrChannelConfigNotFound)
	}
	return nil
}

// UpsertChannelIdentityConfig creates or updates a channel identity's channel binding.
func (s *Store) UpsertChannelIdentityConfig(ctx context.Context, channelIdentityID string, channelType ChannelType, req UpsertChannelIdentityConfigRequest) (ChannelIdentityBinding, error) {
	if s.queries == nil {
		return ChannelIdentityBinding{}, errors.New("channel queries not configured")
	}
	if channelType == "" {
		return ChannelIdentityBinding{}, errors.New("channel type is required")
	}
	normalized, err := s.registry.NormalizeUserConfig(channelType, req.Config)
	if err != nil {
		return ChannelIdentityBinding{}, err
	}
	payload, err := json.Marshal(normalized)
	if err != nil {
		return ChannelIdentityBinding{}, err
	}
	pgChannelIdentityID, err := db.ParseUUID(channelIdentityID)
	if err != nil {
		return ChannelIdentityBinding{}, err
	}
	row, err := s.queries.UpsertUserChannelBinding(ctx, sqlc.UpsertUserChannelBindingParams{
		UserID:      pgChannelIdentityID,
		ChannelType: channelType.String(),
		Config:      payload,
	})
	if err != nil {
		return ChannelIdentityBinding{}, err
	}
	return normalizeChannelIdentityBinding(row)
}

// ResolveEffectiveConfig returns the active channel configuration for a bot.
// For configless channel types, a synthetic config is returned.
func (s *Store) ResolveEffectiveConfig(ctx context.Context, botID string, channelType ChannelType) (ChannelConfig, error) {
	if s.queries == nil {
		return ChannelConfig{}, errors.New("channel queries not configured")
	}
	if channelType == "" {
		return ChannelConfig{}, errors.New("channel type is required")
	}
	if s.registry.IsConfigless(channelType) {
		// Configless channels have no bot_channel_configs row to carry a
		// team, and turn.Service fails closed on an empty TeamID. The whole
		// runtime is session-bound to the self-hosted singleton team (see
		// internal/db.db.go GUC binding); a hosted multi-team runtime
		// replaces this with request-scoped team resolution.
		return ChannelConfig{
			ID:          channelType.String() + ":" + strings.TrimSpace(botID),
			TeamID:      team.DefaultTeamID,
			BotID:       strings.TrimSpace(botID),
			ChannelType: channelType,
		}, nil
	}
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return ChannelConfig{}, err
	}
	row, err := s.queries.GetBotChannelConfig(ctx, sqlc.GetBotChannelConfigParams{
		BotID:       botUUID,
		ChannelType: channelType.String(),
	})
	if err == nil {
		return normalizeChannelConfigFromGetRow(row)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ChannelConfig{}, err
	}
	return ChannelConfig{}, fmt.Errorf("%w", ErrChannelConfigNotFound)
}

// ListBotConfigs returns all registered channel configs for a bot.
// Missing configs are skipped so callers can enumerate platform state without
// knowing which integrations are currently configured.
func (s *Store) ListBotConfigs(ctx context.Context, botID string) ([]ChannelConfig, error) {
	if strings.TrimSpace(botID) == "" {
		return nil, errors.New("bot id is required")
	}
	types := s.registry.Types()
	sort.Slice(types, func(i, j int) bool {
		return strings.Compare(types[i].String(), types[j].String()) < 0
	})

	items := make([]ChannelConfig, 0, len(types))
	for _, channelType := range types {
		cfg, err := s.ResolveEffectiveConfig(ctx, botID, channelType)
		if err != nil {
			if errors.Is(err, ErrChannelConfigNotFound) {
				continue
			}
			return nil, err
		}
		items = append(items, cfg)
	}
	return items, nil
}

// ListConfigsByType returns all channel configurations of the given type.
func (s *Store) ListConfigsByType(ctx context.Context, channelType ChannelType) ([]ChannelConfig, error) {
	if s.queries == nil {
		return nil, errors.New("channel queries not configured")
	}
	if s.registry.IsConfigless(channelType) {
		return []ChannelConfig{}, nil
	}
	rows, err := s.queries.ListBotChannelConfigsByType(ctx, channelType.String())
	if err != nil {
		return nil, err
	}
	items := make([]ChannelConfig, 0, len(rows))
	for _, row := range rows {
		item, err := normalizeChannelConfigFromListRow(row)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

// GetChannelIdentityConfig returns the channel identity's channel binding for the given channel type.
func (s *Store) GetChannelIdentityConfig(ctx context.Context, channelIdentityID string, channelType ChannelType) (ChannelIdentityBinding, error) {
	if s.queries == nil {
		return ChannelIdentityBinding{}, errors.New("channel queries not configured")
	}
	if channelType == "" {
		return ChannelIdentityBinding{}, errors.New("channel type is required")
	}
	pgChannelIdentityID, err := db.ParseUUID(channelIdentityID)
	if err != nil {
		return ChannelIdentityBinding{}, err
	}
	row, err := s.queries.GetUserChannelBinding(ctx, sqlc.GetUserChannelBindingParams{
		UserID:      pgChannelIdentityID,
		ChannelType: channelType.String(),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ChannelIdentityBinding{}, errors.New("channel user config not found")
		}
		return ChannelIdentityBinding{}, err
	}
	config, err := DecodeConfigMap(row.Config)
	if err != nil {
		return ChannelIdentityBinding{}, err
	}
	return ChannelIdentityBinding{
		ID:                row.ID.String(),
		ChannelType:       ChannelType(row.ChannelType),
		ChannelIdentityID: row.UserID.String(),
		Config:            config,
		CreatedAt:         db.TimeFromPg(row.CreatedAt),
		UpdatedAt:         db.TimeFromPg(row.UpdatedAt),
	}, nil
}

// ListChannelIdentityConfigsByType returns all channel identity bindings for the given channel type.
func (s *Store) ListChannelIdentityConfigsByType(ctx context.Context, channelType ChannelType) ([]ChannelIdentityBinding, error) {
	if s.queries == nil {
		return nil, errors.New("channel queries not configured")
	}
	rows, err := s.queries.ListUserChannelBindingsByPlatform(ctx, channelType.String())
	if err != nil {
		return nil, err
	}
	items := make([]ChannelIdentityBinding, 0, len(rows))
	for _, row := range rows {
		item, err := normalizeChannelIdentityBinding(row)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

// ResolveChannelIdentityBinding finds the channel identity ID whose channel binding matches the given criteria.
func (s *Store) ResolveChannelIdentityBinding(ctx context.Context, channelType ChannelType, criteria BindingCriteria) (string, error) {
	rows, err := s.ListChannelIdentityConfigsByType(ctx, channelType)
	if err != nil {
		return "", err
	}
	if _, ok := s.registry.Get(channelType); !ok {
		return "", fmt.Errorf("unsupported channel type: %s", channelType)
	}
	for _, row := range rows {
		if s.registry.MatchUserBinding(channelType, row.Config, criteria) {
			return row.ChannelIdentityID, nil
		}
	}
	return "", errors.New("channel user binding not found")
}

func normalizeChannelConfigFromRow(row sqlc.BotChannelConfig) (ChannelConfig, error) {
	return normalizeChannelConfigFields(
		row.ID, row.TeamID, row.BotID, row.ChannelType,
		row.Credentials, row.ExternalIdentity, row.SelfIdentity, row.Routing,
		row.Disabled, row.VerifiedAt, row.CreatedAt, row.UpdatedAt,
	)
}

func normalizeChannelConfigFromGetRow(row sqlc.BotChannelConfig) (ChannelConfig, error) {
	return normalizeChannelConfigFields(
		row.ID, row.TeamID, row.BotID, row.ChannelType,
		row.Credentials, row.ExternalIdentity, row.SelfIdentity, row.Routing,
		row.Disabled, row.VerifiedAt, row.CreatedAt, row.UpdatedAt,
	)
}

func normalizeChannelConfigFromListRow(row sqlc.BotChannelConfig) (ChannelConfig, error) {
	return normalizeChannelConfigFields(
		row.ID, row.TeamID, row.BotID, row.ChannelType,
		row.Credentials, row.ExternalIdentity, row.SelfIdentity, row.Routing,
		row.Disabled, row.VerifiedAt, row.CreatedAt, row.UpdatedAt,
	)
}

func normalizeChannelConfigFields(
	id, teamID, botID pgtype.UUID, channelType string,
	credentials []byte, externalIdentity pgtype.Text, selfIdentity, routing []byte,
	disabled bool, verifiedAt, createdAt, updatedAt pgtype.Timestamptz,
) (ChannelConfig, error) {
	credentialsMap, err := DecodeConfigMap(credentials)
	if err != nil {
		return ChannelConfig{}, err
	}
	selfIdentityMap, err := DecodeConfigMap(selfIdentity)
	if err != nil {
		return ChannelConfig{}, err
	}
	routingMap, err := DecodeConfigMap(routing)
	if err != nil {
		return ChannelConfig{}, err
	}
	verifiedAtTime := time.Time{}
	if verifiedAt.Valid {
		verifiedAtTime = verifiedAt.Time
	}
	externalIdentityStr := ""
	if externalIdentity.Valid {
		externalIdentityStr = strings.TrimSpace(externalIdentity.String)
	}
	return ChannelConfig{
		ID:               id.String(),
		TeamID:           teamID.String(),
		BotID:            botID.String(),
		ChannelType:      ChannelType(channelType),
		Credentials:      credentialsMap,
		ExternalIdentity: externalIdentityStr,
		SelfIdentity:     selfIdentityMap,
		Routing:          routingMap,
		Disabled:         disabled,
		VerifiedAt:       verifiedAtTime,
		CreatedAt:        db.TimeFromPg(createdAt),
		UpdatedAt:        db.TimeFromPg(updatedAt),
	}, nil
}

func normalizeChannelIdentityBinding(row sqlc.UserChannelBinding) (ChannelIdentityBinding, error) {
	config, err := DecodeConfigMap(row.Config)
	if err != nil {
		return ChannelIdentityBinding{}, err
	}
	return ChannelIdentityBinding{
		ID:                row.ID.String(),
		ChannelType:       ChannelType(row.ChannelType),
		ChannelIdentityID: row.UserID.String(),
		Config:            config,
		CreatedAt:         db.TimeFromPg(row.CreatedAt),
		UpdatedAt:         db.TimeFromPg(row.UpdatedAt),
	}, nil
}
