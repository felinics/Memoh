// Package local implements storage.Provider backed by the host filesystem.
// Files are stored under {root}/{routingKey}.
package local

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Provider stores media assets on the host filesystem.
type Provider struct {
	root string
}

// New creates a local filesystem storage provider rooted at dir.
func New(root string) *Provider {
	return &Provider{root: root}
}

func (p *Provider) Put(_ context.Context, key string, reader io.Reader) error {
	dest := p.resolve(key)
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	f, err := os.Create(dest) //nolint:gosec // path is constructed from trusted storage key
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := io.Copy(f, reader); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return nil
}

func (p *Provider) Open(_ context.Context, key string) (io.ReadCloser, error) {
	return os.Open(p.resolve(key))
}

func (p *Provider) Delete(_ context.Context, key string) error {
	err := os.Remove(p.resolve(key))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (p *Provider) AccessPath(_ context.Context, key string) string {
	return p.resolve(key)
}

// ListPrefix returns all keys sharing a common prefix (directory listing).
func (p *Provider) ListPrefix(_ context.Context, prefix string) ([]string, error) {
	dir := filepath.Dir(p.resolve(prefix))
	base := filepath.Base(prefix)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), base) {
			rel, _ := filepath.Rel(p.root, filepath.Join(dir, e.Name()))
			if rel != "" {
				keys = append(keys, filepath.ToSlash(rel))
			}
		}
	}
	return keys, nil
}

func (p *Provider) resolve(key string) string {
	return filepath.Join(p.root, filepath.FromSlash(key))
}
