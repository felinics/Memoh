package asset

import (
	"log/slog"

	"github.com/memohai/memoh/domains/media/internal/storage/container"
	"github.com/memohai/memoh/domains/media/internal/storage/fallback"
	"github.com/memohai/memoh/domains/media/internal/storage/local"
	"github.com/memohai/memoh/domains/media/storage"
)

// NewLocalService creates a media service backed by host-local storage at root.
func NewLocalService(log *slog.Logger, root string) *Service {
	return NewService(log, local.New(root))
}

// NewContainerFallbackService creates a media service with container primary
// storage and a local filesystem spill root.
func NewContainerFallbackService(log *slog.Logger, clients storage.ContainerFileClientProvider, fallbackRoot string) *Service {
	return NewService(log, fallback.New(container.New(clients), local.New(fallbackRoot)))
}
