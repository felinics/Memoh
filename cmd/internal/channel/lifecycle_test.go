package channel

import (
	"context"
	"testing"

	"go.uber.org/fx/fxtest"

	"github.com/memohai/memoh/domains/agent/chat/timeline"
	"github.com/memohai/memoh/domains/channel/inbound"
)

type discussLifecycleDriver struct {
	stopped bool
}

func (d *discussLifecycleDriver) NotifyRC(context.Context, string, timeline.RenderedContext, inbound.DiscussSessionConfig) {
}

func (d *discussLifecycleDriver) Shutdown(context.Context) error {
	d.stopped = true
	return nil
}

func TestDiscussDriverStopsWithFoundationLifecycle(t *testing.T) {
	driver := &discussLifecycleDriver{}
	lifecycle := fxtest.NewLifecycle(t, fxtest.EnforceTimeout(true))
	registerDiscussLifecycle(lifecycle, driver)

	lifecycle.RequireStart()
	lifecycle.RequireStop()

	if !driver.stopped {
		t.Fatal("discuss driver was not stopped")
	}
}
