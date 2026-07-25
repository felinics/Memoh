package gateway_test

import (
	"testing"

	"github.com/memohai/memoh/domains/channel/gateway"
	"github.com/memohai/memoh/domains/channel/internal/test/fixture"
)

func TestRenderPartsAsMarkdown_Canonical(t *testing.T) {
	t.Parallel()
	got := gateway.RenderPartsAsMarkdown(fixture.Canonical())
	if got != fixture.CanonicalMarkdown {
		t.Errorf("RenderPartsAsMarkdown(Canonical)\n  got:  %q\n  want: %q", got, fixture.CanonicalMarkdown)
	}
}

func TestRenderPartsAsPlain_Canonical(t *testing.T) {
	t.Parallel()
	got := gateway.RenderPartsAsPlain(fixture.Canonical())
	if got != fixture.CanonicalPlain {
		t.Errorf("RenderPartsAsPlain(Canonical)\n  got:  %q\n  want: %q", got, fixture.CanonicalPlain)
	}
}
