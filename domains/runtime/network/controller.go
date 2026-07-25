package network

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrNotSupported indicates the selected runtime or overlay driver cannot
// report a requested capability yet.
var ErrNotSupported = errors.New("network operation not supported")

// Controller coordinates container runtime networking and optional overlay
// attachment behind a single workspace-facing surface.
type Controller interface {
	EnsureAttached(ctx context.Context, req AttachmentRequest) (AttachmentStatus, error)
	Detach(ctx context.Context, req AttachmentRequest) error
	Status(ctx context.Context, req AttachmentRequest) (AttachmentStatus, error)
}

type BindingResolver interface {
	Resolve(ctx context.Context, botID string) (BotOverlayConfig, error)
}

type controller struct {
	runtime  Runtime
	resolver BindingResolver
	registry *Registry
}

// NewController creates a controller with runtime networking and optional bot
// binding resolution for overlay providers.
func NewController(runtime Runtime, resolver BindingResolver, registry *Registry) Controller {
	return &controller{
		runtime:  runtime,
		resolver: resolver,
		registry: registry,
	}
}

func (c *controller) EnsureAttached(ctx context.Context, req AttachmentRequest) (AttachmentStatus, error) {
	status := AttachmentStatus{}
	runtimeReady := false
	rollbackRuntime := func(cause error) error {
		if !runtimeReady || c.runtime == nil {
			return cause
		}
		if err := c.runtime.RemoveNetwork(ctx, req.Runtime); err != nil {
			return errors.Join(cause, fmt.Errorf("rollback runtime network: %w", err))
		}
		return cause
	}
	if c.runtime != nil && !req.OverlayOnly {
		runtimeStatus, err := c.runtime.EnsureNetwork(ctx, req.Runtime)
		if err != nil {
			return AttachmentStatus{}, err
		}
		status.Runtime = runtimeStatus
		runtimeReady = true
	}
	resolvedReq, overlay, err := c.resolveOverlay(ctx, req)
	if err != nil {
		return AttachmentStatus{}, rollbackRuntime(err)
	}
	overlayStatus, err := overlay.EnsureAttached(ctx, resolvedReq)
	if err != nil {
		return AttachmentStatus{}, rollbackRuntime(err)
	}
	status.Overlay = overlayStatus
	return status, nil
}

func (c *controller) Detach(ctx context.Context, req AttachmentRequest) error {
	resolvedReq, overlay, err := c.resolveOverlay(ctx, req)
	if err != nil {
		return err
	}
	if err := overlay.Detach(ctx, resolvedReq); err != nil {
		return err
	}
	if c.runtime != nil && !req.OverlayOnly {
		return c.runtime.RemoveNetwork(ctx, req.Runtime)
	}
	return nil
}

func (c *controller) Status(ctx context.Context, req AttachmentRequest) (AttachmentStatus, error) {
	status := AttachmentStatus{}
	if c.runtime != nil {
		runtimeStatus, err := c.runtime.StatusNetwork(ctx, req.Runtime)
		if err != nil && !errors.Is(err, ErrNotSupported) {
			return AttachmentStatus{}, err
		}
		if err == nil {
			status.Runtime = runtimeStatus
		}
	}
	resolvedReq, overlay, err := c.resolveOverlay(ctx, req)
	if err != nil {
		return AttachmentStatus{}, err
	}
	overlayStatus, err := overlay.Status(ctx, resolvedReq)
	if err != nil && !errors.Is(err, ErrNotSupported) {
		return AttachmentStatus{}, err
	}
	if err == nil {
		status.Overlay = overlayStatus
	}
	return status, nil
}

func (c *controller) resolveOverlay(ctx context.Context, req AttachmentRequest) (AttachmentRequest, OverlayDriver, error) {
	if c.resolver != nil && req.BotID != "" && !req.Overlay.Enabled && strings.TrimSpace(req.Overlay.Provider) == "" {
		overlayReq, err := c.resolver.Resolve(ctx, req.BotID)
		if err != nil {
			return AttachmentRequest{}, nil, err
		}
		req.Overlay = overlayReq
	}
	if !req.Overlay.Enabled || strings.TrimSpace(req.Overlay.Provider) == "" {
		return req, NoopOverlayDriver{}, nil
	}
	if c.registry == nil {
		return AttachmentRequest{}, nil, fmt.Errorf("network provider registry not configured for %s", req.Overlay.Provider)
	}
	provider, ok := c.registry.Get(req.Overlay.Provider)
	if !ok {
		return AttachmentRequest{}, nil, fmt.Errorf("network provider not registered: %s", req.Overlay.Provider)
	}
	driver, err := provider.BuildDriver(req.Overlay)
	if err != nil {
		return AttachmentRequest{}, nil, err
	}
	if driver == nil {
		return AttachmentRequest{}, nil, fmt.Errorf("network provider %s returned nil driver", req.Overlay.Provider)
	}
	return req, driver, nil
}
