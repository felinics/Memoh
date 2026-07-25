package network

import "context"

// NoopOverlayDriver is the default overlay driver until a real SD-WAN provider
// is selected. It preserves current behavior by leaving overlay state untouched.
type NoopOverlayDriver struct{}

func (NoopOverlayDriver) Kind() string { return "noop" }

func (NoopOverlayDriver) EnsureAttached(context.Context, AttachmentRequest) (OverlayStatus, error) {
	return OverlayStatus{Provider: "noop", State: "disabled"}, nil
}

func (NoopOverlayDriver) Detach(context.Context, AttachmentRequest) error { return nil }

func (NoopOverlayDriver) Status(context.Context, AttachmentRequest) (OverlayStatus, error) {
	return OverlayStatus{Provider: "noop", State: "disabled"}, nil
}
