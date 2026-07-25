package gateway

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	team "github.com/memohai/memoh/domains/iam/team"
)

// ErrChannelConfigNotFound indicates the bot has no persisted config for the channel type.
var ErrChannelConfigNotFound = errors.New("channel config not found")

// ErrChannelIdentityConfigNotFound indicates the identity has no binding for the channel type.
var ErrChannelIdentityConfigNotFound = errors.New("channel user config not found")

// ErrChannelDiscoveryFailed indicates that a platform-side self identity check failed.
var ErrChannelDiscoveryFailed = errors.New("channel identity discovery failed")

// ErrExternalIdentityConflict indicates that a persisted channel identity must be unique.
var ErrExternalIdentityConflict = errors.New("channel external identity conflict")

// ConfigWrite is the persistence-neutral input for a bot channel config.
type ConfigWrite struct {
	BotID            string
	ChannelType      ChannelType
	Credentials      map[string]any
	ExternalIdentity string
	SelfIdentity     map[string]any
	Routing          map[string]any
	Disabled         bool
	VerifiedAt       *time.Time
}

// IdentityBindingWrite is the persistence-neutral input for a channel identity binding.
type IdentityBindingWrite struct {
	ChannelIdentityID string
	ChannelType       ChannelType
	Config            map[string]any
}

// IdentityBindingStore persists API-owned outbound delivery configuration.
type IdentityBindingStore interface {
	UpsertIdentityBinding(context.Context, IdentityBindingWrite) (ChannelIdentityBinding, error)
	FindIdentityBinding(context.Context, string, ChannelType) (ChannelIdentityBinding, error)
	ListIdentityBindingsByType(context.Context, ChannelType) ([]ChannelIdentityBinding, error)
}

// Persistence is the Channel-owned config and binding persistence surface.
type Persistence interface {
	IdentityBindingStore
	UpsertConfig(context.Context, ConfigWrite) (ChannelConfig, error)
	DeleteConfig(context.Context, string, ChannelType) error
	UpdateConfigDisabled(context.Context, string, ChannelType, bool) (ChannelConfig, error)
	SaveMatrixSyncSinceToken(context.Context, string, string) error
	FindConfig(context.Context, string, ChannelType) (ChannelConfig, error)
	ListConfigsByType(context.Context, ChannelType) ([]ChannelConfig, error)
}

// Service provides channel config and identity-binding business operations.
type Service struct {
	persistence Persistence
	queries     Persistence // compatibility for webhook_endpoint.go's configured check
	registry    *Registry
}

// Store is retained as an API-compatible name for Service.
type Store = Service

// NewService creates a channel config service.
func NewService(persistence Persistence, registry *Registry) *Service {
	if registry == nil {
		registry = NewRegistry()
	}
	return &Service{persistence: persistence, queries: persistence, registry: registry}
}

// NewStore creates a channel config service.
func NewStore(persistence Persistence, registry *Registry) *Store {
	return NewService(persistence, registry)
}

// UpsertConfig creates or updates a bot's channel configuration.
func (s *Service) UpsertConfig(ctx context.Context, botID string, channelType ChannelType, req UpsertConfigRequest) (ChannelConfig, error) {
	if s.persistence == nil {
		return ChannelConfig{}, errors.New("channel persistence not configured")
	}
	if channelType == "" {
		return ChannelConfig{}, errors.New("channel type is required")
	}
	normalized, err := s.registry.NormalizeConfig(channelType, req.Credentials)
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
	routing := req.Routing
	if routing == nil {
		routing = map[string]any{}
	}
	var verifiedAt *time.Time
	if req.VerifiedAt != nil {
		value := req.VerifiedAt.UTC()
		verifiedAt = &value
	}
	cfg, err := s.persistence.UpsertConfig(ctx, ConfigWrite{
		BotID:            strings.TrimSpace(botID),
		ChannelType:      channelType,
		Credentials:      normalized,
		ExternalIdentity: externalIdentity,
		SelfIdentity:     selfIdentity,
		Routing:          routing,
		Disabled:         disabled,
		VerifiedAt:       verifiedAt,
	})
	if err != nil {
		if errors.Is(err, ErrExternalIdentityConflict) && strings.TrimSpace(policy.DuplicateExternalIdentityMessage) != "" {
			return ChannelConfig{}, errors.New(policy.DuplicateExternalIdentityMessage)
		}
		return ChannelConfig{}, err
	}
	return cfg, nil
}

// DeleteConfig removes a bot's channel configuration.
func (s *Service) DeleteConfig(ctx context.Context, botID string, channelType ChannelType) error {
	if s.persistence == nil {
		return errors.New("channel persistence not configured")
	}
	if channelType == "" {
		return errors.New("channel type is required")
	}
	return s.persistence.DeleteConfig(ctx, botID, channelType)
}

// UpdateConfigDisabled updates only the disabled flag for a bot channel config and returns latest config.
func (s *Service) UpdateConfigDisabled(ctx context.Context, botID string, channelType ChannelType, disabled bool) (ChannelConfig, error) {
	if s.persistence == nil {
		return ChannelConfig{}, errors.New("channel persistence not configured")
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
	return s.persistence.UpdateConfigDisabled(ctx, botID, channelType, disabled)
}

func (s *Service) getPreviousConfig(ctx context.Context, botID string, channelType ChannelType) (ChannelConfig, bool, error) {
	cfg, err := s.ResolveEffectiveConfig(ctx, botID, channelType)
	if err == nil {
		return cfg, true, nil
	}
	if errors.Is(err, ErrChannelConfigNotFound) {
		return ChannelConfig{}, false, nil
	}
	return ChannelConfig{}, false, err
}

func (s *Service) prepareSelfIdentity(
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
func (s *Service) ListConfigs(ctx context.Context, botID string) ([]ChannelConfig, error) {
	if s.persistence == nil {
		return nil, errors.New("channel persistence not configured")
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
func (s *Service) SaveMatrixSyncSinceToken(ctx context.Context, configID string, since string) error {
	if s.persistence == nil {
		return errors.New("channel persistence not configured")
	}
	return s.persistence.SaveMatrixSyncSinceToken(ctx, configID, strings.TrimSpace(since))
}

// UpsertChannelIdentityConfig creates or updates a channel identity's channel binding.
func (s *Service) UpsertChannelIdentityConfig(ctx context.Context, channelIdentityID string, channelType ChannelType, req UpsertChannelIdentityConfigRequest) (ChannelIdentityBinding, error) {
	if s.persistence == nil {
		return ChannelIdentityBinding{}, errors.New("channel persistence not configured")
	}
	if channelType == "" {
		return ChannelIdentityBinding{}, errors.New("channel type is required")
	}
	normalized, err := s.registry.NormalizeUserConfig(channelType, req.Config)
	if err != nil {
		return ChannelIdentityBinding{}, err
	}
	return s.persistence.UpsertIdentityBinding(ctx, IdentityBindingWrite{
		ChannelIdentityID: strings.TrimSpace(channelIdentityID),
		ChannelType:       channelType,
		Config:            normalized,
	})
}

// ResolveEffectiveConfig returns the active channel configuration for a bot.
// For configless channel types, a synthetic config is returned.
func (s *Service) ResolveEffectiveConfig(ctx context.Context, botID string, channelType ChannelType) (ChannelConfig, error) {
	if s.persistence == nil {
		return ChannelConfig{}, errors.New("channel persistence not configured")
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
	return s.persistence.FindConfig(ctx, botID, channelType)
}

// ListBotConfigs returns all registered channel configs for a bot.
// Missing configs are skipped so callers can enumerate platform state without
// knowing which integrations are currently configured.
func (s *Service) ListBotConfigs(ctx context.Context, botID string) ([]ChannelConfig, error) {
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
func (s *Service) ListConfigsByType(ctx context.Context, channelType ChannelType) ([]ChannelConfig, error) {
	if s.persistence == nil {
		return nil, errors.New("channel persistence not configured")
	}
	if s.registry.IsConfigless(channelType) {
		return []ChannelConfig{}, nil
	}
	return s.persistence.ListConfigsByType(ctx, channelType)
}

// GetChannelIdentityConfig returns the channel identity's channel binding for the given channel type.
func (s *Service) GetChannelIdentityConfig(ctx context.Context, channelIdentityID string, channelType ChannelType) (ChannelIdentityBinding, error) {
	if s.persistence == nil {
		return ChannelIdentityBinding{}, errors.New("channel persistence not configured")
	}
	if channelType == "" {
		return ChannelIdentityBinding{}, errors.New("channel type is required")
	}
	return s.persistence.FindIdentityBinding(ctx, channelIdentityID, channelType)
}

// ListChannelIdentityConfigsByType returns all channel identity bindings for the given channel type.
func (s *Service) ListChannelIdentityConfigsByType(ctx context.Context, channelType ChannelType) ([]ChannelIdentityBinding, error) {
	if s.persistence == nil {
		return nil, errors.New("channel persistence not configured")
	}
	return s.persistence.ListIdentityBindingsByType(ctx, channelType)
}

// ResolveChannelIdentityBinding finds the channel identity ID whose channel binding matches the given criteria.
func (s *Service) ResolveChannelIdentityBinding(ctx context.Context, channelType ChannelType, criteria BindingCriteria) (string, error) {
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
