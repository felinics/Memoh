package channel_test

import (
	"testing"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/partsfixture"
)

func TestRenderPartsAsMarkdown_Canonical(t *testing.T) {
	t.Parallel()
	got := channel.RenderPartsAsMarkdown(partsfixture.Canonical())
	if got != partsfixture.CanonicalMarkdown {
		t.Errorf("RenderPartsAsMarkdown(Canonical)\n  got:  %q\n  want: %q", got, partsfixture.CanonicalMarkdown)
	}
}

func TestRenderPartsAsPlain_Canonical(t *testing.T) {
	t.Parallel()
	got := channel.RenderPartsAsPlain(partsfixture.Canonical())
	if got != partsfixture.CanonicalPlain {
		t.Errorf("RenderPartsAsPlain(Canonical)\n  got:  %q\n  want: %q", got, partsfixture.CanonicalPlain)
	}
}
