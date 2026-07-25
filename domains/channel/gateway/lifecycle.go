package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// LifecycleStore persists channel configs for lifecycle orchestration.
type LifecycleStore interface {
	ResolveEffectiveConfig(ctx context.Context, botID string, channelType ChannelType) (ChannelConfig, error)
	UpsertConfig(ctx context.Context, botID string, channelType ChannelType, req UpsertConfigRequest) (ChannelConfig, error)
	UpdateConfigDisabled(ctx context.Context, botID string, channelType ChannelType, disabled bool) (ChannelConfig, error)
	DeleteConfig(ctx context.Context, botID string, channelType ChannelType) error
}

// ConnectionController controls runtime channel connections.
type ConnectionController interface {
	EnsureConnection(ctx context.Context, cfg ChannelConfig) error
	RemoveConnection(ctx context.Context, botID string, channelType ChannelType)
}

// ErrEnableChannelFailed indicates that enabling the channel (e.g. EnsureConnection) failed.
var ErrEnableChannelFailed = errors.New("enable channel failed")

// Lifecycle coordinates persisted config updates and runtime connection state.
type Lifecycle struct {
	store      LifecycleStore
	controller ConnectionController
}

// NewLifecycle creates a lifecycle coordinator from storage and connection controller.
func NewLifecycle(store LifecycleStore, controller ConnectionController) *Lifecycle {
	return &Lifecycle{
		store:      store,
		controller: controller,
	}
}

// UpsertBotChannelConfig updates config and applies connection lifecycle.
// For disabled=true, it stores config and stops any active connection.
// For disabled=false, it stores config then starts connection; on start failure it rolls back.
func (s *Lifecycle) UpsertBotChannelConfig(ctx context.Context, botID string, channelType ChannelType, req UpsertConfigRequest) (ChannelConfig, error) {
	if s.store == nil {
		return ChannelConfig{}, errors.New("channel lifecycle store not configured")
	}
	disabled := false
	if req.Disabled != nil {
		disabled = *req.Disabled
	}
	if !disabled && s.controller == nil {
		return ChannelConfig{}, errors.New("channel connection controller not configured")
	}

	previous, hadPrevious, err := s.getPreviousConfig(ctx, botID, channelType)
	if err != nil {
		return ChannelConfig{}, err
	}

	updated, err := s.store.UpsertConfig(ctx, botID, channelType, req)
	if err != nil {
		return ChannelConfig{}, err
	}

	if disabled {
		if s.controller != nil {
			s.controller.RemoveConnection(ctx, botID, channelType)
		}
		return updated, nil
	}

	if err := s.controller.EnsureConnection(ctx, updated); err != nil {
		if rollbackErr := s.rollbackUpsert(ctx, botID, channelType, hadPrevious, previous); rollbackErr != nil {
			return ChannelConfig{}, fmt.Errorf("%w (rollback failed: %w): %w", ErrEnableChannelFailed, rollbackErr, err)
		}
		return ChannelConfig{}, fmt.Errorf("%w: %w", ErrEnableChannelFailed, err)
	}
	return updated, nil
}

// DeleteBotChannelConfig removes persisted config and stops active runtime connection.
func (s *Lifecycle) DeleteBotChannelConfig(ctx context.Context, botID string, channelType ChannelType) error {
	if s.store == nil {
		return errors.New("channel lifecycle store not configured")
	}
	if err := s.store.DeleteConfig(ctx, botID, channelType); err != nil {
		return err
	}
	if s.controller != nil {
		s.controller.RemoveConnection(ctx, botID, channelType)
	}
	return nil
}

// SetBotChannelStatus updates only the disabled status and applies runtime lifecycle.
func (s *Lifecycle) SetBotChannelStatus(ctx context.Context, botID string, channelType ChannelType, disabled bool) (ChannelConfig, error) {
	if s.store == nil {
		return ChannelConfig{}, errors.New("channel lifecycle store not configured")
	}
	if s.controller == nil {
		return ChannelConfig{}, errors.New("channel connection controller not configured")
	}

	updated, err := s.store.UpdateConfigDisabled(ctx, botID, channelType, disabled)
	if err != nil {
		return ChannelConfig{}, err
	}
	if disabled {
		s.controller.RemoveConnection(ctx, botID, channelType)
		return updated, nil
	}

	if err := s.controller.EnsureConnection(ctx, updated); err != nil {
		if _, rollbackErr := s.store.UpdateConfigDisabled(ctx, botID, channelType, true); rollbackErr != nil {
			return ChannelConfig{}, fmt.Errorf("%w (status rollback failed: %w): %w", ErrEnableChannelFailed, rollbackErr, err)
		}
		s.controller.RemoveConnection(ctx, botID, channelType)
		return ChannelConfig{}, fmt.Errorf("%w: %w", ErrEnableChannelFailed, err)
	}
	return updated, nil
}

func (s *Lifecycle) getPreviousConfig(ctx context.Context, botID string, channelType ChannelType) (ChannelConfig, bool, error) {
	cfg, err := s.store.ResolveEffectiveConfig(ctx, botID, channelType)
	if err == nil {
		return cfg, true, nil
	}
	if isChannelConfigNotFound(err) {
		return ChannelConfig{}, false, nil
	}
	return ChannelConfig{}, false, err
}

func (s *Lifecycle) rollbackUpsert(ctx context.Context, botID string, channelType ChannelType, hadPrevious bool, previous ChannelConfig) error {
	if !hadPrevious {
		if err := s.store.DeleteConfig(ctx, botID, channelType); err != nil {
			return err
		}
		if s.controller != nil {
			s.controller.RemoveConnection(ctx, botID, channelType)
		}
		return nil
	}

	restoreReq := upsertRequestFromConfig(previous)
	restored, err := s.store.UpsertConfig(ctx, botID, channelType, restoreReq)
	if err != nil {
		return err
	}
	if s.controller == nil {
		return nil
	}
	if restored.Disabled {
		s.controller.RemoveConnection(ctx, botID, channelType)
		return nil
	}
	return s.controller.EnsureConnection(ctx, restored)
}

func isChannelConfigNotFound(err error) bool {
	return errors.Is(err, ErrChannelConfigNotFound)
}

func upsertRequestFromConfig(cfg ChannelConfig) UpsertConfigRequest {
	disabled := cfg.Disabled
	restored := UpsertConfigRequest{
		Credentials:      cloneAnyMap(cfg.Credentials),
		ExternalIdentity: strings.TrimSpace(cfg.ExternalIdentity),
		SelfIdentity:     cloneAnyMap(cfg.SelfIdentity),
		Routing:          cloneAnyMap(cfg.Routing),
		Disabled:         &disabled,
	}
	if !cfg.VerifiedAt.IsZero() {
		verifiedAt := cfg.VerifiedAt.UTC()
		restored.VerifiedAt = &verifiedAt
	}
	return restored
}

func cloneAnyMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = cloneAnyValue(value)
	}
	return out
}

func cloneAnyValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		return cloneAnyMap(v)
	case []any:
		items := make([]any, 0, len(v))
		for _, item := range v {
			items = append(items, cloneAnyValue(item))
		}
		return items
	case []string:
		items := make([]string, len(v))
		copy(items, v)
		return items
	case time.Time:
		return v
	default:
		return v
	}
}
