package registry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/memohai/memoh/domains/memory/provider"
)

// defaultSingletonTeamID matches domains/iam/team.DefaultTeamID without importing
// project-root internal packages from this public leaf.
const defaultSingletonTeamID = "00000000-0000-0000-0000-000000000001"

// Factory creates an Instance from a provider type string and JSON config.
type Factory func(ctx context.Context, teamID, id string, config map[string]any) (provider.Instance, error)

// ProviderConfigLoader loads one provider configuration under the team
// already bound to ctx. It lets a registry lazily instantiate providers on
// first use instead of requiring an all-team startup scan.
type ProviderConfigLoader func(ctx context.Context, id string) (providerType string, config map[string]any, err error)

// TeamDefaultFactory creates the builtin fallback independently for each
// team. It is used for bots without an explicit memory provider.
type TeamDefaultFactory func(ctx context.Context, teamID string) (provider.Instance, error)

type providerCloser interface {
	Close() error
}

type registryKey struct {
	teamID     string
	providerID string
}

// Registry manages provider instances keyed by their DB id.
// It caches instantiated providers and uses registered factories to create
// them on demand from stored configuration.
type Registry struct {
	mu             sync.RWMutex
	instances      map[registryKey]provider.Instance
	factories      map[string]Factory
	resolveTeam    provider.TeamIDResolver
	configLoader   ProviderConfigLoader
	defaultFactory TeamDefaultFactory
	logger         *slog.Logger
}

func NewRegistry(log *slog.Logger, resolvers ...provider.TeamIDResolver) *Registry {
	if log == nil {
		log = slog.Default()
	}
	resolver := defaultTeamIDResolver
	if len(resolvers) > 0 && resolvers[0] != nil {
		resolver = resolvers[0]
	}
	return &Registry{
		instances:   map[registryKey]provider.Instance{},
		factories:   map[string]Factory{},
		resolveTeam: resolver,
		logger:      log.With(slog.String("component", "memory_provider_registry")),
	}
}

func defaultTeamIDResolver(context.Context) (string, error) {
	return defaultSingletonTeamID, nil
}

// RegisterFactory registers a factory for a given provider type (e.g. "builtin").
func (r *Registry) RegisterFactory(providerType string, factory Factory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.factories[strings.TrimSpace(providerType)] = factory
}

// SetConfigLoader configures lazy provider lookup for cache misses.
func (r *Registry) SetConfigLoader(loader ProviderConfigLoader) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.configLoader = loader
}

// SetTeamDefaultFactory configures lazy, team-owned builtin fallbacks.
func (r *Registry) SetTeamDefaultFactory(factory TeamDefaultFactory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.defaultFactory = factory
}

// Register adds a pre-built provider instance by ID.
func (r *Registry) Register(id string, inst provider.Instance) {
	if err := r.RegisterContext(context.Background(), id, inst); err != nil {
		r.logger.Error("register memory provider failed", slog.String("id", id), slog.Any("error", err))
	}
}

// RegisterContext adds a pre-built provider under the team resolved from ctx.
func (r *Registry) RegisterContext(ctx context.Context, id string, inst provider.Instance) error {
	key, err := r.key(ctx, id)
	if err != nil {
		return err
	}
	r.mu.Lock()
	previous := r.instances[key]
	r.instances[key] = inst
	r.mu.Unlock()
	if previous != nil && previous != inst {
		closeProvider(previous)
	}
	return nil
}

// Get returns the team-owned provider for the given DB record ID. Cache
// misses are loaded and instantiated under the same team scope.
func (r *Registry) Get(ctx context.Context, id string) (provider.Instance, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("provider id is required")
	}
	key, err := r.key(ctx, id)
	if err != nil {
		return nil, err
	}
	r.mu.RLock()
	p, ok := r.instances[key]
	loader := r.configLoader
	defaultFactory := r.defaultFactory
	r.mu.RUnlock()
	if ok {
		return p, nil
	}
	if id == provider.DefaultBuiltinProviderID && defaultFactory != nil {
		return r.instantiateDefault(ctx, key, defaultFactory)
	}
	if loader != nil {
		providerType, config, err := loader(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("load memory provider %s for team %s: %w", id, key.teamID, err)
		}
		return r.instantiate(ctx, key, providerType, config)
	}
	return nil, fmt.Errorf("memory provider not found: %s", id)
}

// Instantiate creates a provider from a DB row and caches it.
// If the instance already exists, it is returned directly.
func (r *Registry) Instantiate(ctx context.Context, id, providerType string, config map[string]any) (provider.Instance, error) {
	id = strings.TrimSpace(id)
	providerType = strings.TrimSpace(providerType)
	key, err := r.key(ctx, id)
	if err != nil {
		return nil, err
	}
	return r.instantiate(ctx, key, providerType, config)
}

func (r *Registry) instantiate(ctx context.Context, key registryKey, providerType string, config map[string]any) (provider.Instance, error) {
	// Factory construction is serialized with Remove. Besides deduplicating
	// concurrent cache misses, this prevents an in-flight old configuration
	// from being stored after Update has evicted it.
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.instances[key]; ok {
		return p, nil
	}
	factory, ok := r.factories[providerType]
	if !ok {
		return nil, fmt.Errorf("unknown memory provider type: %s", providerType)
	}
	p, err := factory(ctx, key.teamID, key.providerID, config)
	if err != nil {
		return nil, fmt.Errorf("instantiate memory provider %s (%s) for team %s: %w", key.providerID, providerType, key.teamID, err)
	}
	r.instances[key] = p
	return p, nil
}

func (r *Registry) instantiateDefault(ctx context.Context, key registryKey, factory TeamDefaultFactory) (provider.Instance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.instances[key]; ok {
		return p, nil
	}
	p, err := factory(ctx, key.teamID)
	if err != nil {
		return nil, fmt.Errorf("instantiate default memory provider for team %s: %w", key.teamID, err)
	}
	r.instances[key] = p
	return p, nil
}

// Remove evicts a cached provider instance (e.g. after config update or delete).
func (r *Registry) Remove(ctx context.Context, id string) error {
	key, err := r.key(ctx, id)
	if err != nil {
		return err
	}
	r.mu.Lock()
	inst := r.instances[key]
	delete(r.instances, key)
	r.mu.Unlock()
	closeProvider(inst)
	return nil
}

// Close releases every instantiated provider. It is safe to call more than
// once and is used during process shutdown as well as team registry teardown.
func (r *Registry) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	providers := make([]provider.Instance, 0, len(r.instances))
	for key, inst := range r.instances {
		providers = append(providers, inst)
		delete(r.instances, key)
	}
	r.mu.Unlock()
	for _, inst := range providers {
		closeProvider(inst)
	}
	return nil
}

func closeProvider(inst provider.Instance) {
	if closer, ok := inst.(providerCloser); ok && closer != nil {
		_ = closer.Close()
	}
}

func (r *Registry) key(ctx context.Context, id string) (registryKey, error) {
	if r == nil || r.resolveTeam == nil {
		return registryKey{}, errors.New("memory team resolver is not configured")
	}
	teamID, err := r.resolveTeam(ctx)
	if err != nil {
		return registryKey{}, fmt.Errorf("resolve memory team: %w", err)
	}
	teamID = strings.TrimSpace(teamID)
	id = strings.TrimSpace(id)
	if teamID == "" {
		return registryKey{}, errors.New("memory team id is required")
	}
	if id == "" {
		return registryKey{}, errors.New("provider id is required")
	}
	return registryKey{teamID: teamID, providerID: id}, nil
}
