package adapters

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/felinics/memoh/internal/team"
)

// Factory creates a Provider from a provider type string and JSON config.
// The registry uses factories to lazily instantiate providers from DB rows.
type Factory func(ctx context.Context, teamID, id string, config map[string]any) (Provider, error)

// TeamIDResolver resolves the team that owns a memory operation. Hosted
// deployments can inject a strict request-context resolver; upstream defaults
// to its published singleton team.
type TeamIDResolver func(context.Context) (string, error)

// ProviderConfigLoader loads one provider configuration under the team
// already bound to ctx. It lets a registry lazily instantiate providers on
// first use instead of requiring an all-team startup scan.
type ProviderConfigLoader func(ctx context.Context, id string) (providerType string, config map[string]any, err error)

// TeamDefaultFactory creates the builtin fallback independently for each
// team. It is used for bots without an explicit memory provider.
type TeamDefaultFactory func(ctx context.Context, teamID string) (Provider, error)

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
	instances      map[registryKey]Provider
	factories      map[string]Factory
	resolveTeam    TeamIDResolver
	configLoader   ProviderConfigLoader
	defaultFactory TeamDefaultFactory
	logger         *slog.Logger
}

func NewRegistry(log *slog.Logger, resolvers ...TeamIDResolver) *Registry {
	if log == nil {
		log = slog.Default()
	}
	resolver := defaultTeamIDResolver
	if len(resolvers) > 0 && resolvers[0] != nil {
		resolver = resolvers[0]
	}
	return &Registry{
		instances:   map[registryKey]Provider{},
		factories:   map[string]Factory{},
		resolveTeam: resolver,
		logger:      log.With(slog.String("component", "memory_provider_registry")),
	}
}

func defaultTeamIDResolver(context.Context) (string, error) {
	return team.DefaultTeamID, nil
}

// FixedTeamIDResolver returns a resolver permanently scoped to teamID.
// Provider factories use this for team-owned runtimes whose background work
// may outlive the request context that instantiated them.
func FixedTeamIDResolver(teamID string) TeamIDResolver {
	teamID = strings.TrimSpace(teamID)
	return func(context.Context) (string, error) {
		if teamID == "" {
			return "", errors.New("memory team id is required")
		}
		return teamID, nil
	}
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
func (r *Registry) Register(id string, provider Provider) {
	if err := r.RegisterContext(context.Background(), id, provider); err != nil {
		r.logger.Error("register memory provider failed", slog.String("id", id), slog.Any("error", err))
	}
}

// RegisterContext adds a pre-built provider under the team resolved from ctx.
func (r *Registry) RegisterContext(ctx context.Context, id string, provider Provider) error {
	key, err := r.key(ctx, id)
	if err != nil {
		return err
	}
	r.mu.Lock()
	previous := r.instances[key]
	r.instances[key] = provider
	r.mu.Unlock()
	if previous != nil && previous != provider {
		closeProvider(previous)
	}
	return nil
}

// Get returns the team-owned provider for the given DB record ID. Cache
// misses are loaded and instantiated under the same team scope.
func (r *Registry) Get(ctx context.Context, id string) (Provider, error) {
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
	if id == DefaultBuiltinProviderID && defaultFactory != nil {
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
func (r *Registry) Instantiate(ctx context.Context, id, providerType string, config map[string]any) (Provider, error) {
	id = strings.TrimSpace(id)
	providerType = strings.TrimSpace(providerType)
	key, err := r.key(ctx, id)
	if err != nil {
		return nil, err
	}
	return r.instantiate(ctx, key, providerType, config)
}

func (r *Registry) instantiate(ctx context.Context, key registryKey, providerType string, config map[string]any) (Provider, error) {
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

func (r *Registry) instantiateDefault(ctx context.Context, key registryKey, factory TeamDefaultFactory) (Provider, error) {
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
	provider := r.instances[key]
	delete(r.instances, key)
	r.mu.Unlock()
	closeProvider(provider)
	return nil
}

// Close releases every instantiated provider. It is safe to call more than
// once and is used during process shutdown as well as team registry teardown.
func (r *Registry) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	providers := make([]Provider, 0, len(r.instances))
	for key, provider := range r.instances {
		providers = append(providers, provider)
		delete(r.instances, key)
	}
	r.mu.Unlock()
	for _, provider := range providers {
		closeProvider(provider)
	}
	return nil
}

func closeProvider(provider Provider) {
	if closer, ok := provider.(providerCloser); ok && closer != nil {
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
