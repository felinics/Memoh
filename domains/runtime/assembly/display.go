package assembly

import (
	"context"
	"errors"
	"log/slog"

	runtimedisplay "github.com/memohai/memoh/domains/runtime/display"
	internaldisplay "github.com/memohai/memoh/domains/runtime/internal/display"
)

// DisplayDeps are the explicit public inputs required to assemble a display Service.
type DisplayDeps struct {
	Log       *slog.Logger
	Workspace runtimedisplay.Workspace
}

// NewDisplay constructs the public workspace display Service. The returned
// cleanup stops active encoder sessions and must be called on process shutdown.
func NewDisplay(deps DisplayDeps) (runtimedisplay.Service, func(context.Context) error, error) {
	if deps.Workspace == nil {
		return nil, nil, errors.New("workspace is required")
	}
	log := deps.Log
	if log == nil {
		log = slog.Default()
	}
	svc := internaldisplay.NewService(log, deps.Workspace)
	return svc, svc.Shutdown, nil
}
