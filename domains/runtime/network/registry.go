package network

import (
	"errors"
	"fmt"
	"strings"
	"sync"
)

// Registry stores overlay providers and exposes descriptor/capability queries.
type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
}

// NewRegistry creates an empty provider registry.
func NewRegistry() *Registry {
	return &Registry{
		providers: map[string]Provider{},
	}
}

// Register adds a provider to the registry.
func (r *Registry) Register(provider Provider) error {
	if provider == nil {
		return errors.New("provider is nil")
	}
	kind := normalizeKind(provider.Kind())
	if kind == "" {
		return errors.New("provider kind is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.providers[kind]; exists {
		return fmt.Errorf("provider already registered: %s", kind)
	}
	r.providers[kind] = provider
	return nil
}

// MustRegister calls Register and panics on error.
func (r *Registry) MustRegister(provider Provider) {
	if err := r.Register(provider); err != nil {
		panic(err)
	}
}

// Get returns the provider for the given kind.
func (r *Registry) Get(kind string) (Provider, bool) {
	normalized := normalizeKind(kind)
	r.mu.RLock()
	defer r.mu.RUnlock()
	provider, ok := r.providers[normalized]
	return provider, ok
}

// List returns all registered providers.
func (r *Registry) List() []Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	items := make([]Provider, 0, len(r.providers))
	for _, provider := range r.providers {
		items = append(items, provider)
	}
	return items
}

// GetDescriptor returns the read-only descriptor for the given provider kind.
func (r *Registry) GetDescriptor(kind string) (ProviderDescriptor, bool) {
	provider, ok := r.Get(kind)
	if !ok {
		return ProviderDescriptor{}, false
	}
	return provider.Descriptor(), true
}

// ListDescriptors returns all registered provider descriptors.
func (r *Registry) ListDescriptors() []ProviderDescriptor {
	providers := r.List()
	items := make([]ProviderDescriptor, 0, len(providers))
	for _, provider := range providers {
		items = append(items, provider.Descriptor())
	}
	return items
}

// GetCapabilities returns the capability matrix for the given provider kind.
func (r *Registry) GetCapabilities(kind string) (ProviderCapabilities, bool) {
	desc, ok := r.GetDescriptor(kind)
	if !ok {
		return ProviderCapabilities{}, false
	}
	return desc.Capabilities, true
}

func normalizeKind(raw string) string {
	return strings.TrimSpace(strings.ToLower(raw))
}
