// Package storage defines stable Media storage ports and errors.
// Implementations live outside this package; it must stay free of project
// internal imports and concrete providers.
package storage

import (
	"context"
	"errors"
	"io"
)

var (
	// ErrContainerFileNotSupported is returned when no underlying provider
	// implements ContainerFileOpener.
	ErrContainerFileNotSupported = errors.New("provider does not support workspace file reading")
	// ErrAccessPathUnavailable means the stored bytes cannot currently be made
	// addressable in the target consumer's filesystem namespace.
	ErrAccessPathUnavailable = errors.New("storage object has no consumer-accessible path")
)

// Provider abstracts object storage operations.
type Provider interface {
	// Put writes data to storage under the given key.
	Put(ctx context.Context, key string, reader io.Reader) error
	// Open returns a reader for the given storage key.
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	// Delete removes the object at key.
	Delete(ctx context.Context, key string) error
	// AccessPath returns a consumer-accessible reference for a storage key, or
	// an empty string when no reachable reference can be resolved. Resolution
	// may perform I/O and must honor ctx. The format depends on the backend
	// (e.g. container path, signed URL).
	AccessPath(ctx context.Context, key string) string
}

// AccessPathEnsurer is implemented by providers that can materialize an object
// into consumer-addressable storage before returning its path.
type AccessPathEnsurer interface {
	EnsureAccessPath(ctx context.Context, key string) (string, error)
}

// ContainerFileOpener is an optional interface that providers can implement
// to open arbitrary files from a bot's container data directory.
type ContainerFileOpener interface {
	OpenContainerFile(ctx context.Context, botID, containerPath string) (io.ReadCloser, error)
}

// PrefixLister is an optional interface for providers that can list keys
// sharing a common prefix (e.g. directory listing on a filesystem backend).
type PrefixLister interface {
	ListPrefix(ctx context.Context, prefix string) ([]string, error)
}
