// Package fallback implements storage.Provider that tries a primary provider
// first (e.g. containerfs) and falls back to a secondary (e.g. localfs) on
// failure. Reads check both providers so assets stored by either are reachable.
package fallback

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/felinics/memoh/internal/storage"
)

var (
	_ storage.ContainerFileOpener = (*Provider)(nil)
	_ storage.AccessPathEnsurer   = (*Provider)(nil)
)

// Provider delegates to primary and falls back to secondary on write errors.
type Provider struct {
	primary   storage.Provider
	secondary storage.Provider
}

// New creates a fallback provider.
func New(primary, secondary storage.Provider) *Provider {
	return &Provider{primary: primary, secondary: secondary}
}

func (p *Provider) Put(ctx context.Context, key string, reader io.Reader) error {
	err := p.primary.Put(ctx, key, reader)
	if err == nil {
		return nil
	}
	if seeker, ok := reader.(io.Seeker); ok {
		if _, seekErr := seeker.Seek(0, io.SeekStart); seekErr != nil {
			return err
		}
	}
	return p.secondary.Put(ctx, key, reader)
}

func (p *Provider) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	rc, err := p.primary.Open(ctx, key)
	if err == nil {
		return rc, nil
	}
	return p.secondary.Open(ctx, key)
}

func (p *Provider) Delete(ctx context.Context, key string) error {
	var errs []error
	if p != nil && p.primary != nil {
		if err := p.primary.Delete(ctx, key); err != nil {
			errs = append(errs, fmt.Errorf("delete primary: %w", err))
		}
	}
	if p != nil && p.secondary != nil {
		if err := p.secondary.Delete(ctx, key); err != nil {
			errs = append(errs, fmt.Errorf("delete secondary: %w", err))
		}
	}
	return errors.Join(errs...)
}

// AccessPath returns only a path backed by primary storage. Objects that exist
// solely in secondary spill storage are promoted first; a secondary provider's
// path is never exposed because it may live in a different filesystem namespace
// from the workspace consumer.
func (p *Provider) AccessPath(ctx context.Context, key string) string {
	accessPath, _ := p.EnsureAccessPath(ctx, key)
	return accessPath
}

// EnsureAccessPath promotes secondary-only bytes into primary storage and then
// returns the primary provider's consumer-visible path.
func (p *Provider) EnsureAccessPath(ctx context.Context, key string) (string, error) {
	if p == nil || p.primary == nil {
		return "", storage.ErrAccessPathUnavailable
	}
	primaryPath, primaryPresent, primaryErr := providerAccessPath(ctx, p.primary, key)
	if primaryPresent {
		if primaryPath == "" {
			return "", fmt.Errorf("primary object is not addressable: %w", storage.ErrAccessPathUnavailable)
		}
		return primaryPath, nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if p.secondary == nil {
		return "", errors.Join(primaryErr, storage.ErrAccessPathUnavailable)
	}

	source, err := p.secondary.Open(ctx, key)
	if err != nil {
		return "", errors.Join(primaryErr, fmt.Errorf("open secondary: %w", err), storage.ErrAccessPathUnavailable)
	}
	if source == nil {
		return "", errors.Join(primaryErr, errors.New("secondary returned a nil reader"), storage.ErrAccessPathUnavailable)
	}
	defer func() { _ = source.Close() }()

	if err := p.primary.Put(ctx, key, source); err != nil {
		// A concurrent promotion (or a lost success response) may have committed
		// the canonical object even though this Put reported an error.
		if accessPath, present, _ := providerAccessPath(ctx, p.primary, key); present && accessPath != "" {
			_ = p.secondary.Delete(ctx, key)
			return accessPath, nil
		}
		return "", errors.Join(primaryErr, fmt.Errorf("promote secondary object to primary: %w", err), storage.ErrAccessPathUnavailable)
	}
	primaryPath = strings.TrimSpace(p.primary.AccessPath(ctx, key))
	if primaryPath == "" {
		return "", fmt.Errorf("promoted primary object is not addressable: %w", storage.ErrAccessPathUnavailable)
	}
	// The primary copy is now canonical. A failed spill cleanup must not make a
	// successfully materialized object unavailable; Delete retries both stores.
	_ = p.secondary.Delete(ctx, key)
	return primaryPath, nil
}

func providerAccessPath(ctx context.Context, provider storage.Provider, key string) (string, bool, error) {
	if provider == nil {
		return "", false, errors.New("provider is nil")
	}
	rc, err := provider.Open(ctx, key)
	if err != nil {
		return "", false, err
	}
	if rc != nil {
		_ = rc.Close()
	}
	return strings.TrimSpace(provider.AccessPath(ctx, key)), true, nil
}

// ListPrefix delegates to both providers and deduplicates.
func (p *Provider) ListPrefix(ctx context.Context, prefix string) ([]string, error) {
	keys, _ := tryListPrefix(ctx, p.primary, prefix)
	secondaryKeys, _ := tryListPrefix(ctx, p.secondary, prefix)
	seen := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		seen[k] = struct{}{}
	}
	for _, k := range secondaryKeys {
		if _, ok := seen[k]; !ok {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil, nil
	}
	return keys, nil
}

func tryListPrefix(ctx context.Context, p storage.Provider, prefix string) ([]string, error) {
	if lister, ok := p.(storage.PrefixLister); ok {
		return lister.ListPrefix(ctx, prefix)
	}
	return nil, nil
}

// OpenContainerFile delegates to whichever inner provider implements
// storage.ContainerFileOpener, trying the primary first.
// If the primary implements the interface but returns an error, that error
// is propagated rather than silently swallowed — the secondary is only tried
// when the primary does not implement ContainerFileOpener at all.
func (p *Provider) OpenContainerFile(ctx context.Context, botID, containerPath string) (io.ReadCloser, error) {
	if opener, ok := p.primary.(storage.ContainerFileOpener); ok {
		rc, err := opener.OpenContainerFile(ctx, botID, containerPath)
		if err != nil {
			return nil, fmt.Errorf("primary provider: %w", err)
		}
		return rc, nil
	}
	if opener, ok := p.secondary.(storage.ContainerFileOpener); ok {
		return opener.OpenContainerFile(ctx, botID, containerPath)
	}
	return nil, storage.ErrContainerFileNotSupported
}
